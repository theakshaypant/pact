package main

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/theakshaypant/pact/pkg/cluster"
	"github.com/theakshaypant/pact/pkg/git"
	"github.com/theakshaypant/pact/pkg/pac"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// deps carries the command wiring so tests can substitute fakes.
type deps struct {
	openGit    func(dir string) (git.Inspector, error)
	newEngine  func(verbose bool) pac.Engine
	newCluster func(opts cluster.Options) (clusterManager, error)
	newRunner  func(progress io.Writer) runRunner
	out        io.Writer
	errOut     io.Writer
}

// globalFlags are shared by every subcommand.
type globalFlags struct {
	dir       string // repository (or subdirectory) to operate on
	tektonDir string // tekton directory, relative to the repo root
	verbose   bool
}

func newRootCmd(d deps) *cobra.Command {
	var g globalFlags
	root := &cobra.Command{
		Use:   "pact",
		Short: "Run Tekton Pipelines-as-Code locally",
		Long: `pact tests, evaluates and executes Tekton Pipelines-as-Code (.tekton/)
definitions locally against uncommitted code, without requiring a remote git
forge or webhooks.

Commands:
  match    Print the .tekton pipelines matching the simulated event
  resolve  Print the hydrated PipelineRun YAML matching the simulated event
  run      Execute the matching pipelines against the local cluster
  up       Ensure the local cluster and install Tekton on it
  down     Remove the local cluster (or pact's Tekton installation)`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&g.dir, "dir", "C", ".",
		"repository directory to operate on (a subdirectory is fine)")
	root.PersistentFlags().StringVar(&g.tektonDir, "tekton-dir", ".tekton",
		"directory containing the Pipelines-as-Code YAML, relative to the repository root")
	root.PersistentFlags().BoolVarP(&g.verbose, "verbose", "v", false,
		"verbose logging on stderr")

	root.AddCommand(newMatchCmd(d, &g), newResolveCmd(d, &g), newRunCmd(d, &g), newUpCmd(d), newDownCmd(d))
	return root
}

// clusterOptions resolves the --provider and --kubeconfig flag pair into
// cluster.Options, with progress messages going to out. An explicit
// --kubeconfig selects a pre-existing cluster, so it implies the external
// provider; combining it with an explicitly requested other provider is an
// error rather than a silent ignore.
func clusterOptions(cmd *cobra.Command, out io.Writer, provider, kubeconfig string) (cluster.Options, error) {
	if kubeconfig == "" {
		return cluster.Options{Provider: provider, Progress: out}, nil
	}
	if cmd.Flags().Changed("provider") && provider != cluster.ProviderExternal {
		return cluster.Options{}, fmt.Errorf("--kubeconfig cannot be combined with --provider %s", provider)
	}
	return cluster.Options{Provider: cluster.ProviderExternal, Kubeconfig: kubeconfig, Progress: out}, nil
}

// buildLogger returns a no-op logger, or a debug-level development logger
// writing to w when verbose.
func buildLogger(verbose bool, w io.Writer) *zap.SugaredLogger {
	if !verbose {
		return zap.NewNop().Sugar()
	}
	cfg := zap.NewDevelopmentConfig()
	cfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	log, err := cfg.Build(zap.ErrorOutput(zapcore.AddSync(w)))
	if err != nil {
		fmt.Fprintf(w, "warning: building verbose logger: %v; falling back to silent logging\n", err)
		return zap.NewNop().Sugar()
	}
	return log.Sugar()
}

// tektonPath resolves the tekton directory against the repository root.
func tektonPath(repoRoot, tektonDir string) string {
	if filepath.IsAbs(tektonDir) {
		return tektonDir
	}
	return filepath.Join(repoRoot, tektonDir)
}
