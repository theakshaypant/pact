package event

import (
	"fmt"
	"strings"
)

// pullRequestPayload builds a GitHub pull_request webhook-shaped body.
func pullRequestPayload(e *Event) map[string]any {
	repo := repositoryPayload(e)
	payload := map[string]any{
		"action": e.Action,
		"number": e.PRNumber,
		"pull_request": map[string]any{
			"number": e.PRNumber,
			"title":  e.PRTitle,
			"state":  "open",
			"user":   map[string]any{"login": e.Sender},
			"head":   refPayload(e.HeadBranch, e.SHA, repo),
			"base":   refPayload(e.BaseBranch, e.BaseSHA, repo),
			"labels": labelsPayload(e),
		},
		"repository": repo,
		"sender":     map[string]any{"login": e.Sender},
	}
	if len(e.Labels) > 0 {
		// GitHub's labeled webhook also carries the added label at the top
		// level; pact simulates applying all labels at once, so the last
		// one plays that role.
		payload["label"] = map[string]any{"name": e.Labels[len(e.Labels)-1]}
	}
	return payload
}

// labelsPayload renders the pull request's labels as GitHub-shaped
// objects. It is never nil so "labels" is always a JSON array.
func labelsPayload(e *Event) []any {
	labels := make([]any, 0, len(e.Labels))
	for _, l := range e.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	return labels
}

// issueCommentPayload builds a GitHub issue_comment webhook-shaped body
// for a comment on the simulated pull request.
func issueCommentPayload(e *Event) map[string]any {
	repo := repositoryPayload(e)
	return map[string]any{
		"action": e.Action,
		"issue": map[string]any{
			"number": e.PRNumber,
			"title":  e.PRTitle,
			"state":  "open",
			"user":   map[string]any{"login": e.Sender},
			// GitHub marks an issue as a pull request with this key.
			"pull_request": map[string]any{
				"url": fmt.Sprintf("%s/pull/%d", e.HeadURL, e.PRNumber),
			},
		},
		"comment": map[string]any{
			"body": e.Comment,
			"user": map[string]any{"login": e.Sender},
		},
		"repository": repo,
		"sender":     map[string]any{"login": e.Sender},
	}
}

// pushPayload builds a GitHub push webhook-shaped body. The ref keeps the
// full refs/heads/ form, as GitHub sends it.
func pushPayload(e *Event) map[string]any {
	before := e.BaseSHA
	if before == "" {
		before = strings.Repeat("0", 40)
	}
	repo := repositoryPayload(e)
	return map[string]any{
		"ref":    "refs/heads/" + e.HeadBranch,
		"before": before,
		"after":  e.SHA,
		"head_commit": map[string]any{
			"id":      e.SHA,
			"message": e.SHATitle,
			"author":  map[string]any{"name": e.Sender},
		},
		"repository": repo,
		"pusher":     map[string]any{"name": e.Sender},
		"sender":     map[string]any{"login": e.Sender},
	}
}

// refPayload builds a pull request head/base ref entry.
func refPayload(ref, sha string, repo map[string]any) map[string]any {
	return map[string]any{
		"ref":  ref,
		"sha":  sha,
		"repo": repo,
	}
}

// repositoryPayload builds the nested repository entry shared by both event
// kinds.
func repositoryPayload(e *Event) map[string]any {
	fullName := ""
	if e.Owner != "" && e.Repo != "" {
		fullName = e.Owner + "/" + e.Repo
	}
	return map[string]any{
		"name":           e.Repo,
		"full_name":      fullName,
		"html_url":       e.HeadURL,
		"clone_url":      e.CloneURL,
		"default_branch": e.DefaultBranch,
		"owner":          map[string]any{"login": e.Owner},
	}
}
