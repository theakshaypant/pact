package pacdef

import (
	packeys "github.com/openshift-pipelines/pipelines-as-code/pkg/apis/pipelinesascode/keys"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// defaultImage is the container image of every built step. No test
// asserts on it.
const defaultImage = "alpine:3.20"

// defaultScript is the script of a step no Script call overrides. No
// test asserts on it beyond resolving to something visible.
const defaultScript = "echo hello"

// PipelineRunDoc builds a PipelineRun manifest: one inline task with
// one step, matched by PAC annotations. Every method returns the doc,
// so calls chain.
type PipelineRunDoc struct {
	pr tektonv1.PipelineRun
}

// PipelineRun starts a PipelineRun named name with one inline task and
// one step.
func PipelineRun(name string) *PipelineRunDoc {
	d := &PipelineRunDoc{pr: tektonv1.PipelineRun{
		TypeMeta:   metav1.TypeMeta{APIVersion: tektonv1.SchemeGroupVersion.String(), Kind: "PipelineRun"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}}
	d.pr.Spec.PipelineSpec = &tektonv1.PipelineSpec{
		Tasks: []tektonv1.PipelineTask{{Name: "run", TaskSpec: &tektonv1.EmbeddedTask{}}},
	}
	d.pr.Spec.PipelineSpec.Tasks[0].TaskSpec.Steps = []tektonv1.Step{{
		Name: "run", Image: defaultImage, Script: defaultScript,
	}}
	return d
}

// onEvent sets the on-event annotation, a bracketed list.
func (d *PipelineRunDoc) onEvent(events ...string) *PipelineRunDoc {
	return d.annotate(packeys.OnEvent, bracket(events))
}

// onTargetBranch sets the on-target-branch annotation, a bracketed
// list.
func (d *PipelineRunDoc) onTargetBranch(branches ...string) *PipelineRunDoc {
	return d.annotate(packeys.OnTargetBranch, bracket(branches))
}

// OnPullRequest matches pull_request events targeting branches (main
// when none is given).
func (d *PipelineRunDoc) OnPullRequest(branches ...string) *PipelineRunDoc {
	if len(branches) == 0 {
		branches = []string{"main"}
	}
	return d.onEvent("pull_request").onTargetBranch(branches...)
}

// OnPush matches push events targeting branches (main when none is
// given).
func (d *PipelineRunDoc) OnPush(branches ...string) *PipelineRunDoc {
	if len(branches) == 0 {
		branches = []string{"main"}
	}
	return d.onEvent("push").onTargetBranch(branches...)
}

// OnLabel sets the on-label annotation, a bracketed list.
func (d *PipelineRunDoc) OnLabel(labels ...string) *PipelineRunDoc {
	return d.annotate(packeys.OnLabel, bracket(labels))
}

// OnComment sets the on-comment regex annotation.
func (d *PipelineRunDoc) OnComment(regex string) *PipelineRunDoc {
	return d.annotate(packeys.OnComment, regex)
}

// OnPathChange sets the on-path-change annotation, a bracketed list of
// globs.
func (d *PipelineRunDoc) OnPathChange(globs ...string) *PipelineRunDoc {
	return d.annotate(packeys.OnPathChange, bracket(globs))
}

// OnCEL sets the on-cel-expression annotation. Unlike the list
// annotations it is a bare expression with no event or branch guard, so
// the expression itself must scope the event where that matters.
func (d *PipelineRunDoc) OnCEL(expression string) *PipelineRunDoc {
	return d.annotate(packeys.OnCelExpression, expression)
}

func (d *PipelineRunDoc) annotate(key, value string) *PipelineRunDoc {
	if d.pr.Annotations == nil {
		d.pr.Annotations = map[string]string{}
	}
	d.pr.Annotations[key] = value
	return d
}

// Param adds a spec.params entry and declares the param on the pipeline
// and the inline task, wiring $(params.name) into the task.
func (d *PipelineRunDoc) Param(name, value string) *PipelineRunDoc {
	d.pr.Spec.Params = append(d.pr.Spec.Params, param(name, value))
	d.declareParam(name)
	return d
}

// DeclareParam declares a param the event fills in — a {{ placeholder }}
// value, a trigger comment, ... — without adding a spec.params entry.
func (d *PipelineRunDoc) DeclareParam(name string) *PipelineRunDoc {
	d.declareParam(name)
	return d
}

// declareParam declares name on the pipeline and the inline task. A
// taskRef task is left alone: the referenced Task declares its own
// params.
func (d *PipelineRunDoc) declareParam(name string) {
	spec := d.pr.Spec.PipelineSpec
	spec.Params = appendDeclared(spec.Params, name)
	task := &spec.Tasks[0]
	if task.TaskRef != nil {
		return
	}
	task.Params = append(task.Params, param(name, "$(params."+name+")"))
	taskSpec := &task.TaskSpec.TaskSpec
	taskSpec.Params = appendDeclared(taskSpec.Params, name)
}

// Script sets the inline step's script. It requires the inline task:
// after UsesTask there is no step to script.
func (d *PipelineRunDoc) Script(script string) *PipelineRunDoc {
	d.pr.Spec.PipelineSpec.Tasks[0].TaskSpec.Steps[0].Script = script
	return d
}

// UsesTask replaces the inline task with a taskRef to a Task manifest
// expected as another file in the same directory. The referenced Task
// declares its own params, so any task-level wiring added by Param is
// dropped; the pipeline-level declarations stay.
func (d *PipelineRunDoc) UsesTask(name string) *PipelineRunDoc {
	task := &d.pr.Spec.PipelineSpec.Tasks[0]
	task.TaskSpec = nil
	task.Params = nil
	task.TaskRef = &tektonv1.TaskRef{Name: name}
	return d
}

// YAML renders the manifest, pruned of typed-struct marshal noise.
func (d *PipelineRunDoc) YAML() string {
	return render(&d.pr)
}

// param builds a string param. ParamValue must carry an explicit type:
// its custom marshaller errors on an empty one.
func param(name, value string) tektonv1.Param {
	return tektonv1.Param{Name: name, Value: tektonv1.ParamValue{
		Type: tektonv1.ParamTypeString, StringVal: value,
	}}
}

// appendDeclared appends a name-only ParamSpec unless name is already
// declared.
func appendDeclared(decls tektonv1.ParamSpecs, name string) tektonv1.ParamSpecs {
	for _, d := range decls {
		if d.Name == name {
			return decls
		}
	}
	return append(decls, tektonv1.ParamSpec{Name: name})
}
