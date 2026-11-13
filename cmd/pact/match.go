package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newMatchCmd(d deps, g *globalFlags) *cobra.Command {
	var ef eventFlags
	cmd := &cobra.Command{
		Use:   "match",
		Short: "Print the .tekton pipelines matching the simulated event",
		Long: `match prints the filename of every .tekton file containing a pipeline
whose annotations match the simulated event, one per line (a file with
several matching runs is printed once). No match is a valid result: the
output is empty and the exit code is 0.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := prepare(cmd.Context(), d, g, ef)
			if err != nil {
				return err
			}
			matches, err := p.engine.Match(cmd.Context(), p.tektonDir, p.event)
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, m := range matches {
				if seen[m.File] {
					continue
				}
				seen[m.File] = true
				fmt.Fprintln(d.out, m.File)
			}
			return nil
		},
	}
	ef.bind(cmd)
	return cmd
}
