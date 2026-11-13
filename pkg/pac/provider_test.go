package pac

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/pkg/git"
)

func TestLocalProviderGetFiles(t *testing.T) {
	p := newLocalProvider(git.ChangedFiles{
		All:      []string{"a.go", "a.go", "b.txt"},
		Added:    []string{"a.go"},
		Modified: []string{"b.txt"},
	})
	files, err := p.GetFiles(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.go", "b.txt"}, files.All)
	assert.Equal(t, []string{"a.go"}, files.Added)
	assert.Equal(t, []string{"b.txt"}, files.Modified)
}
