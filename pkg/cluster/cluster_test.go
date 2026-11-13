package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// fakeProvider is a scripted Provider for Manager tests.
type fakeProvider struct {
	describe   string
	removed    string
	ensure     func(ctx context.Context) (bool, error)
	kubeconfig func(ctx context.Context) ([]byte, error)
	delete     func(ctx context.Context, force bool) error
}

func (f *fakeProvider) Describe() string { return f.describe }

func (f *fakeProvider) Removed() string { return f.removed }

func (f *fakeProvider) Ensure(ctx context.Context) (bool, error) {
	return f.ensure(ctx)
}

func (f *fakeProvider) Kubeconfig(ctx context.Context) ([]byte, error) {
	return f.kubeconfig(ctx)
}

func (f *fakeProvider) Delete(ctx context.Context, force bool) error {
	return f.delete(ctx, force)
}

// fakeObjectClient records operations and scripts Get responses. Without
// a getFunc it models a cluster with nothing pre-installed: an object is
// NotFound until pact has applied it, then reports Established.
type fakeObjectClient struct {
	applied []string
	deleted []string
	getFunc func(obj *unstructured.Unstructured) (*unstructured.Unstructured, error)
}

func key(obj *unstructured.Unstructured) string {
	return obj.GetKind() + "/" + obj.GetName()
}

func (f *fakeObjectClient) Apply(_ context.Context, obj *unstructured.Unstructured) error {
	f.applied = append(f.applied, key(obj))
	return nil
}

func (f *fakeObjectClient) Get(_ context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if f.getFunc != nil {
		return f.getFunc(obj)
	}
	for _, k := range f.applied {
		if k == key(obj) {
			return establishedCRD(obj)
		}
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: obj.GroupVersionKind().Group, Resource: obj.GetKind()}, obj.GetName())
}

func (f *fakeObjectClient) Delete(_ context.Context, obj *unstructured.Unstructured) error {
	f.deleted = append(f.deleted, key(obj))
	return nil
}

// fakeLister scripts deployment availability.
type fakeLister struct {
	calls     int
	available func(call int) (bool, error)
}

func (f *fakeLister) IsAvailable(_ context.Context, _, _ string) (bool, error) {
	f.calls++
	return f.available(f.calls)
}

// newTestManager wires a Manager entirely from fakes.
func newTestManager(t *testing.T, provider Provider, objClient objectClient, lister deploymentLister) (*Manager, *bytes.Buffer, *[]byte) {
	t.Helper()
	var progress bytes.Buffer
	var gotKubeconfig []byte
	m := &Manager{
		provider: provider,
		newClients: func(kubeconfig []byte) (objectClient, deploymentLister, error) {
			gotKubeconfig = kubeconfig
			return objClient, lister, nil
		},
		progress: &progress,
	}
	return m, &progress, &gotKubeconfig
}

