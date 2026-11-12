package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedWhen keeps commits deterministic across test runs.
var fixedWhen = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

type testRepo struct {
	t    *testing.T
	repo *git.Repository
	wt   *git.Worktree
	dir  string
}

func initRepo(t *testing.T) *testRepo {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	wt, err := r.Worktree()
	require.NoError(t, err)
	return &testRepo{t: t, repo: r, wt: wt, dir: dir}
}

// commit writes a file, stages it and commits it.
func (tr *testRepo) commit(path, content, msg string) plumbing.Hash {
	tr.t.Helper()
	if idx := strings.LastIndexByte(path, '/'); idx > 0 {
		require.NoError(tr.t, tr.wt.Filesystem.MkdirAll(path[:idx], 0o755))
	}
	f, err := tr.wt.Filesystem.Create(path)
	require.NoError(tr.t, err)
	_, err = f.Write([]byte(content))
	require.NoError(tr.t, err)
	require.NoError(tr.t, f.Close())
	_, err = tr.wt.Add(path)
	require.NoError(tr.t, err)
	h, err := tr.wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{Name: "Test Author", Email: "author@example.com", When: fixedWhen},
	})
	require.NoError(tr.t, err)
	return h
}

func (tr *testRepo) createBranch(name string) {
	tr.t.Helper()
	require.NoError(tr.t, tr.wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
		Create: true,
	}))
}

func (tr *testRepo) checkoutBranch(name string) {
	tr.t.Helper()
	require.NoError(tr.t, tr.wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
	}))
}

func (tr *testRepo) checkoutDetached(h plumbing.Hash) {
	tr.t.Helper()
	require.NoError(tr.t, tr.wt.Checkout(&git.CheckoutOptions{Hash: h}))
}

// setOrigin adds an origin remote plus remote-tracking refs and an
// origin/HEAD symbolic ref pointing at the given default branch.
func (tr *testRepo) setOrigin(url string, refs map[string]plumbing.Hash, defaultBranch string) {
	tr.t.Helper()
	_, err := tr.repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})
	require.NoError(tr.t, err)
	for name, h := range refs {
		require.NoError(tr.t, tr.repo.Storer.SetReference(
			plumbing.NewHashReference(plumbing.NewRemoteReferenceName("origin", name), h)))
	}
	if defaultBranch != "" {
		require.NoError(tr.t, tr.repo.Storer.SetReference(
			plumbing.NewSymbolicReference(
				plumbing.NewRemoteHEADReferenceName("origin"),
				plumbing.NewRemoteReferenceName("origin", defaultBranch),
			)))
	}
}

// writeWorktree writes an unstaged file into the worktree.
func (tr *testRepo) writeWorktree(path, content string) {
	tr.t.Helper()
	if idx := strings.LastIndexByte(path, '/'); idx > 0 {
		require.NoError(tr.t, tr.wt.Filesystem.MkdirAll(path[:idx], 0o755))
	}
	f, err := tr.wt.Filesystem.Create(path)
	require.NoError(tr.t, err)
	_, err = f.Write([]byte(content))
	require.NoError(tr.t, err)
	require.NoError(tr.t, f.Close())
}

// takeSnapshot opens the repository at dir and takes a snapshot.
func takeSnapshot(t *testing.T, dir string, opts Options) *Snapshot {
	t.Helper()
	insp, err := Open(dir)
	require.NoError(t, err)
	snap, err := insp.Snapshot(context.Background(), opts)
	require.NoError(t, err)
	return snap
}

func TestInspectBranchAheadOfTarget(t *testing.T) {
	tr := initRepo(t)
	base := tr.commit("base.txt", "base", "base commit")
	tr.createBranch("main")
	tr.checkoutBranch("main")
	tr.createBranch("feature")
	feature := tr.commit("src/feature.go", "package main", "feature commit")
	tr.setOrigin("https://github.com/example/repo.git",
		map[string]plumbing.Hash{"main": base}, "main")

	snap := takeSnapshot(t, tr.dir, Options{})

	assert.Equal(t, "feature", snap.Branch)
	assert.False(t, snap.Detached)
	assert.Equal(t, feature.String(), snap.HeadSHA)
	assert.Equal(t, "feature commit", snap.HeadTitle)
	assert.Equal(t, "Test Author", snap.Author)
	assert.Equal(t, "main", snap.TargetBranch)
	assert.Equal(t, base.String(), snap.BaseSHA)
	assert.Equal(t, "github.com", snap.Remote.Host)
	assert.Equal(t, "example", snap.Remote.Owner)
	assert.Equal(t, "repo", snap.Remote.Repo)
	assert.True(t, snap.Remote.IsGitHub)
	assert.Equal(t, []string{"src/feature.go"}, snap.ChangedFiles.Added)
	assert.Equal(t, []string{"src/feature.go"}, snap.ChangedFiles.All)
}

