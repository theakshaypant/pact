package pac

import (
	"strconv"
	"strings"

	"github.com/openshift-pipelines/pipelines-as-code/pkg/opscomments"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/params/info"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/params/triggertype"
	"github.com/theakshaypant/pact/pkg/event"
)

// toInfoEvent converts pact's event model into Pipelines-as-Code's
// info.Event. A fresh info.Event is built on every call because PAC mutates
// the event during matching (e.g. the on-comment path rewrites EventType).
func toInfoEvent(ev *event.Event) *info.Event {
	ie := info.NewEvent() // Request must be non-nil or PAC panics
	ie.TriggerTarget = triggertype.Trigger(ev.Type)
	ie.EventType = string(ev.Kind)
	ie.PullRequestLabel = ev.Labels
	ie.TriggerComment = ev.Comment
	ie.BaseBranch = ev.BaseBranch
	ie.HeadBranch = ev.HeadBranch
	ie.BaseURL = ev.BaseURL
	ie.HeadURL = ev.HeadURL
	ie.SHA = ev.SHA
	ie.Sender = ev.Sender
	ie.SHATitle = ev.SHATitle
	ie.Organization = ev.Owner
	ie.Repository = ev.Repo
	ie.URL = ev.HeadURL
	ie.PullRequestNumber = ev.PRNumber
	ie.PullRequestTitle = ev.PRTitle
	ie.Event = ev.Payload
	ie.Request.Header = ev.Headers
	return ie
}

// templateEventType maps an event kind to the event_type value PAC's
// templates see: PAC's matcher rewrites a matched comment event's type to
// "on-comment" before templates are resolved, so every run that survives
// matching sees that value.
func templateEventType(k event.Kind) string {
	if k == event.KindComment {
		return opscomments.OnCommentEventType.String()
	}
	return string(k)
}

// templateDict builds the flat parameter dictionary substituted into
// {{ key }} placeholders. The keys mirror PAC's standard params.
func templateDict(ev *event.Event) map[string]string {
	repoURL := ev.CloneURL
	if repoURL == "" {
		repoURL = ev.HeadURL
	}
	return map[string]string{
		"revision":            ev.SHA,
		"repo_url":            repoURL,
		"repo_owner":          ev.Owner,
		"repo_name":           ev.Repo,
		"target_branch":       ev.BaseBranch,
		"source_branch":       ev.HeadBranch,
		"source_url":          ev.HeadURL,
		"sender":              ev.Sender,
		"event_type":          templateEventType(ev.Kind),
		"pull_request_number": strconv.Itoa(ev.PRNumber),
		"trigger_comment":     singleLine(ev.Comment),
		"pull_request_labels": singleLine(strings.Join(ev.Labels, "\n")),
	}
}

// singleLine escapes newlines the way PAC renders trigger_comment and
// pull_request_labels, so multi-line values stay usable inside annotations
// and params.
func singleLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\\n"), "\n", "\\n")
}
