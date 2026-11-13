package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestExternal wires an externalProvider with scripted kubeconfig
// resolution and ping.
func newTestExternal(kubeconfig func() (string, []byte, error),
	ping func(ctx context.Context, content []byte) error, newClients clientsFactory) *externalProvider {
	return &externalProvider{kubeconfig: kubeconfig, ping: ping, newClients: newClients}
}

func TestExternalEnsureVerifiesReachability(t *testing.T) {
	var pingedContent []byte
	p := newTestExternal(
		func() (string, []byte, error) { return "/some/kubeconfig", []byte("kc-content"), nil },
		func(_ context.Context, content []byte) error {
			pingedContent = content
			return nil
		},
		nil,
	)

	created, err := p.Ensure(context.Background())
	require.NoError(t, err)
	assert.False(t, created, "an external cluster is never created by pact")
	assert.Equal(t, []byte("kc-content"), pingedContent)
}

func TestExternalEnsureWrapsUnreachableAPI(t *testing.T) {
	p := newTestExternal(
		func() (string, []byte, error) { return "/some/kubeconfig", []byte("kc-content"), nil },
		func(context.Context, []byte) error { return errors.New("connection refused") },
		nil,
	)

	_, err := p.Ensure(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "external cluster API unreachable via /some/kubeconfig")
	assert.Contains(t, err.Error(), "is the cluster running and reachable")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestExternalEnsureSurfacesKubeconfigError(t *testing.T) {
	p := newTestExternal(
		func() (string, []byte, error) { return "", nil, errors.New("no kubeconfig found") },
		func(context.Context, []byte) error { return nil },
		nil,
	)

	_, err := p.Ensure(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no kubeconfig found")
}

func TestExternalKubeconfigReturnsContent(t *testing.T) {
	p := newTestExternal(
		func() (string, []byte, error) { return "/some/kubeconfig", []byte("kc-content"), nil },
		func(context.Context, []byte) error { return nil },
		nil,
	)

	content, err := p.Kubeconfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []byte("kc-content"), content)
}

func TestExternalDeleteUninstallsTekton(t *testing.T) {
	objClient := &fakeObjectClient{getFunc: pactTektonCRD}
	var gotKubeconfig []byte
	p := newTestExternal(
		func() (string, []byte, error) { return "/some/kubeconfig", []byte("kc-content"), nil },
		func(context.Context, []byte) error { return nil },
		func(kubeconfig []byte) (objectClient, deploymentLister, error) {
			gotKubeconfig = kubeconfig
			return objClient, nil, nil
		},
	)

	require.NoError(t, p.Delete(context.Background(), false))
	assert.Equal(t, []byte("kc-content"), gotKubeconfig)
	assert.NotEmpty(t, objClient.deleted, "the tekton install must be deleted")
}

func TestExternalDeleteRefusesForeignTekton(t *testing.T) {
	objClient := &fakeObjectClient{getFunc: foreignTektonCRD}
	p := newTestExternal(
		func() (string, []byte, error) { return "/some/kubeconfig", []byte("kc-content"), nil },
		func(context.Context, []byte) error { return nil },
		func(kubeconfig []byte) (objectClient, deploymentLister, error) {
			return objClient, nil, nil
		},
	)

	err := p.Delete(context.Background(), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "was not installed by pact")
	assert.Contains(t, err.Error(), "--force")
	assert.Empty(t, objClient.deleted, "a foreign tekton install must be left in place")
}

func TestExternalDeleteForceRemovesForeignTekton(t *testing.T) {
	objClient := &fakeObjectClient{getFunc: foreignTektonCRD}
	p := newTestExternal(
		func() (string, []byte, error) { return "/some/kubeconfig", []byte("kc-content"), nil },
		func(context.Context, []byte) error { return nil },
		func(kubeconfig []byte) (objectClient, deploymentLister, error) {
			return objClient, nil, nil
		},
	)

	require.NoError(t, p.Delete(context.Background(), true))
	assert.NotEmpty(t, objClient.deleted, "force must remove even a foreign install")
}

func TestNewExternalProviderExplicitKubeconfig(t *testing.T) {
	path := writeFile(t, t.TempDir(), "kubeconfig")
	p := newExternalProvider(path, nil)

	gotPath, content, err := p.kubeconfig()
	require.NoError(t, err)
	assert.Equal(t, path, gotPath)
	assert.Equal(t, []byte("apiVersion: v1\n"), content)

	assert.Contains(t, p.Describe(), path, "the progress messages name the explicit kubeconfig")
}

func TestNewExternalProviderExplicitKubeconfigUnreadable(t *testing.T) {
	p := newExternalProvider(filepath.Join(t.TempDir(), "missing"), nil)

	_, _, err := p.kubeconfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading kubeconfig")
}

// writeFile creates a file with content under dir.
func writeFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("apiVersion: v1\n"), 0o644))
	return path
}

func TestDiscoverKubeconfig(t *testing.T) {
	homeDir := t.TempDir()
	userPath := writeFile(t, homeDir, ".kube/config")
	emptyGetenv := func(string) string { return "" }

	t.Run("first KUBECONFIG entry wins", func(t *testing.T) {
		envPath := writeFile(t, t.TempDir(), "custom/kubeconfig")
		getenv := func(string) string { return envPath + string(os.PathListSeparator) + userPath }
		got, err := discoverKubeconfig(getenv, homeDir)
		require.NoError(t, err)
		assert.Equal(t, envPath, got)
	})

	t.Run("user default when KUBECONFIG unset", func(t *testing.T) {
		got, err := discoverKubeconfig(emptyGetenv, homeDir)
		require.NoError(t, err)
		assert.Equal(t, userPath, got)
	})

	t.Run("nothing found lists candidates and hints", func(t *testing.T) {
		_, err := discoverKubeconfig(emptyGetenv, t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no kubeconfig found")
		assert.Contains(t, err.Error(), "tried")
		assert.Contains(t, err.Error(), "--kubeconfig")
	})

	t.Run("unreadable candidate is skipped", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can read any file, so unreadability cannot be simulated")
		}
		unreadable := filepath.Join(t.TempDir(), "kubeconfig")
		require.NoError(t, os.WriteFile(unreadable, []byte("apiVersion: v1\n"), 0o000))
		getenv := func(string) string { return unreadable }
		got, err := discoverKubeconfig(getenv, homeDir)
		require.NoError(t, err)
		assert.Equal(t, userPath, got, "the unreadable file must not be chosen")
	})
}

func TestPingAPI(t *testing.T) {
	t.Run("invalid kubeconfig content is a load error", func(t *testing.T) {
		err := pingAPI(context.Background(), []byte("not a kubeconfig"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loading kubeconfig")
	})

	t.Run("cancelled context", func(t *testing.T) {
		// A syntactically valid but unreachable kubeconfig: the ping
		// must fail, and a cancelled context must surface.
		content := []byte("apiVersion: v1\nkind: Config\nclusters:\n" +
			"- name: c\n  cluster:\n    server: https://127.0.0.1:1\n" +
			"contexts:\n- name: c\n  context:\n    cluster: c\n    user: u\n" +
			"current-context: c\nusers:\n- name: u\n  user: {}\n")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := pingAPI(ctx, content)
		require.Error(t, err)
	})
}
