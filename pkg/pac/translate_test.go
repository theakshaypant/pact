package pac

import (
	"net/http"
	"testing"

	"github.com/openshift-pipelines/pipelines-as-code/pkg/params/triggertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/pkg/event"
)

func testEvent(t *testing.T, typ event.Type) *event.Event {
	t.Helper()
	return matchEvent(t, typ, nil)
}

func TestToInfoEvent(t *testing.T) {
	ev := testEvent(t, event.PullRequest)
	ie := toInfoEvent(ev)

	assert.Equal(t, triggertype.PullRequest, ie.TriggerTarget)
	assert.Equal(t, "pull_request", ie.EventType)
	assert.Equal(t, "main", ie.BaseBranch)
	assert.Equal(t, "feature", ie.HeadBranch)
	assert.Equal(t, "https://github.com/example/repo", ie.HeadURL)
	assert.Equal(t, "abcdef", ie.SHA)
	assert.Equal(t, "Dev", ie.Sender)
	assert.Equal(t, 1, ie.PullRequestNumber)
	assert.Equal(t, "feat: add local testing", ie.PullRequestTitle)
	assert.Equal(t, "example", ie.Organization)
	assert.Equal(t, "repo", ie.Repository)
	assert.Equal(t, ev.Payload, ie.Event)
	require.NotNil(t, ie.Request)
	assert.Equal(t, http.Header{"X-GitHub-Event": []string{"pull_request"}}, ie.Request.Header)
}

func TestToInfoEventFreshPerCall(t *testing.T) {
	ev := testEvent(t, event.PullRequest)
	ie1 := toInfoEvent(ev)
	ie1.EventType = "mutated"
	ie2 := toInfoEvent(ev)
	assert.Equal(t, "pull_request", ie2.EventType)
}

func TestTemplateDict(t *testing.T) {
	ev := testEvent(t, event.PullRequest)
	dict := templateDict(ev, nil)

	assert.Equal(t, map[string]string{
		"revision":            "abcdef",
		"repo_url":            "git@github.com:example/repo.git",
		"repo_owner":          "example",
		"repo_name":           "repo",
		"target_branch":       "main",
		"source_branch":       "feature",
		"source_url":          "https://github.com/example/repo",
		"sender":              "Dev",
		"event_type":          "pull_request",
		"pull_request_number": "1",
		"trigger_comment":     "",
		"pull_request_labels": "",
	}, dict)
}

func TestTemplateDictLabeledAndComment(t *testing.T) {
	t.Run("labeled event carries its labels", func(t *testing.T) {
		ev := matchEventOpts(t, event.Options{Labels: []string{"kind/lgtm", "a/b"}}, nil)
		dict := templateDict(ev, nil)
		assert.Equal(t, "pull_request_labeled", dict["event_type"])
		assert.Equal(t, "kind/lgtm\\na/b", dict["pull_request_labels"])
	})

	t.Run("comment event renders the PAC-visible on-comment type and body", func(t *testing.T) {
		ev := matchEventOpts(t, event.Options{Comment: "/test\nplease"}, nil)
		dict := templateDict(ev, nil)
		assert.Equal(t, "on-comment", dict["event_type"])
		assert.Equal(t, "/test\\nplease", dict["trigger_comment"])
	})
}

func TestTemplateDictCustomParams(t *testing.T) {
	ev := testEvent(t, event.PullRequest)
	dict := templateDict(ev, Params{"environment": "prod", "greeting": "hi"})

	assert.Equal(t, "prod", dict["environment"], "custom params join the dictionary")
	assert.Equal(t, "hi", dict["greeting"])
	assert.Equal(t, "abcdef", dict["revision"], "built-in keys are untouched")
}

func TestToInfoEventLabeledAndComment(t *testing.T) {
	t.Run("labeled", func(t *testing.T) {
		ev := matchEventOpts(t, event.Options{Labels: []string{"kind/lgtm"}}, nil)
		ie := toInfoEvent(ev)
		assert.Equal(t, triggertype.PullRequest, ie.TriggerTarget)
		assert.Equal(t, "pull_request_labeled", ie.EventType)
		assert.Equal(t, []string{"kind/lgtm"}, ie.PullRequestLabel)
		assert.Empty(t, ie.TriggerComment)
	})

	t.Run("comment", func(t *testing.T) {
		ev := matchEventOpts(t, event.Options{Comment: "/test please"}, nil)
		ie := toInfoEvent(ev)
		assert.Equal(t, triggertype.PullRequest, ie.TriggerTarget)
		assert.Equal(t, "no-ops-comment", ie.EventType)
		assert.Equal(t, "/test please", ie.TriggerComment)
		assert.Empty(t, ie.PullRequestLabel)
	})
}
