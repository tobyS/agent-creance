package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/generator"
	"github.com/tobyS/agent-creance/internal/gitremote"
	"github.com/tobyS/agent-creance/internal/setupcheck"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// newInitCmd implements `agent-creance init` — the onboarding scaffold that writes
// a commented .agent-creance.yaml template into the project and pre-populates the
// network.egress.generators: list from detected package.json / composer.json. It
// refuses to clobber an existing config unless --force is given. It is pure
// filesystem work over app.FS — no external tools, no new sysdep seam.
// (docs/design.md "Commands".)
func newInitCmd(app *App) *cobra.Command {
	var force, noSetup, gitPush bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter .agent-creance.yaml (detecting package.json / composer.json)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd.Context(), app, ".", force, noSetup, gitPush)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"overwrite an existing .agent-creance.yaml")
	cmd.Flags().BoolVar(&noSetup, "no-setup", false,
		"scaffold the config only; skip the one-time host-setup check (CI / config-only use)")
	cmd.Flags().BoolVar(&gitPush, "git-push", false,
		"allow the agent to push to the project's git remotes (default: read-only; skips the push prompt)")
	return cmd
}

// runInit is the testable body of the init command: dir is the project directory
// ("." in production), taken as a parameter so unit tests can drive it against the
// sysdep fakes. force and noSetup are taken as parameters (rather than read from
// globals) for the same reason — it lets the tests exercise every branch.
//
// The host-setup gate runs FIRST (before the clobber guard and the write): onboarding
// is all-or-nothing, so a declined prompt or a failed setup must abort before any
// config is written (re-running init then retries cleanly, since both setup and the
// scaffold are idempotent). --no-setup skips the gate entirely for config-only use.
func runInit(ctx context.Context, app *App, dir string, force, noSetup, gitPush bool) error {
	if !noSetup {
		if err := ensureHostSetup(ctx, app); err != nil {
			return err // abort before writing any config
		}
	}

	dest := filepath.Join(dir, configFile)

	// Refuse-if-exists, unless --force: never silently clobber a hand-authored config.
	switch _, err := app.FS.Stat(dest); {
	case err == nil:
		if !force {
			return fmt.Errorf("%s already exists (use --force to overwrite)", configFile)
		}
	case errors.Is(err, fs.ErrNotExist):
		// fresh project — fall through to write
	default:
		return fmt.Errorf("init: stat %q: %w", dest, err)
	}

	gens := scanGenerators(app.FS, dir)

	// Auto-allowlist the project's own git remotes (AC-0055). This runs regardless
	// of TTY — the rules are static config — but whether push is granted is an
	// init-time choice: --git-push presets it; otherwise an interactive run prompts
	// and a non-interactive run defaults to the safe read-only option.
	remotes, err := gitremote.Detect(app.FS, dir)
	if err != nil {
		return fmt.Errorf("init: read git remotes: %w", err)
	}
	allowPush := gitPush
	if !gitPush && len(remotes) > 0 && app.Terminal.IsInteractive() {
		ok, err := confirm(app, "Allow the agent to push to your git remote(s)? (write access)")
		if err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		allowPush = ok
	}
	git := buildGitRemoteRules(remotes, allowPush)

	// On an interactive terminal, offer to seed the config from the project's
	// Claude Code settings and from static port detection. Non-interactive runs
	// skip all of this and scaffold exactly as before.
	var (
		allow []config.Rule
		ports []config.HostService
	)
	if app.Terminal.IsInteractive() {
		a, p, err := gatherImports(app, dir)
		if err != nil {
			return err
		}
		allow, ports = a, p
	}

	content := renderConfigTemplate(gens, git.Allow, allow, ports, git.Deny)

	// When an import step or git-remote detection contributed entries, show the
	// result and confirm before writing — but only interactively (a non-interactive
	// confirm would read EOF as "no" and refuse to write). The engineer reviews
	// exactly what lands in the allowlist.
	if app.Terminal.IsInteractive() && (len(allow) > 0 || len(ports) > 0 || len(git.Allow) > 0 || len(git.Deny) > 0) {
		fmt.Fprintf(app.Stdout, "\nResulting %s:\n\n%s\n", configFile, content)
		ok, err := confirm(app, "Write this configuration?")
		if err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if !ok {
			fmt.Fprintf(app.Stdout, "%s not written; re-run `agent-creance init` to try again.\n", configFile)
			return nil
		}
	}

	if err := writeFileAtomic(app.FS, dest, []byte(content), configFilePerm); err != nil {
		return fmt.Errorf("init: write %q: %w", dest, err)
	}

	fmt.Fprintf(app.Stdout, "✓ Wrote %s %s\n", configFile, generatorsNote(gens))
	reportGitRemotes(app, len(remotes), allowPush, git)
	// On the --no-setup path host setup is still pending, so keep pointing at it;
	// otherwise setup is done (already, or just bootstrapped above) and the next
	// step is simply run.
	if noSetup {
		fmt.Fprintln(app.Stdout, "Next: run `agent-creance setup`, then `agent-creance run`.")
	} else {
		fmt.Fprintln(app.Stdout, "Next: run `agent-creance run`.")
	}

	// Finally, offer the agent prompt for the config that can't be inferred
	// statically (stack documentation hosts and any remaining ports).
	maybeOfferAgentPrompt(app)
	return nil
}

