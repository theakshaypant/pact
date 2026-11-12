package git

import (
	"fmt"
	"sort"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/merkletrie"
)

// fileClass classifies a changed path.
type fileClass string

const (
	classAdded    fileClass = "added"
	classModified fileClass = "modified"
	classDeleted  fileClass = "deleted"
	classRenamed  fileClass = "renamed"
)

// diffCommits returns the classified changes between the base and head
// commits (a three-dot style diff: base is expected to be a merge base).
func diffCommits(base, head *object.Commit) (ChangedFiles, error) {
	baseTree, err := base.Tree()
	if err != nil {
		return ChangedFiles{}, fmt.Errorf("reading base tree: %w", err)
	}
	headTree, err := head.Tree()
	if err != nil {
		return ChangedFiles{}, fmt.Errorf("reading head tree: %w", err)
	}
	changes, err := baseTree.Diff(headTree)
	if err != nil {
		return ChangedFiles{}, fmt.Errorf("diffing trees: %w", err)
	}
	classes := map[string]fileClass{}
	for _, ch := range changes {
		path, class := changeClass(ch)
		classes[path] = class
	}
	return fromClasses(classes), nil
}

// changeClass maps a tree change to a path and its classification. go-git
// does no rename detection, so a rename appears as a delete of the old path
// plus an insert of the new one; both are reported, which is accurate for
// path matching.
func changeClass(ch *object.Change) (string, fileClass) {
	action, err := ch.Action()
	if err != nil {
		action = merkletrie.Modify
	}
	path := ch.To.Name
	if path == "" {
		path = ch.From.Name
	}
	switch action {
	case merkletrie.Insert:
		return path, classAdded
	case merkletrie.Delete:
		return path, classDeleted
	default:
		return path, classModified
	}
}

// worktreeChanges returns the classified uncommitted changes (staged,
// unstaged and untracked) of a worktree. go-git's Status honours
// .gitignore. The working-tree column takes precedence over the staging
// column, except for content newly staged relative to HEAD: a staged new
// file is an add regardless of later worktree edits, and one that was also
// deleted from the worktree never existed at all.
func worktreeChanges(w *git.Worktree) (ChangedFiles, error) {
	status, err := w.Status()
	if err != nil {
		return ChangedFiles{}, fmt.Errorf("reading worktree status: %w", err)
	}
	classes := map[string]fileClass{}
	for path, fs := range status {
		if fs.Staging == git.Untracked && fs.Worktree == git.Untracked {
			classes[path] = classAdded // untracked file
			continue
		}
		if fs.Staging == git.Added || fs.Staging == git.Copied {
			// Staged content that is not in HEAD: an add even when the
			// worktree diverged afterwards; a worktree delete cancels it.
			if fs.Worktree == git.Deleted {
				continue
			}
			classes[path] = classAdded
			continue
		}
		class, ok := codeClass(fs.Worktree)
		if !ok {
			class, ok = codeClass(fs.Staging)
			if !ok {
				continue
			}
		}
		classes[path] = class
	}
	return fromClasses(classes), nil
}

// codeClass maps a go-git status code to a file classification.
func codeClass(code git.StatusCode) (fileClass, bool) {
	switch code {
	case git.Added, git.Copied:
		return classAdded, true
	case git.Deleted:
		return classDeleted, true
	case git.Modified, git.UpdatedButUnmerged:
		return classModified, true
	case git.Renamed:
		return classRenamed, true
	}
	return "", false
}

// merge combines committed and working-tree changes into one classified set.
// Working-tree state takes precedence; a path added on the branch and deleted
// in the working tree cancels out, and one deleted then recreated becomes
// modified.
func merge(committed, working ChangedFiles) ChangedFiles {
	classes := toClasses(committed)
	for path, wc := range toClasses(working) {
		cc, ok := classes[path]
		switch {
		case !ok:
			classes[path] = wc
		case cc == classAdded && wc == classDeleted:
			delete(classes, path) // net no-op
		case cc == classDeleted && wc == classAdded:
			classes[path] = classModified
		case cc == classAdded && wc == classModified:
			// still a new file in the combined change
		default:
			classes[path] = wc
		}
	}
	return fromClasses(classes)
}

// fromClasses builds sorted, deduplicated buckets from a path classification.
func fromClasses(classes map[string]fileClass) ChangedFiles {
	var cf ChangedFiles
	for path, class := range classes {
		switch class {
		case classAdded:
			cf.Added = append(cf.Added, path)
		case classModified:
			cf.Modified = append(cf.Modified, path)
		case classDeleted:
			cf.Deleted = append(cf.Deleted, path)
		case classRenamed:
			cf.Renamed = append(cf.Renamed, path)
		}
	}
	cf.All = append(cf.All, cf.Added...)
	cf.All = append(cf.All, cf.Modified...)
	cf.All = append(cf.All, cf.Deleted...)
	cf.All = append(cf.All, cf.Renamed...)
	for _, bucket := range [][]string{cf.All, cf.Added, cf.Modified, cf.Deleted, cf.Renamed} {
		sort.Strings(bucket)
	}
	return cf
}

// toClasses rebuilds a path classification from buckets, ignoring All.
func toClasses(cf ChangedFiles) map[string]fileClass {
	classes := map[string]fileClass{}
	for _, p := range cf.Added {
		classes[p] = classAdded
	}
	for _, p := range cf.Modified {
		classes[p] = classModified
	}
	for _, p := range cf.Deleted {
		classes[p] = classDeleted
	}
	for _, p := range cf.Renamed {
		classes[p] = classRenamed
	}
	return classes
}
