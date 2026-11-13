package pac

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodePipelineRuns(t *testing.T) {
	yaml := `apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: first
---
# a comment
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: second
`
	runs, err := DecodePipelineRuns([]byte(yaml))
	require.NoError(t, err)
	require.Len(t, runs, 2)
	var names []string
	for _, pr := range runs {
		names = append(names, pr.Name)
	}
	assert.Equal(t, []string{"first", "second"}, names)
}

func TestDecodePipelineRunsRejectsForeignKind(t *testing.T) {
	_, err := DecodePipelineRuns([]byte("apiVersion: tekton.dev/v1\nkind: Task\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a PipelineRun")
}

func TestDecodePipelineRunsRejectsNameless(t *testing.T) {
	_, err := DecodePipelineRuns([]byte("apiVersion: tekton.dev/v1\nkind: PipelineRun\n" +
		"metadata:\n  labels: {}\nspec:\n  pipelineSpec:\n    tasks: []\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither name nor generateName")
}

func TestDecodePipelineRunsRejectsEmpty(t *testing.T) {
	_, err := DecodePipelineRuns([]byte("# nothing\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pipelineruns")
}
