package main

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theakshaypant/pact/pkg/event"
)

// eventFlags configure the simulated webhook event. They are shared by
// match, resolve and run.
type eventFlags struct {
	eventType    string
	targetBranch string
	sender       string
	prNumber     int
	prTitle      string
	labels       []string
	comment      string

	// cmd is the command the flags are bound to, so toOptions can tell an
	// explicitly empty --comment apart from an unset one.
	cmd *cobra.Command
}

func (f *eventFlags) bind(cmd *cobra.Command) {
	f.cmd = cmd
	cmd.Flags().StringVar(&f.eventType, "event", "pull_request",
		"simulated GitHub event type: pull_request or push")
	cmd.Flags().StringVar(&f.targetBranch, "target-branch", "",
		"target branch of the simulated event (default: auto-detect origin/HEAD, main, master)")
	cmd.Flags().StringVar(&f.sender, "sender", "",
		"sender login of the simulated event (default: HEAD commit author)")
	cmd.Flags().IntVar(&f.prNumber, "pr-number", 0,
		"pull request number of the simulated event (default: 1)")
	cmd.Flags().StringVar(&f.prTitle, "pr-title", "",
		"pull request title (default: HEAD commit title)")
	cmd.Flags().StringArrayVar(&f.labels, "label", nil,
		"label applied to the simulated pull request (repeatable; simulates a labeled event)")
	cmd.Flags().StringVar(&f.comment, "comment", "",
		"comment body on the simulated pull request (matched by on-comment regexes)")
}

func (f *eventFlags) toOptions() (event.Options, error) {
	t, err := event.ParseType(f.eventType)
	if err != nil {
		return event.Options{}, err
	}
	if f.cmd != nil && f.cmd.Flags().Changed("comment") && strings.TrimSpace(f.comment) == "" {
		return event.Options{}, errors.New("--comment must not be empty or whitespace")
	}
	return event.Options{
		Type:     t,
		Sender:   f.sender,
		PRNumber: f.prNumber,
		PRTitle:  f.prTitle,
		Labels:   f.labels,
		Comment:  f.comment,
	}, nil
}
