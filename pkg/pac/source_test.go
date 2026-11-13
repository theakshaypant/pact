package pac

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadTektonFS(t *testing.T) {
	fsys := fstest.MapFS{
		"b.yaml":     {Data: []byte("kind: b")},
		"a.yml":      {Data: []byte("kind: a")},
		"c.txt":      {Data: []byte("ignored")},
		"sub/d.yaml": {Data: []byte("kind: d")},
		"sub/e.json": {Data: []byte("ignored")},
	}
	files, err := loadTektonFS(fsys)
	require.NoError(t, err)

	var names []string
	var contents []string
	for _, f := range files {
		names = append(names, f.Name)
		contents = append(contents, f.Content)
	}
	assert.Equal(t, []string{"a.yml", "b.yaml", "sub/d.yaml"}, names)
	assert.Equal(t, []string{"kind: a", "kind: b", "kind: d"}, contents)
}

func TestLoadTektonDirMissing(t *testing.T) {
	_, err := loadTektonDir(t.TempDir() + "/.tekton")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading tekton directory")
}