func TestNewRejectsUnknownProvider(t *testing.T) {
	_, err := New(Options{Provider: "minikube"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown cluster provider")
	assert.Contains(t, err.Error(), `"kind" or "external"`)
}

func TestNewDefaultsToExternal(t *testing.T) {
	m, err := New(Options{})
	require.NoError(t, err)
	_, isExternal := m.provider.(*externalProvider)
	assert.True(t, isExternal, "default provider should be external")
}

func TestNewKind(t *testing.T) {
	m, err := New(Options{Provider: ProviderKind})
	require.NoError(t, err)
	_, isKind := m.provider.(*kindProvider)
	assert.True(t, isKind, "kind must be selected explicitly")
}

func TestNewExternal(t *testing.T) {
	m, err := New(Options{Provider: ProviderExternal})
	require.NoError(t, err)
	_, isExternal := m.provider.(*externalProvider)
	assert.True(t, isExternal)
}

func TestNewRejectsKubeconfigWithKind(t *testing.T) {
	_, err := New(Options{Provider: ProviderKind, Kubeconfig: "/some/kubeconfig"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "applies only to the external provider")
}

func TestManagerUpCreatedFlow(t *testing.T) {
	tests := []struct {
		name    string
		created bool
		want    string
	}{
		{name: "fresh cluster", created: true, want: "created kind cluster \"pact-local\""},
		{name: "existing cluster", created: false, want: "reusing kind cluster \"pact-local\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &fakeProvider{
				describe: `kind cluster "pact-local"`,
				ensure:   func(context.Context) (bool, error) { return tt.created, nil },
				kubeconfig: func(context.Context) ([]byte, error) {
					return []byte("fake-kubeconfig"), nil
				},
				delete: func(context.Context, bool) error { return nil },
			}
			// No getFunc: the fake cluster has no Tekton yet, so Up must
			// install the embedded manifest.
			objClient := &fakeObjectClient{}
			lister := &fakeLister{available: func(int) (bool, error) { return true, nil }}
			m, progress, gotKubeconfig := newTestManager(t, provider, objClient, lister)

			require.NoError(t, m.Up(context.Background(), UpOptions{}))
			assert.Equal(t, []byte("fake-kubeconfig"), *gotKubeconfig)
			assert.Contains(t, progress.String(), tt.want)
			assert.Contains(t, progress.String(), "installing tekton pipelines (embedded "+embeddedTektonVersion+")")
			assert.Contains(t, progress.String(), "waiting for tekton to become ready")
			assert.NotEmpty(t, objClient.applied, "the embedded manifest must be applied")
		})
	}
}

func TestManagerUpWrapsErrors(t *testing.T) {
	tests := []struct {
		name     string
		provider *fakeProvider
		lister   *fakeLister
		want     string
	}{
		{
			name: "ensure fails",
			provider: &fakeProvider{
				describe: "external cluster",
				ensure:   func(context.Context) (bool, error) { return false, errors.New("boom") },
			},
			want: "ensuring external cluster: boom",
		},
		{
			name: "kubeconfig fails",
			provider: &fakeProvider{
				describe:   "external cluster",
				ensure:     func(context.Context) (bool, error) { return false, nil },
				kubeconfig: func(context.Context) ([]byte, error) { return nil, errors.New("no kubeconfig") },
			},
			want: "locating kubeconfig for external cluster: no kubeconfig",
		},
		{
			name: "wait fails",
			provider: &fakeProvider{
				describe:   "external cluster",
				ensure:     func(context.Context) (bool, error) { return false, nil },
				kubeconfig: func(context.Context) ([]byte, error) { return []byte("fake-kubeconfig"), nil },
			},
			lister: &fakeLister{available: func(int) (bool, error) {
				return false, errors.New("api down")
			}},
			want: "waiting for tekton readiness: api down",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Tekton is present (and healthy), so the wait phase is
			// reached without installing.
			objClient := &fakeObjectClient{getFunc: foreignTektonCRD}
			lister := tt.lister
			if lister == nil {
				lister = &fakeLister{available: func(int) (bool, error) { return true, nil }}
			}
			m, _, _ := newTestManager(t, tt.provider, objClient, lister)
			err := m.Up(context.Background(), UpOptions{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestManagerUpRejectsBadTektonVersion(t *testing.T) {
	ensureCalled := false
	provider := &fakeProvider{
		describe: "external cluster",
		ensure: func(context.Context) (bool, error) {
			ensureCalled = true
			return false, nil
		},
		kubeconfig: func(context.Context) ([]byte, error) { return []byte("fake-kubeconfig"), nil },
	}
	m, _, _ := newTestManager(t, provider, &fakeObjectClient{getFunc: establishedCRD},
		&fakeLister{available: func(int) (bool, error) { return true, nil }})

	err := m.Up(context.Background(), UpOptions{TektonVersion: "latest"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid tekton version")
	assert.False(t, ensureCalled, "an invalid version must fail before the cluster is touched")
}

func TestManagerDownDelegatesAndWraps(t *testing.T) {
	var gotForce []bool
	provider := &fakeProvider{
		describe: `kind cluster "pact-local"`,
		removed:  `kind cluster "pact-local"`,
		ensure:   func(context.Context) (bool, error) { return false, nil },
		kubeconfig: func(context.Context) ([]byte, error) {
			return []byte("fake-kubeconfig"), nil
		},
		delete: func(_ context.Context, force bool) error {
			gotForce = append(gotForce, force)
			return nil
		},
	}
	m, progress, _ := newTestManager(t, provider, &fakeObjectClient{},
		&fakeLister{available: func(int) (bool, error) { return true, nil }})
	require.NoError(t, m.Down(context.Background(), DownOptions{}))
	assert.Equal(t, []bool{false}, gotForce)
	assert.Contains(t, progress.String(), `removed kind cluster "pact-local"`)

	require.NoError(t, m.Down(context.Background(), DownOptions{Force: true}))
	assert.Equal(t, []bool{false, true}, gotForce, "the force option must reach the provider")

	provider.delete = func(context.Context, bool) error { return errors.New("docker refused") }
	err := m.Down(context.Background(), DownOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `tearing down kind cluster "pact-local": docker refused`)
}

func TestManagerUpReusesExistingTekton(t *testing.T) {
	provider := &fakeProvider{
		describe:   "external cluster",
		ensure:     func(context.Context) (bool, error) { return false, nil },
		kubeconfig: func(context.Context) ([]byte, error) { return []byte("fake-kubeconfig"), nil },
		delete:     func(context.Context, bool) error { return nil },
	}
	objClient := &fakeObjectClient{getFunc: foreignTektonCRD}
	lister := &fakeLister{available: func(int) (bool, error) { return true, nil }}
	m, progress, _ := newTestManager(t, provider, objClient, lister)

	require.NoError(t, m.Up(context.Background(), UpOptions{}))
	assert.Empty(t, objClient.applied, "an existing installation must not be touched")
	assert.Contains(t, progress.String(), "reusing existing tekton installation")
	assert.Contains(t, progress.String(), "waiting for tekton to become ready")
	assert.Positive(t, lister.calls, "the existing installation must still be verified ready")
}

func TestManagerUpRejectsTektonWithoutV1(t *testing.T) {
	provider := &fakeProvider{
		describe:   "external cluster",
		ensure:     func(context.Context) (bool, error) { return false, nil },
		kubeconfig: func(context.Context) ([]byte, error) { return []byte("fake-kubeconfig"), nil },
		delete:     func(context.Context, bool) error { return nil },
	}
	objClient := &fakeObjectClient{getFunc: oldTektonCRD}
	lister := &fakeLister{available: func(int) (bool, error) { return true, nil }}
	m, _ /*progress*/, _ := newTestManager(t, provider, objClient, lister)

	err := m.Up(context.Background(), UpOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not serve tekton.dev/v1")
	assert.Contains(t, err.Error(), "--tekton-version")
	assert.Empty(t, objClient.applied, "an incompatible installation must not be touched")
}

func TestManagerUpWithTektonVersionInstallsOverExisting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: tekton-pipelines\n")
	}))
	t.Cleanup(server.Close)
	oldURL := tektonReleaseURL
	tektonReleaseURL = server.URL + "/download/%s/release.yaml"
	t.Cleanup(func() { tektonReleaseURL = oldURL })

	provider := &fakeProvider{
		describe:   "external cluster",
		ensure:     func(context.Context) (bool, error) { return false, nil },
		kubeconfig: func(context.Context) ([]byte, error) { return []byte("fake-kubeconfig"), nil },
		delete:     func(context.Context, bool) error { return nil },
	}
	objClient := &fakeObjectClient{getFunc: foreignTektonCRD}
	lister := &fakeLister{available: func(int) (bool, error) { return true, nil }}
	m, progress, _ := newTestManager(t, provider, objClient, lister)

	require.NoError(t, m.Up(context.Background(), UpOptions{TektonVersion: "v1.19.0"}))
	assert.NotEmpty(t, objClient.applied, "an explicit version must install even over existing tekton")
	assert.Contains(t, progress.String(), "installing tekton pipelines (fetched v1.19.0)")
}

func TestManagerUpHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := &fakeProvider{
		describe: "external cluster",
		ensure: func(ctx context.Context) (bool, error) {
			return false, ctx.Err()
		},
	}
	m, _, _ := newTestManager(t, provider, &fakeObjectClient{getFunc: establishedCRD},
		&fakeLister{available: func(int) (bool, error) { return true, nil }})
	err := m.Up(ctx, UpOptions{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "want context.Canceled, got %v", err)
	assert.True(t, strings.Contains(err.Error(), "ensuring"), "want wrap location, got %v", err)
}

// establishedCRD is a Get response reporting an Established CRD.
func establishedCRD(obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if obj == nil {
		return nil, fmt.Errorf("establishedCRD: no object")
	}
	live := obj.DeepCopy()
	live.Object["status"] = map[string]any{
		"conditions": []any{map[string]any{"type": "Established", "status": "True"}},
	}
	return live, nil
}

// tektonCRDResponse builds a Get response for the Tekton CRD probe: an
// Established CRD serving the given versions and stamped with the given
// field managers.
func tektonCRDResponse(obj *unstructured.Unstructured, versions []any, managers ...string) (*unstructured.Unstructured, error) {
	live, err := establishedCRD(obj)
	if err != nil {
		return nil, err
	}
	live.Object["spec"] = map[string]any{"versions": versions}
	if len(managers) > 0 {
		fields := make([]any, 0, len(managers))
		for _, m := range managers {
			fields = append(fields, map[string]any{"manager": m})
		}
		if err := unstructured.SetNestedField(live.Object, fields, "metadata", "managedFields"); err != nil {
			return nil, err
		}
	}
	return live, nil
}

// foreignTektonCRD is a Get response for a Tekton CRD installed by
// someone else: Established, serving v1, stamped with kubectl.
func foreignTektonCRD(obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return tektonCRDResponse(obj,
		[]any{map[string]any{"name": "v1", "served": true}},
		"kubectl-client-side-apply")
}

// pactTektonCRD is a Get response for a Tekton CRD pact applied:
// Established, serving v1, stamped with pact's field manager.
func pactTektonCRD(obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return tektonCRDResponse(obj,
		[]any{map[string]any{"name": "v1", "served": true}},
		fieldManager)
}

// oldTektonCRD is a Get response for a Tekton install too old to serve
// tekton.dev/v1.
func oldTektonCRD(obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return tektonCRDResponse(obj,
		[]any{map[string]any{"name": "v1beta1", "served": true}},
		fieldManager)
}
