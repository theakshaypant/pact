package runner

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
)

// defaultWorkspaceName is the workspace name pact binds the source PVC
// to when the pipeline declares it.
const defaultWorkspaceName = "source"

// cloneTaskNames are task names (after normalization) that fetch the
// repository from a remote. pact drops them because the source is
// pre-populated from the local directory.
var cloneTaskNames = map[string]bool{
	"git-clone":           true,
	"clone":               true,
	"clone-repo":          true,
	"clone-repository":    true,
	"fetch":               true,
	"fetch-repo":          true,
	"fetch-repository":    true,
	"fetch-source":        true,
	"fetch-git":           true,
	"checkout":            true,
	"checkout-repo":       true,
	"checkout-repository": true,
	"checkout-source":     true,
}

// nameSeparators folds underscores and whitespace in task names.
var nameSeparators = regexp.MustCompile(`[_\s]+`)

// normalizeTaskName lowercases a task name and folds separators so
// "Fetch_Repository" and "fetch-repository" match alike.
func normalizeTaskName(name string) string {
	return nameSeparators.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
}

// isCloneName reports whether a task, step or action name is one pact
// recognizes as repository-fetching.
func isCloneName(name string) bool {
	return cloneTaskNames[normalizeTaskName(name)]
}

// resolverNamesClone reports whether resolver params identify a remote
// action that pact recognizes as repository-fetching. Resolvers like
// hub carry the remote action's name in a "name" param.
func resolverNamesClone(params v1.Params) bool {
	for _, p := range params {
		if p.Name == "name" && p.Value.Type == v1.ParamTypeString && isCloneName(p.Value.StringVal) {
			return true
		}
	}
	return false
}

// isCloneTask reports whether a pipeline task fetches the repository,
// judged by its own name, the task it references, or the remote task a
// resolver ref fetches.
func isCloneTask(task v1.PipelineTask) bool {
	if isCloneName(task.Name) {
		return true
	}
	if task.TaskRef == nil {
		return false
	}
	return isCloneName(task.TaskRef.Name) || resolverNamesClone(task.TaskRef.Params)
}

// isCloneStep reports whether a step inside an inline taskSpec fetches
// the repository, judged by its own name, the StepAction it references,
// or the remote StepAction a resolver ref fetches. A step without a Ref
// is inline (script or command); a clone-ish name alone is not evidence
// it fetches the repository, and dropping a working step would break
// the run, so such steps are kept.
func isCloneStep(step v1.Step) bool {
	if step.Ref == nil {
		return false
	}
	return isCloneName(step.Name) || isCloneName(step.Ref.Name) || resolverNamesClone(step.Ref.Params)
}

// mutation is the outcome of preparing a resolved PipelineRun for local
// execution.
type mutation struct {
	// Workspace is the pipeline workspace the source PVC is bound to.
	Workspace string
	// DroppedTasks are the clone tasks removed from the pipeline.
	DroppedTasks []string
	// DroppedSteps are the clone steps removed from inline taskSpecs,
	// qualified as "task/step".
	DroppedSteps []string
}

// mutate prepares a resolved PipelineRun for local execution: it drops
// clone tasks (pruning runAfter references to them) and records which
// workspace the source PVC must be bound to. The workspace binding
// itself is added by bindWorkspace once the PVC name is known.
func mutate(pr *v1.PipelineRun, workspaceOverride string, keepCloneTasks bool) (mutation, error) {
	m := mutation{}

	if pr.Spec.PipelineSpec == nil {
		return m, fmt.Errorf("pipelinerun %q has no pipelineSpec (pipelineRef cannot be resolved offline)", pr.Name)
	}

	ws, err := selectWorkspace(pr, workspaceOverride)
	if err != nil {
		return m, err
	}
	m.Workspace = ws

	if !keepCloneTasks {
		var err error
		m.DroppedTasks, m.DroppedSteps, err = dropCloneTasks(pr)
		if err != nil {
			return m, err
		}
	}
	return m, nil
}

// selectWorkspace picks the pipeline workspace the source PVC binds to:
// an explicit override, else a workspace named "source", else the single
// declared workspace.
func selectWorkspace(pr *v1.PipelineRun, override string) (string, error) {
	var declared []string
	for _, w := range pr.Spec.PipelineSpec.Workspaces {
		declared = append(declared, w.Name)
	}
	if len(declared) == 0 {
		return "", fmt.Errorf("pipelinerun %q declares no workspaces; there is nowhere to mount the local source", pr.Name)
	}
	if override != "" {
		for _, n := range declared {
			if n == override {
				return n, nil
			}
		}
		return "", fmt.Errorf("pipelinerun %q does not declare workspace %q (declares: %s)",
			pr.Name, override, strings.Join(declared, ", "))
	}
	for _, n := range declared {
		if n == defaultWorkspaceName {
			return n, nil
		}
	}
	if len(declared) == 1 {
		return declared[0], nil
	}
	return "", fmt.Errorf("pipelinerun %q declares several workspaces (%s); pick one with --workspace",
		pr.Name, strings.Join(declared, ", "))
}

