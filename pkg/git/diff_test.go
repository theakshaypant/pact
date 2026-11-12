package git

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func cf(paths ...string) []string { return paths }

func TestMerge(t *testing.T) {
	tests := []struct {
		name      string
		committed ChangedFiles
		working   ChangedFiles
		want      ChangedFiles
	}{
		{
			name:      "disjoint sets are unioned",
			committed: ChangedFiles{Added: cf("a.go"), All: cf("a.go")},
			working:   ChangedFiles{Added: cf("b.txt"), All: cf("b.txt")},
			want:      ChangedFiles{Added: cf("a.go", "b.txt"), All: cf("a.go", "b.txt")},
		},
		{
			name:      "added then deleted cancels out",
			committed: ChangedFiles{Added: cf("new.go"), All: cf("new.go")},
			working:   ChangedFiles{Deleted: cf("new.go"), All: cf("new.go")},
			want:      ChangedFiles{},
		},
		{
			name:      "deleted then recreated is modified",
			committed: ChangedFiles{Deleted: cf("gone.go"), All: cf("gone.go")},
			working:   ChangedFiles{Added: cf("gone.go"), All: cf("gone.go")},
			want:      ChangedFiles{Modified: cf("gone.go"), All: cf("gone.go")},
		},
		{
			name:      "added then modified stays added",
			committed: ChangedFiles{Added: cf("new.go"), All: cf("new.go")},
			working:   ChangedFiles{Modified: cf("new.go"), All: cf("new.go")},
			want:      ChangedFiles{Added: cf("new.go"), All: cf("new.go")},
		},
		{
			name:      "working classification wins on conflict-free overlap",
			committed: ChangedFiles{Modified: cf("x.go"), All: cf("x.go")},
			working:   ChangedFiles{Deleted: cf("x.go"), All: cf("x.go")},
			want:      ChangedFiles{Deleted: cf("x.go"), All: cf("x.go")},
		},
		{
			name:      "empty inputs",
			committed: ChangedFiles{},
			working:   ChangedFiles{},
			want:      ChangedFiles{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, merge(tt.committed, tt.working))
		})
	}
}
