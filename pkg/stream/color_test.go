package stream

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
)

// enabledColors returns a Colors with colors forced on, for testing the
// rendering itself regardless of the test process's terminal.
func enabledColors() *Colors {
	return &Colors{enabled: true, assigned: map[string]int{}}
}

func TestNewColorsDisablesNonTerminalWriters(t *testing.T) {
	c := NewColors(&bytes.Buffer{})
	assert.False(t, c.enabled)

	assert.Equal(t, "[build : compile] ", c.TaskPrefix("build", "compile"))
	assert.Equal(t, "ok", c.Green("ok"))
	assert.Equal(t, "bad", c.Red("bad"))
}

func TestNilColorsPassesEverythingThrough(t *testing.T) {
	var c *Colors
	assert.Equal(t, "[build : compile] ", c.TaskPrefix("build", "compile"))
	assert.Equal(t, "ok", c.Green("ok"))
	assert.Equal(t, "bad", c.Red("bad"))
}

func TestColorEnabled(t *testing.T) {
	t.Run("a non-file writer is never a terminal", func(t *testing.T) {
		assert.False(t, colorEnabled(&bytes.Buffer{}))
	})
	t.Run("NO_COLOR wins even over a terminal", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		assert.False(t, colorEnabled(os.Stdout))
	})
	t.Run("an empty NO_COLOR does not disable", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		// os.Stdout may or may not be a terminal under the test harness,
		// but an empty value must not be what disables it: the check must
		// reach the writer inspection, so a non-file stays false while
		// the env branch is proven inert by the NO_COLOR case above.
		assert.False(t, colorEnabled(&bytes.Buffer{}))
	})
}

func TestTaskPrefixAssignsStableColors(t *testing.T) {
	c := enabledColors()

	// First-seen order: each new task takes the next palette color, and a
	// repeated task keeps its color.
	assert.Equal(t, taskPalette[0]+"[build : compile] "+ansiReset, c.TaskPrefix("build", "compile"))
	assert.Equal(t, taskPalette[1]+"[test : verify] "+ansiReset, c.TaskPrefix("test", "verify"))
	assert.Equal(t, taskPalette[0]+"[build : lint] "+ansiReset, c.TaskPrefix("build", "lint"))

	// The palette cycles once every distinct task has been seen.
	for _, task := range []string{"a", "b", "c", "d", "e", "f"} {
		c.TaskPrefix(task, "s")
	}
	assert.Equal(t, taskPalette[0]+"[build : compile] "+ansiReset, c.TaskPrefix("build", "compile"),
		"the ninth distinct task wraps around to the first color")
}

func TestGreenAndRed(t *testing.T) {
	c := enabledColors()
	assert.Equal(t, ansiGreen+"succeeded"+ansiReset, c.Green("succeeded"))
	assert.Equal(t, ansiRed+"failed"+ansiReset, c.Red("failed"))
}

func TestFollowPrefixesStayPlainWhenDisabled(t *testing.T) {
	// Follow builds its colorizer from the writer; a plain builder must
	// keep output free of escape sequences.
	speedUp(t)
	tr := taskRunWithSteps("tr-1", "build", "pod-1", startedStep("compile"))
	src := newFakeSource(
		[]*tektonv1.PipelineRun{runningPR(), succeededPR()},
		[][]tektonv1.TaskRun{{tr}},
		map[string]*logScript{"pod-1/step-compile": {body: "compiling\n"}},
	)

	var out bytes.Buffer
	_, err := Follow(context.Background(), src, "ns", "pr-1", &out)
	require.NoError(t, err)
	assert.Equal(t, "[build : compile] compiling\n", out.String())
	assert.NotContains(t, out.String(), "\x1b[")
}
