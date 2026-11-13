package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/theakshaypant/pact/pkg/cluster"
)

// clusterManager abstracts pkg/cluster's Manager so command tests can
// fake the cluster entirely.
type clusterManager interface {
	Up(ctx context.Context, opts cluster.UpOptions) error
	Down(ctx context.Context, opts cluster.DownOptions) error
	Kubeconfig(ctx context.Context) ([]byte, error)
}

func newUpCmd(d deps) *cobra.Command {
	var provider, kubeconfig, tektonVersion string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Ensure the local cluster and install Tekton on it",
		Long: `up makes sure a cluster is available, installs Tekton Pipelines on it
and waits until the controllers are ready. It is idempotent: an existing
cluster is reused and the install converges.

Providers:
  external pact connects to a pre-existing cluster via its kubeconfig
           and never creates or deletes it (default); ` + "`pact down`" + ` removes
           only pact's Tekton installation. The kubeconfig is taken from
           --kubeconfig, then $KUBECONFIG, then ~/.kube/config; passing
           --kubeconfig implies --provider external.
  kind     pact creates and owns a kind cluster named ` + cluster.DefaultName + `
           in Docker, reusing it when it already exists.

Tekton is installed from the official release manifest embedded in this
binary, so no network is needed. An installation that is already present
is reused untouched (only verified); --tekton-version fetches a different
official release and installs it instead (requires network).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := clusterOptions(cmd, provider, kubeconfig)
			if err != nil {
				return err
			}
			mgr, err := d.newCluster(opts)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			if err := mgr.Up(ctx, cluster.UpOptions{TektonVersion: tektonVersion}); err != nil {
				return err
			}
			fmt.Fprintln(d.out, "tekton is ready: kubectl get pods -n tekton-pipelines")
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, "provider", cluster.ProviderExternal,
		"cluster provider (external or kind)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "",
		"kubeconfig of a pre-existing cluster to connect to (implies --provider external)")
	cmd.Flags().StringVar(&tektonVersion, "tekton-version", "",
		"install this official Tekton release instead of the embedded one (e.g. v1.19.0)")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute,
		"maximum time to wait for the cluster and Tekton")
	return cmd
}
