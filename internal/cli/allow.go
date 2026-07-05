package cli

import (
	"context"

	"github.com/spf13/cobra"
)

// newAllowCmd implements `agent-creance allow URL` — append a soft-allow rule to the
// egress policy and recompile so a running proxy picks it up without a restart. By
// default it edits the committed project config; --global edits ~/.config and --once
// writes the out-of-tree session overlay that AC-0020 purges on last-agent-exit.
// (docs/design.md "Commands" + "Session-scoped allows".) It is a thin alias over
// `domain add`: the bare-URL argument resolves to a host-wide rule (no path) or a
// single-path rule, and the shared runDomainAdd body performs the edit (AC-0067).
func newAllowCmd(app *App) *cobra.Command {
	var once, global, inCage bool
	var inject string
	cmd := &cobra.Command{
		Use:   "allow URL",
		Short: "Append a soft-allow rule and recompile the policy",
		Long: "Append a soft-allow egress rule for URL and recompile the policy so a running\n" +
			"proxy picks it up without a restart. A bare host allows the whole host; a URL with a\n" +
			"path allows just that path. By default the rule is written to the committed project\n" +
			"config; --global writes ~/.config/agent-creance.yaml instead, and --once writes the\n" +
			"out-of-tree session overlay that is purged when the last agent exits.\n" +
			"\n" +
			"--inject <name> binds a credential (see 'credential add') so the proxy injects its\n" +
			"secret into requests to this host, and the caged agent never sees it; binding an\n" +
			"already-allowed host updates that rule in place. --in-cage instead marks a host whose\n" +
			"auth headers the proxy must never touch, because the agent authenticates with a\n" +
			"credential it holds in-cage.",
		Example: "  # Allow a host for this project (committed config)\n" +
			"  agent-creance allow https://example.com\n" +
			"\n" +
			"  # Allow just for the current session (purged on exit)\n" +
			"  agent-creance allow --once https://example.com/api\n" +
			"\n" +
			"  # Allow globally, for every project on this machine\n" +
			"  agent-creance allow --global https://example.com\n" +
			"\n" +
			"  # Open GitHub GraphQL with a repo-scoped token the agent never sees\n" +
			"  agent-creance allow api.github.com/graphql --inject github\n" +
			"\n" +
			"  # Mark a host in-cage (the agent authenticates itself; proxy stays hands-off)\n" +
			"  agent-creance allow bedrock.us-east-1.amazonaws.com --in-cage",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := domainAddOpts{once: once, global: global, inject: inject, inCage: inCage}
			return runAllowWith(cmd.Context(), app, ".", args[0], opts)
		},
	}
	cmd.Flags().BoolVar(&once, "once", false,
		"write to the session overlay (purged on last-agent-exit), not the committed config")
	cmd.Flags().BoolVar(&global, "global", false,
		"append to ~/.config/agent-creance.yaml instead of the project config")
	cmd.Flags().StringVar(&inject, "inject", "",
		"inject the named credential's secret into requests to this host (see: credential add)")
	cmd.Flags().BoolVar(&inCage, "in-cage", false,
		"mark this host in-cage: the proxy never touches its auth headers")
	return cmd
}

// runAllow is the testable body for a plain allow (no auth axis): dir and the
// once/global flags are parameters — not globals — so unit tests can drive every
// target against the sysdep fakes.
func runAllow(ctx context.Context, app *App, dir, rawURL string, once, global bool) error {
	return runAllowWith(ctx, app, dir, rawURL, domainAddOpts{once: once, global: global})
}

// runAllowWith maps the URL to a domainAddOpts (host-wide for a bare host, single-path
// otherwise), preserving the caller-supplied fields (once/global/inject/inCage), and
// delegates to runDomainAdd.
func runAllowWith(ctx context.Context, app *App, dir, rawURL string, opts domainAddOpts) error {
	host, path, err := splitURL(rawURL)
	if err != nil {
		return err
	}
	setURLPath(&opts, path)
	return runDomainAdd(ctx, app, dir, host, opts)
}
