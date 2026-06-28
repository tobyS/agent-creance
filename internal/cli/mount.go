package cli

// mount.go implements the `agent-creance mount` command group (AC-0067): add and remove
// safehouse.add_dirs_rw / add_dirs_ro entries (filesystem mounts). Like service, these
// are baked into the Seatbelt profile at launch, so an edit cannot reach a running cage —
// the commands write the config and warn (applyAndWarn) when a cage is live. Mounts are
// project-only, so there is no --global.

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/config"
)

func newMountCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mount",
		Short: "Add or remove filesystem mounts (safehouse add_dirs)",
	}
	cmd.AddCommand(newMountAddCmd(app), newMountRemoveCmd(app))
	return cmd
}

func newMountAddCmd(app *App) *cobra.Command {
	var rw, ro bool
	cmd := &cobra.Command{
		Use:   "add PATH",
		Short: "Add a filesystem mount (prompts for --rw/--ro if neither is given)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runMountAdd(app, ".", args[0], rw, ro)
		},
	}
	cmd.Flags().BoolVar(&rw, "rw", false, "mount read-write (add_dirs_rw)")
	cmd.Flags().BoolVar(&ro, "ro", false, "mount read-only (add_dirs_ro)")
	return cmd
}

func newMountRemoveCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove PATH",
		Short: "Remove a filesystem mount (from whichever list holds it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runMountRemove(app, ".", args[0])
		},
	}
	return cmd
}

// runMountAdd appends path to add_dirs_rw or add_dirs_ro, prompting for the access mode
// when neither --rw nor --ro is supplied.
func runMountAdd(app *App, dir, path string, rw, ro bool) error {
	if rw && ro {
		return errors.New("cannot combine --rw and --ro")
	}
	if !rw && !ro {
		if err := requireInteractive(app, "pass --rw or --ro"); err != nil {
			return err
		}
		choice, err := promptSelect(app, "Mount read-write or read-only?", []string{"read-write", "read-only"})
		if err != nil {
			return err
		}
		rw = choice == 0
	}
	verb := "mounted read-only"
	if rw {
		verb = "mounted read-write"
	}
	target := filepath.Join(dir, configFile)
	return applyAndWarn(app, dir, target, configFile, path, verb,
		func(src []byte) ([]byte, bool, error) { return config.AppendDir(src, path, rw) })
}

// runMountRemove removes path from the add_dirs lists (both, if present in both). A path
// that is not mounted is a clear non-zero error.
func runMountRemove(app *App, dir, path string) error {
	target := filepath.Join(dir, configFile)
	apply := func(src []byte) ([]byte, bool, error) {
		out, _, _, changed, err := config.RemoveDir(src, path)
		return out, changed, err
	}
	err := applyAndWarn(app, dir, target, configFile, path, "unmounted", apply)
	if errors.Is(err, config.ErrNotFound) {
		return fmt.Errorf("%s is not a mount in %s; nothing to remove", path, configFile)
	}
	return err
}
