// Package git extracts repository state from a local git checkout using the
// go-git library. It never shells out to the git CLI.
package git

import "context"

// Remote describes the parsed origin remote of a repository.
type Remote struct {
	Host     string // e.g. "github.com"
	Owner    string // best-effort owner or group
	Repo     string // repository name without the .git suffix
	IsGitHub bool   // host is github.com
}

// ChangedFiles is the classified set of file paths that differ between the
// target branch, HEAD and the working tree. All contains the sorted union of
// every bucket.
type ChangedFiles struct {
	All      []string
	Added    []string
	Modified []string
	Deleted  []string
	Renamed  []string
}

// Snapshot is a point-in-time view of the repository state pact cares about.
type Snapshot struct {
	RepoRoot     string // absolute path of the worktree root
	Branch       string // current branch, empty when detached
	Detached     bool   // HEAD is not on a branch
	HeadSHA      string // full SHA of HEAD
	HeadTitle    string // first line of the HEAD commit message
	Author       string // HEAD commit author name
	RemoteURL    string // raw origin URL, empty when there is no origin
	Remote       Remote // parsed origin remote, zero when unparseable
	TargetBranch string // resolved target branch, e.g. "main"
	TargetSHA    string // tip of the target branch
	BaseSHA      string // merge base of HEAD and the target branch
	ChangedFiles ChangedFiles
}

// Options controls how a Snapshot is taken.
type Options struct {
	// TargetBranch overrides the target branch. Empty means auto-detect
	// (origin/HEAD, then main, then master).
	TargetBranch string
}

// Inspector takes snapshots of a git repository.
type Inspector interface {
	Snapshot(ctx context.Context, opts Options) (*Snapshot, error)
}
