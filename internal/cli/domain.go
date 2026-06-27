package cli

// domain.go implements the `agent-creance domain` command group (AC-0067): a
// noun-verb surface for the egress allow/deny rules that supersedes the bare-URL
// allow/deny verbs. `domain add` exposes the full rule shape as flags — paths,
// methods, mode, deny — and falls back to an interactive prompt for the
// all-paths-vs-specific-paths decision when neither --path nor --all-paths is given.
// `domain remove` deletes a whole rule or one path from it. The legacy allow/deny
// commands are kept as thin aliases that delegate to runDomainAdd (allow.go, deny.go),
// so there is one implementation of the edit.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/config"
)

// domainAddOpts carries the resolved flags for an add. once is set only by the allow
// alias (the domain command itself never writes the session overlay).
type domainAddOpts struct {
	paths    []string
	methods  []string
	mode     string
	allPaths bool
	deny     bool
	reason   string
	global   bool
	once     bool
}

func newDomainCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Add or remove egress allow/deny rules",
	}
	cmd.AddCommand(newDomainAddCmd(app), newDomainRemoveCmd(app))
	return cmd
}

func newDomainAddCmd(app *App) *cobra.Command {
	var opts domainAddOpts
	cmd := &cobra.Command{
		Use:   "add HOST",
		Short: "Add an egress rule for a host (prompts for any choice not given as a flag)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDomainAdd(cmd.Context(), app, ".", args[0], opts)
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&opts.paths, "path", nil, "restrict the rule to this path prefix (repeatable)")
	f.StringArrayVar(&opts.methods, "method", nil, "restrict the rule to this HTTP method (repeatable)")
	f.StringVar(&opts.mode, "mode", "", "enforcement mode: intercept (default) or passthrough")
	f.BoolVar(&opts.allPaths, "all-paths", false, "make a host-wide rule (no paths)")
	f.BoolVar(&opts.deny, "deny", false, "write a deny_always rule instead of an allow")
	f.StringVar(&opts.reason, "reason", "", "explanation recorded with the rule (shown to the agent)")
	f.BoolVar(&opts.global, "global", false, "edit ~/.config/agent-creance.yaml instead of the project config")
	return cmd
}

func newDomainRemoveCmd(app *App) *cobra.Command {
	var path string
	var global bool
	cmd := &cobra.Command{
		Use:   "remove HOST",
		Short: "Remove an egress rule (or one path from it with --path)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDomainRemove(cmd.Context(), app, ".", args[0], path, global)
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "remove only this path from the rule (default: the whole rule)")
	cmd.Flags().BoolVar(&global, "global", false, "edit ~/.config/agent-creance.yaml instead of the project config")
	return cmd
}