// dropCloneTasks removes repository-fetching tasks and steps from the
// pipeline, prunes runAfter references to dropped tasks, and rejects
// pipelines whose remaining tasks still reference a dropped task (e.g.
// via $(tasks.git-clone.results.commit)), which Tekton would refuse at
// admission. It returns the dropped task names and the dropped steps,
// qualified as "task/step".
func dropCloneTasks(pr *v1.PipelineRun) (droppedTasks, droppedSteps []string, err error) {
	spec := pr.Spec.PipelineSpec
	droppedSet := map[string]bool{}
	tasks := make([]v1.PipelineTask, 0, len(spec.Tasks))
	for _, task := range spec.Tasks {
		if isCloneTask(task) {
			droppedTasks = append(droppedTasks, task.Name)
			droppedSet[task.Name] = true
			continue
		}
		if task.TaskSpec != nil {
			// Repository fetching also hides as steps inside inline
			// taskSpecs (e.g. a git-clone StepAction); the source is
			// pre-populated, so they are dropped too.
			steps, dropped := dropCloneSteps(task.Name, task.TaskSpec.Steps)
			droppedSteps = append(droppedSteps, dropped...)
			if len(dropped) > 0 && len(steps) == 0 {
				// The task did nothing but fetch the repository.
				// Dropping it whole keeps the pipeline valid — a
				// taskSpec with no steps is rejected by Tekton — and
				// feeds the same runAfter pruning.
				droppedTasks = append(droppedTasks, task.Name)
				droppedSet[task.Name] = true
				continue
			}
			task.TaskSpec.Steps = steps
		}
		tasks = append(tasks, task)
	}
	spec.Tasks = tasks
	pruneRunAfter(spec, droppedSet)

	if refs := danglingReferences(spec, droppedSet); len(refs) > 0 {
		return nil, nil, fmt.Errorf(
			"dropping clone task(s) %s would leave dangling reference(s) %s in the remaining pipeline; "+
				"restructure the pipeline or run with --keep-clone-tasks",
			strings.Join(droppedTasks, ", "), strings.Join(refs, ", "))
	}
	return droppedTasks, droppedSteps, nil
}

// dropCloneSteps filters repository-fetching steps out of an inline
// taskSpec, returning the kept steps and the dropped step names
// (qualified as "task/step").
func dropCloneSteps(taskName string, steps []v1.Step) (kept []v1.Step, dropped []string) {
	kept = make([]v1.Step, 0, len(steps))
	for _, step := range steps {
		if isCloneStep(step) {
			dropped = append(dropped, taskName+"/"+step.Name)
			continue
		}
		kept = append(kept, step)
	}
	return kept, dropped
}

// pruneRunAfter removes references to dropped tasks from the remaining
// tasks' runAfter lists.
func pruneRunAfter(spec *v1.PipelineSpec, dropped map[string]bool) {
	for i := range spec.Tasks {
		pruned := make([]string, 0, len(spec.Tasks[i].RunAfter))
		for _, dep := range spec.Tasks[i].RunAfter {
			if !dropped[dep] {
				pruned = append(pruned, dep)
			}
		}
		spec.Tasks[i].RunAfter = pruned
	}
}

// danglingReferences finds references to dropped tasks that survive the
// drop, such as $(tasks.git-clone.results.commit) in a parameter, a
// when expression or a pipeline result.
func danglingReferences(spec *v1.PipelineSpec, dropped map[string]bool) []string {
	var refs []string
	add := func(values ...string) {
		for _, v := range values {
			for name := range dropped {
				if strings.Contains(v, "$(tasks."+name+".") {
					refs = append(refs, "$(tasks."+name+"....)")
				}
			}
		}
	}
	for _, task := range spec.Tasks {
		for _, p := range task.Params {
			add(valueStrings(p.Value)...)
		}
		for _, w := range task.When {
			add(w.Input, w.CEL)
			add(w.Values...)
		}
	}
	for _, r := range spec.Results {
		add(valueStrings(r.Value)...)
	}
	return refs
}

// valueStrings returns every string inside a param value, whatever its
// type.
func valueStrings(value v1.ParamValue) []string {
	out := []string{value.StringVal}
	out = append(out, value.ArrayVal...)
	for _, v := range value.ObjectVal {
		out = append(out, v)
	}
	return out
}

// bindWorkspace binds the given pipeline workspace to the source PVC on
// the PipelineRun, replacing any existing binding for that workspace
// (e.g. an emptyDir the manifest shipped with).
func bindWorkspace(pr *v1.PipelineRun, workspace, pvcName string) {
	binding := v1.WorkspaceBinding{
		Name: workspace,
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: pvcName,
		},
	}
	for i, existing := range pr.Spec.Workspaces {
		if existing.Name == workspace {
			pr.Spec.Workspaces[i] = binding
			return
		}
	}
	pr.Spec.Workspaces = append(pr.Spec.Workspaces, binding)
}
