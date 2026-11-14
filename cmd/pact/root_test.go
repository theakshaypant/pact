package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/pkg/event"
	"github.com/theakshaypant/pact/pkg/git"
	"github.com/theakshaypant/pact/pkg/pac"
	"github.com/theakshaypant/pact/test/pkg/pacdef"
)

// fakeInspector returns a canned snapshot and records the options it got.
type fakeInspector struct {
	snap   *git.Snapshot
	opts   git.Options
	gotDir string
}

func (f *fakeInspector) Snapshot(_ context.Context, opts git.Options) (*git.Snapshot, error) {
	f.opts = opts
	return f.snap, nil
}

// fakeEngine records its inputs and replays canned outputs.
type fakeEngine struct {
	matchMatches []pac.Match
	matchErr     error
	resolveOut   []byte
	resolveErr   error

	gotDir   string
	gotEvent *event.Event
}

func (f *fakeEngine) Match(_ context.Context, dir string, ev *event.Event) ([]pac.Match, error) {
	f.gotDir, f.gotEvent = dir, ev
	return f.matchMatches, f.matchErr
}

func (f *fakeEngine) Resolve(_ context.Context, dir string, ev *event.Event) ([]byte, error) {
	f.gotDir, f.gotEvent = dir, ev
	return f.resolveOut, f.resolveErr
}

// cliSnapshot builds a realistic GitHub snapshot rooted at repoRoot.
func cliSnapshot(repoRoot string) *git.Snapshot {
	return &git.Snapshot{
		RepoRoot:     repoRoot,
		Branch:       "feature",
		HeadSHA:      "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		HeadTitle:    "add feature",
		Author:       "Dev",
		RemoteURL:    "git@github.com:example/repo.git",
		Remote:       git.Remote{Host: "github.com", Owner: "example", Repo: "repo", IsGitHub: true},
		TargetBranch: "main",
		BaseSHA:      "1234561234561234561234561234561234561234",
		ChangedFiles: git.ChangedFiles{All: []string{"src/main.go"}, Added: []string{"src/main.go"}},
	}
}

// runCLI wires fakes into the root command, executes args and returns the
// captured stdout, stderr, error and the inspector that saw the call.
func runCLI(t *testing.T, snap *git.Snapshot, eng pac.Engine, args ...string) (string, string, *fakeInspector, error) {
	t.Helper()
	insp := &fakeInspector{snap: snap}
	var out, errOut bytes.Buffer
	d := deps{
		openGit: func(dir string) (git.Inspector, error) {
			insp.gotDir = dir
			return insp, nil
		},
		newEngine: func(bool, pac.Params) pac.Engine { return eng },
		out:       &out,
		errOut:    &errOut,
	}
	root := newRootCmd(d)
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), insp, err
}

// fakeEng is a minimal fakeEngine value for tests that don't inspect it.
func fakeEng() *fakeEngine { return &fakeEngine{} }

func TestMatchPrintsFilenames(t *testing.T) {
	eng := &fakeEngine{matchMatches: []pac.Match{{File: "a.yaml", PipelineRun: "a"}, {File: "b.yml", PipelineRun: "b"}}}
	out, _, _, err := runCLI(t, cliSnapshot(t.TempDir()), eng, "match")
	require.NoError(t, err)
	assert.Equal(t, "a.yaml\nb.yml\n", out)
}

func TestMatchDeduplicatesFiles(t *testing.T) {
	// Two matching PipelineRuns in one file: the file is printed once.
	eng := &fakeEngine{matchMatches: []pac.Match{
		{File: "a.yaml", PipelineRun: "first"},
		{File: "a.yaml", PipelineRun: "second"},
	}}
	out, _, _, err := runCLI(t, cliSnapshot(t.TempDir()), eng, "match")
	require.NoError(t, err)
	assert.Equal(t, "a.yaml\n", out)
}

