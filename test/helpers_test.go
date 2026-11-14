// Package test holds pact's black-box e2e suite: every scenario builds a
// real git repository, runs the compiled pact binary against it and
// validates the output, mirroring the test layout of
// openshift-pipelines/pipelines-as-code (scenario files in test/, shared
// harness packages in test/pkg/). Pipeline manifests are built with
// test/pkg/pacdef, shared with the faster in-package unit tests in
// pkg/pac; see manifests_test.go for this suite's scenarios.
package test

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"github.com/theakshaypant/pact/test/pkg/gitrepo"
	"github.com/theakshaypant/pact/test/pkg/pactcli"
	"github.com/theakshaypant/pact/test/pkg/tekton"
)

// mustRun runs the pact binary in r's directory and requires a
// successful exit.
func mustRun(t *testing.T, r *gitrepo.Repo, args ...string) pactcli.Result {
	t.Helper()
	res := pactcli.Run(t, append([]string{"-C", r.Dir}, args...)...)
	require.Equal(t, 0, res.ExitCode, "stderr: %s", res.Stderr)
	return res
}

// mustMatch runs pact match, requires a successful exit and returns the
// matched filenames (stdout).
func mustMatch(t *testing.T, r *gitrepo.Repo, args ...string) string {
	t.Helper()
	return mustRun(t, r, append([]string{"match"}, args...)...).Stdout
}

// resolveRuns runs pact resolve, requires a successful exit, decodes the
// printed PipelineRuns and validates them with Tekton's admission-level
// validation.
func resolveRuns(t *testing.T, r *gitrepo.Repo, args ...string) []*tektonv1.PipelineRun {
	t.Helper()
	res := mustRun(t, r, append([]string{"resolve"}, args...)...)
	prs := tekton.DecodePipelineRuns(t, res.Stdout)
	tekton.Validate(t, prs)
	return prs
}

// setupMainBranch creates a repository checked out on its main branch,
// with main also set as the origin default branch. The base commit
// carries a Go file so scenarios can modify or delete tracked content.
func setupMainBranch(t *testing.T) *gitrepo.Repo {
	t.Helper()
	r := gitrepo.New(t)
	r.Commit("README.md", "base content", "base commit")
	tip := r.Commit("src/existing.go", "package main", "add existing source")
	r.CreateBranch("main")
	r.Checkout("main")
	r.SetOrigin("git@github.com:example/repo.git", map[string]plumbing.Hash{"main": tip}, "main")
	return r
}

// setupFeatureBranch creates a repository whose origin default branch is
// main and whose checkout is a feature branch one commit ahead, with the
// given commit message (it doubles as the simulated PR title) changing a
// Go file.
func setupFeatureBranch(t *testing.T, featureMsg string) *gitrepo.Repo {
	t.Helper()
	return setupFeatureBranchChanging(t, "src/feature.go", "package main", featureMsg)
}

// setupFeatureBranchChanging is setupFeatureBranch with control over the
// file changed by the feature commit.
func setupFeatureBranchChanging(t *testing.T, path, content, featureMsg string) *gitrepo.Repo {
	t.Helper()
	r := setupMainBranch(t)
	r.CreateBranch("feature")
	r.Commit(path, content, featureMsg)
	return r
}
