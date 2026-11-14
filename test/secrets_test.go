package test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/test/pkg/pactcli"
)

// writeSecretsFile writes a dotenv secrets file and returns its path.
func writeSecretsFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.env")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestRunBadSecretFlagFails(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")

	res := pactcli.Run(t, "-C", r.Dir, "run", "--secret", "noequals")
	assert.Equal(t, 1, res.ExitCode)
	assert.Contains(t, res.Stderr, "invalid --secret: want name.key=value")
	assert.Empty(t, res.Stdout)
}

// A validation error must quote only the reference, never the value.
func TestRunInvalidSecretNameNeverLeaksValue(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")

	res := pactcli.Run(t, "-C", r.Dir, "run", "--secret", "Bad_Name.k=seekrit")
	assert.Equal(t, 1, res.ExitCode)
	assert.Contains(t, res.Stderr, "invalid secret name")
	assert.NotContains(t, res.Stderr, "seekrit")
}

func TestRunMissingSecretFileFails(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")

	res := pactcli.Run(t, "-C", r.Dir, "run", "--secret-file", filepath.Join(t.TempDir(), "nope.env"))
	assert.Equal(t, 1, res.ExitCode)
	assert.Contains(t, res.Stderr, "reading --secret-file")
}

// Secrets are parsed before any git work, so a bad flag fails even in a
// directory that is not a repository.
func TestRunBadSecretFailsBeforeGitWork(t *testing.T) {
	res := pactcli.Run(t, "-C", t.TempDir(), "run", "--secret", "noequals")
	assert.Equal(t, 1, res.ExitCode)
	assert.Contains(t, res.Stderr, "invalid --secret: want name.key=value")
}

func TestResolveAndMatchRejectSecretFlags(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	file := writeSecretsFile(t, "db.password=hunter2\n")

	for _, cmd := range []string{"resolve", "match"} {
		res := pactcli.Run(t, "-C", r.Dir, cmd, "--secret", "db.password=x")
		assert.Equal(t, 1, res.ExitCode, "%s must not accept --secret", cmd)
		assert.Contains(t, res.Stderr, "unknown flag")

		res = pactcli.Run(t, "-C", r.Dir, cmd, "--secret-file", file)
		assert.Equal(t, 1, res.ExitCode, "%s must not accept --secret-file", cmd)
		assert.Contains(t, res.Stderr, "unknown flag")
	}
}
