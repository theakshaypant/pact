package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newResolveCmd(d deps, g *globalFlags) *cobra.Command {
	var ef eventFlags
	var pf paramFlags
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Print the hydrated PipelineRun YAML matching the simulated event",
		Long: `resolve hydrates every .tekton PipelineRun matching the simulated event,
exactly as Pipelines-as-Code would before submitting it: template
placeholders are substituted and referenced local tasks and pipelines are
inlined into a self-contained PipelineRun. Output is a multi-document YAML
stream on stdout.

Custom params (--param, --param-file) substitute {{ name }} placeholders
and are merged into the PipelineRuns' spec.params, so the output previews
exactly what ` + "`pact run`" + ` would submit with them.

Remote task references are not fetched (offline mode); they fail with an
error instead. Redirect stderr to keep the YAML stream clean.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := prepare(cmd.Context(), d, g, ef, pf)
			if err != nil {
				return err
			}
			out, err := p.engine.Resolve(cmd.Context(), p.tektonDir, p.event)
			if err != nil {
				return err
			}
			if _, err := d.out.Write(out); err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
			return nil
		},
	}
	ef.bind(cmd)
	pf.bind(cmd)
	return cmd
}
