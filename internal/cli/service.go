package cli

// service.go implements the `agent-creance service` command group (AC-0067): add and
// remove network.host_services entries (in-cage "label:port" binds). These compile into
// the Seatbelt profile at launch, so an edit cannot reach a running cage — the commands
// write the config and, when a cage is live, warn that it takes effect on the next run
// (applyAndWarn). host_services are project-only, so there is no --global.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/config"
)

func newServiceCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Add or remove in-cage host services (label:port)",
	}
	cmd.AddCommand(newServiceAddCmd(app), newServiceRemoveCmd(app))
	return cmd
}

func newServiceAddCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add LABEL:PORT",
		Short: "Add a host service bind (prompts for a missing label)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runServiceAdd(app, ".", args[0])
		},
	}
	return cmd
}

func newServiceRemoveCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove PORT",
		Short: "Remove the host service on a port",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runServiceRemove(app, ".", args[0])
		},
	}
	return cmd
}

// runServiceAdd parses the LABEL:PORT argument (prompting for the label when only a port
// is given) and appends it to network.host_services.
func runServiceAdd(app *App, dir, arg string) error {
	spec := arg
	if !strings.Contains(arg, ":") {
		// Only a port was given — collect the (cosmetic but required) label interactively.
		if err := requireInteractive(app, "give the service as LABEL:PORT (e.g. mysql:3306)"); err != nil {
			return err
		}
		label, err := promptText(app, "Service label (e.g. mysql)")
		if err != nil {
			return err
		}
		spec = label + ":" + arg
	}
	hs, err := config.ParseHostService(spec)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, configFile)
	subject := hs.Label + ":" + strconv.Itoa(hs.Port)
	return applyAndWarn(app, dir, target, configFile, subject, "added",
		func(src []byte) ([]byte, bool, error) { return config.AppendHostService(src, hs) })
}

// runServiceRemove removes the host service bound to the given port (the label is
// cosmetic). A port not present is a clear non-zero error.
func runServiceRemove(app *App, dir, portArg string) error {
	port, err := strconv.Atoi(strings.TrimSpace(portArg))
	if err != nil {
		return fmt.Errorf("invalid port %q: %w", portArg, err)
	}
	target := filepath.Join(dir, configFile)
	subject := "port " + strconv.Itoa(port)
	err = applyAndWarn(app, dir, target, configFile, subject, "removed",
		func(src []byte) ([]byte, bool, error) { return config.RemoveHostService(src, port) })
	if errors.Is(err, config.ErrNotFound) {
		return fmt.Errorf("no host service on port %d in %s; nothing to remove", port, configFile)
	}
	return err
}
