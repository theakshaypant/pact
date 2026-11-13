package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// wsPipeline builds a PipelineRun whose pipelineSpec declares the given
// workspace names and a single task.
func wsPipeline(name string, workspaces ...string) *tektonv1.PipelineRun {
	pr := &tektonv1.PipelineRun{
		ObjectMeta: testMeta(name),
		Spec: tektonv1.PipelineRunSpec{
			PipelineSpec: &tektonv1.PipelineSpec{
				Tasks: []tektonv1.PipelineTask{{Name: "build", TaskSpec: &tektonv1.EmbeddedTask{}}},
			},
		},
	}
	for _, ws := range workspaces {
		pr.Spec.PipelineSpec.Workspaces = append(pr.Spec.PipelineSpec.Workspaces,
			tektonv1.PipelineWorkspaceDeclaration{Name: ws})
	}
	return pr
}

func testMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name}
}

func TestSelectWorkspace(t *testing.T) {
	tests := []struct {
		name       string
		workspaces []string
		override   string
		want       string
		wantErr    string
	}{
		{name: "source convention wins", workspaces: []string{"source", "cache"}, want: "source"},
		{name: "single workspace", workspaces: []string{"code"}, want: "code"},
		{name: "override picks explicitly", workspaces: []string{"cache", "source"}, override: "cache", want: "cache"},
		{name: "override must be declared", workspaces: []string{"source"}, override: "nope", wantErr: `does not declare workspace "nope"`},
		{name: "ambiguous without convention", workspaces: []string{"cache", "artifacts"}, wantErr: "several workspaces"},
		{name: "no workspaces cannot receive source", workspaces: nil, wantErr: "declares no workspaces"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := wsPipeline("pr", tt.workspaces...)
			got, err := selectWorkspace(pr, tt.override)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMutateRejectsPipelineRef(t *testing.T) {
	pr := &tektonv1.PipelineRun{ObjectMeta: testMeta("ref-only")}
	_, err := mutate(pr, "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pipelineSpec")
}

// clonePipeline builds a pipeline with clone and build tasks wired via
// runAfter.
func clonePipeline() *tektonv1.PipelineRun {
	pr := wsPipeline("clone-pr", "source")
	pr.Spec.PipelineSpec.Tasks = []tektonv1.PipelineTask{
		{Name: "git-clone", TaskRef: &tektonv1.TaskRef{Name: "git-clone"}},
		{Name: "Fetch_Repository", TaskSpec: &tektonv1.EmbeddedTask{}},
		{
			Name:     "build",
			TaskSpec: &tektonv1.EmbeddedTask{},
			RunAfter: []string{"git-clone", "setup"},
		},
		{Name: "setup", TaskSpec: &tektonv1.EmbeddedTask{}},
		{Name: "test", TaskSpec: &tektonv1.EmbeddedTask{}, RunAfter: []string{"Fetch_Repository"}},
	}
	return pr
}

func TestMutateDropsCloneTasksAndPrunesRunAfter(t *testing.T) {
	pr := clonePipeline()

	m, err := mutate(pr, "", false)
	require.NoError(t, err)
	assert.Equal(t, "source", m.Workspace)
	assert.ElementsMatch(t, []string{"git-clone", "Fetch_Repository"}, m.DroppedTasks)

	var names []string
	for _, task := range pr.Spec.PipelineSpec.Tasks {
		names = append(names, task.Name)
	}
	assert.Equal(t, []string{"build", "setup", "test"}, names, "clone tasks must be gone")

	byName := map[string]tektonv1.PipelineTask{}
	for _, task := range pr.Spec.PipelineSpec.Tasks {
		byName[task.Name] = task
	}
	assert.Equal(t, []string{"setup"}, byName["build"].RunAfter, "runAfter must prune dropped tasks")
	assert.Empty(t, byName["test"].RunAfter)
}

func TestMutateKeepsCloneTasksWhenAsked(t *testing.T) {
	pr := clonePipeline()

	m, err := mutate(pr, "", true)
	require.NoError(t, err)
	assert.Empty(t, m.DroppedTasks)
	assert.Len(t, pr.Spec.PipelineSpec.Tasks, 5)
}

func TestIsCloneTask(t *testing.T) {
	tests := []struct {
		name    string
		taskRef *tektonv1.TaskRef
		want    bool
	}{
		{name: "git-clone", want: true},
		{name: "Fetch_Repository", want: true},
		{name: "fetch source", want: true},
		{name: "CHECKOUT", want: true},
		{name: "build", want: false},
		{name: "fetch-deps", want: false},
		{name: "cloneapp", want: false},
		{name: "deploy", taskRef: &tektonv1.TaskRef{Name: "git-clone"}, want: true}, // judged by the referenced task
		{name: "deploy", taskRef: &tektonv1.TaskRef{Name: "build-task"}, want: false},
		{
			// judged by the remote task a hub resolver fetches
			name: "get-source",
			taskRef: &tektonv1.TaskRef{
				ResolverRef: tektonv1.ResolverRef{
					Resolver: "hub",
					Params: tektonv1.Params{
						{Name: "name", Value: tektonv1.ParamValue{Type: tektonv1.ParamTypeString, StringVal: "git-clone"}},
					},
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := tektonv1.PipelineTask{Name: tt.name, TaskRef: tt.taskRef}
			assert.Equal(t, tt.want, isCloneTask(task))
		})
	}
}

func TestIsCloneStep(t *testing.T) {
	tests := []struct {
		name        string
		step        tektonv1.Step
		want        bool
		description string
	}{
		{
			name:        "step name alone does not match",
			step:        tektonv1.Step{Name: "fetch-repo", Script: "go mod download"},
			want:        false,
			description: "an inline step with a clone-ish name is kept: the name alone is not evidence",
		},
		{
			name: "step name matches with a ref",
			step: tektonv1.Step{
				Name: "fetch-repo",
				Ref:  &tektonv1.Ref{ResolverRef: tektonv1.ResolverRef{Resolver: "hub"}},
			},
			want:        true,
			description: "a StepAction-referencing step named like a clone is dropped",
		},
		{
			name: "resolver param names git-clone",
			step: tektonv1.Step{
				Name: "get-code",
				Ref: &tektonv1.Ref{
					ResolverRef: tektonv1.ResolverRef{
						Resolver: "hub",
						Params: tektonv1.Params{
							{Name: "name", Value: tektonv1.ParamValue{Type: tektonv1.ParamTypeString, StringVal: "git-clone"}},
							{Name: "version", Value: tektonv1.ParamValue{Type: tektonv1.ParamTypeString, StringVal: "1.7.0"}},
						},
					},
				},
			},
			want:        true,
			description: "a hub-resolved git-clone stepaction is dropped by its remote name",
		},
		{
			name: "local stepaction ref matches",
			step: tektonv1.Step{
				Name: "get-code",
				Ref:  &tektonv1.Ref{Name: "git-clone"},
			},
			want:        true,
			description: "a cluster stepaction named git-clone is dropped",
		},
		{
			name: "resolver param names something else",
			step: tektonv1.Step{
				Name: "cache-fetch",
				Ref: &tektonv1.Ref{
					ResolverRef: tektonv1.ResolverRef{
						Resolver: "http",
						Params: tektonv1.Params{
							{Name: "name", Value: tektonv1.ParamValue{Type: tektonv1.ParamTypeString, StringVal: "cache-fetch"}},
						},
					},
				},
			},
			want:        false,
			description: "non-clone resolver refs are kept",
		},
		{
			name:        "plain build step",
			step:        tektonv1.Step{Name: "unittest", Image: "golang:1.27"},
			want:        false,
			description: "ordinary steps are kept",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isCloneStep(tt.step), tt.description)
		})
	}
}

// stepClonePipeline mirrors .tekton/go.yaml: one task whose inline
// taskSpec fetches the repo as a step, plus build steps.
func stepClonePipeline() *tektonv1.PipelineRun {
	pr := wsPipeline("step-clone-pr", "source")
	pr.Spec.PipelineSpec.Tasks = []tektonv1.PipelineTask{{
		Name: "go",
		TaskSpec: &tektonv1.EmbeddedTask{
			TaskSpec: tektonv1.TaskSpec{
				Steps: []tektonv1.Step{
					{Name: "fetch-repo", Ref: &tektonv1.Ref{
						ResolverRef: tektonv1.ResolverRef{
							Resolver: "hub",
							Params: tektonv1.Params{
								{Name: "name", Value: tektonv1.ParamValue{Type: tektonv1.ParamTypeString, StringVal: "git-clone"}},
							},
						},
					}},
					{Name: "cache-fetch", Ref: &tektonv1.Ref{
						ResolverRef: tektonv1.ResolverRef{
							Resolver: "http",
							Params: tektonv1.Params{
								{Name: "name", Value: tektonv1.ParamValue{Type: tektonv1.ParamTypeString, StringVal: "cache-fetch"}},
							},
						},
					}},
					{Name: "unittest", Image: "golang:1.27"},
				},
			},
		},
	}}
	return pr
}

func TestMutateDropsCloneStepsInTaskSpec(t *testing.T) {
	pr := stepClonePipeline()

	m, err := mutate(pr, "", false)
	require.NoError(t, err)
	assert.Empty(t, m.DroppedTasks, "no whole task is dropped")
	assert.Equal(t, []string{"go/fetch-repo"}, m.DroppedSteps)

	steps := pr.Spec.PipelineSpec.Tasks[0].TaskSpec.Steps
	var names []string
	for _, s := range steps {
		names = append(names, s.Name)
	}
	assert.Equal(t, []string{"cache-fetch", "unittest"}, names, "only the fetch step is dropped")
}

func TestMutateKeepsCloneStepsWhenAsked(t *testing.T) {
	pr := stepClonePipeline()

	m, err := mutate(pr, "", true)
	require.NoError(t, err)
	assert.Empty(t, m.DroppedSteps)
	assert.Len(t, pr.Spec.PipelineSpec.Tasks[0].TaskSpec.Steps, 3)
}

// wholeClonePipeline: a task whose only step fetches the repo, plus a
// dependent build task.
func wholeClonePipeline() *tektonv1.PipelineRun {
	pr := wsPipeline("whole-clone-pr", "source")
	pr.Spec.PipelineSpec.Tasks = []tektonv1.PipelineTask{
		{
			Name: "fetcher",
			TaskSpec: &tektonv1.EmbeddedTask{
				TaskSpec: tektonv1.TaskSpec{
					Steps: []tektonv1.Step{{
						Name: "fetch-repo",
						Ref: &tektonv1.Ref{
							ResolverRef: tektonv1.ResolverRef{
								Resolver: "hub",
								Params: tektonv1.Params{
									{Name: "name", Value: tektonv1.ParamValue{Type: tektonv1.ParamTypeString, StringVal: "git-clone"}},
								},
							},
						},
					}},
				},
			},
		},
		{Name: "build", TaskSpec: &tektonv1.EmbeddedTask{}, RunAfter: []string{"fetcher"}},
	}
	return pr
}

// A task left with zero steps after the clone-step drop would be
// rejected by Tekton; the whole task must go instead, runAfter
// references and all.
func TestMutateDropsWholeTaskWhenOnlyCloneSteps(t *testing.T) {
	pr := wholeClonePipeline()

	m, err := mutate(pr, "", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"fetcher"}, m.DroppedTasks)
	assert.Equal(t, []string{"fetcher/fetch-repo"}, m.DroppedSteps)

	var names []string
	for _, task := range pr.Spec.PipelineSpec.Tasks {
		names = append(names, task.Name)
	}
	assert.Equal(t, []string{"build"}, names, "the fetch-only task must be dropped whole")
	assert.Empty(t, pr.Spec.PipelineSpec.Tasks[0].RunAfter, "runAfter must prune the dropped task")
}

// danglingRefPipeline: a clone task whose result is consumed by another
// task's parameter.
func danglingRefPipeline() *tektonv1.PipelineRun {
	pr := wsPipeline("dangling-pr", "source")
	pr.Spec.PipelineSpec.Tasks = []tektonv1.PipelineTask{
		{Name: "git-clone", TaskRef: &tektonv1.TaskRef{Name: "git-clone"}},
		{
			Name:     "build",
			TaskSpec: &tektonv1.EmbeddedTask{},
			Params: tektonv1.Params{{
				Name:  "commit",
				Value: tektonv1.ParamValue{Type: tektonv1.ParamTypeString, StringVal: "$(tasks.git-clone.results.commit)"},
			}},
		},
	}
	return pr
}

func TestMutateRejectsDanglingReferences(t *testing.T) {
	pr := danglingRefPipeline()

	_, err := mutate(pr, "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dangling reference")
	assert.Contains(t, err.Error(), "--keep-clone-tasks")
}

func TestMutateKeepsCloneTasksAvoidsDanglingReferenceError(t *testing.T) {
	pr := danglingRefPipeline()

	_, err := mutate(pr, "", true)
	require.NoError(t, err, "keeping clone tasks must not trip the dangling-reference check")
}

func TestBindWorkspace(t *testing.T) {
	pr := wsPipeline("bind", "source")
	bindWorkspace(pr, "source", "pact-source")

	require.Len(t, pr.Spec.Workspaces, 1)
	binding := pr.Spec.Workspaces[0]
	assert.Equal(t, "source", binding.Name)
	require.NotNil(t, binding.PersistentVolumeClaim)
	assert.Equal(t, "pact-source", binding.PersistentVolumeClaim.ClaimName)
	assert.Equal(t, corev1.PersistentVolumeClaimVolumeSource{ClaimName: "pact-source"}, *binding.PersistentVolumeClaim)
}

// Pipelines-as-Code manifests often bind the source workspace to an
// emptyDir; pact must replace that binding, not append a second one.
func TestBindWorkspaceReplacesExistingBinding(t *testing.T) {
	pr := wsPipeline("rebind", "source", "cache")
	pr.Spec.Workspaces = []tektonv1.WorkspaceBinding{
		{Name: "source", EmptyDir: &corev1.EmptyDirVolumeSource{}},
		{Name: "cache", EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}

	bindWorkspace(pr, "source", "pact-source")

	require.Len(t, pr.Spec.Workspaces, 2, "the source binding must be replaced, not duplicated")
	assert.Equal(t, "pact-source", pr.Spec.Workspaces[0].PersistentVolumeClaim.ClaimName)
	assert.Nil(t, pr.Spec.Workspaces[0].EmptyDir, "the emptyDir binding must be gone")
	assert.NotNil(t, pr.Spec.Workspaces[1].EmptyDir, "other workspace bindings are untouched")
}