// runDomainAdd validates the flag combination, resolves the paths decision (prompting
// when neither --path nor --all-paths was supplied), builds the rule, and appends it via
// the shared recompiling edit pipeline.
func runDomainAdd(ctx context.Context, app *App, dir, host string, opts domainAddOpts) error {
	if opts.once && opts.global {
		return errors.New("cannot combine --once and --global: pick a session overlay or the global config")
	}
	if opts.allPaths && len(opts.paths) > 0 {
		return errors.New("cannot combine --all-paths with --path")
	}
	if opts.deny {
		if opts.mode != "" {
			return errors.New("--mode is not valid with --deny (a deny rule has no enforcement mode)")
		}
		if len(opts.methods) > 0 {
			return errors.New("--method is not valid with --deny (a deny rule is not method-scoped)")
		}
	}
	mode := opts.mode
	if mode != "" && mode != config.ModeIntercept && mode != config.ModePassthrough {
		return fmt.Errorf("unknown --mode %q (want %q or %q)", mode, config.ModeIntercept, config.ModePassthrough)
	}
	if err := config.ValidateHost(host); err != nil {
		return fmt.Errorf("invalid host %q: %w", host, err)
	}

	paths := opts.paths
	allPaths := opts.allPaths
	// A deny defaults to whole-host (matching the legacy `deny HOST` verb), and
	// passthrough is host-granularity only (a raw CONNECT tunnel) — in both cases there
	// is no paths decision to make, so an unscoped add is host-wide and skips the prompt.
	if len(paths) == 0 && !allPaths && (opts.deny || mode == config.ModePassthrough) {
		allPaths = true
	}
	// Explicit-or-prompt: if neither the all-paths nor a specific path was supplied, ask
	// (or fail with a flag hint when there is no terminal). Methods and mode use their
	// safe defaults (any method, intercept) when omitted — see the package doc and the
	// implementation note in the plan.
	if !allPaths && len(paths) == 0 {
		if err := requireInteractive(app, "pass --all-paths or one or more --path values"); err != nil {
			return err
		}
		decidedAll, decidedPaths, err := promptPaths(app)
		if err != nil {
			return err
		}
		allPaths = decidedAll
		paths = decidedPaths
	}

	if mode == config.ModePassthrough && (len(paths) > 0 || len(opts.methods) > 0) {
		return errors.New("mode passthrough cannot carry paths or methods (it is a raw TLS tunnel, host-granularity only)")
	}

	rule := config.Rule{Host: host, Reason: opts.reason}
	if !allPaths && len(paths) > 0 {
		p := append([]string{}, paths...)
		rule.Paths = &p
	}
	if len(opts.methods) > 0 {
		m := make([]string, len(opts.methods))
		for i, x := range opts.methods {
			m[i] = strings.ToUpper(strings.TrimSpace(x))
		}
		if err := config.ValidateMethods(m); err != nil {
			return fmt.Errorf("invalid --method: %w", err)
		}
		rule.Methods = &m
	}
	if mode != "" && mode != config.ModeIntercept {
		rule.Mode = mode
	}

	path, label, err := mutationTarget(app, dir, opts.once, opts.global)
	if err != nil {
		return err
	}
	list, verb := config.AllowList, "allowed"
	if opts.deny {
		list, verb = config.DenyList, "denied"
	}
	return mutateAndRecompile(ctx, app, dir, path, label, list, rule, verb)
}

// promptPaths asks whether to allow all paths or specific ones and, for the latter,
// collects the path prefixes from one free-text line.
func promptPaths(app *App) (allPaths bool, paths []string, err error) {
	choice, err := promptSelect(app, "Allow all paths on this host, or specific paths?",
		[]string{"All paths", "Specific paths"})
	if err != nil {
		return false, nil, err
	}
	if choice == 0 {
		return true, nil, nil
	}
	text, err := promptText(app, "Enter path prefixes (space- or comma-separated), e.g. /repos/ /user/")
	if err != nil {
		return false, nil, err
	}
	fields := strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' })
	if len(fields) == 0 {
		return false, nil, errors.New("no paths entered; pass --all-paths to allow the whole host")
	}
	return false, fields, nil
}

// runDomainRemove removes a rule (or one path from it) from whichever egress list holds
// the host, recompiling so a running proxy picks up the change. A host present in neither
// list is a clear non-zero error (AC-0067), not a silent no-op.
func runDomainRemove(ctx context.Context, app *App, dir, host, path string, global bool) error {
	target, label, err := mutationTarget(app, dir, false /*once*/, global)
	if err != nil {
		return err
	}
	apply := func(src []byte) ([]byte, bool, error) {
		out, changed, rerr := config.RemoveRule(src, config.AllowList, host, path)
		if errors.Is(rerr, config.ErrNotFound) {
			out, changed, rerr = config.RemoveRule(src, config.DenyList, host, path)
		}
		return out, changed, rerr
	}
	subject := host
	if path != "" {
		subject = host + " path " + path
	}
	err = applyAndRecompile(ctx, app, dir, target, label, subject, "removed", apply)
	if errors.Is(err, config.ErrNotFound) {
		return fmt.Errorf("%s is not in %s; nothing to remove", subject, label)
	}
	return err
}
