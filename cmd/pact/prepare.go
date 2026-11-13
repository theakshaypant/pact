package main

import (
	"context"
	"fmt"

	"github.com/theakshaypant/pact/pkg/event"
	"github.com/theakshaypant/pact/pkg/git"
	"github.com/theakshaypant/pact/pkg/pac"
)

// prepared carries everything a command needs after repository inspection:
// the engine, the synthetic event and the resolved tekton directory.
type prepared struct {
	engine    pac.Engine
	event     *event.Event
	tektonDir string
	repoRoot  string
}

// prepare opens the repository, takes a snapshot, builds the synthetic
// event and constructs the engine, printing warnings to errOut. Every
// command that needs an event (match, resolve, run) goes
// through here.
func prepare(ctx context.Context, d deps, g *globalFlags, ef eventFlags) (*prepared, error) {
	insp, err := d.openGit(g.dir)
	if err != nil {
		return nil, err
	}
	snap, err := insp.Snapshot(ctx, git.Options{TargetBranch: ef.targetBranch})
	if err != nil {
		return nil, err
	}
	opts, err := ef.toOptions()
	if err != nil {
		return nil, err
	}
	ev, err := event.Build(snap, opts)
	if err != nil {
		return nil, err
	}
	if ev.NonGitHubRemote {
		fmt.Fprintf(d.errOut, "warning: origin remote %q is not GitHub; GitHub-specific event fields use neutral defaults\n", snap.RemoteURL)
	}
	if snap.Detached {
		fmt.Fprintf(d.errOut, "warning: HEAD is detached; the simulated event uses the literal branch \"HEAD\"\n")
	}
	return &prepared{
		engine:    d.newEngine(g.verbose),
		event:     ev,
		tektonDir: tektonPath(snap.RepoRoot, g.tektonDir),
		repoRoot:  snap.RepoRoot,
	}, nil
}
