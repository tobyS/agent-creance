package cli

// credential.go implements the `agent-creance credential` command group (AC-0068d):
// a noun-verb surface for the top-level credentials: block that the proxy resolves
// host-side and injects (AC-0068c). `credential add` registers a name → source
// reference + header shape; `credential list` shows what is configured (never a
// resolved value); `credential remove` deletes an entry. add/remove reuse the shared
// recompiling edit pipeline so a running cage hot-reloads the change; the shape flags
// are UX sugar over the value-template (config/template.go), and --source is only
// syntax-checked here — the secret is resolved at proxy spawn, not at add time.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

func newCredentialCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credential",
		Short: "Manage injected credentials (host-side secrets the agent never sees)",
		Long: "Manage the credentials: block — named, indirected references (op:// / keychain:// /\n" +
			"env://) the proxy resolves host-side and injects into requests to a bound host, so the\n" +
			"caged agent authenticates without ever holding the secret. credential add registers a\n" +
			"credential and its header shape, list shows what is configured (never a value), and\n" +
			"remove deletes one. Bind a credential to a host with 'allow <host> --inject <name>'.",
	}
	cmd.AddCommand(newCredentialAddCmd(app), newCredentialListCmd(app), newCredentialRemoveCmd(app))
	return cmd
}

// credentialAddOpts carries the resolved flags for `credential add`.
type credentialAddOpts struct {
	source   string
	bearer   bool
	token    bool
	raw      bool
	basic    bool
	header   string
	username string
	template string
	global   bool
}

func newCredentialAddCmd(app *App) *cobra.Command {
	var opts credentialAddOpts
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Register a credential (name → source reference + header shape)",
		Long: "Register a credential named NAME that resolves --source (an op:// , keychain:// , or\n" +
			"env:// reference) host-side at run time. The header shape defaults to Bearer; pick\n" +
			"another with --token, --raw (bare token), --basic (with --username), a custom --header\n" +
			"NAME, or a full --template. --source is only syntax-checked here — it is resolved when\n" +
			"the cage starts, and a resolve failure fails closed (HTTP 472), never leaking a value.\n" +
			"--global writes ~/.config instead of the project config; the policy is recompiled so a\n" +
			"running cage hot-reloads.",
		Example: "  # A GitHub token injected as 'Authorization: Bearer <token>'\n" +
			"  agent-creance credential add github --source op://Private/GitHub/token --bearer\n" +
			"\n" +
			"  # A PyPI upload token as HTTP Basic with the __token__ sentinel\n" +
			"  agent-creance credential add pypi --source op://Private/PyPI/token --basic --username __token__\n" +
			"\n" +
			"  # A custom-header API key (bare token in x-api-key)\n" +
			"  agent-creance credential add anthropic --source op://Private/Anthropic/key --header x-api-key --raw",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCredentialAdd(cmd.Context(), app, ".", args[0], opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.source, "source", "", "secret reference: op:// , keychain:// , or env:// (prompted if omitted)")
	f.BoolVar(&opts.bearer, "bearer", false, "inject as 'Bearer <token>' (the default shape)")
	f.BoolVar(&opts.token, "token", false, "inject as 'token <token>' (e.g. gh REST)")
	f.BoolVar(&opts.raw, "raw", false, "inject the bare <token> as the header value")
	f.BoolVar(&opts.basic, "basic", false, "inject as HTTP Basic base64(<username>:<token>) (needs --username)")
	f.StringVar(&opts.header, "header", "", "target header name (default: Authorization)")
	f.StringVar(&opts.username, "username", "", "username sentinel for --basic (e.g. __token__, x-access-token, oauth2)")
	f.StringVar(&opts.template, "template", "", "value-template escape hatch, e.g. 'Bearer {token}' (mutually exclusive with the shape flags)")
	f.BoolVar(&opts.global, "global", false, "edit ~/.config/agent-creance.yaml instead of the project config")
	return cmd
}

// resolveTemplate turns the shape flags into a value-template string. Exactly one of
// the shape flags or --template may be given; none defaults to Bearer.
func resolveTemplate(opts credentialAddOpts) (string, error) {
	n := 0
	for _, b := range []bool{opts.bearer, opts.token, opts.raw, opts.basic, opts.template != ""} {
		if b {
			n++
		}
	}
	if n > 1 {
		return "", errors.New("pick only one header shape: --bearer, --token, --raw, --basic, or --template")
	}
	switch {
	case opts.token:
		return "token {token}", nil
	case opts.raw:
		return "{token}", nil
	case opts.basic:
		return "Basic base64({user}:{token})", nil
	case opts.template != "":
		return opts.template, nil
	default: // --bearer or nothing
		return "Bearer {token}", nil
	}
}