func TestInspectOnTargetBranchWithDirtyTree(t *testing.T) {
	tr := initRepo(t)
	base := tr.commit("tracked.txt", "v1", "base commit")
	tr.createBranch("main")
	tr.checkoutBranch("main")
	tr.setOrigin("https://github.com/example/repo.git",
		map[string]plumbing.Hash{"main": base}, "main")

	// unstaged modification plus an untracked file
	tr.writeWorktree("tracked.txt", "v2 dirty")
	tr.writeWorktree("untracked.txt", "new")

	snap := takeSnapshot(t, tr.dir, Options{})

	assert.Equal(t, "main", snap.Branch)
	assert.Equal(t, "main", snap.TargetBranch)
	assert.Equal(t, snap.HeadSHA, snap.BaseSHA) // on the target: no committed diff
	assert.Equal(t, []string{"untracked.txt"}, snap.ChangedFiles.Added)
	assert.Equal(t, []string{"tracked.txt"}, snap.ChangedFiles.Modified)
	assert.Equal(t, []string{"tracked.txt", "untracked.txt"}, snap.ChangedFiles.All)
}

func TestInspectDeletedInWorktree(t *testing.T) {
	tr := initRepo(t)
	base := tr.commit("doomed.txt", "v1", "base commit")
	tr.createBranch("main")
	tr.checkoutBranch("main")
	tr.setOrigin("https://github.com/example/repo.git",
		map[string]plumbing.Hash{"main": base}, "main")

	_, err := tr.wt.Remove("doomed.txt")
	require.NoError(t, err)

	snap := takeSnapshot(t, tr.dir, Options{})
	assert.Equal(t, []string{"doomed.txt"}, snap.ChangedFiles.Deleted)
}

func TestInspectDetachedHead(t *testing.T) {
	tr := initRepo(t)
	base := tr.commit("base.txt", "base", "base commit")
	tr.createBranch("main")
	tr.checkoutBranch("main")
	head := tr.commit("next.txt", "next", "next commit")
	tr.setOrigin("https://github.com/example/repo.git",
		map[string]plumbing.Hash{"main": base}, "main")
	tr.checkoutDetached(head)

	snap := takeSnapshot(t, tr.dir, Options{})
	assert.True(t, snap.Detached)
	assert.Empty(t, snap.Branch)
}

