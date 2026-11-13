package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/test/pkg/tekton"
)

func TestPullRequestMatchAndResolve(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("pr.yaml", manifest(t, "pull-request-basic.yaml"))

	assert.Equal(t, "pr.yaml\n", mustMatch(t, r))

	prs := resolveRuns(t, r)
	require.Len(t, prs, 1)

	pr := tekton.ByGenerateName(t, prs, "pr-basic-")
	require.NotNil(t, pr.Spec.PipelineSpec)
	require.Len(t, pr.Spec.PipelineSpec.Tasks, 1)
	step := pr.Spec.PipelineSpec.Tasks[0].TaskSpec.Steps[0]
	assert.Equal(t, "cat README.md", step.Script)
}

func TestPullRequestMultipleRunsInOneFile(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("multi.yaml", manifest(t, "pull-request-multi.yaml"))

	// Both runs match, but the file is listed once.
	assert.Equal(t, "multi.yaml\n", mustMatch(t, r))

	prs := resolveRuns(t, r)
	require.Len(t, prs, 2)
	tekton.ByGenerateName(t, prs, "pr-multi-first-")
	tekton.ByGenerateName(t, prs, "pr-multi-second-")
}

func TestPullRequestMultipleMatchingFiles(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("first.yaml", manifest(t, "pull-request-basic.yaml"))
	r.WriteTekton("second.yaml", manifest(t, "cel-body-title.yaml"))

	// Matching files are listed one per line, sorted by name.
	assert.Equal(t, "first.yaml\nsecond.yaml\n", mustMatch(t, r))
}

func TestNoMatchPrintsNothingAndExitsZero(t *testing.T) {
	// The manifest only targets release-9, but the simulated pull request
	// targets the detected default branch main.
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("pr.yaml", manifest(t, "pr-other-branch.yaml"))

	assert.Empty(t, mustMatch(t, r))
}

func TestTargetBranchOverrideChangesMatching(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.CreateBranch("release-9")
	r.Checkout("feature")
	r.WriteTekton("pr.yaml", manifest(t, "pr-other-branch.yaml"))

	// With an explicit override the manifest does match.
	assert.Equal(t, "pr.yaml\n", mustMatch(t, r, "--target-branch", "release-9"))
}
