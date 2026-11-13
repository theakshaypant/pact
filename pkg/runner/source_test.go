package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSourceRepo creates a git repository with a committed file, an
// uncommitted tracked change, an untracked file and an ignored file.
func newSourceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	git("init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("committed"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o644))
	git("add", ".")
	git("commit", "-m", "init")

	// The working tree state pact must capture:
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("UNCOMMITTED"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("untracked"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "nested file.txt"), []byte("nested"), 0o644))
	// ...and the state it must not:
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("ignored"), 0o644))
	return dir
}

func TestPackageSource(t *testing.T) {
	dir := newSourceRepo(t)

	var buf bytes.Buffer
	files, err := packageSource(context.Background(), dir, &buf)
	require.NoError(t, err)
	assert.Equal(t, 4, files) // README.md, .gitignore, untracked.txt, src/nested file.txt

	names, contents := readArchive(t, &buf)
	assert.Equal(t, []string{".gitignore", "README.md", "src/nested file.txt", "untracked.txt"}, names,
		"ignored files must be excluded, nested and spaced names kept")
	assert.Equal(t, "UNCOMMITTED", contents["README.md"], "the uncommitted content must be packaged")
	assert.Equal(t, "nested", contents["src/nested file.txt"])
	assert.NotContains(t, names, "ignored.txt")
}

func TestPackageSourceNotARepository(t *testing.T) {
	var buf bytes.Buffer
	_, err := packageSource(context.Background(), t.TempDir(), &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a git repository")
}

// A deletion in the working tree is part of the change under test: the
// file must simply be absent from the archive, not abort the run.
// Symlinks are skipped but must not be counted as packaged files.
func TestPackageSourceSkipsDeletedAndSpecialFiles(t *testing.T) {
	dir := newSourceRepo(t)
	require.NoError(t, os.Remove(filepath.Join(dir, "README.md")))
	require.NoError(t, os.Symlink("untracked.txt", filepath.Join(dir, "link.txt")))

	var buf bytes.Buffer
	files, err := packageSource(context.Background(), dir, &buf)
	require.NoError(t, err, "a deleted tracked file must not fail the run")
	assert.Equal(t, 3, files, ".gitignore, untracked.txt and src/nested file.txt only")

	names, _ := readArchive(t, &buf)
	assert.Equal(t, []string{".gitignore", "src/nested file.txt", "untracked.txt"}, names,
		"deleted files and symlinks must not be archived")
}

// readArchive decompresses and lists a packageSource archive.
func readArchive(t *testing.T, buf *bytes.Buffer) (names []string, contents map[string]string) {
	t.Helper()
	gz, err := gzip.NewReader(buf)
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	contents = map[string]string{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		body, err := io.ReadAll(tr)
		require.NoError(t, err)
		names = append(names, header.Name)
		contents[header.Name] = string(body)
	}
	sort.Strings(names)
	return names, contents
}
