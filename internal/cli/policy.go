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
		Args:  cobra.NoArgs,
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
			fmt.Fprint(app.Stdout, render.Refresh(res))
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
		Args:  cobra.NoArgs,
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
			fmt.Fprint(app.Stdout, render.Show(compiled))
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
		Args:  cobra.ExactArgs(1),
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
			fmt.Fprint(app.Stdout, render.Explain(compiled, req))
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
