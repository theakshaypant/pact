package pac

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/pkg/event"
	"github.com/theakshaypant/pact/pkg/git"
)

// matchEvent builds a full synthetic event with the given changed files.
func matchEvent(t *testing.T, typ event.Type, files []string) *event.Event {
	t.Helper()
	return matchEventOnBranch(t, typ, files, "feature")
}

// matchEventOnBranch is matchEvent with a custom current branch. A push
// event targets the branch it pushes to, so push fixtures expecting "main"
// need the checkout to be on main.
func matchEventOnBranch(t *testing.T, typ event.Type, files []string, branch string) *event.Event {
	t.Helper()
	return matchEventOptsOnBranch(t, event.Options{Type: typ, PRTitle: "feat: add local testing"}, files, branch)
}

// matchEventOpts builds a synthetic event with full control over the
// options (labels, comment, ...) on the standard feature branch.
func matchEventOpts(t *testing.T, opts event.Options, files []string) *event.Event {
	t.Helper()
	return matchEventOptsOnBranch(t, opts, files, "feature")
}

// matchEventOptsOnBranch is matchEventOpts with a custom current branch.
func matchEventOptsOnBranch(t *testing.T, opts event.Options, files []string, branch string) *event.Event {
	t.Helper()
	snap := &git.Snapshot{
		Branch:       branch,
		HeadSHA:      "abcdef",
		HeadTitle:    "a commit",
		Author:       "Dev",
		RemoteURL:    "git@github.com:example/repo.git",
		Remote:       git.Remote{Host: "github.com", Owner: "example", Repo: "repo", IsGitHub: true},
		TargetBranch: "main",
		BaseSHA:      "123456",
		ChangedFiles: git.ChangedFiles{All: files, Added: files},
	}
	ev, err := event.Build(snap, opts)
	require.NoError(t, err)
	return ev
}
