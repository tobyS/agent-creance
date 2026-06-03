// Package cli wires the cobra command tree together and exposes the process
// entrypoint. main.go is intentionally a one-liner that calls Main here, so the
// real wiring stays in a package that can be unit- and script-tested.
package cli

import (
	"context"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/buildinfo"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// App holds the dependencies every command needs. Bundling them in one struct
// (rather than reaching for globals) is what lets tests construct an App with
// fakes and redirected output. This is the composition root.
type App struct {
	Commander sysdep.Commander
	Stdout    io.Writer
	Stderr    io.Writer
	// Tested is the tested-against version map; injected so tests can pin it.
	Tested map[string]string
}

// newRootCmd builds the cobra command tree for the given App.
//
// cobra (the same library behind kubectl, gh, helm) is roughly the Go analog of
// Symfony Console or oclif: you declare commands with a Use string, flags, and a
// RunE function. RunE (vs Run) returns an error, which cobra prints and turns
// into a non-zero exit — idiomatic error handling rather than calling
// os.Exit deep in a handler.
func newRootCmd(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:           "agent-creance",
		Short:         "Run a coding agent inside an isolated, egress-filtered cage",
		SilenceUsage:  true, // don't dump usage on a runtime error
		SilenceErrors: true, // we print errors ourselves in Main
	}
	// Route cobra's own output through the App writers so tests capture it.
	root.SetOut(app.Stdout)
	root.SetErr(app.Stderr)

	root.AddCommand(newVersionCmd(app))
	root.AddCommand(newDoctorCmd(app))
	return root
}

// Main is the real entrypoint. It returns a process exit code instead of
// calling os.Exit directly, which is what makes it usable both from main() and
// from the testscript harness (which maps "agent-creance" to this function and
// runs it in-process).
func Main() int {
	app := &App{
		Commander: sysdep.ExecCommander{},
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		Tested:    buildinfo.TestedVersions,
	}
	if err := newRootCmd(app).ExecuteContext(context.Background()); err != nil {
		// Cobra already validated args; this is a runtime failure.
		_, _ = io.WriteString(app.Stderr, "error: "+err.Error()+"\n")
		return 1
	}
	return 0
}
