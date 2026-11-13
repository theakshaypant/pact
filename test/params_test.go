package test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/test/pkg/pactcli"
	"github.com/theakshaypant/pact/test/pkg/tekton"
)

// writeParamsFile writes a dotenv params file and returns its path.
func writeParamsFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "params.env")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestResolveCustomParamsFromFlags(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("params.yaml", manifest(t, "custom-params.yaml"))

	prs := resolveRuns(t, r, "--param", "environment=prod", "--param", "greeting=hi")
	require.Len(t, prs, 1)

	params := map[string]string{}
	for _, p := range prs[0].Spec.Params {
		params[p.Name] = p.Value.StringVal
	}
	assert.Equal(t, "prod", params["environment"], "{{ environment }} must be substituted")
	assert.Equal(t, "hi", params["greeting"], "a declared param with no spec.params entry must be appended")
}

func TestResolveCustomParamsFromFileWithFlagOverride(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("params.yaml", manifest(t, "custom-params.yaml"))
	file := writeParamsFile(t, "# deployment settings\nenvironment=staging\ngreeting=hello\n")

	prs := resolveRuns(t, r, "--param-file", file, "--param", "environment=prod")
	require.Len(t, prs, 1)

	params := map[string]string{}
	for _, p := range prs[0].Spec.Params {
		params[p.Name] = p.Value.StringVal
	}
	assert.Equal(t, "prod", params["environment"], "a --param flag overrides the file value")
	assert.Equal(t, "hello", params["greeting"], "unrelated file values survive")
}

func TestResolveUnknownCustomParamFails(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("params.yaml", manifest(t, "custom-params.yaml"))

	res := pactcli.Run(t, "-C", r.Dir, "resolve", "--param", "environment=prod", "--param", "greetin=x")
	assert.Equal(t, 1, res.ExitCode)
	assert.Contains(t, res.Stderr, "unknown params")
	assert.Contains(t, res.Stderr, `"greetin"`)
	assert.NotContains(t, res.Stderr, `"greeting"`)
	assert.Empty(t, res.Stdout)
}

func TestResolveParamShadowingBuiltinFails(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("params.yaml", manifest(t, "custom-params.yaml"))

	res := pactcli.Run(t, "-C", r.Dir, "resolve", "--param", "revision=x")
	assert.Equal(t, 1, res.ExitCode)
	assert.Contains(t, res.Stderr, "shadows a built-in")
}

func TestResolveMissingParamFileFails(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("params.yaml", manifest(t, "custom-params.yaml"))

	res := pactcli.Run(t, "-C", r.Dir, "resolve", "--param-file", filepath.Join(t.TempDir(), "nope.env"))
	assert.Equal(t, 1, res.ExitCode)
	assert.Contains(t, res.Stderr, "reading --param-file")
}

func TestResolveWithoutParamsLeavesPlaceholders(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("params.yaml", manifest(t, "custom-params.yaml"))

	// An unsupplied custom param stays a literal {{ }} placeholder, the
	// same way PAC leaves unknown keys; the run still resolves.
	res := mustRun(t, r, "resolve")
	prs := tekton.DecodePipelineRuns(t, res.Stdout)
	require.Len(t, prs, 1)
	require.Len(t, prs[0].Spec.Params, 1)
	assert.Equal(t, "environment", prs[0].Spec.Params[0].Name)
	assert.Equal(t, "{{ environment }}", prs[0].Spec.Params[0].Value.StringVal)
}
