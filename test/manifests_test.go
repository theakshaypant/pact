package test

import (
	"github.com/theakshaypant/pact/test/pkg/pacdef"
)

// Scenario builders for this suite's scratch repositories. Scenarios
// shared with the pkg/pac unit suite live in test/pkg/pacdef and are
// built identically on both sides.

// prBasic matches a pull request on main and cats the README.
func prBasic() *pacdef.PipelineRunDoc {
	return pacdef.PipelineRun("pr-basic").OnPullRequest("main").Script("cat README.md")
}

// prMulti packs two matching runs in one file.
func prMulti() pacdef.Manifest {
	return pacdef.Stream(
		pacdef.PipelineRun("pr-multi-first").OnPullRequest("main").Script("echo first"),
		pacdef.PipelineRun("pr-multi-second").OnPullRequest("main").Script("echo second"),
	)
}

// prOtherBranch targets a release branch only.
func prOtherBranch() *pacdef.PipelineRunDoc {
	return pacdef.PipelineRun("pr-other-branch").OnPullRequest("release-9")
}

// pushBasic matches a push to main.
func pushBasic() *pacdef.PipelineRunDoc {
	return pacdef.PipelineRun("push-basic").OnPush("main").Script("echo building")
}

// celTitle matches on the pull request title.
func celTitle() *pacdef.PipelineRunDoc {
	return pacdef.PipelineRun("cel-title").OnPullRequest("main").
		OnCEL(`body.pull_request.title == "feat: add local testing"`).Script("echo title matched")
}

// celGoFiles matches when any changed file is Go.
func celGoFiles() *pacdef.PipelineRunDoc {
	return pacdef.PipelineRun("cel-go-files").OnPullRequest("main").
		OnCEL(`files.all.exists(f, f.endsWith('.go'))`).Script("echo go files changed")
}

// badCEL carries a syntactically invalid CEL expression.
func badCEL() *pacdef.PipelineRunDoc {
	return pacdef.PipelineRun("bad-cel").OnPullRequest("main").OnCEL("event == ((((").Script("echo never")
}

// sharedTask is the Task definition referenced across files.
func sharedTask() *pacdef.TaskDoc {
	return pacdef.Task("shared-task").Script("echo from shared task")
}

// taskRefPipeline references sharedTask from another file.
func taskRefPipeline() *pacdef.PipelineRunDoc {
	return pacdef.PipelineRun("pr-with-taskref").OnPullRequest("main").UsesTask("shared-task")
}
