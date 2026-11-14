package pacdef

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	packeys "github.com/openshift-pipelines/pipelines-as-code/pkg/apis/pipelinesascode/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	k8syaml "sigs.k8s.io/yaml"
)

// decode unmarshals YAML into a PipelineRun, proving the output is
// valid and round-trips through the typed API.
func decode(t *testing.T, y string) *tektonv1.PipelineRun {
	t.Helper()
	var pr tektonv1.PipelineRun
	require.NoError(t, k8syaml.Unmarshal([]byte(y), &pr))
	return &pr
}

func TestPipelineRunRendersHandWrittenShape(t *testing.T) {
	y := PipelineRun("pr-basic").OnPullRequest("main").Script("cat README.md").YAML()

	// No typed-struct marshal noise survives the prune pass.
	for _, noise := range []string{"status", "creationTimestamp", "taskRunTemplate", "computeResources"} {
		assert.NotContains(t, y, noise+":", "marshal noise %q must be pruned", noise)
	}
	assert.True(t, strings.HasSuffix(y, "\n"), "YAML must be newline terminated")

	pr := decode(t, y)
	assert.Equal(t, "tekton.dev/v1", pr.APIVersion)
	assert.Equal(t, "PipelineRun", pr.Kind)
	assert.Equal(t, "pr-basic", pr.Name)
	assert.Equal(t, "[pull_request]", pr.Annotations[packeys.OnEvent])
	assert.Equal(t, "[main]", pr.Annotations[packeys.OnTargetBranch])
	task := pr.Spec.PipelineSpec.Tasks[0]
	require.NotNil(t, task.TaskSpec)
	assert.Equal(t, "cat README.md", task.TaskSpec.Steps[0].Script)
}

func TestPipelineRunAnnotations(t *testing.T) {
	y := PipelineRun("p").
		OnPush("release-9").
		OnLabel("kind/lgtm", "docs-needed").
		OnComment("^/test").
		OnPathChange("docs/**").
		YAML()
	pr := decode(t, y)
	assert.Equal(t, "[push]", pr.Annotations[packeys.OnEvent])
	assert.Equal(t, "[release-9]", pr.Annotations[packeys.OnTargetBranch])
	assert.Equal(t, "[kind/lgtm, docs-needed]", pr.Annotations[packeys.OnLabel])
	assert.Equal(t, "^/test", pr.Annotations[packeys.OnComment])
	assert.Equal(t, "[docs/**]", pr.Annotations[packeys.OnPathChange])
}

func TestOnCELIsBare(t *testing.T) {
	// CEL is a raw expression, not a bracketed list.
	y := PipelineRun("p").OnCEL(`event == "pull_request"`).YAML()
	pr := decode(t, y)
	assert.Equal(t, `event == "pull_request"`, pr.Annotations[packeys.OnCelExpression])
}

func TestParamDeclaresAndWires(t *testing.T) {
	y := PipelineRun("p").
		Param("environment", "{{ environment }}").
		DeclareParam("greeting").
		YAML()
	pr := decode(t, y)

	// Only Param adds a spec.params entry; DeclareParam does not.
	require.Len(t, pr.Spec.Params, 1)
	assert.Equal(t, tektonv1.Param{Name: "environment", Value: tektonv1.ParamValue{
		Type: tektonv1.ParamTypeString, StringVal: "{{ environment }}",
	}}, pr.Spec.Params[0])

	// Both are declared on the pipeline and the task; the task is wired
	// to the pipeline values.
	spec := pr.Spec.PipelineSpec
	assert.Equal(t, []string{"environment", "greeting"}, paramSpecNames(spec.Params))
	task := spec.Tasks[0]
	assert.Equal(t, []string{"environment", "greeting"}, paramNames(task.Params))
	for _, p := range task.Params {
		assert.Equal(t, "$(params."+p.Name+")", p.Value.StringVal)
	}
	assert.Equal(t, []string{"environment", "greeting"}, paramSpecNames(task.TaskSpec.Params))
}

func TestUsesTaskSkipsTaskWiring(t *testing.T) {
	y := PipelineRun("p").OnPullRequest("main").Param("revision", "{{ revision }}").UsesTask("shared").YAML()
	pr := decode(t, y)
	task := pr.Spec.PipelineSpec.Tasks[0]
	assert.Nil(t, task.TaskSpec)
	assert.Equal(t, "shared", task.TaskRef.Name)
	assert.Empty(t, task.Params, "the referenced Task declares its own params")
	// The pipeline still declares what it passes.
	assert.Equal(t, []string{"revision"}, paramSpecNames(pr.Spec.PipelineSpec.Params))
}

func TestTaskDoc(t *testing.T) {
	y := Task("local-task").Script("echo local task").YAML()
	var task tektonv1.Task
	require.NoError(t, k8syaml.Unmarshal([]byte(y), &task))
	assert.Equal(t, "tekton.dev/v1", task.APIVersion)
	assert.Equal(t, "Task", task.Kind)
	assert.Equal(t, "local-task", task.Name)
	assert.Equal(t, "echo local task", task.Spec.Steps[0].Script)
	assert.NotContains(t, y, "status:", "marshal noise must be pruned")
}

func TestStreamJoinsDocuments(t *testing.T) {
	s := Stream(PipelineRun("two-runs-pr"), PipelineRun("two-runs-push")).YAML()
	docs := strings.Split(s, "---\n")
	require.Len(t, docs, 2, "exactly one separator between two docs")
	assert.Contains(t, docs[0], "name: two-runs-pr")
	assert.Contains(t, docs[1], "name: two-runs-push")
}

func TestDirWritesFiles(t *testing.T) {
	dir := Dir{
		"pr.yaml":  PipelineRun("pr-only"),
		"pair.yml": Stream(PipelineRun("two-runs-pr"), Task("shared")),
	}.Write(t)
	for _, name := range []string{"pr.yaml", "pair.yml"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, "Dir.Write must write %s", name)
		assert.NotEmpty(t, string(data))
	}
}

func TestScenarios(t *testing.T) {
	t.Run("custom params", func(t *testing.T) {
		pr := decode(t, CustomParams().YAML())
		require.Len(t, pr.Spec.Params, 1)
		assert.Equal(t, "environment", pr.Spec.Params[0].Name)
		assert.Equal(t, "{{ environment }}", pr.Spec.Params[0].Value.StringVal)
		task := pr.Spec.PipelineSpec.Tasks[0]
		assert.Equal(t, `echo "$(params.environment) $(params.greeting)"`,
			task.TaskSpec.Steps[0].Script)
	})
	t.Run("templated params carry both repo spellings", func(t *testing.T) {
		pr := decode(t, Templated().YAML())
		assert.Equal(t, []string{"revision", "repo", "repo-url", "sender"}, paramNames(pr.Spec.Params))
	})
}

// paramNames returns the names of task-level params.
func paramNames(params []tektonv1.Param) []string {
	var names []string
	for _, p := range params {
		names = append(names, p.Name)
	}
	return names
}

// paramSpecNames returns the names of param declarations.
func paramSpecNames(specs []tektonv1.ParamSpec) []string {
	var names []string
	for _, p := range specs {
		names = append(names, p.Name)
	}
	return names
}
