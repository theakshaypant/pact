package cluster

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
)

// tektonReleaseURL is the official Tekton Pipeline release manifest for
// a given version (%s = e.g. "v1.19.0"). A variable so tests can point
// it at a local server.
var tektonReleaseURL = "https://github.com/tektoncd/pipeline/releases/download/%s/release.yaml"

// fieldManager owns pact's server-side apply of the Tekton install.
const fieldManager = "pact"

// maxManifestSize bounds a fetched release manifest.
const maxManifestSize = 64 << 20

// crdPollInterval is how often CRD establishment is re-checked. A
// variable so tests can speed it up.
var crdPollInterval = 500 * time.Millisecond

var tektonVersionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// objectClient applies, reads and deletes arbitrary cluster objects.
// It abstracts client-go's dynamic client so tests can fake it.
type objectClient interface {
	Apply(ctx context.Context, obj *unstructured.Unstructured) error
	Get(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error)
	Delete(ctx context.Context, obj *unstructured.Unstructured) error
}

// tektonPipelinesCRDName is the CRD every Tekton Pipelines installation
// defines; it is pact's probe for whether Tekton exists and who applied
// it.
const tektonPipelinesCRDName = "pipelines.tekton.dev"

// pipelinesCRDProbe returns the stub object to Get the Tekton Pipelines
// CRD with.
func pipelinesCRDProbe() *unstructured.Unstructured {
	probe := &unstructured.Unstructured{}
	probe.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition",
	})
	probe.SetName(tektonPipelinesCRDName)
	return probe
}

// tektonInstalled reports whether a Tekton Pipelines installation exists
// (the pipelines.tekton.dev CRD is present) and whether it serves the
// tekton.dev/v1 API that pact's PipelineRuns are submitted against.
func tektonInstalled(ctx context.Context, client objectClient) (installed, servesV1 bool, err error) {
	live, err := client.Get(ctx, pipelinesCRDProbe())
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, false, nil
		}
		return false, false, err
	}
	if live == nil {
		return false, false, fmt.Errorf("client returned no object for CRD %q", tektonPipelinesCRDName)
	}
	versions, _, err := unstructured.NestedSlice(live.Object, "spec", "versions")
	if err != nil {
		return true, false, fmt.Errorf("reading CRD versions: %w", err)
	}
	for _, v := range versions {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(m, "name")
		served, _, _ := unstructured.NestedBool(m, "served")
		if name == "v1" && served {
			return true, true, nil
		}
	}
	return true, false, nil
}

// tektonAppliedByPact reports whether the Tekton installation was applied
// by pact: pact's server-side apply stamps field manager "pact" into the
// managedFields of every object it applies, so the Tekton CRD carries the
// stamp iff pact (or a previous pact invocation) installed Tekton here. A
// pre-existing installation applied by kubectl, helm or an operator does
// not.
func tektonAppliedByPact(ctx context.Context, client objectClient) (bool, error) {
	live, err := client.Get(ctx, pipelinesCRDProbe())
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil // nothing pact applied is left
		}
		return false, err
	}
	if live == nil {
		return false, fmt.Errorf("client returned no object for CRD %q", tektonPipelinesCRDName)
	}
	fields, _, err := unstructured.NestedSlice(live.Object, "metadata", "managedFields")
	if err != nil {
		return false, fmt.Errorf("reading CRD managed fields: %w", err)
	}
	for _, f := range fields {
		m, ok := f.(map[string]any)
		if !ok {
			continue
		}
		if manager, _, _ := unstructured.NestedString(m, "manager"); manager == fieldManager {
			return true, nil
		}
	}
	return false, nil
}

// tektonManifest returns the Tekton install objects and a human-readable
// source description. An empty version uses the embedded manifest;
// otherwise the official release for that version is fetched.
func tektonManifest(ctx context.Context, version string) ([]unstructured.Unstructured, string, error) {
	if version == "" {
		objs, err := parseManifest(embeddedTektonRelease)
		return objs, "embedded " + embeddedTektonVersion, err
	}
	if !tektonVersionRe.MatchString(version) {
		return nil, "", fmt.Errorf("invalid tekton version %q: want the form vMAJOR.MINOR.PATCH (e.g. v1.19.0)", version)
	}
	manifest, err := fetch(ctx, fmt.Sprintf(tektonReleaseURL, version))
	if err != nil {
		return nil, "", fmt.Errorf("fetching tekton %s: %w", version, err)
	}
	objs, err := parseManifest(manifest)
	return objs, "fetched " + version, err
}

// parseManifest decodes a multi-document YAML manifest into the objects
// it contains, in document order. List documents are expanded into their
// items.
func parseManifest(manifest []byte) ([]unstructured.Unstructured, error) {
	var objs []unstructured.Unstructured
	dec := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	for {
		var obj unstructured.Unstructured
		if err := dec.Decode(&obj); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decoding manifest document %d: %w", len(objs)+1, err)
		}
		if obj.GetKind() == "" {
			continue // separator or comment-only document
		}
		if strings.HasSuffix(obj.GetKind(), "List") {
			items, _, err := unstructured.NestedSlice(obj.Object, "items")
			if err != nil {
				return nil, fmt.Errorf("expanding %s %q: %w", obj.GetKind(), obj.GetName(), err)
			}
			for _, item := range items {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				objs = append(objs, unstructured.Unstructured{Object: m})
			}
			continue
		}
		objs = append(objs, obj)
	}
	if len(objs) == 0 {
		return nil, fmt.Errorf("manifest contains no objects")
	}
	return objs, nil
}

