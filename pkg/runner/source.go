package runner

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// listSourceFiles returns the files pact packages as source: everything
// git tracks plus untracked files that are not ignored, i.e. exactly the
// working tree as a commit would capture it (.gitignore respected).
// The git CLI is used for this, which AGENTS.md sanctions for repository
// inspection.
func listSourceFiles(ctx context.Context, repoRoot string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot,
		"ls-files", "--cached", "--others", "--exclude-standard", "-z")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing repository files (is %q a git repository?): %w", repoRoot, err)
	}
	var files []string
	for _, name := range strings.Split(string(out), "\x00") {
		if name != "" {
			files = append(files, name)
		}
	}
	return files, nil
}

// packageSource writes a gzipped tar archive of the repository's source
// files (relative to repoRoot) to w, returning the file count and the
// archive size.
func packageSource(ctx context.Context, repoRoot string, w io.Writer) (files int, err error) {
	names, err := listSourceFiles(ctx, repoRoot)
	if err != nil {
		return 0, err
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	defer func() {
		if cerr := tw.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing tar archive: %w", cerr)
		}
		if cerr := gz.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing gzip stream: %w", cerr)
		}
	}()

	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return files, err
		}
		added, err := tarFile(tw, repoRoot, name)
		if err != nil {
			return files, err
		}
		if added {
			files++
		}
	}
	return files, nil
}

// tarFile appends one repository file to the archive, stored under its
// repository-relative name. It reports whether the file was added:
// symlinks, special files and tracked-but-deleted files are skipped —
// a deletion in the working tree is part of the change being tested and
// simply means the file is absent from the uploaded source.
func tarFile(tw *tar.Writer, repoRoot, name string) (added bool, err error) {
	path := filepath.Join(repoRoot, name)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // tracked but deleted in the working tree
		}
		return false, fmt.Errorf("stating %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return false, nil // skip symlinks and other specials: git ls-files -z lists them, tar of targets is wrong
	}
	f, err := os.Open(path) //nolint:gosec // path comes from git ls-files inside the user's own repo
	if err != nil {
		return false, fmt.Errorf("opening %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()

	header := &tar.Header{
		Name:     filepath.ToSlash(name),
		Mode:     int64(info.Mode().Perm()),
		Size:     info.Size(),
		ModTime:  info.ModTime(),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(header); err != nil {
		return false, fmt.Errorf("writing tar header for %s: %w", name, err)
	}
	if _, err := io.Copy(tw, f); err != nil {
		return false, fmt.Errorf("archiving %s: %w", name, err)
	}
	return true, nil
}
