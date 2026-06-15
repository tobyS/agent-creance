package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/config"
)

// import.go implements `agent-creance import FILE` — merge an agent-generated (or
// hand-written) config fragment into the project .agent-creance.yaml after the
// engineer reviews it. It is the paste-back half of init's agent-prompt flow: the
// agent writes a fragment listing local ports and stack documentation hosts, the
// engineer inspects the file, then imports it here.
//
// The fragment is strict-validated with the same Parse the compiler uses (an
// unknown key is an error, so a malformed fragment never reaches the config), then
// its egress allow/deny rules and host_services are spliced into the existing file
// with the comment-preserving AppendRule/AppendHostService. The merged result is
// shown and confirmed before anything is written.

func newImportCmd(app *App) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "import FILE",
		Short: "Merge a YAML config fragment into the project config after review",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd.Context(), app, ".", args[0], yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false,
		"skip the confirmation prompt and write the merge (for non-interactive use)")
	return cmd
}

// runImport is the testable body: dir (the project dir, "." in production), the
// fragment file, and --yes are parameters so unit tests can drive every path.
func runImport(ctx context.Context, app *App, dir, file string, yes bool) error {
	fragData, err := app.FS.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	frag, err := config.Parse(fragData)
	if err != nil {
		return fmt.Errorf("invalid config fragment %s: %w", file, err)
	}

	dest := filepath.Join(dir, configFile)
	data, err := app.FS.ReadFile(dest)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("read %s: %w", dest, err)
		}
		data = nil // no project config yet — the merge synthesizes one
	}

	out, changed, err := applyFragment(data, frag)
	if err != nil {
		return err
	}
	if note := ignoredSectionsNote(frag); note != "" {
		fmt.Fprintln(app.Stdout, note)
	}
	if !changed {
		fmt.Fprintf(app.Stdout, "Nothing to import — %s already contains these entries.\n", configFile)
		return nil
	}

	fmt.Fprintf(app.Stdout, "\nResulting %s:\n\n%s\n", configFile, string(out))
	if !yes {
		if !app.Terminal.IsInteractive() {
			fmt.Fprintln(app.Stdout, "Not writing: re-run with --yes to apply these changes non-interactively.")
			return nil
		}
		ok, err := confirm(app, fmt.Sprintf("Write these changes to %s?", configFile))
		if err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if !ok {
			fmt.Fprintln(app.Stdout, "Import cancelled; no changes written.")
			return nil
		}
	}

	if err := app.FS.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}
	if err := writeFileAtomic(app.FS, dest, out, configFilePerm); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	if err := recompile(ctx, app, dir); err != nil {
		return fmt.Errorf("imported config into %s, but recompiling the policy failed: %w", configFile, err)
	}
	fmt.Fprintf(app.Stdout, "✓ Imported config into %s; policy recompiled\n", configFile)
	return nil
}

// applyFragment splices the fragment's egress allow/deny rules and host_services
// into data (nil = no existing config), returning the merged bytes and whether any
// entry was actually new. Each splice preserves the existing file's comments and is
// validated by re-parsing; a duplicate entry is a no-op.
func applyFragment(data []byte, frag *config.Config) (out []byte, changed bool, err error) {
	out = data
	apply := func(o []byte, c bool, e error) error {
		if e != nil {
			return e
		}
		out = o
		if c {
			changed = true
		}
		return nil
	}
	for _, r := range frag.Network.Egress.Allow {
		if err := apply(config.AppendRule(out, config.AllowList, r)); err != nil {
			return nil, false, err
		}
	}
	for _, r := range frag.Network.Egress.DenyAlways {
		if err := apply(config.AppendRule(out, config.DenyList, r)); err != nil {
			return nil, false, err
		}
	}
	for _, hs := range frag.Network.HostServices {
		if err := apply(config.AppendHostService(out, hs)); err != nil {
			return nil, false, err
		}
	}
	return out, changed, nil
}

// ignoredSectionsNote lists any fragment sections import does not merge, so the
// engineer is not surprised that, e.g., an agent: block in the fragment was
// dropped. import deliberately handles only egress rules and host_services.
func ignoredSectionsNote(frag *config.Config) string {
	var ignored []string
	if len(frag.Agent.Command) > 0 || frag.Agent.Workdir != "" {
		ignored = append(ignored, "agent")
	}
	if len(frag.Safehouse.AddDirsRW)+len(frag.Safehouse.AddDirsRO)+len(frag.Safehouse.Enable) > 0 {
		ignored = append(ignored, "safehouse")
	}
	if len(frag.Network.Egress.Generators) > 0 {
		ignored = append(ignored, "generators")
	}
	if len(frag.Env) > 0 {
		ignored = append(ignored, "env")
	}
	if len(frag.Include) > 0 {
		ignored = append(ignored, "include")
	}
	if len(ignored) == 0 {
		return ""
	}
	return "Note: import only merges egress allow/deny rules and host_services; ignored sections: " +
		strings.Join(ignored, ", ") + "."
}