func TestMatchNoMatchIsEmptyAndExitsZero(t *testing.T) {
	out, _, _, err := runCLI(t, cliSnapshot(t.TempDir()), fakeEng(), "match")
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestResolvePrintsYAML(t *testing.T) {
	eng := &fakeEngine{resolveOut: []byte("---\nkind: PipelineRun\n")}
	out, _, _, err := runCLI(t, cliSnapshot(t.TempDir()), eng, "resolve")
	require.NoError(t, err)
	assert.Equal(t, "---\nkind: PipelineRun\n", out)
}

func TestMatchErrorsSurfaceAsCommandError(t *testing.T) {
	eng := &fakeEngine{matchErr: assert.AnError}
	_, _, _, err := runCLI(t, cliSnapshot(t.TempDir()), eng, "match")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestEventFlagsPlumbThrough(t *testing.T) {
	dir := t.TempDir()
	eng := fakeEng()
	_, _, _, err := runCLI(t, cliSnapshot(dir), eng,
		"match", "--event", "push", "--sender", "ops", "--pr-title", "My title", "--pr-number", "7")
	require.NoError(t, err)
	require.NotNil(t, eng.gotEvent)
	assert.Equal(t, event.Push, eng.gotEvent.Type)
	assert.Equal(t, "ops", eng.gotEvent.Sender)
	assert.Equal(t, "My title", eng.gotEvent.PRTitle)
	assert.Equal(t, 7, eng.gotEvent.PRNumber)
}

func TestLabelFlagPlumbsThrough(t *testing.T) {
	dir := t.TempDir()
	eng := fakeEng()
	_, _, _, err := runCLI(t, cliSnapshot(dir), eng, "match", "--label", "kind/lgtm", "--label", "docs-needed")
	require.NoError(t, err)
	require.NotNil(t, eng.gotEvent)
	assert.Equal(t, event.PullRequest, eng.gotEvent.Type)
	assert.Equal(t, event.KindLabeled, eng.gotEvent.Kind)
	assert.Equal(t, []string{"kind/lgtm", "docs-needed"}, eng.gotEvent.Labels)
	assert.Equal(t, "pull_request", eng.gotEvent.Webhook)
	assert.Empty(t, eng.gotEvent.Comment)
}

func TestCommentFlagPlumbsThrough(t *testing.T) {
	dir := t.TempDir()
	eng := fakeEng()
	_, _, _, err := runCLI(t, cliSnapshot(dir), eng, "match", "--comment", "/test please")
	require.NoError(t, err)
	require.NotNil(t, eng.gotEvent)
	assert.Equal(t, event.PullRequest, eng.gotEvent.Type)
	assert.Equal(t, event.KindComment, eng.gotEvent.Kind)
	assert.Equal(t, "/test please", eng.gotEvent.Comment)
	assert.Equal(t, "issue_comment", eng.gotEvent.Webhook)
	assert.Empty(t, eng.gotEvent.Labels)
}

func TestLabelCommentFlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"labels on a push event", []string{"match", "--event", "push", "--label", "x"}, "only to pull_request events"},
		{"comment on a push event", []string{"match", "--event", "push", "--comment", "hi"}, "only to pull_request events"},
		{"labels and a comment", []string{"match", "--label", "x", "--comment", "hi"}, "cannot be combined"},
		{"empty label", []string{"match", "--label", ""}, "must not be empty"},
		{"explicitly empty comment", []string{"match", "--comment", ""}, "--comment must not be empty"},
		{"whitespace comment", []string{"match", "--comment", "  "}, "--comment must not be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := runCLI(t, cliSnapshot(t.TempDir()), fakeEng(), tt.args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestTargetBranchFlagReachesInspector(t *testing.T) {
	dir := t.TempDir()
	insp := &fakeInspector{snap: cliSnapshot(dir)}
	var out, errOut bytes.Buffer
	d := deps{
		openGit:   func(string) (git.Inspector, error) { return insp, nil },
		newEngine: func(bool, pac.Params) pac.Engine { return fakeEng() },
		out:       &out,
		errOut:    &errOut,
	}
	root := newRootCmd(d)
	root.SetArgs([]string{"match", "--target-branch", "release-1.0"})
	require.NoError(t, root.ExecuteContext(context.Background()))
	assert.Equal(t, "release-1.0", insp.opts.TargetBranch)
}

func TestTektonDirResolvedAgainstRepoRoot(t *testing.T) {
	// The user runs from a subdirectory; the tekton dir is relative to the
	// repo root that the snapshot reports, not the working directory.
	dir := t.TempDir()
	eng := fakeEng()
	_, _, insp, err := runCLI(t, cliSnapshot(dir), eng, "-C", filepath.Join(dir, "sub"), "match", "--tekton-dir", ".tekton")
	require.NoError(t, err)
	// The -C flag must reach the git inspector (this pins the cobra
	// persistent-flag wiring: globalFlags is shared by pointer).
	assert.Equal(t, filepath.Join(dir, "sub"), insp.gotDir)
	assert.Equal(t, filepath.Join(dir, ".tekton"), eng.gotDir)
}

func TestNonGitHubRemoteWarnsOnStderr(t *testing.T) {
	snap := cliSnapshot(t.TempDir())
	snap.RemoteURL = "git@gitlab.com:example/repo.git"
	snap.Remote = git.Remote{Host: "gitlab.com", Owner: "example", Repo: "repo"}
	out, errOut, _, err := runCLI(t, snap, fakeEng(), "match")
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "warning:")
	assert.Contains(t, errOut, "gitlab.com")
}

