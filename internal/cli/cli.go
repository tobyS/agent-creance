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
	// The filesystem/path/clock/HTTP seams the policy commands need to compile and
	// read a project's egress policy on demand (the policy compiler takes all four).
	FS    sysdep.FileSystem
	Paths sysdep.PathResolver
	Clock sysdep.Clock
	HTTP  sysdep.HTTPGetter
	// Keychain reads the Anthropic OAuth credential item; internal/cred uses it
	// to detect credential availability before a caged run (its consumers, the
	// run/doctor preconditions, land in later phases).
	Keychain sysdep.Keychain
	// ProcessGroup starts the caged agent in its own process group and forwards
	// SIGINT/SIGTERM to the whole group; the run command (AC-0025) drives it via
	// cage.Runner to tear the agent's subtree down on Ctrl-C before the lock decrement.
	ProcessGroup sysdep.ProcessGroup
	// Flock, ProcessManager, and PortAllocator are the seams proxy.Manager needs to
	// run the refcounted shared-proxy lifecycle the run command (AC-0025) drives:
	// flock-guarded proxy.lock RMW, spawning/pruning/killing mitmproxy by PID, and
	// ephemeral port allocation + liveness probing.
	Flock          sysdep.Flock
	ProcessManager sysdep.ProcessManager
	PortAllocator  sysdep.PortAllocator
	// TLSProber and Sleeper are the two extra seams setup.Installer needs (beyond
	// those above) for the live CA verification probe and the CA-generation poll;
	// the setup command (AC-0028) drives them. Tests wire the sysdeptest fakes.
	TLSProber sysdep.TLSProber
	Sleeper   sysdep.Sleeper
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
	root.AddCommand(newPolicyCmd(app))
	root.AddCommand(newLogsCmd(app))
	root.AddCommand(newRunCmd(app))
	root.AddCommand(newSetupCmd(app))
	return root
}

// Main is the real entrypoint. It returns a process exit code instead of
// calling os.Exit directly, which is what makes it usable both from main() and
// from the testscript harness (which maps "agent-creance" to this function and
// runs it in-process).
func Main() int {
	app := &App{
		Commander:      sysdep.ExecCommander{},
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
		Tested:         buildinfo.TestedVersions,
		FS:             sysdep.OSFileSystem{},
		Paths:          sysdep.OSPathResolver{},
		Clock:          sysdep.OSClock{},
		HTTP:           sysdep.OSHTTPGetter{},
		Keychain:       sysdep.OSKeychain{},
		ProcessGroup:   sysdep.OSProcessGroup{},
		Flock:          sysdep.OSFlock{},
		ProcessManager: sysdep.OSProcessManager{},
		PortAllocator:  sysdep.OSPortAllocator{},
		TLSProber:      sysdep.OSTLSProber{},
		Sleeper:        sysdep.OSSleeper{},
	}
	if err := newRootCmd(app).ExecuteContext(context.Background()); err != nil {
		// Cobra already validated args; this is a runtime failure.
		_, _ = io.WriteString(app.Stderr, "error: "+err.Error()+"\n")
		return 1
	}
	return 0
}
