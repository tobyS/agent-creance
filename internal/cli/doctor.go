package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/prereq"
)

// newDoctorCmd implements the slice of `agent-creance doctor` that checks
// prerequisites and reports version compatibility. The full doctor (orphan
// proxies, CA trust, exposed host services) lands as those subsystems are built.
func newDoctorCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check prerequisites and report version compatibility",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// cmd.Context() carries cancellation from ExecuteContext, so a hung
			// `--version` call is interruptible.
			tools := prereq.DefaultTools(app.Tested)
			results := prereq.Check(cmd.Context(), app.Commander, tools)

			fmt.Fprint(app.Stdout, prereq.Report(results))

			// If anything is missing, doctor still prints the report above, then
			// appends the actionable install block and exits non-zero so scripts
			// can detect the unhealthy state.
			if instructions := prereq.MissingInstructions(results); instructions != "" {
				fmt.Fprintln(app.Stdout)
				fmt.Fprint(app.Stdout, instructions)
				return fmt.Errorf("%d prerequisite(s) missing", len(prereq.Missing(results)))
			}
			return nil
		},
	}
}
