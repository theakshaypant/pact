package main

import (
	"context"
	"time"

	"github.com/spf13/cobra"
	"github.com/theakshaypant/pact/pkg/cluster"
)

func newDownCmd(d deps) *cobra.Command {
	var provider, kubeconfig string
	var force bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Remove the local cluster (or pact's Tekton installation)",
		Long: `down removes what pact manages. For external (the default) it is
only the Tekton installation pact applied — the cluster keeps running,
and a Tekton installation that was already there before pact is left in
place unless --force is passed. For kind it is the whole ` + cluster.DefaultName + `
cluster in Docker.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := clusterOptions(cmd, d.out, provider, kubeconfig)
			if err != nil {
				return err
			}
			mgr, err := d.newCluster(opts)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			return mgr.Down(ctx, cluster.DownOptions{Force: force})
		},
	}
	cmd.Flags().StringVar(&provider, "provider", cluster.ProviderExternal,
		"cluster provider (external or kind)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "",
		"kubeconfig of a pre-existing cluster to connect to (implies --provider external)")
	cmd.Flags().BoolVar(&force, "force", false,
		"remove a Tekton installation even if pact did not apply it (external provider only)")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute,
		"maximum time to wait for the teardown")
	return cmd
}
