package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"github.com/theakshaypant/pact/pkg/cluster"
	"github.com/theakshaypant/pact/pkg/pac"
	"github.com/theakshaypant/pact/pkg/runner"
	"github.com/theakshaypant/pact/pkg/stream"
)

// runRunner abstracts pkg/runner's Runner so command tests can fake the
// cluster entirely.
type runRunner interface {
	Run(ctx context.Context, kubeconfig []byte, runs []*tektonv1.PipelineRun,
		opts runner.Options) (runner.Result, error)
}

func newRunCmd(d deps, g *globalFlags) *cobra.Command {
	var ef eventFlags
	var provider, kubeconfigPath, workspace, workspaceSize string
	var keepCloneTasks, noCleanup bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute the matching pipelines against the local cluster",
		Long: `run executes every .tekton pipeline matching the simulated event
against the local cluster, using your uncommitted working tree as the
source.

It ensures the cluster and Tekton are up (like ` + "`pact up`" + `, idempotent),
packages the repository (respecting .gitignore) into a fresh namespace,
binds it to the pipeline's source workspace, and submits each matching
PipelineRun, streaming its task and step logs live with color-coded
[task : step] prefixes, one color per task (plain when not writing to a
terminal or NO_COLOR is set). Tasks that fetch the repository (git-clone,
fetch-repository, ...) are dropped since the source is pre-populated.

The workspace bound to the source is the one named "source" if the
pipeline declares it, else the single declared workspace; --workspace
picks explicitly. The run namespace is deleted after a fully
successful run and kept for inspection when any run fails or
--no-cleanup is passed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()

			p, err := prepare(ctx, d, g, ef)
			if err != nil {
				return err
			}
			runs, err := planRuns(ctx, p)
			if err != nil {
				return err
			}
			if runs == nil {
				fmt.Fprintln(d.out, "no matching pipelines")
				return nil
			}

			kubeconfig, err := ensureCluster(ctx, d, cmd, provider, kubeconfigPath)
			if err != nil {
				return err
			}

			result, err := d.newRunner(d.out).Run(ctx, kubeconfig, runs, runner.Options{
				RepoRoot:       p.repoRoot,
				Workspace:      workspace,
				WorkspaceSize:  workspaceSize,
				KeepCloneTasks: keepCloneTasks,
				NoCleanup:      noCleanup,
			})
			if err != nil {
				return err
			}
			return reportRuns(d.out, result)
		},
	}
	ef.bind(cmd)
	cmd.Flags().StringVar(&provider, "provider", cluster.ProviderExternal,
		"cluster provider (external or kind)")
	cmd.Flags().StringVar(&kubeconfigPath, "kubeconfig", "",
		"kubeconfig of a pre-existing cluster to connect to (implies --provider external)")
	cmd.Flags().StringVar(&workspace, "workspace", "",
		"pipeline workspace to bind the source to (default: \"source\", else the single declared workspace)")
	cmd.Flags().StringVar(&workspaceSize, "workspace-size", runner.DefaultWorkspaceSize,
		"size of the source workspace volume (e.g. 1Gi)")
	cmd.Flags().BoolVar(&keepCloneTasks, "keep-clone-tasks", false,
		"do not drop repository-fetching tasks")
	cmd.Flags().BoolVar(&noCleanup, "no-cleanup", false,
		"keep the run namespace even when every run succeeded")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute,
		"maximum time to wait for the cluster and the runs")
	return cmd
}

// planRuns matches and resolves the .tekton definitions into runnable
// PipelineRuns. A nil result means nothing matched.
func planRuns(ctx context.Context, p *prepared) ([]*tektonv1.PipelineRun, error) {
	matches, err := p.engine.Match(ctx, p.tektonDir, p.event)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	resolved, err := p.engine.Resolve(ctx, p.tektonDir, p.event)
	if err != nil {
		return nil, err
	}
	return pac.DecodePipelineRuns(resolved)
}

// ensureCluster brings the cluster up and returns its kubeconfig.
func ensureCluster(ctx context.Context, d deps, cmd *cobra.Command, provider, kubeconfigPath string) ([]byte, error) {
	opts, err := clusterOptions(cmd, provider, kubeconfigPath)
	if err != nil {
		return nil, err
	}
	mgr, err := d.newCluster(opts)
	if err != nil {
		return nil, err
	}
	if err := mgr.Up(ctx, cluster.UpOptions{}); err != nil {
		return nil, err
	}
	return mgr.Kubeconfig(ctx)
}

// reportRuns prints each run's outcome and returns a failure error when
// any run failed.
func reportRuns(out io.Writer, result runner.Result) error {
	colors := stream.NewColors(out)
	for _, run := range result.Runs {
		if run.Success {
			fmt.Fprintln(out, colors.Green(fmt.Sprintf("%s: succeeded", run.Name)))
			continue
		}
		fmt.Fprintln(out, colors.Red(fmt.Sprintf("%s: failed: %s", run.Name, run.Message)))
	}
	if failed := result.FailedCount(); failed > 0 {
		return fmt.Errorf("%d of %d pipelinerun(s) failed", failed, len(result.Runs))
	}
	return nil
}
