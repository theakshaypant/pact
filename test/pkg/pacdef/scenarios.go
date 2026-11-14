package pacdef

// CustomParams builds the custom-params scenario: an environment param
// substituted from the event plus a greeting the caller supplies with
// --param. Shared by the e2e and unit suites.
func CustomParams() *PipelineRunDoc {
	return PipelineRun("custom-params").
		OnPullRequest("main").
		Param("environment", "{{ environment }}").
		DeclareParam("greeting").
		Script(`echo "$(params.environment) $(params.greeting)"`)
}

// Templated builds the templated-params scenario: the standard event
// placeholders revision, repo_url and sender. Both the repo and
// repo-url spellings are declared because the unit and e2e suites each
// read one.
func Templated() *PipelineRunDoc {
	return PipelineRun("templated").
		OnPullRequest("main").
		Param("revision", "{{ revision }}").
		Param("repo", "{{ repo_url }}").
		Param("repo-url", "{{ repo_url }}").
		Param("sender", "{{ sender }}").
		Script("echo $(params.revision)")
}

// Labeled builds the on-label scenario: a pipeline matched by the
// kind/lgtm label on a main-targeting pull request. Shared by the e2e
// and unit suites.
func Labeled() *PipelineRunDoc {
	return PipelineRun("on-label").OnPullRequest("main").
		OnLabel("kind/lgtm").Script("echo label matched")
}

// Comment builds the on-comment scenario: a pipeline matched by a /test
// comment, receiving the comment body as its param. Shared by the e2e
// and unit suites.
func Comment() *PipelineRunDoc {
	return PipelineRun("on-comment").OnComment("^/test").
		Param("comment", "{{ trigger_comment }}").Script("echo $(params.comment)")
}
