package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/policy/compile"
	"github.com/tobyS/agent-creance/internal/state"
)

// mutate.go holds the machinery shared by `allow` and `deny` (AC-0030): turning a URL
// argument into a rule, resolving which config file to write, appending the rule while
// preserving the file's comments (config.AppendRule), and recompiling so a running
// proxy hot-reloads the change. The commands themselves (allow.go, deny.go) are thin
// wrappers that pick the list and the target.

// splitURL normalises an allow/deny/explain argument into its host and path. A bare
// host/path with no scheme parses correctly once https:// is prepended; the path is
// returned verbatim (callers decide what an empty path means).
func splitURL(raw string) (host, path string, err error) {
	s := raw
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", "", fmt.Errorf("parse URL %q: %w", raw, err)
	}
	host = u.Hostname()
	if host == "" {
		return "", "", fmt.Errorf("URL %q has no host", raw)
	}
	return host, u.Path, nil
}

// ruleFromURL builds an egress Rule from a command-line URL. A bare host (empty or "/"
// path) becomes a whole-host rule (no paths); a host+path scopes the rule to that path
// prefix. reason is recorded for deny rules and left empty for allow. v0.1 has no
// --method flag, so a rule never constrains methods.
func ruleFromURL(raw, reason string) (config.Rule, error) {
	host, path, err := splitURL(raw)
	if err != nil {
		return config.Rule{}, err
	}
	rule := config.Rule{Host: host, Reason: reason}
	if path != "" && path != "/" {
		p := []string{path}
		rule.Paths = &p
	}
	return rule, nil
}

// mutationTarget resolves the file a mutation writes to and a human label for it.
// --once targets the out-of-tree session overlay (purged on last-agent-exit by
// AC-0020); --global the implicit global config; otherwise the project file.
func mutationTarget(app *App, dir string, once, global bool) (path, label string, err error) {
	switch {
	case once:
		layout, err := state.New(app.Paths).Resolve(dir)
		if err != nil {
			return "", "", err
		}
		return layout.SessionOverlay(), "the session overlay", nil
	case global:
		p, err := config.NewLoader(app.FS, app.Paths).GlobalPath()
		if err != nil {
			return "", "", err
		}
		return p, p, nil
	default:
		return filepath.Join(dir, configFile), configFile, nil
	}
}

// mutateAndRecompile appends rule to the list in the file at path, then recompiles the
// policy so a running proxy hot-reloads (the rule changes the compiler's input hash,
// forcing a policy.json rewrite). An identical rule already present is a reported
// no-op — no write, no recompile.
func mutateAndRecompile(ctx context.Context, app *App, dir, path, label string, list config.RuleList, rule config.Rule, verb string) error {
	data, err := app.FS.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("read %s: %w", path, err)
		}
		data = nil // absent target (e.g. first --once, or no global yet) — create it
	}

	out, changed, err := config.AppendRule(data, list, rule)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(app.Stdout, "%s is already %s in %s; nothing to do\n", ruleLabel(rule), verb, label)
		return nil
	}

	if err := app.FS.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := writeFileAtomic(app.FS, path, out, configFilePerm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	if err := recompile(ctx, app, dir); err != nil {
		return fmt.Errorf("%s %s in %s, but recompiling the policy failed: %w", verb, ruleLabel(rule), label, err)
	}

	fmt.Fprintf(app.Stdout, "✓ %s %s in %s; policy recompiled\n", verb, ruleLabel(rule), label)
	return nil
}

// recompile rebuilds the project's policy.json. The compiler is idempotent and the
// mutation changed the input hash, so this rewrites the artifact (advancing its mtime)
// and the enforcer's mtime poll reloads it within ~1s.
func recompile(ctx context.Context, app *App, dir string) error {
	compiler, err := compile.New(app.FS, app.Paths, app.Clock, app.HTTP, nil /*silent*/)
	if err != nil {
		return err
	}
	_, err = compiler.Compile(ctx, dir)
	return err
}

// ruleLabel renders host[/path] for human-facing messages.
func ruleLabel(rule config.Rule) string {
	if rule.Paths != nil && len(*rule.Paths) > 0 {
		return rule.Host + (*rule.Paths)[0]
	}
	return rule.Host
}
