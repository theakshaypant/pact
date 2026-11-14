package pac

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/pkg/event"
)

func matchNames(t *testing.T, matches []Match) []string {
	t.Helper()
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.File
	}
	return names
}

func TestMatchBasic(t *testing.T) {
	tests := []struct {
		name   string
		typ    event.Type
		branch string // current branch; a push event targets the branch it pushes to
		files  []string
		want   []string
	}{
		{
			name:   "pull request with go changes",
			typ:    event.PullRequest,
			branch: "feature",
			files:  []string{"src/main.go"},
			want:   []string{"local-task.yaml", "pr-only.yaml", "two-runs.yaml"},
		},
		{
			name:   "pull request with docs changes also matches docs-only yml",
			typ:    event.PullRequest,
			branch: "feature",
			files:  []string{"docs/readme.md"},
			want:   []string{"docs-only.yml", "local-task.yaml", "pr-only.yaml", "two-runs.yaml"},
		},
		{
			name:   "push to main matches push-only fixtures",
			typ:    event.Push,
			branch: "main",
			files:  []string{"src/main.go"},
			want:   []string{"push-only.yaml", "two-runs.yaml"},
		},
		{
			name:   "push to feature branch does not match main-targeting fixtures",
			typ:    event.Push,
			branch: "feature",
			files:  []string{"src/main.go"},
			want:   []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := New(nil)
			matches, err := eng.Match(context.Background(), basicDir(t), matchEventOnBranch(t, tt.typ, tt.files, tt.branch))
			require.NoError(t, err)
			assert.Equal(t, tt.want, matchNames(t, matches))
		})
	}
}

func TestMatchCEL(t *testing.T) {
	t.Run("both expressions match with pr title and go files", func(t *testing.T) {
		eng := New(nil)
		matches, err := eng.Match(context.Background(), celDir(t), matchEvent(t, event.PullRequest, []string{"src/main.go"}))
		require.NoError(t, err)
		assert.Equal(t, []string{"cel-body.yaml", "cel-files.yaml"}, matchNames(t, matches))
	})

	t.Run("files expression needs a go file", func(t *testing.T) {
		ev := matchEvent(t, event.PullRequest, []string{"README.md"})
		eng := New(nil)
		matches, err := eng.Match(context.Background(), celDir(t), ev)
		require.NoError(t, err)
		assert.Equal(t, []string{"cel-body.yaml"}, matchNames(t, matches))
	})

	t.Run("push event matches neither CEL fixture", func(t *testing.T) {
		eng := New(nil)
		matches, err := eng.Match(context.Background(), celDir(t), matchEvent(t, event.Push, []string{"x.go"}))
		require.NoError(t, err)
		assert.Empty(t, matches)
	})
}

func TestMatchBadCELIsSkippedNotFailed(t *testing.T) {
	// PAC logs the CEL evaluation error and skips the PipelineRun, so a
	// directory with only an invalid expression yields no matches and no
	// error. This pins that behavior.
	eng := New(nil)
	matches, err := eng.Match(context.Background(), badcelDir(t), matchEvent(t, event.PullRequest, nil))
	require.NoError(t, err)
	assert.Empty(t, matches)
}

func TestMatchNoMatchDir(t *testing.T) {
	eng := New(nil)
	matches, err := eng.Match(context.Background(), nomatchDir(t), matchEvent(t, event.PullRequest, nil))
	require.NoError(t, err)
	assert.Empty(t, matches)
}

