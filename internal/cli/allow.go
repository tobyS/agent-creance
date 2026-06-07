package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/config"
)

// newAllowCmd implements `agent-creance allow URL` — append a soft-allow rule to the
// egress policy and recompile so a running proxy picks it up without a restart. By
// default it edits the committed project config; --global edits ~/.config and --once
// writes the out-of-tree session overlay that AC-0020 purges on last-agent-exit.
// (docs/design.md "Commands" + "Session-scoped allows".)
func newAllowCmd(app *App) *cobra.Command {
	var once, global bool
	cmd := &cobra.Command{
		Use:   "allow URL",
		Short: "Append a soft-allow rule and recompile the policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAllow(cmd.Context(), app, ".", args[0], once, global)
		},
	}
	cmd.Flags().BoolVar(&once, "once", false,
		"write to the session overlay (purged on last-agent-exit), not the committed config")
	cmd.Flags().BoolVar(&global, "global", false,
		"append to ~/.config/agent-creance.yaml instead of the project config")
	return cmd
}

// runAllow is the testable body: dir (the project directory, "." in production) and
// the once/global flags are parameters — not globals — so unit tests can drive every
// target against the sysdep fakes.
func runAllow(ctx context.Context, app *App, dir, rawURL string, once, global bool) error {
	if once && global {
		return errors.New("cannot combine --once and --global: pick a session overlay or the global config")
	}
	rule, err := ruleFromURL(rawURL, "")
	if err != nil {
		return err
	}
	path, label, err := mutationTarget(app, dir, once, global)
	if err != nil {
		return err
	}
	return mutateAndRecompile(ctx, app, dir, path, label, config.AllowList, rule, "allowed")
}
