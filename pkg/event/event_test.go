package event

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/pkg/git"
)

const (
	testHeadSHA = "abc123def4567890abc123def4567890abc12345"
	testBaseSHA = "fedcba0987654321fedcba0987654321fedcba09"
)

func githubSnapshot() *git.Snapshot {
	return &git.Snapshot{
		RepoRoot:     "/repo",
		Branch:       "feature",
		HeadSHA:      testHeadSHA,
		HeadTitle:    "feat: add local testing",
		Author:       "Dev Person",
		RemoteURL:    "git@github.com:example/repo.git",
		Remote:       git.Remote{Host: "github.com", Owner: "example", Repo: "repo", IsGitHub: true},
		TargetBranch: "main",
		TargetSHA:    testBaseSHA,
		BaseSHA:      testBaseSHA,
		ChangedFiles: git.ChangedFiles{All: []string{"a.go"}, Added: []string{"a.go"}},
	}
}

func repoPayloadMap() map[string]any {
	return map[string]any{
		"name":           "repo",
		"full_name":      "example/repo",
		"html_url":       "https://github.com/example/repo",
		"clone_url":      "git@github.com:example/repo.git",
		"default_branch": "main",
		"owner":          map[string]any{"login": "example"},
	}
}

func TestParseType(t *testing.T) {
	tests := []struct {
		in      string
		want    Type
		wantErr bool
	}{
		{"pull_request", PullRequest, false},
		{"push", Push, false},
		{"Pull Request", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		got, err := ParseType(tt.in)
		if tt.wantErr {
			require.Error(t, err, tt.in)
			continue
		}
		require.NoError(t, err, tt.in)
		assert.Equal(t, tt.want, got)
	}
}

func TestBuildPullRequest(t *testing.T) {
	ev, err := Build(githubSnapshot(), Options{})
	require.NoError(t, err)

	assert.Equal(t, PullRequest, ev.Type)
	assert.Equal(t, "opened", ev.Action)
	assert.Equal(t, "feature", ev.HeadBranch)
	assert.Equal(t, "main", ev.BaseBranch)
	assert.Equal(t, "https://github.com/example/repo", ev.HeadURL)
	assert.Equal(t, testHeadSHA, ev.SHA)
	assert.Equal(t, "Dev Person", ev.Sender)
	assert.Equal(t, 1, ev.PRNumber)
	assert.Equal(t, "feat: add local testing", ev.PRTitle)
	assert.Equal(t, []string{"a.go"}, ev.ChangedFiles.All)
	assert.Equal(t, http.Header{"X-GitHub-Event": []string{"pull_request"}}, ev.Headers)

	repo := repoPayloadMap()
	want := map[string]any{
		"action": "opened",
		"number": 1,
		"pull_request": map[string]any{
			"number": 1,
			"title":  "feat: add local testing",
			"state":  "open",
			"user":   map[string]any{"login": "Dev Person"},
			"head":   map[string]any{"ref": "feature", "sha": testHeadSHA, "repo": repo},
			"base":   map[string]any{"ref": "main", "sha": testBaseSHA, "repo": repo},
			"labels": []any{},
		},
		"repository": repo,
		"sender":     map[string]any{"login": "Dev Person"},
	}
	assert.Equal(t, want, ev.Payload)
}

func TestBuildPush(t *testing.T) {
	ev, err := Build(githubSnapshot(), Options{Type: Push})
	require.NoError(t, err)

	assert.Equal(t, Push, ev.Type)
	assert.Equal(t, "feature", ev.HeadBranch)
	assert.Equal(t, "feature", ev.BaseBranch)
	assert.Equal(t, http.Header{"X-GitHub-Event": []string{"push"}}, ev.Headers)

	repo := repoPayloadMap()
	want := map[string]any{
		"ref":    "refs/heads/feature",
		"before": testBaseSHA,
		"after":  testHeadSHA,
		"head_commit": map[string]any{
			"id":      testHeadSHA,
			"message": "feat: add local testing",
			"author":  map[string]any{"name": "Dev Person"},
		},
		"repository": repo,
		"pusher":     map[string]any{"name": "Dev Person"},
		"sender":     map[string]any{"login": "Dev Person"},
	}
	assert.Equal(t, want, ev.Payload)
}

func TestBuildNonGitHubRemote(t *testing.T) {
	snap := githubSnapshot()
	snap.RemoteURL = "https://gitlab.com/group/sub/repo.git"
	snap.Remote = git.Remote{Host: "gitlab.com", Owner: "sub", Repo: "repo"}

	ev, err := Build(snap, Options{})
	require.NoError(t, err)

	assert.True(t, ev.NonGitHubRemote)
	assert.Equal(t, "https://gitlab.com/group/sub/repo.git", ev.HeadURL)
	assert.Equal(t, "sub", ev.Owner)
	assert.Equal(t, "repo", ev.Repo)
	assert.NotContains(t, ev.Payload, "https://github.com")
}

func TestBuildNoRemote(t *testing.T) {
	snap := githubSnapshot()
	snap.RemoteURL = ""
	snap.Remote = git.Remote{}

	ev, err := Build(snap, Options{})
	require.NoError(t, err)
	assert.False(t, ev.NonGitHubRemote)
	assert.Empty(t, ev.HeadURL)
}

func TestBuildDetachedHead(t *testing.T) {
	snap := githubSnapshot()
	snap.Detached = true
	snap.Branch = ""

	ev, err := Build(snap, Options{Type: Push})
	require.NoError(t, err)
	assert.Equal(t, "HEAD", ev.HeadBranch)
	assert.Equal(t, "refs/heads/HEAD", ev.Payload["ref"])
}

func TestBuildDefaultsAndOverrides(t *testing.T) {
	snap := githubSnapshot()
	snap.Author = ""

	ev, err := Build(snap, Options{})
	require.NoError(t, err)
	assert.Equal(t, "pact", ev.Sender) // author empty, no override

	ev, err = Build(snap, Options{Sender: "over", PRNumber: 7, PRTitle: "custom"})
	require.NoError(t, err)
	assert.Equal(t, "over", ev.Sender)
	assert.Equal(t, 7, ev.PRNumber)
	assert.Equal(t, "custom", ev.PRTitle)
}

func TestBuildNilSnapshot(t *testing.T) {
	_, err := Build(nil, Options{})
	require.Error(t, err)
}

// TestJSONGolden pins the payload JSON against the committed webhook
// fixtures, compared as parsed values so formatting never matters.
func TestJSONGolden(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    Options
		fixture string
	}{
		{"pull_request", Options{}, "../../test/fixtures/webhooks/pull_request.json"},
		{"push", Options{Type: Push}, "../../test/fixtures/webhooks/push.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := Build(githubSnapshot(), tc.opts)
			require.NoError(t, err)

			raw, err := os.ReadFile(tc.fixture)
			require.NoError(t, err)
			var want map[string]any
			require.NoError(t, json.Unmarshal(raw, &want))

			got, err := ev.JSON()
			require.NoError(t, err)
			var actual map[string]any
			require.NoError(t, json.Unmarshal(got, &actual))

			assert.Equal(t, want, actual)
		})
	}
}
