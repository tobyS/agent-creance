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

// setURLPath translates the path component of an allow/deny URL into a domainAddOpts
// paths decision: a bare host (empty or "/" path) becomes a host-wide rule (allPaths),
// while a host+path scopes the rule to that single path prefix. This is how the legacy
// allow/deny aliases resolve the paths decision without an interactive prompt.
func setURLPath(opts *domainAddOpts, path string) {
	if path != "" && path != "/" {
		opts.paths = []string{path}
		return
	}
	opts.allPaths = true
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
// policy so a running proxy hot-reloads. An identical rule already present is a reported
// no-op — no write, no recompile. It is a thin wrapper over applyAndRecompile that
// plugs in config.AppendRule and the rule's human label.
func mutateAndRecompile(ctx context.Context, app *App, dir, path, label string, list config.RuleList, rule config.Rule, verb string) error {
	return applyAndRecompile(ctx, app, dir, path, label, ruleLabel(rule), verb,
		func(src []byte) ([]byte, bool, error) { return config.AppendRule(src, list, rule) })
}

// applyAndRecompile is the read → append → atomic-write → recompile skeleton shared by
// the config-editing commands (allow/deny via mutateAndRecompile, and include). apply
// performs the comment-preserving edit and reports whether anything changed; subject is
// the human label of the thing being added (a rule label or an include path) and verb
// the past-tense action ("allowed"/"denied"/"included"). A no-op (changed=false) is
// reported without writing or recompiling. The recompile forces a policy.json rewrite
// (the edit changed the compiler's input hash) so a running proxy hot-reloads.
func applyAndRecompile(ctx context.Context, app *App, dir, path, fileLabel, subject, verb string, apply func(src []byte) (out []byte, changed bool, err error)) error {
	return withConfigLock(app, path, func() error {
		// Read fresh under the lock: config.AppendRule re-validates against exactly these
		// bytes, so a rule appended by a concurrent run that already released the lock is
		// seen here and preserved rather than clobbered.
		data, err := app.FS.ReadFile(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("read %s: %w", path, err)
			}
			data = nil // absent target (e.g. first --once, or no global yet) — create it
		}

		out, changed, err := apply(data)
		if err != nil {
			return err
		}
		if !changed {
			fmt.Fprintf(app.Stdout, "%s is already %s in %s; nothing to do\n", subject, verb, fileLabel)
			return nil
		}

		if err := app.FS.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := writeFileAtomic(app.FS, path, out, configFilePerm); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}

		if err := recompile(ctx, app, dir); err != nil {
			return fmt.Errorf("%s %s in %s, but recompiling the policy failed: %w", verb, subject, fileLabel, err)
		}

		fmt.Fprintf(app.Stdout, "%s %s %s in %s; policy recompiled\n", app.OutStyle.OK("✓"), verb, subject, fileLabel)
		return nil
	})
}

// withConfigLock serializes a config read-modify-write against concurrent
// allow/deny/import runs (and a developer's editor) by holding an exclusive advisory
// lock for the duration of fn. The lock is a separate file in the out-of-tree cache
// (state.ConfigLock), keyed by the target path — deliberately not the config file
// itself, because the config is replaced via temp+rename and a rename swaps the inode
// out from under an flock, silently breaking exclusion. fn still writes the config
// atomically; the lock only guarantees no two mutations interleave and lose a rule
// (the dangerous case being a dropped deny_always, AC-0059 F9).
func withConfigLock(app *App, target string, fn func() error) error {
	lockPath, err := state.New(app.Paths).ConfigLock(target)
	if err != nil {
		return err
	}
	if err := app.FS.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(lockPath), err)
	}
	lf, err := app.Flock.Acquire(lockPath)
	if err != nil {
		return fmt.Errorf("lock %s: %w", lockPath, err)
	}
	defer func() { _ = lf.Release() }()
	return fn()
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
