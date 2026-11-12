package git

import (
	"context"
	"errors"
	"fmt"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// repository is the go-git backed Inspector implementation.
type repository struct {
	r    *git.Repository
	root string
}

// Open opens the git repository containing dir, walking up parent
// directories until a .git is found. Linked worktrees are supported.
func Open(dir string) (Inspector, error) {
	r, err := git.PlainOpenWithOptions(dir, &git.PlainOpenOptions{
		DetectDotGit:          true,
		EnableDotGitCommonDir: true,
	})
	if err != nil {
		return nil, fmt.Errorf("opening git repository at %s (are you inside one?): %w", dir, err)
	}
	insp := &repository{r: r, root: dir}
	// Prefer the actual worktree root so .tekton and file paths are stable
	// regardless of the subdirectory pact was invoked from.
	if wt, err := r.Worktree(); err == nil {
		if root := wt.Filesystem.Root(); root != "" {
			insp.root = root
		}
	}
	return insp, nil
}

// Snapshot extracts the repository state: HEAD, remote, target branch and
// the merged changed files. See merge for the combination semantics.
func (repo *repository) Snapshot(ctx context.Context, opts Options) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("inspecting repository: %w", err)
	}

	snap := &Snapshot{RepoRoot: repo.root}
	if err := repo.headInfo(snap); err != nil {
		return nil, err
	}

	var err error
	snap.RemoteURL, snap.Remote, err = repo.remote()
	if err != nil {
		return nil, err
	}

	target := opts.TargetBranch
	if target == "" {
		if target, err = repo.defaultTargetBranch(); err != nil {
			return nil, err
		}
	}
	snap.TargetBranch = target

	targetHash, err := repo.resolveTarget(target)
	if err != nil {
		return nil, fmt.Errorf("resolving target branch %q: %w", target, err)
	}
	snap.TargetSHA = targetHash.String()

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("inspecting repository: %w", err)
	}
	headCommit, err := repo.r.CommitObject(plumbing.NewHash(snap.HeadSHA))
	if err != nil {
		return nil, fmt.Errorf("reading HEAD commit: %w", err)
	}
	snap.HeadTitle = firstLine(headCommit.Message)
	snap.Author = headCommit.Author.Name

	committed, err := repo.committedChanges(headCommit, targetHash, target)
	if err != nil {
		return nil, err
	}
	snap.BaseSHA = committed.BaseSHA

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("inspecting repository: %w", err)
	}
	working := ChangedFiles{}
	if wt, werr := repo.r.Worktree(); werr == nil {
		if working, err = worktreeChanges(wt); err != nil {
			return nil, err
		}
	} // bare repository: no working-tree changes

	snap.ChangedFiles = merge(committed.Files, working)
	return snap, nil
}

// headInfo fills the HEAD-derived snapshot fields.
func (repo *repository) headInfo(snap *Snapshot) error {
	head, err := repo.r.Head()
	if err != nil {
		return fmt.Errorf("reading HEAD (does the repository have any commits?): %w", err)
	}
	snap.HeadSHA = head.Hash().String()
	if head.Name().IsBranch() {
		snap.Branch = head.Name().Short()
	} else {
		snap.Detached = true
	}
	return nil
}

// committedResult pairs the merge base with the committed diff.
type committedResult struct {
	BaseSHA string
	Files   ChangedFiles
}

// committedChanges returns the changes between the target branch and HEAD
// (a three-dot style diff from the merge base). When HEAD is the target
// tip the committed diff is empty and the base is HEAD itself.
func (repo *repository) committedChanges(head *object.Commit, targetHash plumbing.Hash, target string) (committedResult, error) {
	if targetHash == head.Hash {
		// HEAD is the target tip: a branch onto itself has an empty diff,
		// only working-tree changes count.
		return committedResult{BaseSHA: head.Hash.String()}, nil
	}
	targetCommit, err := repo.r.CommitObject(targetHash)
	if err != nil {
		return committedResult{}, fmt.Errorf("reading target branch %q commit: %w", target, err)
	}
	bases, err := head.MergeBase(targetCommit)
	if err != nil {
		return committedResult{}, fmt.Errorf("computing merge base with %q: %w", target, err)
	}
	if len(bases) == 0 {
		return committedResult{}, fmt.Errorf("no common ancestor between HEAD and target branch %q "+
			"(unrelated histories or shallow clone; try `git fetch --unshallow`)", target)
	}
	files, err := diffCommits(bases[0], head)
	if err != nil {
		return committedResult{}, fmt.Errorf("diffing HEAD against %q: %w", target, err)
	}
	return committedResult{BaseSHA: bases[0].Hash.String(), Files: files}, nil
}

// remote returns the raw origin URL and its parsed form. A missing origin or
// an unparseable URL is not an error: the raw URL is kept and the parsed
// remote left zero.
func (repo *repository) remote() (string, Remote, error) {
	rem, err := repo.r.Remote("origin")
	if err != nil {
		if errors.Is(err, git.ErrRemoteNotFound) {
			return "", Remote{}, nil
		}
		return "", Remote{}, fmt.Errorf("reading origin remote: %w", err)
	}
	urls := rem.Config().URLs
	if len(urls) == 0 {
		return "", Remote{}, nil
	}
	raw := urls[0]
	parsed, err := ParseRemote(raw)
	if err != nil {
		return raw, Remote{}, nil
	}
	return raw, parsed, nil
}

// defaultTargetBranch resolves the default branch: the origin HEAD symbolic
// ref when present, falling back to main then master.
func (repo *repository) defaultTargetBranch() (string, error) {
	if ref, err := repo.r.Reference(plumbing.NewRemoteHEADReferenceName("origin"), false); err == nil {
		if target := ref.Target(); target.IsRemote() {
			if name := strings.TrimPrefix(target.Short(), "origin/"); name != "" {
				return name, nil
			}
		}
	}
	for _, name := range []string{"main", "master"} {
		if _, err := repo.resolveTarget(name); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("could not detect the default target branch (no origin/HEAD, main or master); pass --target-branch")
}

// resolveTarget resolves a branch name to a commit hash, preferring the
// remote-tracking ref over a possibly stale local one.
func (repo *repository) resolveTarget(name string) (plumbing.Hash, error) {
	revs := []string{
		plumbing.NewRemoteReferenceName("origin", name).String(),
		plumbing.NewBranchReferenceName(name).String(),
	}
	for _, rev := range revs {
		if h, err := repo.r.ResolveRevision(plumbing.Revision(rev)); err == nil {
			return *h, nil
		}
	}
	return plumbing.ZeroHash, fmt.Errorf("branch %q not found locally or on origin", name)
}

// firstLine returns the first line of a commit message.
func firstLine(msg string) string {
	msg = strings.TrimRight(msg, "\r\n")
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return strings.TrimSpace(msg)
}
