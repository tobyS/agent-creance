package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/generator"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// newInitCmd implements `agent-creance init` — the onboarding scaffold that writes
// a commented .agent-creance.yaml template into the project and pre-populates the
// network.egress.generators: list from detected package.json / composer.json. It
// refuses to clobber an existing config unless --force is given. It is pure
// filesystem work over app.FS — no external tools, no new sysdep seam.
// (docs/design.md "Commands".)
func newInitCmd(app *App) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter .agent-creance.yaml (detecting package.json / composer.json)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd.Context(), app, ".", force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"overwrite an existing .agent-creance.yaml")
	return cmd
}

// runInit is the testable body of the init command: dir is the project directory
// ("." in production), taken as a parameter so unit tests can drive it against the
// sysdep fakes. force is taken as a parameter (rather than read from a global) for
// the same reason — it lets the tests exercise the refuse/overwrite branches.
func runInit(_ context.Context, app *App, dir string, force bool) error {
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
	fmt.Fprintln(app.Stdout, "Next: run `agent-creance setup`, then `agent-creance run`.")
	return nil
}

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
