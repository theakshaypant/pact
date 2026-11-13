package cluster

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/kind/pkg/log"
)

func TestNewKindLoggerNilWriterIsNoop(t *testing.T) {
	l := newKindLogger(nil)
	require.IsType(t, log.NoopLogger{}, l)
	// The no-op logger must accept calls without panicking.
	l.V(0).Info("nothing")
	l.Warnf("nothing %s", "here")
}

func TestKindLoggerForwardsStatusMessages(t *testing.T) {
	var buf bytes.Buffer
	l := newKindLogger(&buf)

	l.V(0).Infof(" • Preparing nodes %s  ...\n", "📦")
	l.V(0).Infof(" ✓ Preparing nodes %s", "📦")
	l.Warnf("docker is sleepy")

	out := buf.String()
	assert.Contains(t, out, " • Preparing nodes 📦  ...\n")
	assert.Contains(t, out, " ✓ Preparing nodes 📦\n", "a missing trailing newline must be added")
	assert.Contains(t, out, "docker is sleepy\n")
	assert.Equal(t, 3, strings.Count(out, "\n"), "no double newlines")
}

func TestKindLoggerHidesDebugLevels(t *testing.T) {
	var buf bytes.Buffer
	l := newKindLogger(&buf)

	assert.False(t, l.V(1).Enabled(), "V(1) is kind debug noise and must be disabled")
	l.V(1).Info("debug detail")
	l.V(3).Infof("trace detail %d", 1)
	assert.Empty(t, buf.String(), "nothing above V(0) may reach the progress stream")
}

func TestKindLoggerErrorsAndPlainMessages(t *testing.T) {
	var buf bytes.Buffer
	l := newKindLogger(&buf)

	l.Error("boom")
	l.Errorf("boom %d", 2)
	l.V(0).Info("plain")

	assert.Contains(t, buf.String(), "boom\n")
	assert.Contains(t, buf.String(), "boom 2\n")
	assert.Contains(t, buf.String(), "plain\n")
}
