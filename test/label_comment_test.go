package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/test/pkg/pacdef"
	"github.com/theakshaypant/pact/test/pkg/pactcli"
	"github.com/theakshaypant/pact/test/pkg/tekton"
)

func TestLabelEventMatchAndResolve(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("label.yaml", pacdef.Labeled().YAML())

	// The labeled event matches, and a plain pull request or a different
	// label does not.
	assert.Equal(t, "label.yaml\n", mustMatch(t, r, "--label", "kind/lgtm"))
	assert.Empty(t, mustMatch(t, r))
	assert.Empty(t, mustMatch(t, r, "--label", "other"))

	prs := resolveRuns(t, r, "--label", "kind/lgtm")
	require.Len(t, prs, 1)
	tekton.ByGenerateName(t, prs, "on-label-")
}

func TestCommentEventMatchAndResolve(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("comment.yaml", pacdef.Comment().YAML())

	assert.Equal(t, "comment.yaml\n", mustMatch(t, r, "--comment", "/test please"))
	assert.Empty(t, mustMatch(t, r, "--comment", "unrelated chatter"))
	assert.Empty(t, mustMatch(t, r))

	prs := resolveRuns(t, r, "--comment", "/test please")
	require.Len(t, prs, 1)
	pr := tekton.ByGenerateName(t, prs, "on-comment-")
	require.Len(t, pr.Spec.Params, 1)
	assert.Equal(t, "/test please", pr.Spec.Params[0].Value.StringVal)
}

func TestLabelAndCommentAreMutuallyExclusive(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("label.yaml", pacdef.Labeled().YAML())

	res := pactcli.Run(t, "-C", r.Dir, "match", "--label", "kind/lgtm", "--comment", "/test")
	assert.NotEqual(t, 0, res.ExitCode)
	assert.Contains(t, res.Stderr, "cannot be combined")
}
