package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/doctor"
	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/setup"
	"github.com/tobyS/agent-creance/internal/state"
)

// newDoctorCmd implements `agent-creance doctor`: the full diagnostic covering
// prerequisite versions, live CA trust, the current project's proxy health, exposed
// host services, and flock-unreliable filesystems. --fix safely remediates what it
// can (today: cleaning an orphan proxy). It exits non-zero when an actionable
// problem remains (untrusted CA, an un-fixed orphan, or a missing prerequisite).
func newDoctorCmd(app *App) *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose prerequisites, CA trust, proxies, and environment",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd.Context(), app, fix)
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false,
		"remediate what can be safely fixed (e.g. clean orphan proxies)")
	return cmd
}

// runDoctor is the testable body: it builds the doctor.Checker from the App seams,
// runs every check, renders the report, and turns remaining actionable problems into
// a non-zero exit (Main prints the error to stderr). Taking fix as a parameter is
// what lets the unit tests drive both modes against the sysdep fakes.
func runDoctor(ctx context.Context, app *App, fix bool) error {
	chk := &doctor.Checker{
		Commander: app.Commander,
		Tested:    app.Tested,
		Installer: setup.NewInstaller(
			app.FS, app.Keychain, app.ProcessManager, app.PortAllocator,
			app.TLSProber, app.Sleeper, app.Paths,
		),
		Manager:   proxy.NewManager(app.FS, app.Flock, app.ProcessManager, app.PortAllocator, app.Sleeper, app.Stderr),
		Resolver:  state.New(app.Paths),
		Listeners: app.Listeners,
		FSType:    app.FSType,
		Paths:     app.Paths,
		Keychain:  app.Keychain,
		FS:        app.FS,
	}

	rep, err := chk.Run(ctx, fix)
	if err != nil {
		return err
	}
	fmt.Fprint(app.Stdout, doctor.Render(rep, app.OutStyle))

	if probs := rep.Actionable(); len(probs) > 0 {
		return fmt.Errorf("%d actionable problem(s) remain: %s", len(probs), strings.Join(probs, ", "))
	}
	return nil
}