// ensureHostSetup is init's onboarding gate. It runs the cheap, no-sudo
// setupcheck.Verify (the same gate run uses); on StatusOK it short-circuits with
// no prompt and no side effects. When host setup is missing it either drives the
// full setup (interactive: explain + confirm, then reuse runSetup so the CA/skill
// messages can't drift from `agent-creance setup`) or refuses with an actionable
// instruction (non-interactive, or keychain locked). Every non-OK outcome that
// can't proceed returns a non-nil error so init aborts before writing the config.
func ensureHostSetup(ctx context.Context, app *App) error {
	res, err := setupcheck.Verify(app.Keychain, app.FS, app.Paths)
	if err != nil {
		return fmt.Errorf("verify setup: %w", err)
	}
	switch {
	case res.OK():
		return nil // already set up — fast path: no prompt, no sudo, no side effects
	case res.Status == setupcheck.StatusKeychainLocked:
		// Can't even read setup state; surface the unlock instruction and abort.
		fmt.Fprintln(app.Stdout, res.Message())
		return fmt.Errorf("setup incomplete")
	}

	// CA not trusted or skill missing — host setup must run before the config is written.
	if !app.Terminal.IsInteractive() {
		// No TTY to confirm on: never silently raise a sudo/keychain dialog. Print the
		// actionable instruction (run's refusal style) plus the config-only escape hatch.
		fmt.Fprintln(app.Stdout, res.Message())
		fmt.Fprintln(app.Stdout, msgNoSetupHint)
		return fmt.Errorf("setup incomplete")
	}

	fmt.Fprintln(app.Stdout, msgInitNeedsSetup)
	ok, err := confirm(app, "Run host setup now?")
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if !ok {
		return fmt.Errorf("host setup declined; %s not written "+
			"(re-run `agent-creance init` when ready)", configFile)
	}
	// Reuse the setup orchestration verbatim (full CA + skill + global baseline);
	// its actionable error surfaces on failure, and init aborts without writing
	// the config.
	if err := runSetup(ctx, app, false, false, false); err != nil {
		return err
	}
	return nil
}

// confirm prints prompt to app.Stdout and reads a single line from app.Stdin,
// returning true only for an explicit yes (y / yes, case-insensitive). A closed or
// empty input (io.EOF) reads as no, so a stray non-interactive call defaults to the
// safe, non-destructive answer. Only a genuine read failure returns an error.
//
// It reads exactly up to the newline (not via a buffered reader) so several
// sequential confirm calls can share one app.Stdin without an earlier call
// swallowing later answers into a discarded buffer.
func confirm(app *App, prompt string) (bool, error) {
	fmt.Fprintf(app.Stdout, "%s [y/N]: ", prompt)
	line, err := readLine(app.Stdin)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// readLine reads one line (without the trailing newline) from r, consuming exactly
// the bytes up to and including the newline and no more — so the remainder stays
// available for the next read. It returns any accumulated bytes alongside a
// terminal error (e.g. io.EOF on the last, newline-less line).
func readLine(r io.Reader) (string, error) {
	var b []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return string(b), nil
			}
			b = append(b, buf[0])
		}
		if err != nil {
			return string(b), err
		}
	}
}

// msgInitNeedsSetup explains, right before the confirm prompt, that the one-time
// host setup hasn't run yet and what init is about to do — so the keychain dialog
// that setup raises next reads as expected. Concrete tone matching setup's messages.
const msgInitNeedsSetup = `This host hasn't completed the one-time agent-creance setup yet. Before writing
the project config, init needs to trust the mitmproxy CA (used to filter the
cage's network egress) and install the agent-creance Claude Code skill. This is
the same work ` + "`agent-creance setup`" + ` does, and it runs only once per machine.`

