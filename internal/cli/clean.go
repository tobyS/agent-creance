package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/state"
)

// newCleanCmd implements `agent-creance clean`: tear down this project's proxy +
// lock + session overlay, idempotently and orphan-safe. It refuses (non-zero exit)
// when live agents are still attached, unless --force is given.
func newCleanCmd(app *App) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Tear down this project's proxy, lock, and session overlay",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return runClean(app, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"tear down even while agents are still attached (they will lose egress)")
	return cmd
}

// runClean is the testable body: it resolves this project's layout and drives
// proxy.Manager.Clean, reporting the outcome. A refusal is a non-zero exit so the
// operator notices their request was not carried out.
func runClean(app *App, force bool) error {
	layout, err := state.New(app.Paths).Resolve(".")
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}
	mgr := proxy.NewManager(app.FS, app.Flock, app.ProcessManager, app.PortAllocator, app.Stderr)
	res, err := mgr.Clean(layout, force)
	if err != nil {
		return fmt.Errorf("clean proxy: %w", err)
	}

	switch {
	case res.Refused:
		fmt.Fprintf(app.Stdout,
			"%d agent(s) still attached (PIDs %s); stop them or re-run with --force\n",
			len(res.LiveAgents), formatPIDs(res.LiveAgents))
		return fmt.Errorf("clean refused: agents still attached")
	case res.Cleaned:
		fmt.Fprintf(app.Stdout,
			"stopped proxy (pid %d); cleared lock and session overlay\n", res.ProxyPID)
	default:
		fmt.Fprintln(app.Stdout, "nothing to clean (no proxy running)")
	}
	return nil
}

// formatPIDs renders a PID slice as a comma-separated list for the refusal message.
func formatPIDs(pids []int) string {
	parts := make([]string, len(pids))
	for i, p := range pids {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ", ")
}
