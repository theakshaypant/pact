package pacdef

import (
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TaskDoc builds a Task manifest with one step, for PipelineRuns that
// reference it by name from the same directory.
type TaskDoc struct {
	task tektonv1.Task
}

// Task starts a Task named name with one step echoing the name unless
// Script overrides it.
func Task(name string) *TaskDoc {
	t := &TaskDoc{task: tektonv1.Task{
		TypeMeta:   metav1.TypeMeta{APIVersion: tektonv1.SchemeGroupVersion.String(), Kind: "Task"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}}
	t.task.Spec.Steps = []tektonv1.Step{{Name: "run", Image: defaultImage, Script: "echo " + name}}
	return t
}

// Script sets the step's script.
func (t *TaskDoc) Script(script string) *TaskDoc {
	t.task.Spec.Steps[0].Script = script
	return t
}

// YAML renders the manifest, pruned of typed-struct marshal noise.
func (t *TaskDoc) YAML() string {
	return render(&t.task)
}
