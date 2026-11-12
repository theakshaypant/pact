package git

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRemote(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    Remote
		wantErr bool
	}{
		{
			name: "https with .git",
			raw:  "https://github.com/example/repo.git",
			want: Remote{Host: "github.com", Owner: "example", Repo: "repo", IsGitHub: true},
		},
		{
			name: "https without .git",
			raw:  "https://github.com/example/repo",
			want: Remote{Host: "github.com", Owner: "example", Repo: "repo", IsGitHub: true},
		},
		{
			name: "https with token",
			raw:  "https://token@github.com/example/repo.git",
			want: Remote{Host: "github.com", Owner: "example", Repo: "repo", IsGitHub: true},
		},
		{
			name: "scp-like",
			raw:  "git@github.com:example/repo.git",
			want: Remote{Host: "github.com", Owner: "example", Repo: "repo", IsGitHub: true},
		},
		{
			name: "ssh with port",
			raw:  "ssh://git@github.com:22/example/repo.git",
			want: Remote{Host: "github.com", Owner: "example", Repo: "repo", IsGitHub: true},
		},
		{
			name: "gitlab nested groups use last two segments",
			raw:  "https://gitlab.com/group/subgroup/repo.git",
			want: Remote{Host: "gitlab.com", Owner: "subgroup", Repo: "repo", IsGitHub: false},
		},
		{
			name:    "empty",
			raw:     "",
			wantErr: true,
		},
		{
			name:    "no owner path",
			raw:     "https://github.com",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRemote(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
