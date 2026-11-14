package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/theakshaypant/pact/test/pkg/pactcli"
)

func TestRemoteTaskRefFailsOffline(t *testing.T) {
	// The PipelineRun references shared-task but its Task definition is
	// nowhere in .tekton: matching succeeds (it only reads annotations)
	// but offline resolution must fail loudly.
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("pipeline.yaml", taskRefPipeline().YAML())

	assert.Equal(t, "pipeline.yaml\n", mustMatch(t, r))

	res := pactcli.Run(t, "-C", r.Dir, "resolve")
	assert.Equal(t, 1, res.ExitCode)
	assert.Contains(t, res.Stderr, "cannot find referenced task")
	assert.Contains(t, res.Stderr, "offline mode")
}
