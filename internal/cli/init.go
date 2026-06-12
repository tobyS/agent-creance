package cli

import (
	"bufio"
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
	var force, noSetup bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter .agent-creance.yaml (detecting package.json / composer.json)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd.Context(), app, ".", force, noSetup)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"overwrite an existing .agent-creance.yaml")
	cmd.Flags().BoolVar(&noSetup, "no-setup", false,
		"scaffold the config only; skip the one-time host-setup check (CI / config-only use)")
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
func runInit(ctx context.Context, app *App, dir string, force, noSetup bool) error {
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
	content := renderConfigTemplate(gens)
	if err := writeFileAtomic(app.FS, dest, []byte(content), configFilePerm); err != nil {
		return fmt.Errorf("init: write %q: %w", dest, err)
	}

	fmt.Fprintf(app.Stdout, "✓ Wrote %s %s\n", configFile, generatorsNote(gens))
	// On the --no-setup path host setup is still pending, so keep pointing at it;
	// otherwise setup is done (already, or just bootstrapped above) and the next
	// step is simply run.
	if noSetup {
		fmt.Fprintln(app.Stdout, "Next: run `agent-creance setup`, then `agent-creance run`.")
	} else {
		fmt.Fprintln(app.Stdout, "Next: run `agent-creance run`.")
	}
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
func confirm(app *App, prompt string) (bool, error) {
	fmt.Fprintf(app.Stdout, "%s [y/N]: ", prompt)
	line, err := bufio.NewReader(app.Stdin).ReadString('\n')
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

// renderConfigTemplate builds the .agent-creance.yaml template, splicing in the
// generators block for the detected manifests. The result is a minimal but valid
// config with commented allow/deny stubs as inline guidance; per docs/design.md it
// never writes a .gitignore block (all runtime state lives out-of-tree).
func renderConfigTemplate(generators []config.Generator) string {
	return fmt.Sprintf(configTemplate, generatorsBlock(generators))
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

// configTemplate is the starter config. The single %s is the generators block
// (rendered by generatorsBlock). Indentation is significant — keep it in sync with
// the schema (internal/config) and the design's example (docs/design.md).
const configTemplate = `# .agent-creance.yaml — agent-creance project config.
# Full schema and guidance: docs/design.md. Manage egress rules interactively
# with ` + "`agent-creance allow`" + ` / ` + "`agent-creance deny`" + `.
agent:
  command: ["claude", "--dangerously-skip-permissions"]
  workdir: .

safehouse:
  add_dirs_rw: [.]

network:
  egress:
%s    # allow:
    #   - host: api.github.com
    #     paths: ["/repos/you/your-project/"]
    #     methods: [GET, POST]
    # deny_always:
    #   - host: w3schools.com
    #     reason: "Known low-quality source. Use MDN or official docs instead."
`