// msgNoSetupHint is the non-interactive escape hatch, printed under the refusal so
// a CI/scripted caller knows how to scaffold the config without host setup.
const msgNoSetupHint = "To scaffold the config without host setup (e.g. in CI), re-run with " +
	"`agent-creance init --no-setup`."

// configFilePerm is the mode for the human-authored, in-tree project config.
const configFilePerm fs.FileMode = 0o644

// scanDepth bounds the init manifest scan: a manifest at the project root is depth 0,
// one directory down is depth 1, two down is depth 2 — the deepest a standard monorepo
// puts a package (apps/web/package.json, packages/ui/package.json). Anything deeper is
// skipped, as are the generators' own installed-dependency directories.
const scanDepth = 2

// scanGenerators discovers each package's manifest under dir, bounded to scanDepth
// directory levels, and returns one config.Generator per detected manifest (object
// form: the path is filled, relative to dir). It does not descend into a generator's
// installed-dependency directory (node_modules/, vendor/) — those hold installed
// dependencies, not monorepo packages — nor into symlinked or dot-prefixed directories.
// The recognised manifest filenames and the dependency-dir skip-set both come from the
// generator metadata, so a new ecosystem generator extends both with no change here.
// Results are ordered by (depth, path) for deterministic output, root manifests first.
func scanGenerators(fsys sysdep.FileSystem, dir string) []config.Generator {
	byFile := map[string]string{} // manifest filename → generator type
	skip := map[string]bool{}     // installed-dependency dir names to skip
	typeOrder := map[string]int{} // generator type → registry order (for stable sort)
	for i, m := range generator.All() {
		byFile[m.ManifestFile] = m.Type
		typeOrder[m.Type] = i
		for _, d := range m.DependencyDirs {
			skip[d] = true
		}
	}

	var found []config.Generator
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		entries, err := fsys.ReadDir(filepath.Join(dir, rel))
		if err != nil {
			return // absent or unreadable directory contributes nothing
		}
		for _, e := range entries {
			name := e.Name()
			if e.Type()&fs.ModeSymlink != 0 {
				continue // never follow symlinks (cycle safety)
			}
			if e.IsDir() {
				if depth >= scanDepth || skip[name] || strings.HasPrefix(name, ".") {
					continue
				}
				walk(filepath.Join(rel, name), depth+1)
				continue
			}
			if typ, ok := byFile[name]; ok {
				found = append(found, config.Generator{Type: typ, Path: filepath.Join(rel, name)})
			}
		}
	}
	walk("", 0)

	// Order root manifests first (by depth), then by generator type's registry order
	// (keeping the conventional package_json-before-composer_json grouping), then by
	// path — fully deterministic for stable golden output.
	sort.Slice(found, func(i, j int) bool {
		di, dj := pathDepth(found[i].Path), pathDepth(found[j].Path)
		if di != dj {
			return di < dj
		}
		if ti, tj := typeOrder[found[i].Type], typeOrder[found[j].Type]; ti != tj {
			return ti < tj
		}
		return found[i].Path < found[j].Path
	})
	return found
}

// pathDepth is the number of directory levels in a relative manifest path (root
// manifest → 0), used to order scan results root-first.
func pathDepth(p string) int {
	if filepath.Dir(p) == "." {
		return 0
	}
	return strings.Count(filepath.ToSlash(p), "/")
}

// generatorsNote summarises the detection result for the success line.
func generatorsNote(gens []config.Generator) string {
	if len(gens) == 0 {
		return "(no manifests detected — generators left commented)"
	}
	paths := make([]string, len(gens))
	for i, g := range gens {
		paths[i] = g.Path
	}
	return fmt.Sprintf("(%d manifest(s): %s)", len(gens), strings.Join(paths, ", "))
}

// writeFileAtomic writes data to name via a temp file + rename, so a crash mid-write
// never leaves a torn config — the same idiom as setup's writeSkillIfChanged.
func writeFileAtomic(fsys sysdep.FileSystem, name string, data []byte, perm fs.FileMode) error {
	tmp := name + ".tmp"
	if err := fsys.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := fsys.Rename(tmp, name); err != nil {
		_ = fsys.Remove(tmp) // best-effort cleanup of the orphaned temp file
		return err
	}
	return nil
}