// installTekton applies the Tekton manifest with server-side apply:
// namespaces first, then CRDs, then everything else once the CRDs are
// Established. Applying is idempotent and converges on re-runs.
func installTekton(ctx context.Context, client objectClient, objs []unstructured.Unstructured) error {
	namespaces, crds, rest := orderManifest(objs)
	for _, bucket := range [][]unstructured.Unstructured{namespaces, crds} {
		for _, obj := range bucket {
			if err := applyObject(ctx, client, &obj); err != nil {
				return err
			}
		}
	}
	if err := waitForCRDs(ctx, client, crds); err != nil {
		return fmt.Errorf("waiting for CRDs to be established: %w", err)
	}
	for _, obj := range rest {
		if err := applyObject(ctx, client, &obj); err != nil {
			return err
		}
	}
	return nil
}

// uninstallTekton deletes every object of the embedded Tekton manifest
// in reverse document order, so dependents go before their owners.
// Tekton object names are stable across versions, so this also removes
// an installation that was applied from a different release. Objects
// that are already gone are fine.
func uninstallTekton(ctx context.Context, client objectClient) error {
	objs, err := parseManifest(embeddedTektonRelease)
	if err != nil {
		return fmt.Errorf("parsing embedded tekton manifest: %w", err)
	}
	for i := len(objs) - 1; i >= 0; i-- {
		obj := objs[i]
		if err := client.Delete(ctx, &obj); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting %s %q: %w", obj.GetKind(), obj.GetName(), err)
		}
	}
	return nil
}

// orderManifest partitions objects into apply order: namespaces, CRDs,
// then the rest. Document order is preserved within each bucket.
func orderManifest(objs []unstructured.Unstructured) (namespaces, crds, rest []unstructured.Unstructured) {
	for _, obj := range objs {
		switch {
		case obj.GetKind() == "Namespace":
			namespaces = append(namespaces, obj)
		case obj.GroupVersionKind().Group == "apiextensions.k8s.io" && obj.GetKind() == "CustomResourceDefinition":
			crds = append(crds, obj)
		default:
			rest = append(rest, obj)
		}
	}
	return namespaces, crds, rest
}

// applyObject server-side applies one object under pact's field manager.
func applyObject(ctx context.Context, client objectClient, obj *unstructured.Unstructured) error {
	if err := client.Apply(ctx, obj); err != nil {
		return fmt.Errorf("applying %s %q: %w", obj.GetKind(), obj.GetName(), err)
	}
	return nil
}

// waitForCRDs polls until every CRD reports Established, the signal that
// its API is served and objects of that kind can be created.
func waitForCRDs(ctx context.Context, client objectClient, crds []unstructured.Unstructured) error {
	return wait.PollUntilContextCancel(ctx, crdPollInterval, true, func(ctx context.Context) (bool, error) {
		for _, crd := range crds {
			established, err := crdEstablished(ctx, client, crd)
			if err != nil {
				return false, err
			}
			if !established {
				return false, nil
			}
		}
		return true, nil
	})
}

// crdEstablished reports whether the live CRD has condition Established.
func crdEstablished(ctx context.Context, client objectClient, crd unstructured.Unstructured) (bool, error) {
	live, err := client.Get(ctx, &crd)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil // not created yet, keep waiting
		}
		return false, err
	}
	if live == nil {
		return false, fmt.Errorf("client returned no object for CRD %q", crd.GetName())
	}
	conditions, _, err := unstructured.NestedSlice(live.Object, "status", "conditions")
	if err != nil {
		return false, fmt.Errorf("reading CRD conditions: %w", err)
	}
	for _, c := range conditions {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, _, _ := unstructured.NestedString(m, "type"); t != "Established" {
			continue
		}
		if s, _, _ := unstructured.NestedString(m, "status"); s == "True" {
			return true, nil
		}
	}
	return false, nil
}

// dynamicObjectClient implements objectClient on client-go's dynamic
// client with a discovery-built RESTMapper.
type dynamicObjectClient struct {
	client dynamic.Interface
	mapper meta.RESTMapper
}

// resourceFor resolves the dynamic resource interface for an object's
// GVK, namespaced or cluster-scoped as the mapping dictates.
func (d *dynamicObjectClient) resourceFor(obj *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	gvk := obj.GroupVersionKind()
	mapping, err := d.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("mapping %s: %w", gvk, err)
	}
	ri := d.client.Resource(mapping.Resource)
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return ri.Namespace(obj.GetNamespace()), nil
	}
	return ri, nil
}

func (d *dynamicObjectClient) Apply(ctx context.Context, obj *unstructured.Unstructured) error {
	ri, err := d.resourceFor(obj)
	if err != nil {
		return err
	}
	_, err = ri.Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{FieldManager: fieldManager, Force: true})
	return err
}

func (d *dynamicObjectClient) Get(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	ri, err := d.resourceFor(obj)
	if err != nil {
		return nil, err
	}
	return ri.Get(ctx, obj.GetName(), metav1.GetOptions{})
}

func (d *dynamicObjectClient) Delete(ctx context.Context, obj *unstructured.Unstructured) error {
	ri, err := d.resourceFor(obj)
	if err != nil {
		return err
	}
	return ri.Delete(ctx, obj.GetName(), metav1.DeleteOptions{})
}

// fetch downloads url as manifest bytes.
func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	// http.Client.Do only promises a non-nil response when err is nil;
	// guard so a misbehaving client fails loudly instead of panicking.
	if resp == nil {
		return nil, fmt.Errorf("GET %s: no response", url)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	// Read one byte past the limit so truncation is detectable instead
	// of silently producing a partial manifest.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: reading body: %w", url, err)
	}
	if len(body) > maxManifestSize {
		return nil, fmt.Errorf("GET %s: manifest exceeds %d bytes", url, maxManifestSize)
	}
	return body, nil
}
