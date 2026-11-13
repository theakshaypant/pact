package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/test/pkg/tekton"
)

func TestPushToMainMatchesAndResolves(t *testing.T) {
	r := setupMainBranch(t)
	r.WriteTekton("push.yaml", manifest(t, "push-basic.yaml"))
	// An uncommitted change on main: exactly the state a push would carry.
	r.WriteWorktree("ci.txt", "uncommitted")

	assert.Equal(t, "push.yaml\n", mustMatch(t, r, "--event", "push"))

	prs := resolveRuns(t, r, "--event", "push")
	require.Len(t, prs, 1)
	tekton.ByGenerateName(t, prs, "push-basic-")
}

func TestPushToFeatureBranchDoesNotMatchMainTarget(t *testing.T) {
	// A push event targets the branch being pushed to; a push from feature
	// does not satisfy a manifest targeting main.
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("push.yaml", manifest(t, "push-basic.yaml"))

	assert.Empty(t, mustMatch(t, r, "--event", "push"))
}
