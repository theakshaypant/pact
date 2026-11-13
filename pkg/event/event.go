// Package event builds synthetic GitHub webhook events from local git
// state, so Pipelines-as-Code matching can run without a remote forge.
package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/theakshaypant/pact/pkg/git"
)

// Type is the kind of GitHub event to simulate.
type Type string

const (
	PullRequest Type = "pull_request"
	Push        Type = "push"
)

// Kind is the event's Pipelines-as-Code matching semantics: which PAC
// annotation set applies (on-event/on-target-branch, on-label or
// on-comment). Its values map one-to-one onto PAC's info.Event EventType,
// so pkg/pac translates them with a plain copy. Type, in contrast, is only
// the webhook family; the two differ for label and comment events, which
// are pull-request-scoped but match dedicated annotations.
type Kind string

const (
	KindPullRequest Kind = "pull_request"
	KindPush        Kind = "push"
	KindLabeled     Kind = "pull_request_labeled"
	// KindComment is PAC's event type for a plain (non-ops) pull request
	// comment. PAC's matcher rewrites it to "on-comment" internally when a
	// PipelineRun's on-comment regex matches.
	KindComment Kind = "no-ops-comment"
)

// webhookIssueComment is GitHub's X-GitHub-Event value for PR comment
// webhooks.
const webhookIssueComment = "issue_comment"

// ParseType converts a string flag value into a Type.
func ParseType(s string) (Type, error) {
	switch Type(s) {
	case PullRequest:
		return PullRequest, nil
	case Push:
		return Push, nil
	}
	return "", fmt.Errorf("unknown event type %q (want %q or %q)", s, PullRequest, Push)
}

// defaultSender is the synthetic sender when neither the flag nor the HEAD
// commit author provides one.
const defaultSender = "pact"

// Event is pact's model of a synthetic webhook event. It is deliberately
// decoupled from Pipelines-as-Code's info.Event: the translation happens in
// pkg/pac, so PAC types never leak past that package.
type Event struct {
	Type   Type
	Action string // webhook action, e.g. "opened", "labeled", "created"

	// Kind selects the Pipelines-as-Code matching semantics (see Kind).
	Kind Kind
	// Webhook is the X-GitHub-Event header value a real forge would send.
	Webhook string

	// Labels are the pull request's labels (labeled events).
	Labels []string
	// Comment is the body of the triggering comment (comment events).
	Comment string

	Owner string
	Repo  string

	// NonGitHubRemote is true when an origin remote exists but is not
	// GitHub; GitHub-specific fields then carry neutral defaults.
	NonGitHubRemote bool

	DefaultBranch string // repository default branch (the detected target)
	BaseBranch    string // target branch of the simulated event
	HeadBranch    string // source branch (current checkout)
	BaseURL       string // target repository web URL
	HeadURL       string // source repository web URL
	CloneURL      string // git clone URL of the source

	SHA      string
	BaseSHA  string
	SHATitle string
	Sender   string
	PRNumber int
	PRTitle  string

	ChangedFiles git.ChangedFiles

	// Payload is the webhook-shaped body; it feeds CEL body.* expressions
	// and {{ body.x }} templates.
	Payload map[string]any
	// Headers carries the provider headers a real forge would send.
	Headers http.Header
}

// Options tweaks how the synthetic event is built.
type Options struct {
	Type     Type   // empty defaults to PullRequest
	Sender   string // override the sender login; empty uses the HEAD commit author
	PRNumber int    // override the pull request number; 0 becomes 1
	PRTitle  string // override the pull request title; empty uses the commit title

	// Labels simulate a labeled pull request: the kind becomes
	// pull_request_labeled and only PipelineRuns with a matching on-label
	// annotation match.
	Labels []string
	// Comment simulates a comment on the pull request (free text, matched
	// by on-comment regexes).
	Comment string
}

