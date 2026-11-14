package pacdef

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Dir is a .tekton directory: file name to manifest. The file names are
// part of the match output contract, so they stay literal.
type Dir map[string]Manifest

// Write writes the directory to a fresh temp dir and returns its path.
func (d Dir) Write(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	for name, m := range d {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(m.YAML()), 0o644))
	}
	return dir
}