// renderConfigTemplate builds the .agent-creance.yaml template: the generators
// block for the detected manifests, the project's git-remote allow entries
// (gitAllow) and any read-only push-block deny entries (gitDeny), plus any imported
// host_services (ports) and allow rules. With no git remotes and no imports the
// output is the original minimal scaffold with commented allow/deny stubs —
// byte-for-byte unchanged, so a manifest-free non-interactive init behaves exactly
// as before. Per docs/design.md it never writes a .gitignore block (all runtime
// state lives out-of-tree).
func renderConfigTemplate(generators []config.Generator, gitAllow, allow []config.Rule, ports []config.HostService, gitDeny []config.Rule) string {
	var b strings.Builder
	b.WriteString(configHeader)
	if len(ports) > 0 {
		b.WriteString("  host_services:\n")
		for _, hs := range ports {
			for _, line := range config.RenderHostService(hs, 4) {
				b.WriteString(line + "\n")
			}
		}
	}
	b.WriteString("  egress:\n")
	b.WriteString(generatorsBlock(generators))

	if len(gitAllow) > 0 || len(allow) > 0 {
		b.WriteString("    allow:\n")
		if len(gitAllow) > 0 {
			b.WriteString("      # Project git remotes — repo, API, and content hosts (agent-creance init)\n")
			for _, r := range gitAllow {
				for _, line := range config.RenderRule(r, 6) {
					b.WriteString(line + "\n")
				}
			}
		}
		if len(allow) > 0 {
			if len(gitAllow) > 0 {
				b.WriteString("      # Imported from project settings\n")
			}
			for _, r := range allow {
				for _, line := range config.RenderRule(r, 6) {
					b.WriteString(line + "\n")
				}
			}
		}
	} else {
		b.WriteString(commentedAllowStub)
	}

	if len(gitDeny) > 0 {
		b.WriteString("    deny_always:\n")
		b.WriteString("      # Read-only git access — block push (git-receive-pack) to project remotes\n")
		for _, r := range gitDeny {
			for _, line := range config.RenderRule(r, 6) {
				b.WriteString(line + "\n")
			}
		}
	} else {
		b.WriteString(commentedDenyStub)
	}
	return b.String()
}

// reportGitRemotes prints init's summary of what it added for the project's git
// remotes: how many were allowlisted, whether push was granted, and any caveats
// (non-HTTPS transport, uninferable companion hosts, unparseable remotes). A project
// with no remotes prints nothing, preserving the original output.
func reportGitRemotes(app *App, n int, allowPush bool, git gitRemoteResult) {
	if n > 0 && len(git.Allow) > 0 {
		access := "read-only — re-run with --git-push to allow push"
		if allowPush {
			access = "push allowed"
		}
		fmt.Fprintf(app.Stdout, "  Git remotes: allowlisted %d remote(s) (%s).\n", n, access)
	}
	for _, note := range git.Notes {
		fmt.Fprintf(app.Stdout, "  Note: %s\n", note)
	}
}

// generatorsBlock renders the network.egress.generators: region: a real list of
// object-form entries (type + path) when manifests were detected, otherwise a
// commented placeholder showing both forms so the user can uncomment the one(s) they
// want. Every detected entry is written parameterized (explicit path) so the form is
// uniform whether the repo is single-package or a monorepo.
func generatorsBlock(generators []config.Generator) string {
	if len(generators) == 0 {
		return "    # generators:\n" +
			"    #   - package_json                 # = ./package.json\n" +
			"    #   - type: composer_json\n" +
			"    #     path: services/api/composer.json\n"
	}
	var b strings.Builder
	b.WriteString("    generators:\n")
	for _, g := range generators {
		fmt.Fprintf(&b, "      - type: %s\n", g.Type)
		fmt.Fprintf(&b, "        path: %s\n", g.Path)
	}
	return b.String()
}

// The starter config is assembled from these pieces by renderConfigTemplate.
// Indentation is significant — keep it in sync with the schema (internal/config)
// and the design's example (docs/design.md). configHeader ends at "network:\n" so
// an optional host_services block, then "  egress:\n", follow it.
const configHeader = `# .agent-creance.yaml — agent-creance project config.
# Full schema and guidance: docs/design.md. Manage egress rules interactively
# with ` + "`agent-creance allow`" + ` / ` + "`agent-creance deny`" + `.
agent:
  command: ["claude", "--dangerously-skip-permissions"]
  workdir: .

safehouse:
  add_dirs_rw: [.]

network:
`

// commentedAllowStub is the inline allow: guidance emitted when no rules were
// imported (so a fresh scaffold shows the user how to add one).
const commentedAllowStub = `    # allow:
    #   - host: api.github.com
    #     paths: ["/repos/you/your-project/"]
    #     methods: [GET, POST]
`

// commentedDenyStub is the always-present deny_always: guidance.
const commentedDenyStub = `    # deny_always:
    #   - host: w3schools.com
    #     reason: "Known low-quality source. Use MDN or official docs instead."
`
