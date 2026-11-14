package pac

import (
	"testing"

	"github.com/theakshaypant/pact/test/pkg/pacdef"
)

// The Dir functions below define the tekton fixture directories the
// match and resolve tests run against, built with pacdef instead of
// hand-written YAML. The file names stay literal because match output
// and error messages quote them.

// basicDir is the plain event-matching set.
func basicDir(t *testing.T) string {
	t.Helper()
	return pacdef.Dir{
		"docs-only.yml": pacdef.PipelineRun("docs-only").
			OnPullRequest("main").OnPathChange("docs/**").Script("echo linting docs"),
		"local-task.yaml": pacdef.PipelineRun("local-task").OnPullRequest("main").UsesTask("local-task"),
		"pr-only.yaml":    pacdef.PipelineRun("pr-only").OnPullRequest("main"),
		"push-only.yaml":  pacdef.PipelineRun("push-only").OnPush("main"),
		"task-def.yaml":   pacdef.Task("local-task").Script("echo local task"),
		"two-runs.yaml": pacdef.Stream(
			pacdef.PipelineRun("two-runs-pr").OnPullRequest("main").Script("echo pr"),
			pacdef.PipelineRun("two-runs-push").OnPush("main").Script("echo push"),
		),
	}.Write(t)
}

// celDir holds CEL-only matching: bare expressions scoping the event
// themselves.
func celDir(t *testing.T) string {
	t.Helper()
	return pacdef.Dir{
		"cel-body.yaml": pacdef.PipelineRun("cel-body").OnCEL(
			`event == "pull_request" && body.pull_request.title == "feat: add local testing"`),
		"cel-files.yaml": pacdef.PipelineRun("cel-files").OnCEL(
			`event == "pull_request" && files.all.exists(f, f.endsWith('.go'))`),
	}.Write(t)
}

// badcelDir holds one PipelineRun whose CEL expression does not parse.
func badcelDir(t *testing.T) string {
	t.Helper()
	return pacdef.Dir{
		"bad-cel.yaml": pacdef.PipelineRun("bad-cel").OnCEL("event == (((("),
	}.Write(t)
}

// nomatchDir never matches the events the tests send.
func nomatchDir(t *testing.T) string {
	t.Helper()
	return pacdef.Dir{
		"no-match.yaml": pacdef.PipelineRun("no-match").OnPush("release-*"),
	}.Write(t)
}

// templatedDir holds the {{ placeholder }} substitution scenario.
func templatedDir(t *testing.T) string {
	t.Helper()
	return pacdef.Dir{"templated.yaml": pacdef.Templated()}.Write(t)
}

// remoteDir references a task no local file defines.
func remoteDir(t *testing.T) string {
	t.Helper()
	return pacdef.Dir{
		"remote-ref.yaml": pacdef.PipelineRun("remote-ref").OnPullRequest("main").UsesTask("not-present-anywhere"),
	}.Write(t)
}

// labelCommentDir covers on-label, label-aware CEL and on-comment
// matching.
func labelCommentDir(t *testing.T) string {
	t.Helper()
	return pacdef.Dir{
		"cel-label.yaml": pacdef.PipelineRun("cel-label").OnCEL(
			`body.pull_request.labels.exists(l, l.name == "kind/lgtm")`),
		"on-comment.yaml": pacdef.Comment(),
		"on-label-any.yaml": pacdef.PipelineRun("on-label-any").OnPullRequest("main").
			OnLabel("kind/lgtm", "docs-needed").Script("echo any label matched"),
		"on-label.yaml": pacdef.Labeled(),
	}.Write(t)
}

// paramsDir holds the custom-params scenario.
func paramsDir(t *testing.T) string {
	t.Helper()
	return pacdef.Dir{"params.yaml": pacdef.CustomParams()}.Write(t)
}
