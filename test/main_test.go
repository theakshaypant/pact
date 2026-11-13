package test

import (
	"os"
	"testing"

	"github.com/theakshaypant/pact/test/pkg/pactcli"
)

// TestMain cleans up the compiled pact binary after all scenarios ran.
// The binary itself is built lazily by the first pactcli.Run call.
func TestMain(m *testing.M) {
	code := m.Run()
	pactcli.Cleanup()
	os.Exit(code)
}
