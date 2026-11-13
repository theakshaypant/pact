package pac

import (
	"context"
	"fmt"
	"strings"

	"github.com/openshift-pipelines/pipelines-as-code/pkg/params"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/resolve"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"github.com/theakshaypant/pact/pkg/event"
)

// describeEvent renders the event for user-facing messages in pact's flag
// vocabulary, rather than PAC's internal kind names, including the labels
// or comment that refine the labeled and comment kinds.
func describeEvent(ev *event.Event) string {
	var what, detail string
	switch ev.Kind {
	case event.KindLabeled:
		what = "labeled pull_request"
		detail = fmt.Sprintf(" (labels: %s)", strings.Join(ev.Labels, ", "))
	case event.KindComment:
		what = "pull_request comment"
		detail = fmt.Sprintf(" (comment: %q)", ev.Comment)
	default:
		what = string(ev.Kind)
	}
	return what + " event" + detail
}

// Resolve hydrates the PipelineRuns matching the event into self-contained
// multi-document YAML. Only the matched runs are resolved, so the output is
// exactly what PAC would have run for this event. Referenced local tasks
// and pipelines are inlined; remote references are not fetched (offline
// mode) and surface PAC's own error.
func (e *engine) Resolve(ctx context.Context, tektonDir string, ev *event.Event) ([]byte, error) {
	res, err := e.evaluate(ctx, tektonDir, ev)
	if err != nil {
		return nil, fmt.Errorf("resolving pipelines: %w", err)
	}
	if len(res.matchedPRs) == 0 {
		return nil, fmt.Errorf("no pipelinerun in %s matches the simulated %s", tektonDir, describeEvent(ev))
	}

	// Merge every file's types so a task defined in one file can be
	// referenced from a pipeline in another, then keep only the matched
	// PipelineRuns.
	matched := map[*tektonv1.PipelineRun]bool{}
	for _, pr := range res.matchedPRs {
		matched[pr] = true
	}
	merged := resolve.NewTektonTypes()
	for _, name := range sortedFileNames(res.perFile) {
		types := res.perFile[name]
		merged.Pipelines = append(merged.Pipelines, types.Pipelines...)
		merged.Tasks = append(merged.Tasks, types.Tasks...)
		merged.TaskRuns = append(merged.TaskRuns, types.TaskRuns...)
		for _, pr := range types.PipelineRuns {
			if matched[pr] {
				merged.PipelineRuns = append(merged.PipelineRuns, pr)
			}
		}
	}

	resolved, err := resolve.Resolve(ctx, &params.Run{}, e.log, newLocalProvider(ev.ChangedFiles), merged, toInfoEvent(ev), &resolve.Opts{
		GenerateName: true,
		RemoteTasks:  false,
	})
	if err != nil {
		return nil, fmt.Errorf("resolving pipelineruns (offline mode: remote tasks are not fetched): %w", err)
	}

	// Custom params: fail on names nothing matched uses (a typo would
	// otherwise vanish silently), then merge the values into every
	// PipelineRun before rendering, so resolve previews and run submits
	// the same spec.params.
	if len(e.params) > 0 {
		if unknown := e.params.unknown(usedParamNames(resolved, res.matchedFileContent())); len(unknown) > 0 {
			quoted := make([]string, len(unknown))
			for i, name := range unknown {
				quoted[i] = fmt.Sprintf("%q", name)
			}
			return nil, fmt.Errorf("resolving pipelines: %w: %s (no matched pipeline declares or references them)",
				ErrUnknownParams, strings.Join(quoted, ", "))
		}
		applyParams(resolved, e.params)
	}

	out, err := renderPipelineRuns(resolved)
	if err != nil {
		return nil, fmt.Errorf("resolving pipelines: %w", err)
	}
	return out, nil
}
