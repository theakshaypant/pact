// Command pact runs Tekton Pipelines-as-Code definitions locally against
// uncommitted code, without a remote git forge or webhooks.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/theakshaypant/pact/pkg/cluster"
	"github.com/theakshaypant/pact/pkg/git"
	"github.com/theakshaypant/pact/pkg/pac"
	"github.com/theakshaypant/pact/pkg/runner"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "pact:", err)
		os.Exit(1)
	}
}

// run builds the root command with real dependencies and executes it.
func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	d := deps{
		openGit:    git.Open,
		newEngine:  func(verbose bool) pac.Engine { return pac.New(buildLogger(verbose, errOut)) },
		newCluster: func(opts cluster.Options) (clusterManager, error) { return cluster.New(opts) },
		newRunner: func(progress io.Writer) runRunner {
			return runner.New(runner.NewClient, progress)
		},
		out:    out,
		errOut: errOut,
	}
	root := newRootCmd(d)
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(errOut)
	return root.ExecuteContext(ctx)
}
