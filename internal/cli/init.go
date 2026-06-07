package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

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

	gens := detectGenerators(app.FS, dir)
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

// detectGenerators returns the generator names whose manifests are present in dir,
// in a deterministic order (package_json before composer_json). Detection is purely
// presence-based — init lists the generators; running them (reading the manifest's
// dependencies) is the generators' job at policy-compile time.
func detectGenerators(fsys sysdep.FileSystem, dir string) []string {
	var gens []string
	for _, m := range []struct {
		manifest string
		name     string
	}{
		{"package.json", generator.GeneratorPackageJSON},
		{"composer.json", generator.GeneratorComposerJSON},
	} {
		if _, err := fsys.Stat(filepath.Join(dir, m.manifest)); err == nil {
			gens = append(gens, m.name)
		}
	}
	return gens
}

// generatorsNote summarises the detection result for the success line.
func generatorsNote(gens []string) string {
	if len(gens) == 0 {
		return "(no manifests detected — generators left commented)"
	}
	return fmt.Sprintf("(generators: %s)", strings.Join(gens, ", "))
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
func renderConfigTemplate(generators []string) string {
	return fmt.Sprintf(configTemplate, generatorsBlock(generators))
}

// generatorsBlock renders the network.egress.generators: region: a real list when
// manifests were detected, otherwise a commented placeholder listing the available
// names so the user can uncomment the one(s) they want.
func generatorsBlock(generators []string) string {
	if len(generators) == 0 {
		return "    # generators:\n" +
			"    #   - package_json\n" +
			"    #   - composer_json\n"
	}
	var b strings.Builder
	b.WriteString("    generators:\n")
	for _, g := range generators {
		fmt.Fprintf(&b, "      - %s\n", g)
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
