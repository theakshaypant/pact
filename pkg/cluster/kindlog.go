package cluster

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"sigs.k8s.io/kind/pkg/log"
)

// kindLogger adapts an io.Writer to kind's log.Logger so kind's own
// progress output ("• Preparing nodes ...", "✓ Preparing nodes") shows up
// in pact's progress stream. Without it kind logs nothing in library
// mode, and cluster creation looks frozen.
type kindLogger struct {
	w  io.Writer
	mu sync.Mutex
}

// newKindLogger returns a log.Logger writing to w, or kind's no-op
// logger when w is nil.
func newKindLogger(w io.Writer) log.Logger {
	if w == nil {
		return log.NoopLogger{}
	}
	return &kindLogger{w: w}
}

// writeMsg writes one message, ensuring exactly one trailing newline
// (kind's status lines already end in one).
func (l *kindLogger) writeMsg(format string, args ...any) {
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, format, args...)
}

func (l *kindLogger) Warn(message string) { l.writeMsg("%s", message) }

func (l *kindLogger) Warnf(format string, args ...any) { l.writeMsg(format, args...) }

func (l *kindLogger) Error(message string) { l.writeMsg("%s", message) }

func (l *kindLogger) Errorf(format string, args ...any) { l.writeMsg(format, args...) }

// V returns an info logger for a verbosity level. Only V(0) — kind's
// user-facing status messages — is enabled; higher levels are debug
// noise pact does not want in its progress stream.
func (l *kindLogger) V(level log.Level) log.InfoLogger {
	return kindInfoLogger{parent: l, enabled: level == 0}
}

// kindInfoLogger implements kind's log.InfoLogger for a verbosity level.
type kindInfoLogger struct {
	parent  *kindLogger
	enabled bool
}

func (i kindInfoLogger) Enabled() bool { return i.enabled }

func (i kindInfoLogger) Info(message string) {
	if i.enabled {
		i.parent.writeMsg("%s", message)
	}
}

func (i kindInfoLogger) Infof(format string, args ...any) {
	if i.enabled {
		i.parent.writeMsg(format, args...)
	}
}
