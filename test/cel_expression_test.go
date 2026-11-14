package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/theakshaypant/pact/test/pkg/gitrepo"
)

func TestCELBodyTitle(t *testing.T) {
	// The simulated PR title defaults to the HEAD commit message.
	tests := []struct {
		name      string
		title     string
		wantMatch bool
	}{
		{"matching title", "feat: add local testing", true},
		{"unrelated title", "chore: unrelated cleanup", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := setupFeatureBranch(t, tt.title)
			r.WriteTekton("cel.yaml", celTitle().YAML())
			if tt.wantMatch {
				assert.Equal(t, "cel.yaml\n", mustMatch(t, r))
			} else {
				assert.Empty(t, mustMatch(t, r))
			}
		})
	}
}

func TestCELFilesGoChangedClasses(t *testing.T) {
	// Every case commits only docs/feature.md on the branch, so the Go
	// file under test is the sole .go file in the diff: the expression
	// cannot match unless that class of uncommitted change is reported.
	tests := []struct {
		name   string
		change func(r *gitrepo.Repo)
		match  bool
	}{
		{
			name: "staged new file edited after staging",
			change: func(r *gitrepo.Repo) {
				r.WriteWorktree("src/staged.go", "package main")
				r.Stage("src/staged.go")
				r.WriteWorktree("src/staged.go", "package main // edited after staging")
			},
			match: true,
		},
		{
			name: "untracked file",
			change: func(r *gitrepo.Repo) {
				r.WriteWorktree("src/untracked.go", "package main")
			},
			match: true,
		},
		{
			name: "unstaged modification",
			change: func(r *gitrepo.Repo) {
				r.WriteWorktree("src/existing.go", "package main // modified")
			},
			match: true,
		},
		{
			name: "unstaged deletion",
			change: func(r *gitrepo.Repo) {
				r.RemoveWorktree("src/existing.go")
			},
			match: true,
		},
		{
			name: "markdown only",
			change: func(r *gitrepo.Repo) {
				r.WriteWorktree("docs/notes.md", "notes")
			},
			match: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := setupFeatureBranchChanging(t, "docs/feature.md", "notes", "feat: add local testing")
			r.WriteTekton("cel.yaml", celGoFiles().YAML())
			tt.change(r)
			if tt.match {
				assert.Equal(t, "cel.yaml\n", mustMatch(t, r))
			} else {
				assert.Empty(t, mustMatch(t, r))
			}
		})
	}
}

func TestCELMalformedExpressionSkipsManifest(t *testing.T) {
	// A syntactically broken CEL expression makes PAC skip the
	// PipelineRun rather than fail the command: exit 0, no output.
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("badcel.yaml", badCEL().YAML())
	assert.Empty(t, mustMatch(t, r))
}
