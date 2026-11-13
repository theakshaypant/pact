package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/pkg/cluster"
)

// fakeClusterManager records calls and replays scripted errors.
type fakeClusterManager struct {
	gotProvider   string // provider the manager was constructed for
	gotKubeconfig string // explicit kubeconfig path, when one was given

	upCalled bool
	upOpts   cluster.UpOptions
	upErr    error
	upCtxErr error // ctx.Err() at call time, to assert timeout plumbing

	downCalled bool
	downOpts   cluster.DownOptions
	downErr    error

	kubeconfig    []byte
	kubeconfigErr error
}

func (f *fakeClusterManager) Up(ctx context.Context, opts cluster.UpOptions) error {
	f.upCalled, f.upOpts = true, opts
	f.upCtxErr = ctx.Err()
	return f.upErr
}

func (f *fakeClusterManager) Down(_ context.Context, opts cluster.DownOptions) error {
	f.downCalled, f.downOpts = true, opts
	return f.downErr
}

func (f *fakeClusterManager) Kubeconfig(context.Context) ([]byte, error) {
	return f.kubeconfig, f.kubeconfigErr
}

// runClusterCLI wires a fake cluster manager into the root command,
// executes args and returns the captured stdout, stderr, error and the
// manager that saw the call.
func runClusterCLI(t *testing.T, mgr *fakeClusterManager, newClusterErr error, args ...string) (string, string, *fakeClusterManager, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	d := deps{
		newCluster: func(opts cluster.Options) (clusterManager, error) {
			mgr.gotProvider = opts.Provider
			mgr.gotKubeconfig = opts.Kubeconfig
			if newClusterErr != nil {
				return nil, newClusterErr
			}
			return mgr, nil
		},
		out:    &out,
		errOut: &errOut,
	}
	root := newRootCmd(d)
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), mgr, err
}

func TestUpDefaultsToExternalAndPrintsReady(t *testing.T) {
	mgr := &fakeClusterManager{}
	out, _, _, err := runClusterCLI(t, mgr, nil, "up")
	require.NoError(t, err)
	assert.Equal(t, "external", mgr.gotProvider)
	assert.Empty(t, mgr.gotKubeconfig, "no --kubeconfig means discovery")
	assert.True(t, mgr.upCalled)
	assert.Empty(t, mgr.upOpts.TektonVersion, "no --tekton-version means the embedded manifest")
	assert.Contains(t, out, "tekton is ready")
	assert.Contains(t, out, "kubectl get pods -n tekton-pipelines")
}

func TestUpPlumbsExplicitKind(t *testing.T) {
	mgr := &fakeClusterManager{}
	_, _, _, err := runClusterCLI(t, mgr, nil, "up", "--provider", "kind")
	require.NoError(t, err)
	assert.Equal(t, "kind", mgr.gotProvider)
}

func TestUpPlumbsProviderAndTektonVersion(t *testing.T) {
	mgr := &fakeClusterManager{}
	_, _, _, err := runClusterCLI(t, mgr, nil,
		"up", "--provider", "external", "--tekton-version", "v1.19.0")
	require.NoError(t, err)
	assert.Equal(t, "external", mgr.gotProvider)
	assert.Empty(t, mgr.gotKubeconfig, "external without --kubeconfig discovers one")
	assert.Equal(t, "v1.19.0", mgr.upOpts.TektonVersion)
}

func TestUpKubeconfigImpliesExternal(t *testing.T) {
	mgr := &fakeClusterManager{}
	_, _, _, err := runClusterCLI(t, mgr, nil,
		"up", "--kubeconfig", "/some/kubeconfig")
	require.NoError(t, err)
	assert.Equal(t, "external", mgr.gotProvider)
	assert.Equal(t, "/some/kubeconfig", mgr.gotKubeconfig)
}

func TestUpRejectsKubeconfigWithExplicitKind(t *testing.T) {
	mgr := &fakeClusterManager{}
	_, _, _, err := runClusterCLI(t, mgr, nil,
		"up", "--provider", "kind", "--kubeconfig", "/some/kubeconfig")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--kubeconfig cannot be combined with --provider kind")
	assert.False(t, mgr.upCalled, "the manager must never be reached")
}

func TestUpRejectsUnknownProvider(t *testing.T) {
	_, _, mgr, err := runClusterCLI(t, &fakeClusterManager{},
		errors.New(`unknown cluster provider "minikube"`), "up", "--provider", "minikube")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "minikube")
	require.NotNil(t, mgr)
	assert.False(t, mgr.upCalled, "the manager must never be reached")
}

func TestUpSurfacesManagerError(t *testing.T) {
	mgr := &fakeClusterManager{upErr: errors.New("docker daemon not running")}
	_, _, _, err := runClusterCLI(t, mgr, nil, "up")
	require.Error(t, err)
	assert.ErrorIs(t, err, mgr.upErr)
}

func TestUpRejectsExtraArgs(t *testing.T) {
	_, _, _, err := runClusterCLI(t, &fakeClusterManager{}, nil, "up", "extra")
	require.Error(t, err)
}

func TestDownDefaultsToExternal(t *testing.T) {
	mgr := &fakeClusterManager{}
	_, _, _, err := runClusterCLI(t, mgr, nil, "down")
	require.NoError(t, err)
	assert.Equal(t, "external", mgr.gotProvider)
	assert.True(t, mgr.downCalled)
}

func TestDownExternal(t *testing.T) {
	mgr := &fakeClusterManager{}
	_, _, _, err := runClusterCLI(t, mgr, nil, "down", "--provider", "external")
	require.NoError(t, err)
	assert.Equal(t, "external", mgr.gotProvider)
	assert.True(t, mgr.downCalled)
}

func TestDownKubeconfigImpliesExternal(t *testing.T) {
	mgr := &fakeClusterManager{}
	_, _, _, err := runClusterCLI(t, mgr, nil, "down", "--kubeconfig", "/some/kubeconfig")
	require.NoError(t, err)
	assert.Equal(t, "external", mgr.gotProvider)
	assert.Equal(t, "/some/kubeconfig", mgr.gotKubeconfig)
	assert.True(t, mgr.downCalled)
}

func TestDownPlumbsForce(t *testing.T) {
	mgr := &fakeClusterManager{}
	_, _, _, err := runClusterCLI(t, mgr, nil, "down", "--provider", "external", "--force")
	require.NoError(t, err)
	assert.True(t, mgr.downCalled)
	assert.True(t, mgr.downOpts.Force)
}

func TestDownSurfacesManagerError(t *testing.T) {
	mgr := &fakeClusterManager{downErr: errors.New("refused to delete")}
	_, _, _, err := runClusterCLI(t, mgr, nil, "down")
	require.Error(t, err)
	assert.ErrorIs(t, err, mgr.downErr)
}

func TestUpAppliesTimeoutToContext(t *testing.T) {
	// A zero timeout makes the context already expired at Up time,
	// pinning the context.WithTimeout wiring without real waiting.
	mgr := &fakeClusterManager{}
	_, _, _, err := runClusterCLI(t, mgr, nil, "up", "--timeout", "0")
	require.NoError(t, err)
	assert.True(t, mgr.upCalled)
	assert.Error(t, mgr.upCtxErr, "the --timeout flag must bound Up's context")
}
