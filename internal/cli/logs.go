package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/audit"
	"github.com/tobyS/agent-creance/internal/state"
)

// newLogsCmd implements `agent-creance logs`: read the project's out-of-tree egress
// audit log. Bare, it dumps the rotated (.1) then current file as one stream, one
// human-formatted line per entry. --summary prints allow/soft-deny/hard-deny counts
// and exits; --follow streams new entries live, rotation-aware (native fsnotify, not
// `tail -f`). The two flags are mutually exclusive.
func newLogsCmd(app *App) *cobra.Command {
	var (
		follow  bool
		summary bool
	)
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Read the egress audit log (dump, --summary, or --follow)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if follow && summary {
				return fmt.Errorf("--follow and --summary are mutually exclusive")
			}
			layout, err := state.New(app.Paths).Resolve(".")
			if err != nil {
				return err
			}
			cur := layout.EgressJSONL()
			rot := layout.EgressJSONLRotated()

			switch {
			case summary:
				s, err := audit.SummarizeFiles(rot, cur)
				if err != nil {
					return err
				}
				fmt.Fprint(app.Stdout, s.Render(app.OutStyle))
				return nil
			case follow:
				return audit.Follow(cmd.Context(), app.Stdout, filepath.Dir(cur), cur, app.OutStyle)
			default:
				return audit.Dump(app.Stdout, rot, cur, app.OutStyle)
			}
		},
	}
	cmd.Flags().BoolVar(&follow, "follow", false, "stream new entries live, rotation-aware")
	cmd.Flags().BoolVar(&summary, "summary", false, "print allow/soft-deny/hard-deny counts and exit")
	return cmd
}
