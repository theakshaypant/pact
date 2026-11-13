package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/test/pkg/pactcli"
	"github.com/theakshaypant/pact/test/pkg/tekton"
)

func TestResolveInlinesTaskAcrossFiles(t *testing.T) {
	// The Task lives in one .tekton file and is referenced by name from
	// the PipelineRun in another; resolve must inline it.
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("task.yaml", manifest(t, "task-definition.yaml"))
	r.WriteTekton("pipeline.yaml", manifest(t, "pipeline-referencing-task.yaml"))

	// Only the file containing a matching PipelineRun is listed.
	assert.Equal(t, "pipeline.yaml\n", mustMatch(t, r))

	prs := resolveRuns(t, r)
	require.Len(t, prs, 1)

	pr := tekton.ByGenerateName(t, prs, "pr-with-taskref-")
	require.Len(t, pr.Spec.PipelineSpec.Tasks, 1)
	task := pr.Spec.PipelineSpec.Tasks[0]
	assert.Nil(t, task.TaskRef, "taskRef must be replaced by an inline taskSpec")
	require.NotNil(t, task.TaskSpec, "the task from task.yaml must be inlined")
	assert.Equal(t, "echo from shared task", task.TaskSpec.Steps[0].Script)
}

func TestResolveSubstitutesTemplateParams(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("templated.yaml", manifest(t, "templated-params.yaml"))

	// Raw stdout is needed for the no-leftover-placeholders assertion, so
	// this scenario decodes and validates by hand instead of resolveRuns.
	res := mustRun(t, r, "resolve")
	prs := tekton.DecodePipelineRuns(t, res.Stdout)
	require.Len(t, prs, 1)
	tekton.Validate(t, prs)

	params := map[string]string{}
	for _, p := range prs[0].Spec.Params {
		params[p.Name] = p.Value.StringVal
	}
	assert.Equal(t, r.Head().String(), params["revision"], "{{ revision }} must become the HEAD SHA")
	assert.Equal(t, "git@github.com:example/repo.git", params["repo-url"], "{{ repo_url }} must become the origin URL")
	assert.Equal(t, "Test Author", params["sender"], "{{ sender }} must become the HEAD commit author")
	assert.NotContains(t, res.Stdout, "{{", "no unsubstituted placeholders may remain")
}

func TestResolveNoMatchFails(t *testing.T) {
	r := setupFeatureBranch(t, "feat: add local testing")
	r.WriteTekton("pr.yaml", manifest(t, "pr-other-branch.yaml"))

	res := pactcli.Run(t, "-C", r.Dir, "resolve")
	assert.Equal(t, 1, res.ExitCode)
	assert.Contains(t, res.Stderr, "no pipelinerun")
	assert.Empty(t, res.Stdout)
}