func TestDetachedHEADWarnsOnStderr(t *testing.T) {
	snap := cliSnapshot(t.TempDir())
	snap.Detached = true
	snap.Branch = ""
	_, errOut, _, err := runCLI(t, snap, fakeEng(), "match")
	require.NoError(t, err)
	assert.Contains(t, errOut, "warning: HEAD is detached")
}

func TestBadEventTypeIsAnError(t *testing.T) {
	_, _, _, err := runCLI(t, cliSnapshot(t.TempDir()), fakeEng(), "match", "--event", "bogus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}

func TestUnknownCommandIsAnError(t *testing.T) {
	_, _, _, err := runCLI(t, cliSnapshot(t.TempDir()), fakeEng(), "frobnicate")
	require.Error(t, err)
}

// TestEndToEndWithRealEngine drives the full CLI path with the real PAC
// engine and a .tekton directory, faking only the git snapshot.
func TestEndToEndWithRealEngine(t *testing.T) {
	dir := t.TempDir()
	tekton := filepath.Join(dir, ".tekton")
	require.NoError(t, os.MkdirAll(tekton, 0o755))
	manifest := pacdef.PipelineRun("e2e-run").OnPullRequest("main").YAML()
	require.NoError(t, os.WriteFile(filepath.Join(tekton, "e2e.yaml"), []byte(manifest), 0o644))

	eng := pac.New(nil)

	out, _, _, err := runCLI(t, cliSnapshot(dir), eng, "match")
	require.NoError(t, err)
	assert.Equal(t, "e2e.yaml\n", out)

	out, _, _, err = runCLI(t, cliSnapshot(dir), eng, "resolve")
	require.NoError(t, err)
	assert.Contains(t, out, "kind: PipelineRun")
	assert.Contains(t, out, "hello")
	assert.NotContains(t, out, "{{") // no unsubstituted placeholders

	// A push event on the feature branch does not match the main-targeting
	// fixture: match stays empty and exits zero.
	out, _, _, err = runCLI(t, cliSnapshot(dir), eng, "match", "--event", "push")
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestTektonPath(t *testing.T) {
	assert.Equal(t, filepath.Join("/repo", ".tekton"), tektonPath("/repo", ".tekton"))
	assert.Equal(t, "/abs/tekton", tektonPath("/repo", "/abs/tekton"))
}

func TestBuildLoggerVerbose(t *testing.T) {
	assert.NotNil(t, buildLogger(false, nil))
	assert.NotNil(t, buildLogger(true, &strings.Builder{}))
}
