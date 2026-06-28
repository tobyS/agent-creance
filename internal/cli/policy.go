package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/policy"
	"github.com/tobyS/agent-creance/internal/policy/compile"
	"github.com/tobyS/agent-creance/internal/policy/render"
)

// newPolicyCmd is the `policy` parent: it groups the read-only visibility
// subcommands (show, explain). It has no RunE of its own, so invoking it bare
// prints the subcommand help — cobra's default for a command with children.
func newPolicyCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Inspect the resolved egress policy",
		Long: "Inspect the egress policy that the cage will enforce. Subcommands compile the\n" +
			"project's effective policy on demand and let you dump it (show), test a single URL\n" +
			"against it (explain), or force a re-fetch of generator metadata (refresh). None of\n" +
			"them require a running cage.",
	}
	cmd.AddCommand(newPolicyShowCmd(app), newPolicyExplainCmd(app), newPolicyRefreshCmd(app))
	return cmd
}

// newPolicyRefreshCmd implements `policy refresh`: force a re-fetch of generator
// registry metadata (invalidating this project's per-package cache and the generator
// output cache) and recompile the policy, regardless of the 30-day refresh window or
// the input-hash cache. It reports what was refreshed and exits 0; --json emits the
// structured report. It does not require the cage to be running.
func newPolicyRefreshCmd(app *App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Force a re-fetch of generator metadata and recompile the policy",
		Long: "Force a re-fetch of generator registry metadata — invalidating this project's\n" +
			"per-package cache and the generator output cache — and recompile the policy,\n" +
			"ignoring the 30-day refresh window and the input-hash cache. Reports what was\n" +
			"refreshed and exits 0. --json emits the structured report.",
		Example: "  # Re-fetch generator metadata and recompile\n" +
			"  agent-creance policy refresh",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			compiler, err := compile.New(app.FS, app.Paths, app.Clock, app.HTTP, nil /*silent*/)
			if err != nil {
				return err
			}
			res, err := compiler.Refresh(cmd.Context(), ".")
			if err != nil {
				return err
			}
			if asJSON {
				out, err := render.RefreshJSON(res)
				if err != nil {
					return err
				}
				fmt.Fprint(app.Stdout, out)
				return nil
			}
			fmt.Fprint(app.Stdout, render.Refresh(res, app.OutStyle))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the refresh report as JSON")
	return cmd
}

// newPolicyShowCmd implements `policy show`: compile the project's policy on demand
// (cached) and dump it with per-rule source annotations and passthrough/lower-trust
// flags. --json re-emits the compiled artifact verbatim.
func newPolicyShowCmd(app *App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Dump the fully-resolved policy with rule sources",
		Long: "Compile the project's effective policy on demand (cached) and dump every rule\n" +
			"with its source annotation and passthrough / lower-trust flags, so you can see what\n" +
			"the cage will allow and where each rule came from. --json re-emits the compiled\n" +
			"artifact verbatim.",
		Example: "  # Dump the resolved policy\n" +
			"  agent-creance policy show\n" +
			"\n" +
			"  # Machine-readable output\n" +
			"  agent-creance policy show --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			compiled, err := resolvePolicy(cmd.Context(), app, ".")
			if err != nil {
				return err
			}
			if asJSON {
				out, err := render.ShowJSON(compiled)
				if err != nil {
					return err
				}
				fmt.Fprint(app.Stdout, out)
				return nil
			}
			fmt.Fprint(app.Stdout, render.Show(compiled, app.OutStyle))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the compiled policy as JSON")
	return cmd
}

// newPolicyExplainCmd implements `policy explain URL`: parse the URL into a request,
// run the shared matcher, and report the decision + matching rule. A URL carries no
// HTTP method, so --method (default GET) supplies the one the matcher needs.
func newPolicyExplainCmd(app *App) *cobra.Command {
	var (
		asJSON bool
		method string
	)
	cmd := &cobra.Command{
		Use:   "explain URL",
		Short: "Show which rule (if any) decides a given URL",
		Long: "Evaluate URL against the compiled policy and report the decision (allow /\n" +
			"soft-deny / hard-deny) and the matching rule, so you can debug why a request would\n" +
			"be let through or blocked. A URL carries no HTTP method, so --method (default GET)\n" +
			"supplies the one the matcher evaluates. --json emits the explanation as JSON.",
		Example: "  # Explain how a URL would be decided\n" +
			"  agent-creance policy explain https://api.github.com/repos/foo/bar\n" +
			"\n" +
			"  # Evaluate against a specific method\n" +
			"  agent-creance policy explain --method POST https://api.example.com",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := requestFromURL(args[0], method)
			if err != nil {
				return err
			}
			compiled, err := resolvePolicy(cmd.Context(), app, ".")
			if err != nil {
				return err
			}
			if asJSON {
				out, err := render.ExplainJSON(compiled, req)
				if err != nil {
					return err
				}
				fmt.Fprint(app.Stdout, out)
				return nil
			}
			fmt.Fprint(app.Stdout, render.Explain(compiled, req, app.OutStyle))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the explanation as JSON")
	cmd.Flags().StringVar(&method, "method", "GET", "HTTP method to evaluate the URL against")
	return cmd
}

// resolvePolicy compiles dir's effective policy on demand (the compiler is cached
// and idempotent — a cache hit makes zero network calls) and reads the resulting
// artifact back into memory. Re-reading the file covers the cache-hit and
// freshly-written paths uniformly.
func resolvePolicy(ctx context.Context, app *App, dir string) (policy.Compiled, error) {
	compiler, err := compile.New(app.FS, app.Paths, app.Clock, app.HTTP, nil /*silent*/)
	if err != nil {
		return policy.Compiled{}, err
	}
	result, err := compiler.Compile(ctx, dir)
	if err != nil {
		return policy.Compiled{}, err
	}
	data, err := app.FS.ReadFile(result.PolicyPath)
	if err != nil {
		return policy.Compiled{}, fmt.Errorf("read compiled policy: %w", err)
	}
	var compiled policy.Compiled
	if err := json.Unmarshal(data, &compiled); err != nil {
		return policy.Compiled{}, fmt.Errorf("parse compiled policy %s: %w", result.PolicyPath, err)
	}
	return compiled, nil
}

// requestFromURL parses an explain argument into a matcher request. It shares URL
// normalisation with the allow/deny commands via splitURL, but unlike a rule a
// concrete request needs a path, so an empty path normalises to "/".
func requestFromURL(raw, method string) (policy.Request, error) {
	host, path, err := splitURL(raw)
	if err != nil {
		return policy.Request{}, err
	}
	if path == "" {
		path = "/"
	}
	return policy.Request{Host: host, Path: path, Method: method}, nil
}
