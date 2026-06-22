package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/status"
)

// newStatusCmd implements `agent-creance status`: a read-only listing of running
// cages across all projects. It enumerates every project's out-of-tree state dir
// and reports each one's proxy state (running/orphan/stranded/down), port, and
// attached-agent count. It never mutates anything and always exits 0.
func newStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "List running cages across all projects",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return runStatus(app)
		},
	}
}

// runStatus is the testable body: it builds the status.Scanner from the App seams,
// scans every project, and renders the table to stdout.
func runStatus(app *App) error {
	scanner := &status.Scanner{
		Manager:  proxy.NewManager(app.FS, app.Flock, app.ProcessManager, app.PortAllocator, app.Sleeper, app.Stderr),
		Resolver: state.New(app.Paths),
		FS:       app.FS,
	}
	rep, err := scanner.Scan()
	if err != nil {
		return fmt.Errorf("scan projects: %w", err)
	}
	fmt.Fprint(app.Stdout, status.Render(rep, app.OutStyle))
	return nil
}