// Build creates a synthetic event from a repository snapshot.
func Build(s *git.Snapshot, o Options) (*Event, error) {
	if s == nil {
		return nil, errors.New("building event: nil git snapshot")
	}
	t := o.Type
	if t == "" {
		t = PullRequest
	}
	if err := validateOptions(t, o); err != nil {
		return nil, err
	}
	labels, err := normalizeLabels(o.Labels)
	if err != nil {
		return nil, err
	}
	o.Labels = labels

	ev := &Event{
		Type:            t,
		Owner:           s.Remote.Owner,
		Repo:            s.Remote.Repo,
		NonGitHubRemote: s.RemoteURL != "" && !s.Remote.IsGitHub,
		DefaultBranch:   s.TargetBranch,
		CloneURL:        s.RemoteURL,
		SHA:             s.HeadSHA,
		BaseSHA:         s.BaseSHA,
		SHATitle:        s.HeadTitle,
		ChangedFiles:    s.ChangedFiles,
	}

	branch := s.Branch
	if s.Detached {
		branch = "HEAD"
	}
	ev.HeadBranch = branch

	ev.Sender = orDefault(o.Sender, orDefault(s.Author, defaultSender))
	ev.PRNumber = o.PRNumber
	if ev.PRNumber == 0 {
		ev.PRNumber = 1
	}
	ev.PRTitle = orDefault(o.PRTitle, s.HeadTitle)

	// Web URLs: GitHub remotes get a proper https URL; anything else falls
	// back to the raw remote URL rather than inventing github.com links.
	webURL := s.RemoteURL
	if s.Remote.IsGitHub {
		webURL = fmt.Sprintf("https://%s/%s/%s", s.Remote.Host, s.Remote.Owner, s.Remote.Repo)
	}
	ev.HeadURL = webURL
	ev.BaseURL = webURL

	if err := ev.applyKind(t, o, s.TargetBranch, branch); err != nil {
		return nil, err
	}
	return ev, nil
}

// kindProfile carries the per-kind webhook fields.
type kindProfile struct {
	action  string // webhook action, empty when the kind has none
	webhook string // X-GitHub-Event header value
}

// kindProfiles maps every event kind to its webhook action and header.
var kindProfiles = map[Kind]kindProfile{
	KindPullRequest: {action: "opened", webhook: string(PullRequest)},
	KindLabeled:     {action: "labeled", webhook: string(PullRequest)},
	KindPush:        {webhook: string(Push)},
	KindComment:     {action: "created", webhook: webhookIssueComment},
}

// applyKind sets the kind-specific fields: the PAC matching kind, the
// webhook header, the action, the payload and the target branch. Labels
// and comments refine a pull_request event into the labeled and comment
// kinds; targetBranch is the repository's detected default branch and
// branch the current checkout.
func (e *Event) applyKind(t Type, o Options, targetBranch, branch string) error {
	e.Kind = Kind(t)
	if len(o.Labels) > 0 {
		e.Kind = KindLabeled
	} else if o.Comment != "" {
		e.Kind = KindComment
	}
	profile, ok := kindProfiles[e.Kind]
	if !ok {
		// Unreachable when the type came from ParseType; guards direct
		// Build callers.
		return fmt.Errorf("building event: unknown kind %q", e.Kind)
	}

	e.Action = profile.action
	e.Webhook = profile.webhook
	e.Labels = o.Labels
	e.Comment = o.Comment
	switch e.Kind {
	case KindPullRequest, KindLabeled:
		e.BaseBranch = targetBranch
		e.Payload = pullRequestPayload(e)
	case KindComment:
		e.BaseBranch = targetBranch
		e.Payload = issueCommentPayload(e)
	case KindPush:
		// A push targets the branch itself; the "before" SHA is the merge
		// base, an approximation of what the remote would have had.
		e.BaseBranch = branch
		e.Payload = pushPayload(e)
	}
	e.Headers = http.Header{"X-GitHub-Event": []string{e.Webhook}}
	return nil
}

// validateOptions rejects option combinations that model no real webhook:
// labels and comments only exist on pull requests, and a single event
// cannot carry both.
func validateOptions(t Type, o Options) error {
	if t != PullRequest && (len(o.Labels) > 0 || o.Comment != "") {
		return fmt.Errorf("building event: labels and comments apply only to %s events, not %q", PullRequest, t)
	}
	if len(o.Labels) > 0 && o.Comment != "" {
		return errors.New("building event: labels and a comment cannot be combined")
	}
	return nil
}

// normalizeLabels trims surrounding whitespace from every label — GitHub
// labels cannot carry it, and a stray space would silently break on-label
// matching — and rejects labels that are empty afterwards.
func normalizeLabels(labels []string) ([]string, error) {
	out := make([]string, len(labels))
	for i, l := range labels {
		out[i] = strings.TrimSpace(l)
		if out[i] == "" {
			return nil, errors.New("building event: labels must not be empty or whitespace")
		}
	}
	return out, nil
}

// JSON marshals the webhook-shaped payload.
func (e *Event) JSON() ([]byte, error) {
	return json.MarshalIndent(e.Payload, "", "  ")
}

func orDefault(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
