package stream

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/mattn/go-isatty"
)

// ANSI escape sequences. Colors are disabled when the writer is not a
// terminal or NO_COLOR is set (https://no-color.org), so piped and
// redirected output stays plain.
const (
	ansiReset = "\x1b[0m"
	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
)

// taskPalette cycles through colors kept distinct from the green and red
// reserved for success and failure lines.
var taskPalette = []string{
	"\x1b[36m", // cyan
	"\x1b[35m", // magenta
	"\x1b[33m", // yellow
	"\x1b[94m", // bright blue
	"\x1b[96m", // bright cyan
	"\x1b[95m", // bright magenta
	"\x1b[93m", // bright yellow
	"\x1b[34m", // blue
}

// Colors renders ANSI-colored terminal output. A nil *Colors and a
// disabled one behave identically: everything passes through plain.
type Colors struct {
	enabled bool
	mu      sync.Mutex
	// assigned maps task name to palette index in first-seen order, so
	// every task in a run keeps one stable color.
	assigned map[string]int
}

// NewColors returns a Colors for w, enabled only when w is a terminal
// and NO_COLOR is unset.
func NewColors(w io.Writer) *Colors {
	return &Colors{enabled: colorEnabled(w), assigned: map[string]int{}}
}

// colorEnabled reports whether colored output makes sense for w.
func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	return ok && isatty.IsTerminal(f.Fd())
}

// Green renders s in green, for success lines.
func (c *Colors) Green(s string) string { return c.paint(s, ansiGreen) }

// Red renders s in red, for failure lines.
func (c *Colors) Red(s string) string { return c.paint(s, ansiRed) }

// TaskPrefix renders "[task : step] " in the task's color: each task
// gets the next palette color in first-seen order.
func (c *Colors) TaskPrefix(task, step string) string {
	prefix := fmt.Sprintf("[%s : %s] ", task, step)
	if c == nil || !c.enabled {
		return prefix
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	idx, ok := c.assigned[task]
	if !ok {
		idx = len(c.assigned) % len(taskPalette)
		c.assigned[task] = idx
	}
	return c.paint(prefix, taskPalette[idx])
}

// paint wraps s in the given color when colors are enabled.
func (c *Colors) paint(s, color string) string {
	if c == nil || !c.enabled {
		return s
	}
	return color + s + ansiReset
}
