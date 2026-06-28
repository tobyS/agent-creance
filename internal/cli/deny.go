package cli

import (
	"context"

	"github.com/spf13/cobra"
)

// newDenyCmd implements `agent-creance deny URL [--reason]` — append a hard-deny
// (deny_always) rule to the committed project config and recompile. Unlike allow,
// deny has no --once/--global in v0.1 (docs/design.md "Commands"): a hard deny is a
// deliberate, committed decision, so it always lands in the project file. It is a thin
// alias over `domain add --deny` (AC-0067).
func newDenyCmd(app *App) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "deny URL",
		Short: "Append a deny_always rule and recompile the policy",
		Long: "Append a hard deny_always egress rule for URL to the committed project config\n" +
			"and recompile. Unlike allow there is no --once/--global: a hard deny is a\n" +
			"deliberate, committed decision, so it always lands in the project file. With\n" +
			"--reason the explanation is recorded and shown to the agent so it does not try to\n" +
			"escalate around the block.",
		Example: "  # Block a host for this project\n" +
			"  agent-creance deny https://example.com\n" +
			"\n" +
			"  # Block with a recorded rationale\n" +
			"  agent-creance deny https://example.com --reason 'low-quality source'",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeny(cmd.Context(), app, ".", args[0], reason)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "",
		"explanation recorded with the deny (the agent is shown it and told not to escalate)")
	return cmd
}

// runDeny is the testable body: dir and reason are parameters so unit tests can assert
// the rule (and its reason) are persisted to the project file. It delegates to
// runDomainAdd with --deny preset.
func runDeny(ctx context.Context, app *App, dir, rawURL, reason string) error {
	host, path, err := splitURL(rawURL)
	if err != nil {
		return err
	}
	opts := domainAddOpts{deny: true, reason: reason}
	setURLPath(&opts, path)
	return runDomainAdd(ctx, app, dir, host, opts)
}
