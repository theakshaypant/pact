package cluster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// nsObj, crdObj and depObj build minimal typed objects for ordering tests.
func nsObj(name string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": name},
	}}
}

func crdObj(name string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]any{"name": name},
	}}
}

func depObj(name string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": "tekton-pipelines"},
	}}
}

func TestParseManifestEmbeddedRelease(t *testing.T) {
	// The real embedded manifest is the strongest parse test: the whole
	// official release.yaml must decode into well-formed objects.
	objs, err := parseManifest(embeddedTektonRelease)
	require.NoError(t, err)
	assert.Greater(t, len(objs), 50, "the release manifest holds 76 objects")

	var namespaces, crds int
	for _, obj := range objs {
		switch {
		case obj.GetKind() == "Namespace":
			namespaces++
		case obj.GetKind() == "CustomResourceDefinition":
			crds++
		}
		require.NotEmpty(t, obj.GetName(), "every object must be named")
	}
	assert.Positive(t, namespaces, "release.yaml creates the tekton-pipelines namespace")
	assert.Positive(t, crds, "release.yaml installs Tekton CRDs")
	assert.Contains(t, objKinds(objs), "Deployment")
}

func TestParseManifestSynthetic(t *testing.T) {
	manifest := `# leading comment, no objects

---
apiVersion: v1
kind: Namespace
metadata:
  name: tekton-pipelines
---
apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: Service
    metadata:
      name: svc-a
  - apiVersion: v1
    kind: Service
    metadata:
      name: svc-b
`
	objs, err := parseManifest([]byte(manifest))
	require.NoError(t, err)
	require.Len(t, objs, 3)
	assert.Equal(t, "Namespace", objs[0].GetKind())
	assert.Equal(t, []string{"Service/svc-a", "Service/svc-b"}, []string{key(&objs[1]), key(&objs[2])},
		"List documents must be expanded into their items")
}

func TestParseManifestEmpty(t *testing.T) {
	_, err := parseManifest([]byte("# nothing but comments\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no objects")
}

func objKinds(objs []unstructured.Unstructured) []string {
	var kinds []string
	for _, obj := range objs {
		kinds = append(kinds, obj.GetKind())
	}
	return kinds
}

func TestOrderManifest(t *testing.T) {
	objs := []unstructured.Unstructured{
		depObj("controller"), crdObj("tasks.tekton.dev"), nsObj("tekton-pipelines"), depObj("webhook"),
	}
	namespaces, crds, rest := orderManifest(objs)

	assert.Equal(t, []string{"Namespace/tekton-pipelines"}, keysOf(namespaces))
	assert.Equal(t, []string{"CustomResourceDefinition/tasks.tekton.dev"}, keysOf(crds))
	assert.Equal(t, []string{"Deployment/controller", "Deployment/webhook"}, keysOf(rest),
		"document order is preserved within a bucket")
}

func keysOf(objs []unstructured.Unstructured) []string {
	var keys []string
	for i := range objs {
		keys = append(keys, key(&objs[i]))
	}
	return keys
}

func TestInstallTektonOrdering(t *testing.T) {
	objs := []unstructured.Unstructured{
		depObj("controller"), crdObj("tasks.tekton.dev"), nsObj("tekton-pipelines"), depObj("webhook"),
	}
	client := &fakeObjectClient{getFunc: establishedCRD}

	require.NoError(t, installTekton(context.Background(), client, objs))
	assert.Equal(t, []string{
		"Namespace/tekton-pipelines",
		"CustomResourceDefinition/tasks.tekton.dev",
		"Deployment/controller",
		"Deployment/webhook",
	}, client.applied, "namespaces and CRDs must be applied before the rest")
}

func TestInstallTektonWaitsForCRDEstablishment(t *testing.T) {
	oldInterval := crdPollInterval
	crdPollInterval = time.Millisecond
	t.Cleanup(func() { crdPollInterval = oldInterval })

	getCalls := 0
	client := &fakeObjectClient{getFunc: func(obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		getCalls++
		if getCalls < 3 {
			// CRD exists but is not Established yet.
			live := obj.DeepCopy()
			live.Object["status"] = map[string]any{
				"conditions": []any{map[string]any{"type": "Established", "status": "False"}},
			}
			return live, nil
		}
		return establishedCRD(obj)
	}}
	objs := []unstructured.Unstructured{nsObj("tekton-pipelines"), crdObj("tasks.tekton.dev"), depObj("controller")}

	require.NoError(t, installTekton(context.Background(), client, objs))
	assert.GreaterOrEqual(t, getCalls, 3, "installation must poll until the CRD is Established")
}

func TestUninstallTektonReverseOrder(t *testing.T) {
	// Shrink the work: uninstall always uses the embedded manifest, so
	// assert on its full object set in reverse document order.
	client := &fakeObjectClient{getFunc: establishedCRD}
	require.NoError(t, uninstallTekton(context.Background(), client))

	objs, err := parseManifest(embeddedTektonRelease)
	require.NoError(t, err)
	require.Len(t, client.deleted, len(objs))
	for i, got := range client.deleted {
		want := key(&objs[len(objs)-1-i])
		assert.Equal(t, want, got, "object %d must be deleted in reverse document order", i)
	}
}

func TestUninstallTektonToleratesMissing(t *testing.T) {
	// Every second delete fails with NotFound, as on a partially
	// uninstalled cluster; those must be ignored.
	var deleted int
	client := &notFoundDeletingClient{deleted: &deleted}
	require.NoError(t, uninstallTekton(context.Background(), client))
	assert.Positive(t, deleted, "at least one real delete must have happened")
}

// notFoundDeletingClient fails every second Delete with NotFound,
// mirroring objects that are already gone from a partial install.
type notFoundDeletingClient struct {
	deleted *int
	count   int
}

func (n *notFoundDeletingClient) Apply(context.Context, *unstructured.Unstructured) error {
	return nil
}

func (n *notFoundDeletingClient) Get(_ context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return establishedCRD(obj)
}

func (n *notFoundDeletingClient) Delete(_ context.Context, obj *unstructured.Unstructured) error {
	n.count++
	if n.count%2 == 0 {
		return apierrors.NewNotFound(schema.GroupResource{Group: obj.GroupVersionKind().Group, Resource: obj.GetKind()}, obj.GetName())
	}
	*n.deleted++
	return nil
}

func TestTektonManifestRejectsBadVersions(t *testing.T) {
	for _, version := range []string{"latest", "v1", "1.19.0", "v1.19", "main"} {
		_, _, err := tektonManifest(context.Background(), version)
		require.Error(t, err, "version %q must be rejected", version)
		assert.Contains(t, err.Error(), "invalid tekton version")
	}
}

func TestTektonManifestFetchesVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/download/v1.19.0/release.yaml" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: fetched\n"))
	}))
	t.Cleanup(server.Close)

	oldURL := tektonReleaseURL
	tektonReleaseURL = server.URL + "/download/%s/release.yaml"
	t.Cleanup(func() { tektonReleaseURL = oldURL })

	objs, source, err := tektonManifest(context.Background(), "v1.19.0")
	require.NoError(t, err)
	assert.Equal(t, "fetched v1.19.0", source)
	require.Len(t, objs, 1)
	assert.Equal(t, "fetched", objs[0].GetName())
}

func TestTektonManifestFetchFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such release", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	oldURL := tektonReleaseURL
	tektonReleaseURL = server.URL + "/download/%s/release.yaml"
	t.Cleanup(func() { tektonReleaseURL = oldURL })

	_, _, err := tektonManifest(context.Background(), "v9.9.9")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching tekton v9.9.9")
	assert.Contains(t, err.Error(), "404")
}

func TestFetchHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fetch(ctx, "http://example.invalid/never")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

func TestTektonInstalled(t *testing.T) {
	notFound := func(*unstructured.Unstructured) (*unstructured.Unstructured, error) {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, tektonPipelinesCRDName)
	}

	t.Run("absent CRD means not installed", func(t *testing.T) {
		installed, servesV1, err := tektonInstalled(context.Background(), &fakeObjectClient{getFunc: notFound})
		require.NoError(t, err)
		assert.False(t, installed)
		assert.False(t, servesV1)
	})

	t.Run("installed and serving v1", func(t *testing.T) {
		installed, servesV1, err := tektonInstalled(context.Background(), &fakeObjectClient{getFunc: foreignTektonCRD})
		require.NoError(t, err)
		assert.True(t, installed)
		assert.True(t, servesV1)
	})

	t.Run("installed without v1", func(t *testing.T) {
		installed, servesV1, err := tektonInstalled(context.Background(), &fakeObjectClient{getFunc: oldTektonCRD})
		require.NoError(t, err)
		assert.True(t, installed)
		assert.False(t, servesV1)
	})

	t.Run("get error surfaces", func(t *testing.T) {
		_, _, err := tektonInstalled(context.Background(), &fakeObjectClient{getFunc: func(*unstructured.Unstructured) (*unstructured.Unstructured, error) {
			return nil, errors.New("api down")
		}})
		require.Error(t, err)
	})
}

func TestTektonAppliedByPact(t *testing.T) {
	t.Run("pact field manager stamp", func(t *testing.T) {
		applied, err := tektonAppliedByPact(context.Background(), &fakeObjectClient{getFunc: pactTektonCRD})
		require.NoError(t, err)
		assert.True(t, applied)
	})

	t.Run("another field manager", func(t *testing.T) {
		applied, err := tektonAppliedByPact(context.Background(), &fakeObjectClient{getFunc: foreignTektonCRD})
		require.NoError(t, err)
		assert.False(t, applied)
	})

	t.Run("absent CRD", func(t *testing.T) {
		applied, err := tektonAppliedByPact(context.Background(), &fakeObjectClient{getFunc: func(*unstructured.Unstructured) (*unstructured.Unstructured, error) {
			return nil, apierrors.NewNotFound(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, tektonPipelinesCRDName)
		}})
		require.NoError(t, err)
		assert.False(t, applied, "nothing pact applied is left")
	})
}
