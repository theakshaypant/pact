package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNonGitHubRemoteWarnsButStillMatches(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.SetOrigin("https://gitlab.com/group/repo.git", nil, "")
	r.WriteTekton("pr.yaml", prBasic().YAML())

	res := mustRun(t, r, "match")
	assert.Equal(t, "pr.yaml\n", res.Stdout)
	assert.Contains(t, res.Stderr, "warning:")
	assert.Contains(t, res.Stderr, "gitlab.com")
}

func TestDetachedHEADWarnsButStillMatches(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.CheckoutDetached(r.Head())
	r.WriteTekton("pr.yaml", prBasic().YAML())

	res := mustRun(t, r, "match")
	assert.Equal(t, "pr.yaml\n", res.Stdout)
	assert.Contains(t, res.Stderr, "warning: HEAD is detached")
}
