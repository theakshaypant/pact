package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"github.com/theakshaypant/pact/pkg/cluster"
	"github.com/theakshaypant/pact/pkg/event"
	"github.com/theakshaypant/pact/pkg/git"
	"github.com/theakshaypant/pact/pkg/pac"
	"github.com/theakshaypant/pact/pkg/runner"
)

// fakeRunRunner records the runner call and replays a scripted result.
type fakeRunRunner struct {
	gotKubeconfig []byte
	gotRuns       []*tektonv1.PipelineRun
	gotOpts       runner.Options
	result        runner.Result
	err           error
}

func (f *fakeRunRunner) Run(_ context.Context, kubeconfig []byte, runs []*tektonv1.PipelineRun,
	opts runner.Options) (runner.Result, error) {
	f.gotKubeconfig, f.gotRuns, f.gotOpts = kubeconfig, runs, opts
	return f.result, f.err
}

// runCLIRunner wires fakes for the run command: a git snapshot, an
// engine, a cluster manager and a runner.
func runCLIRunner(t *testing.T, snap *git.Snapshot, eng pac.Engine, mgr *fakeClusterManager,
	rr *fakeRunRunner, args ...string) (string, string, error) {
	t.Helper()
	insp := &fakeInspector{snap: snap}
	var out, errOut bytes.Buffer
	d := deps{
		openGit: func(string) (git.Inspector, error) { return insp, nil },
		newEngine: func(bool) pac.Engine {
			return eng
		},
		newCluster: func(opts cluster.Options) (clusterManager, error) {
			mgr.gotProvider = opts.Provider
			mgr.gotKubeconfig = opts.Kubeconfig
			return mgr, nil
		},
		newRunner: func(io.Writer) runRunner { return rr },
		out:       &out,
		errOut:    &errOut,
	}
	root := newRootCmd(d)
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// runEngine returns a fakeEngine resolving one PipelineRun named name.
func runEngine(name string) *fakeEngine {
	return &fakeEngine{
		matchMatches: []pac.Match{{File: "pipe.yaml", PipelineRun: name}},
		resolveOut: []byte("apiVersion: tekton.dev/v1\nkind: PipelineRun\nmetadata:\n  name: " + name + "\n" +
			"spec:\n  pipelineSpec:\n    tasks: []\n"),
	}
}

func TestRunExecutesMatchingPipelines(t *testing.T) {
	dir := t.TempDir()
	mgr := &fakeClusterManager{kubeconfig: []byte("kc-content")}
	rr := &fakeRunRunner{result: runner.Result{Runs: []runner.PipelineRunResult{
		{Name: "pr-a", Success: true},
	}}}

	out, _, err := runCLIRunner(t, cliSnapshot(dir), runEngine("pr-a"), mgr, rr, "run")
	require.NoError(t, err)
	assert.True(t, mgr.upCalled, "run must ensure the cluster first")
	assert.Equal(t, []byte("kc-content"), rr.gotKubeconfig)
	require.Len(t, rr.gotRuns, 1)
	assert.Equal(t, "pr-a", rr.gotRuns[0].Name)
	assert.Equal(t, dir, rr.gotOpts.RepoRoot, "the runner must package the repository root")
	assert.Contains(t, out, "pr-a: succeeded")
}

func TestRunFailureIsAnError(t *testing.T) {
	mgr := &fakeClusterManager{kubeconfig: []byte("kc")}
	rr := &fakeRunRunner{result: runner.Result{Runs: []runner.PipelineRunResult{
		{Name: "pr-a", Success: false, Message: "task build failed"},
	}}}

	out, _, err := runCLIRunner(t, cliSnapshot(t.TempDir()), runEngine("pr-a"), mgr, rr, "run")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 of 1 pipelinerun(s) failed")
	assert.Contains(t, out, "pr-a: failed: task build failed")
}

func TestRunNoMatchExitsZero(t *testing.T) {
	mgr := &fakeClusterManager{kubeconfig: []byte("kc")}
	rr := &fakeRunRunner{}

	out, _, err := runCLIRunner(t, cliSnapshot(t.TempDir()), &fakeEngine{}, mgr, rr, "run")
	require.NoError(t, err)
	assert.Contains(t, out, "no matching pipelines")
	assert.Empty(t, rr.gotRuns, "the runner must not be called without a match")
	assert.False(t, mgr.upCalled, "no cluster work is needed without a match")
}

func TestRunPlumbsFlags(t *testing.T) {
	mgr := &fakeClusterManager{kubeconfig: []byte("kc")}
	rr := &fakeRunRunner{result: runner.Result{}}

	_, _, err := runCLIRunner(t, cliSnapshot(t.TempDir()), runEngine("pr-a"), mgr, rr,
		"run", "--workspace", "code", "--workspace-size", "5Gi", "--keep-clone-tasks",
		"--no-cleanup", "--provider", "external")
	require.NoError(t, err)
	assert.Equal(t, "external", mgr.gotProvider)
	assert.Equal(t, "code", rr.gotOpts.Workspace)
	assert.Equal(t, "5Gi", rr.gotOpts.WorkspaceSize)
	assert.True(t, rr.gotOpts.KeepCloneTasks)
	assert.True(t, rr.gotOpts.NoCleanup)
}

func TestRunSurfacesClusterError(t *testing.T) {
	mgr := &fakeClusterManager{kubeconfig: []byte("kc"), upErr: errors.New("docker exploded")}
	rr := &fakeRunRunner{}

	_, _, err := runCLIRunner(t, cliSnapshot(t.TempDir()), runEngine("pr-a"), mgr, rr, "run")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker exploded")
}

func TestRunSurfacesRunnerError(t *testing.T) {
	mgr := &fakeClusterManager{kubeconfig: []byte("kc")}
	rr := &fakeRunRunner{err: errors.New("upload failed")}

	_, _, err := runCLIRunner(t, cliSnapshot(t.TempDir()), runEngine("pr-a"), mgr, rr, "run")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload failed")
}

func TestRunRejectsExtraArgs(t *testing.T) {
	mgr := &fakeClusterManager{kubeconfig: []byte("kc")}
	_, _, err := runCLIRunner(t, cliSnapshot(t.TempDir()), runEngine("pr-a"), mgr, &fakeRunRunner{},
		"run", "extra")
	require.Error(t, err)
}

// The event plumbing is shared with match/resolve; pin that run honours
// the same flags.
func TestRunEventFlagsPlumbThrough(t *testing.T) {
	dir := t.TempDir()
	eng := runEngine("pr-a")
	mgr := &fakeClusterManager{kubeconfig: []byte("kc")}
	rr := &fakeRunRunner{}

	_, _, err := runCLIRunner(t, cliSnapshot(dir), eng, mgr, rr, "run", "--event", "push", "--pr-title", "T")
	require.NoError(t, err)
	require.NotNil(t, eng.gotEvent)
	assert.Equal(t, event.Push, eng.gotEvent.Type)
	assert.Equal(t, "T", eng.gotEvent.PRTitle)
}