func TestMatchMissingDir(t *testing.T) {
	eng := New(nil)
	_, err := eng.Match(context.Background(), "/does/not/exist", matchEvent(t, event.PullRequest, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading tekton directory")
}

func TestMatchLabeled(t *testing.T) {
	t.Run("on-label and label-aware CEL match the labeled event", func(t *testing.T) {
		eng := New(nil)
		ev := matchEventOpts(t, event.Options{Labels: []string{"kind/lgtm"}}, []string{"src/main.go"})
		matches, err := eng.Match(context.Background(), labelCommentDir(t), ev)
		require.NoError(t, err)
		assert.Equal(t, []string{"cel-label.yaml", "on-label-any.yaml", "on-label.yaml"}, matchNames(t, matches))
	})

	t.Run("a label out of the annotation's set does not match", func(t *testing.T) {
		eng := New(nil)
		ev := matchEventOpts(t, event.Options{Labels: []string{"docs-needed"}}, []string{"src/main.go"})
		matches, err := eng.Match(context.Background(), labelCommentDir(t), ev)
		require.NoError(t, err)
		assert.Equal(t, []string{"on-label-any.yaml"}, matchNames(t, matches))
	})

	t.Run("a plain pull request does not match on-label pipelines", func(t *testing.T) {
		eng := New(nil)
		matches, err := eng.Match(context.Background(), labelCommentDir(t),
			matchEvent(t, event.PullRequest, []string{"src/main.go"}))
		require.NoError(t, err)
		assert.Empty(t, matches)
	})

	t.Run("a labeled event does not match plain pull_request pipelines", func(t *testing.T) {
		eng := New(nil)
		ev := matchEventOpts(t, event.Options{Labels: []string{"kind/lgtm"}}, []string{"src/main.go"})
		matches, err := eng.Match(context.Background(), basicDir(t), ev)
		require.NoError(t, err)
		assert.Empty(t, matches)
	})

	t.Run("the labeled CEL gate rejects label-unaware expressions", func(t *testing.T) {
		// PAC skips CEL expressions that reference neither event_type nor
		// labels on labeled events, so the title-based cel-body fixture
		// must not match even though its title check would pass.
		eng := New(nil)
		ev := matchEventOpts(t, event.Options{
			Labels:  []string{"kind/lgtm"},
			PRTitle: "feat: add local testing",
		}, nil)
		matches, err := eng.Match(context.Background(), celDir(t), ev)
		require.NoError(t, err)
		assert.Empty(t, matches)
	})
}

func TestMatchComment(t *testing.T) {
	t.Run("on-comment regex matches the comment body", func(t *testing.T) {
		eng := New(nil)
		ev := matchEventOpts(t, event.Options{Comment: "/test please"}, []string{"src/main.go"})
		matches, err := eng.Match(context.Background(), labelCommentDir(t), ev)
		require.NoError(t, err)
		assert.Equal(t, []string{"on-comment.yaml"}, matchNames(t, matches))
	})

	t.Run("a comment the regex does not match yields nothing", func(t *testing.T) {
		eng := New(nil)
		ev := matchEventOpts(t, event.Options{Comment: "unrelated chatter"}, []string{"src/main.go"})
		matches, err := eng.Match(context.Background(), labelCommentDir(t), ev)
		require.NoError(t, err)
		assert.Empty(t, matches)
	})

	t.Run("a plain pull request does not match on-comment pipelines", func(t *testing.T) {
		eng := New(nil)
		matches, err := eng.Match(context.Background(), labelCommentDir(t),
			matchEvent(t, event.PullRequest, []string{"src/main.go"}))
		require.NoError(t, err)
		assert.Empty(t, matches)
	})

	t.Run("a comment event does not match plain pull_request pipelines", func(t *testing.T) {
		eng := New(nil)
		ev := matchEventOpts(t, event.Options{Comment: "/test please"}, []string{"src/main.go"})
		matches, err := eng.Match(context.Background(), basicDir(t), ev)
		require.NoError(t, err)
		assert.Empty(t, matches)
	})
}

// TestIsNoMatch pins the error-string coupling to PAC's no-match error so a
// library upgrade that changes the wording breaks loudly here.
func TestIsNoMatch(t *testing.T) {
	assert.True(t, isNoMatch(errors.New(
		"cannot match the event to any pipelineruns in the .tekton/ directory, payload target event is push with source branch feature and target branch main.")))
	assert.False(t, isNoMatch(errors.New("some other error")))
	assert.False(t, isNoMatch(nil))
}
