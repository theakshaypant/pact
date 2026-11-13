package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	kind "sigs.k8s.io/kind/pkg/cluster"
)

// newTestKindProvider wires a kindProvider with scripted kind operations.
// The scripted kubeconfig returns content, like the real kind API.
func newTestKindProvider(list func() ([]string, error), create func(name string, opts ...kind.CreateOption) error,
	delete func(name, kubeconfigPath string) error) *kindProvider {
	return &kindProvider{
		name:       "pact-local",
		list:       list,
		create:     create,
		delete:     delete,
		kubeconfig: func(name string, internal bool) (string, error) { return "apiVersion: v1\nkind: Config\n", nil },
	}
}

func TestKindProviderEnsureReusesExistingCluster(t *testing.T) {
	createCalled := false
	p := newTestKindProvider(
		func() ([]string, error) { return []string{"other-cluster", "pact-local"}, nil },
		func(name string, opts ...kind.CreateOption) error {
			createCalled = true
			return nil
		},
		func(name, kubeconfigPath string) error { return nil },
	)

	created, err := p.Ensure(context.Background())
	require.NoError(t, err)
	assert.False(t, created, "an existing cluster must not be recreated")
	assert.False(t, createCalled)
}

func TestKindProviderEnsureCreatesMissingCluster(t *testing.T) {
	var createdName string
	p := newTestKindProvider(
		func() ([]string, error) { return nil, nil },
		func(name string, opts ...kind.CreateOption) error {
			createdName = name
			return nil
		},
		func(name, kubeconfigPath string) error { return nil },
	)

	created, err := p.Ensure(context.Background())
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "pact-local", createdName)
}

func TestKindProviderEnsureWrapsDockerFailure(t *testing.T) {
	p := newTestKindProvider(
		func() ([]string, error) { return nil, errors.New("docker ps failed") },
		func(name string, opts ...kind.CreateOption) error { return nil },
		func(name, kubeconfigPath string) error { return nil },
	)

	_, err := p.Ensure(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is the docker daemon running?")
	assert.Contains(t, err.Error(), "docker ps failed")
}

func TestKindProviderEnsureWrapsCreateFailure(t *testing.T) {
	p := newTestKindProvider(
		func() ([]string, error) { return nil, nil },
		func(name string, opts ...kind.CreateOption) error { return errors.New("node image pull failed") },
		func(name, kubeconfigPath string) error { return nil },
	)

	_, err := p.Ensure(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `creating kind cluster "pact-local"`)
	assert.Contains(t, err.Error(), "node image pull failed")
}

func TestKindProviderDelete(t *testing.T) {
	var deletedName, kubeconfigArg string
	p := newTestKindProvider(
		func() ([]string, error) { return []string{"pact-local"}, nil },
		func(name string, opts ...kind.CreateOption) error { return nil },
		func(name, kubeconfigPath string) error {
			deletedName, kubeconfigArg = name, kubeconfigPath
			return nil
		},
	)

	require.NoError(t, p.Delete(context.Background(), false))
	assert.Equal(t, "pact-local", deletedName)
	assert.Empty(t, kubeconfigArg, "empty explicit kubeconfig path lets kind use the default")
}

func TestKindProviderDescribeAndKubeconfig(t *testing.T) {
	p := newTestKindProvider(
		func() ([]string, error) { return nil, nil },
		func(name string, opts ...kind.CreateOption) error { return nil },
		func(name, kubeconfigPath string) error { return nil },
	)
	assert.Equal(t, `kind cluster "pact-local"`, p.Describe())

	kc, err := p.Kubeconfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []byte("apiVersion: v1\nkind: Config\n"), kc,
		"the kind API returns kubeconfig content, not a path")
}
