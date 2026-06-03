package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/buildinfo"
)

func newVersionCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print agent-creance version and the tool versions it was tested against",
		Args:  cobra.NoArgs,
		// RunE receives the cobra command; we write through app.Stdout (not
		// fmt.Println) so output is captured in tests.
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Fprintf(app.Stdout, "agent-creance %s (commit %s, built %s)\n",
				buildinfo.Version, buildinfo.Commit, buildinfo.Date)
			fmt.Fprintln(app.Stdout, "tested against:")
			// Print in a stable order so the output is deterministic.
			for _, name := range []string{"agent-safehouse", "mitmproxy"} {
				fmt.Fprintf(app.Stdout, "  %-16s %s\n", name, app.Tested[name])
			}
			return nil
		},
	}
}
