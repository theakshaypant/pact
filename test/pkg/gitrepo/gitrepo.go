// Package gitrepo builds real git repositories for tests. It mirrors the
// in-package helpers of pkg/git but is exported so black-box e2e tests can
// compose repository state (branches, merge bases, staged and untracked
// files, remotes) the way a developer's checkout actually looks.
package gitrepo

import (
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

// fixedWhen keeps commits deterministic across test runs.
var fixedWhen = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// Repo is a throwaway git repository bound to a test.
type Repo struct {
	t    *testing.T
	repo *git.Repository
	wt   *git.Worktree
	Dir  string
}

// New creates an empty repository with no commits.
func New(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	wt, err := r.Worktree()
	require.NoError(t, err)
	t.Logf("git repo initialized at %s", dir)
	return &Repo{t: t, repo: r, wt: wt, Dir: dir}
}

// Head returns the hash HEAD points at.
func (r *Repo) Head() plumbing.Hash {
	r.t.Helper()
	h, err := r.repo.Head()
	require.NoError(r.t, err)
	return h.Hash()
}

// Commit writes a file, stages it and commits it, returning the hash.
func (r *Repo) Commit(path, content, msg string) plumbing.Hash {
	r.t.Helper()
	r.WriteWorktree(path, content)
	r.Stage(path)
	h, err := r.wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{Name: "Test Author", Email: "author@example.com", When: fixedWhen},
	})
	require.NoError(r.t, err)
	r.t.Logf("commit %.7s %q (%s)", h, msg, path)
	return h
}

// CreateBranch creates and checks out a new branch.
func (r *Repo) CreateBranch(name string) {
	r.t.Helper()
	require.NoError(r.t, r.wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
		Create: true,
	}))
	r.t.Logf("created branch %s", name)
}

// Checkout switches to an existing branch.
func (r *Repo) Checkout(name string) {
	r.t.Helper()
	require.NoError(r.t, r.wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
	}))
	r.t.Logf("checked out branch %s", name)
}

// CheckoutDetached checks out a commit without a branch.
func (r *Repo) CheckoutDetached(h plumbing.Hash) {
	r.t.Helper()
	require.NoError(r.t, r.wt.Checkout(&git.CheckoutOptions{Hash: h}))
	r.t.Logf("detached checkout at %.7s", h)
}

// Stage adds a file to the index without committing.
func (r *Repo) Stage(path string) {
	r.t.Helper()
	_, err := r.wt.Add(path)
	require.NoError(r.t, err)
	r.t.Logf("staged %s", path)
}

// WriteWorktree writes an unstaged file into the worktree.
func (r *Repo) WriteWorktree(path, content string) {
	r.t.Helper()
	if idx := strings.LastIndexByte(path, '/'); idx > 0 {
		require.NoError(r.t, r.wt.Filesystem.MkdirAll(path[:idx], 0o755))
	}
	r.t.Logf("worktree write %s", path)
	f, err := r.wt.Filesystem.Create(path)
	require.NoError(r.t, err)
	_, err = f.Write([]byte(content))
	require.NoError(r.t, err)
	require.NoError(r.t, f.Close())
}

// RemoveWorktree deletes a file from the worktree (not from the index).
func (r *Repo) RemoveWorktree(path string) {
	r.t.Helper()
	require.NoError(r.t, r.wt.Filesystem.Remove(path))
	r.t.Logf("worktree remove %s", path)
}

// SetOrigin adds an origin remote with remote-tracking refs and an
// origin/HEAD symbolic ref pointing at defaultBranch (empty to skip).
// Any previous origin is replaced, so calling it twice is safe.
func (r *Repo) SetOrigin(url string, refs map[string]plumbing.Hash, defaultBranch string) {
	r.t.Helper()
	if _, err := r.repo.Remote("origin"); err == nil {
		require.NoError(r.t, r.repo.DeleteRemote("origin"))
	}
	_, err := r.repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})
	require.NoError(r.t, err)
	for name, h := range refs {
		require.NoError(r.t, r.repo.Storer.SetReference(
			plumbing.NewHashReference(plumbing.NewRemoteReferenceName("origin", name), h)))
	}
	r.t.Logf("origin set to %s (default branch %q)", url, defaultBranch)
	if defaultBranch != "" {
		require.NoError(r.t, r.repo.Storer.SetReference(
			plumbing.NewSymbolicReference(
				plumbing.NewRemoteHEADReferenceName("origin"),
				plumbing.NewRemoteReferenceName("origin", defaultBranch),
			)))
	}
}

// WriteTekton writes a manifest into the repository's .tekton directory.
func (r *Repo) WriteTekton(name, content string) {
	r.t.Helper()
	r.WriteWorktree(".tekton/"+name, content)
}
