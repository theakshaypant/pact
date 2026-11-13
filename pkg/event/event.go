// Package event builds synthetic GitHub webhook events from local git
// state, so Pipelines-as-Code matching can run without a remote forge.
package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/theakshaypant/pact/pkg/git"
)

// Type is the kind of GitHub event to simulate.
type Type string

const (
	PullRequest Type = "pull_request"
	Push        Type = "push"
)

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
	Action string // pull_request action, e.g. "opened"

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

	switch t {
	case PullRequest:
		ev.Action = "opened"
		ev.BaseBranch = s.TargetBranch
		ev.Payload = pullRequestPayload(ev)
	case Push:
		// A push targets the branch itself; the "before" SHA is the merge
		// base, an approximation of what the remote would have had.
		ev.BaseBranch = branch
		ev.Payload = pushPayload(ev)
	default:
		// Unreachable when the type came from ParseType; guards direct
		// Build callers.
		return nil, fmt.Errorf("building event: unknown type %q", t)
	}

	ev.Headers = http.Header{"X-GitHub-Event": []string{string(t)}}
	return ev, nil
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