func runCredentialAdd(ctx context.Context, app *App, dir, name string, opts credentialAddOpts) error {
	template, err := resolveTemplate(opts)
	if err != nil {
		return err
	}

	source := opts.source
	if source == "" {
		if err := requireInteractive(app, "pass --source with an op:// , keychain:// , or env:// reference"); err != nil {
			return err
		}
		source, err = promptText(app, "Enter the secret source (op:// , keychain:// , or env:// reference)")
		if err != nil {
			return err
		}
	}
	if err := sysdep.ValidateSecretRefSyntax(source); err != nil {
		return fmt.Errorf("invalid --source %q: want an op:// , keychain:// , or env:// reference", source)
	}
	if opts.basic && opts.username == "" {
		return errors.New("--basic needs --username (the sentinel encoded as base64(<username>:<token>), e.g. __token__)")
	}

	cred := config.Credential{Source: source, Template: template, Header: opts.header, Username: opts.username}

	path, label, err := mutationTarget(app, dir, false /*once*/, opts.global)
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("credential %q", name)
	return applyAndRecompile(ctx, app, dir, path, label, subject, "added",
		func(src []byte) ([]byte, bool, error) { return config.AppendCredential(src, name, cred) })
}

func newCredentialListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured credentials by name, source, and shape (never a value)",
		Long: "List the credentials configured for this project (project + global, merged), showing\n" +
			"each one's name, source reference, target header, and value shape. It never resolves\n" +
			"or prints a secret value — only the reference and the shape, so it is safe to run and\n" +
			"share.",
		Example: "  # Show configured credentials\n" +
			"  agent-creance credential list",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runCredentialList(app, ".")
		},
	}
}

func runCredentialList(app *App, dir string) error {
	cfg, err := config.NewLoader(app.FS, app.Paths).Load(filepath.Join(dir, configFile))
	if err != nil {
		return err
	}
	if len(cfg.Credentials) == 0 {
		fmt.Fprintln(app.Stdout, "no credentials configured (add one with 'agent-creance credential add')")
		return nil
	}

	names := make([]string, 0, len(cfg.Credentials))
	for name := range cfg.Credentials {
		names = append(names, name)
	}
	sort.Strings(names)

	w := tabwriter.NewWriter(app.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSOURCE\tHEADER\tSHAPE\tUSERNAME")
	for _, name := range names {
		c := cfg.Credentials[name]
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, c.Source, c.Header, c.Template, dash(c.Username))
	}
	return w.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func newCredentialRemoveCmd(app *App) *cobra.Command {
	var global bool
	cmd := &cobra.Command{
		Use:     "remove NAME",
		Aliases: []string{"rm"},
		Short:   "Remove a credential (blocked while a rule still injects it)",
		Long: "Remove the credential named NAME. If any egress rule still injects it, the removal is\n" +
			"refused with a pointer to unbind it first (otherwise the policy would fail to compile).\n" +
			"Removing a credential that does not exist is a clear non-zero error. --global edits\n" +
			"~/.config instead of the project config; the policy is recompiled.",
		Example: "  # Remove a credential\n" +
			"  agent-creance credential remove github\n" +
			"\n" +
			"  # Same, using the rm alias, from the global config\n" +
			"  agent-creance credential rm github --global",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCredentialRemove(cmd.Context(), app, ".", args[0], global)
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "edit ~/.config/agent-creance.yaml instead of the project config")
	return cmd
}

func runCredentialRemove(ctx context.Context, app *App, dir, name string, global bool) error {
	// Refuse to strand an inject binding: removing a still-referenced credential would
	// make the next compile fail closed (validateInjectRefs). Check the merged config
	// so a rule in either layer is seen, and give an actionable message instead.
	if cfg, err := config.NewLoader(app.FS, app.Paths).Load(filepath.Join(dir, configFile)); err == nil {
		if host := injectingHost(cfg, name); host != "" {
			return fmt.Errorf("credential %q is still injected by %s; unbind it first (allow %s without --inject, or domain remove %s)",
				name, host, host, host)
		}
	}

	path, label, err := mutationTarget(app, dir, false /*once*/, global)
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("credential %q", name)
	err = applyAndRecompile(ctx, app, dir, path, label, subject, "removed",
		func(src []byte) ([]byte, bool, error) { return config.RemoveCredential(src, name) })
	if errors.Is(err, config.ErrNotFound) {
		return fmt.Errorf("credential %q is not defined in %s; nothing to remove", name, label)
	}
	return err
}

// injectingHost returns the first host whose rule injects credential name, or "".
func injectingHost(cfg *config.Config, name string) string {
	for _, r := range cfg.Network.Egress.Allow {
		if r.Inject == name {
			return r.Host
		}
	}
	for _, r := range cfg.Network.Egress.DenyAlways {
		if r.Inject == name {
			return r.Host
		}
	}
	return ""
}
