// Package pactcli runs the pact binary as a black box: it compiles the
// real CLI once per test binary and executes it against scratch
// repositories, asserting on stdout, stderr and the exit code — the same
// surface a user or a CI script sees.
package pactcli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	binary   string
	buildErr error
	once     sync.Once
)

// Result captures one black-box pact invocation.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Build compiles the pact CLI once for the lifetime of the test binary
// and returns its path. The build is hermetic: -mod=vendor is forced so
// host GOFLAGS cannot bypass the vendored dependencies, and no network
// access is needed. The build error (if any) is returned on every call,
// so every failing test reports the real cause.
func Build() (string, error) {
	once.Do(func() {
		dir, err := os.MkdirTemp("", "pact-e2e-bin-*")
		if err != nil {
			buildErr = fmt.Errorf("creating binary dir: %w", err)
			return
		}
		binary = filepath.Join(dir, "pact")
		cmd := exec.CommandContext(context.Background(), "go", "build",
			"-o", binary, "github.com/theakshaypant/pact/cmd/pact")
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=vendor")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("building pact binary: %w\n%s", err, out)
		}
	})
	return binary, buildErr
}

// Cleanup removes the temporary binary directory. Call it from TestMain
// after m.Run.
func Cleanup() {
	if binary != "" {
		_ = os.RemoveAll(filepath.Dir(binary))
	}
}

// Run executes the pact binary with args and captures its output. A
// non-zero exit code is returned in the Result, not as an error, so tests
// can assert on both success and failure paths.
//
// Every invocation and its full output are logged with t.Logf, so they
// appear for failing tests and under go test -v. Set PACT_E2E_VERBOSE=1
// to also pass --verbose to pact, surfacing PAC's own matcher and
// resolver logs in stderr.
func Run(t *testing.T, args ...string) Result {
	t.Helper()
	path, err := Build()
	require.NoError(t, err)
	if os.Getenv("PACT_E2E_VERBOSE") != "" {
		args = append([]string{"--verbose"}, args...)
	}
	t.Logf("pact %s", strings.Join(args, " "))
	cmd := exec.CommandContext(t.Context(), path, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr, "pact failed to start: %v", err)
		code = exitErr.ExitCode()
	}
	t.Logf("pact exited %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	return Result{Stdout: out.String(), Stderr: errOut.String(), ExitCode: code}
}
