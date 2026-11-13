package pac

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// pipelineFile is one .tekton YAML document set, kept per file so matches
// can be attributed back to the file they came from.
type pipelineFile struct {
	Name    string
	Content string
}

// loadTektonDir reads every .yaml and .yml file under dir (recursively),
// sorted by path for deterministic output. It errors when the directory is
// missing or unreadable.
func loadTektonDir(dir string) ([]pipelineFile, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("reading tekton directory: %w", err)
	}
	files, err := loadTektonFS(os.DirFS(dir))
	if err != nil {
		return nil, fmt.Errorf("reading tekton directory %s: %w", dir, err)
	}
	return files, nil
}

// loadTektonFS is the fs.FS-backed implementation of loadTektonDir.
func loadTektonFS(fsys fs.FS) ([]pipelineFile, error) {
	var names []string
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch filepath.Ext(path) {
		case ".yaml", ".yml":
			names = append(names, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking tekton directory: %w", err)
	}
	sort.Strings(names)

	files := make([]pipelineFile, 0, len(names))
	for _, name := range names {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", name, err)
		}
		files = append(files, pipelineFile{Name: name, Content: string(b)})
	}
	return files, nil
}