func TestInspectNoCommits(t *testing.T) {
	tr := initRepo(t)
	insp, err := Open(tr.dir)
	require.NoError(t, err)
	_, err = insp.Snapshot(context.Background(), Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading HEAD")
}

func TestInspectFromSubdirectory(t *testing.T) {
	tr := initRepo(t)
	base := tr.commit("base.txt", "base", "base commit")
	tr.createBranch("main")
	tr.checkoutBranch("main")
	tr.setOrigin("https://github.com/example/repo.git",
		map[string]plumbing.Hash{"main": base}, "main")
	sub := filepath.Join(tr.dir, "sub", "dir")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	tr.writeWorktree("sub/dir/nested.txt", "hello")

	insp, err := Open(sub)
	require.NoError(t, err)
	snap, err := insp.Snapshot(context.Background(), Options{})
	require.NoError(t, err)
	assert.Equal(t, tr.dir, snap.RepoRoot)
	assert.Contains(t, snap.ChangedFiles.All, "sub/dir/nested.txt")
}

func TestInspectTargetBranchOverride(t *testing.T) {
	tr := initRepo(t)
	base := tr.commit("base.txt", "base", "base commit")
	tr.createBranch("release")
	release := tr.commit("release.txt", "release", "release commit")
	tr.checkoutBranch("master")
	head := tr.commit("main.txt", "main", "main commit")
	tr.setOrigin("https://github.com/example/repo.git", nil, "")

	snap := takeSnapshot(t, tr.dir, Options{TargetBranch: "release"})
	assert.Equal(t, "release", snap.TargetBranch)
	assert.Equal(t, release.String(), snap.TargetSHA)
	assert.Equal(t, head.String(), snap.HeadSHA)
	assert.Equal(t, base.String(), snap.BaseSHA)
	assert.Equal(t, []string{"main.txt"}, snap.ChangedFiles.All)
}

func TestInspectDefaultBranchFallsBackToMainThenMaster(t *testing.T) {
	t.Run("origin HEAD wins", func(t *testing.T) {
		tr := initRepo(t)
		base := tr.commit("base.txt", "base", "base commit")
		tr.createBranch("trunk")
		tr.checkoutBranch("trunk")
		tr.setOrigin("https://github.com/example/repo.git",
			map[string]plumbing.Hash{"trunk": base}, "trunk")

		snap := takeSnapshot(t, tr.dir, Options{})
		assert.Equal(t, "trunk", snap.TargetBranch)
	})

	t.Run("main fallback", func(t *testing.T) {
		tr := initRepo(t)
		tr.commit("base.txt", "base", "base commit")
		tr.createBranch("main")
		tr.checkoutBranch("main")
		tr.setOrigin("https://github.com/example/repo.git", nil, "")

		snap := takeSnapshot(t, tr.dir, Options{})
		assert.Equal(t, "main", snap.TargetBranch)
	})

	t.Run("master fallback", func(t *testing.T) {
		tr := initRepo(t) // PlainInit creates master
		tr.commit("base.txt", "base", "base commit")
		tr.setOrigin("https://github.com/example/repo.git", nil, "")

		snap := takeSnapshot(t, tr.dir, Options{})
		assert.Equal(t, "master", snap.TargetBranch)
	})
}

func TestInspectMissingTargetBranch(t *testing.T) {
	tr := initRepo(t)
	tr.commit("base.txt", "base", "base commit")
	tr.setOrigin("https://github.com/example/repo.git", nil, "")

	insp, err := Open(tr.dir)
	require.NoError(t, err)
	_, err = insp.Snapshot(context.Background(), Options{TargetBranch: "nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `resolving target branch "nope"`)
}

func TestInspectNoRemote(t *testing.T) {
	tr := initRepo(t)
	base := tr.commit("base.txt", "base", "base commit")
	tr.createBranch("main")
	tr.checkoutBranch("main")

	snap := takeSnapshot(t, tr.dir, Options{})
	assert.Empty(t, snap.RemoteURL)
	assert.Empty(t, snap.Remote.Owner)
	assert.False(t, snap.Remote.IsGitHub)
	assert.Equal(t, base.String(), snap.TargetSHA)
}

func TestFirstLine(t *testing.T) {
	assert.Equal(t, "title", firstLine("title\n\nbody\n"))
	assert.Equal(t, "title", firstLine("title\r\nbody"))
	assert.Empty(t, firstLine(""))
}

// stage adds a file to the index without committing.
func (tr *testRepo) stage(path string) {
	tr.t.Helper()
	_, err := tr.wt.Add(path)
	require.NoError(tr.t, err)
}

// TestSnapshotStagedNewFiles pins the classification of content that is new
// in the index relative to HEAD and then diverged in the worktree: it stays
// an add when edited, and vanishes when deleted.
func TestSnapshotStagedNewFiles(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, tr *testRepo)
		want  ChangedFiles
	}{
		{
			name: "staged new file edited in worktree stays added",
			setup: func(t *testing.T, tr *testRepo) {
				tr.writeWorktree("new.txt", "v1")
				tr.stage("new.txt")
				tr.writeWorktree("new.txt", "v2")
			},
			want: ChangedFiles{All: []string{"new.txt"}, Added: []string{"new.txt"}},
		},
		{
			name: "staged new file deleted from worktree disappears",
			setup: func(t *testing.T, tr *testRepo) {
				tr.writeWorktree("new.txt", "v1")
				tr.stage("new.txt")
				require.NoError(t, tr.wt.Filesystem.Remove("new.txt"))
			},
			want: ChangedFiles{},
		},
		{
			name: "staged new file is added",
			setup: func(t *testing.T, tr *testRepo) {
				tr.writeWorktree("new.txt", "v1")
				tr.stage("new.txt")
			},
			want: ChangedFiles{All: []string{"new.txt"}, Added: []string{"new.txt"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := initRepo(t)
			tr.commit("base.txt", "base", "base commit")
			tr.createBranch("main")
			tr.checkoutBranch("main")
			tt.setup(t, tr)
			snap := takeSnapshot(t, tr.dir, Options{})
			assert.Equal(t, tt.want, snap.ChangedFiles)
		})
	}
}
