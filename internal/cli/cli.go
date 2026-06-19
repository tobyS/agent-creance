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
	"github.com/tobyS/agent-creance/internal/style"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// App holds the dependencies every command needs. Bundling them in one struct
// (rather than reaching for globals) is what lets tests construct an App with
// fakes and redirected output. This is the composition root.
type App struct {
	Commander sysdep.Commander
	Stdout    io.Writer
	Stderr    io.Writer
	// Stdin is the CLI's standard input, used by the init command's confirm
	// prompt (the CLI's only interactive input). Production wires os.Stdin; tests
	// supply a reader. Paired with Terminal, which decides whether prompting is
	// even appropriate.
	Stdin io.Reader
	// Terminal reports whether Stdin is an interactive terminal; init uses it to
	// choose between prompting (interactive) and refusing with an instruction
	// (non-interactive), so an unattended run never blocks on input.
	Terminal sysdep.Terminal
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
	// FSType and Listeners are the two extra seams doctor (AC-0031) needs: statfs-based
	// filesystem-type detection (the flock-unreliable iCloud/SMB warning) and host
	// listening-socket enumeration (the exposed-0.0.0.0-service scan).
	FSType    sysdep.FilesystemTyper
	Listeners sysdep.ListenerScanner
	// WatcherFactory creates the filesystem watcher the run command (AC-0053) uses
	// to hot-reload the source config + its include graph during an active session;
	// a hand-edit recompiles policy.json, which the proxy enforcer's mtime poll then
	// applies to the live cage. Tests wire the sysdeptest fake.
	WatcherFactory sysdep.FileWatcherFactory
	// OutStyle and ErrStyle carry the semantic color layer (AC-0052) for stdout
	// and stderr respectively. The root command's --color flag resolves them once
	// (per stream, honoring NO_COLOR and isatty) before any subcommand runs.
	// A nil styler is safe and behaves as plain, so tests that omit them — and any
	// path reached before resolution — produce byte-identical plain output.
	OutStyle *style.Styler
	ErrStyle *style.Styler
}

// newRootCmd builds the cobra command tree for the given App.
//
// cobra (the same library behind kubectl, gh, helm) is roughly the Go analog of
// Symfony Console or oclif: you declare commands with a Use string, flags, and a
// RunE function. RunE (vs Run) returns an error, which cobra prints and turns
// into a non-zero exit — idiomatic error handling rather than calling
// os.Exit deep in a handler.
func newRootCmd(app *App) *cobra.Command {
	var colorMode string
	root := &cobra.Command{
		Use:           "agent-creance",
		Short:         "Run a coding agent inside an isolated, egress-filtered cage",
		SilenceUsage:  true, // don't dump usage on a runtime error
		SilenceErrors: true, // we print errors ourselves in Main
		// Resolve the color decision once, before any subcommand runs. Per stream,
		// because a report (stdout) and progress (stderr) can be piped separately.
		// --color=always overrides NO_COLOR per the no-color.org spec.
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			noColor := app.Paths.Getenv("NO_COLOR") != ""
			out, err := style.Resolve(colorMode, noColor, app.Terminal.IsStdoutTerminal())
			if err != nil {
				return err
			}
			errOn, err := style.Resolve(colorMode, noColor, app.Terminal.IsStderrTerminal())
			if err != nil {
				return err
			}
			app.OutStyle = style.New(out)
			app.ErrStyle = style.New(errOn)
			return nil
		},
	}
	// Route cobra's own output through the App writers so tests capture it.
	root.SetOut(app.Stdout)
	root.SetErr(app.Stderr)
	root.PersistentFlags().StringVar(&colorMode, "color", "auto",
		"when to colorize output: auto (a tty, unless NO_COLOR), always, or never")

	root.AddCommand(newInitCmd(app))
	root.AddCommand(newVersionCmd(app))
	root.AddCommand(newDoctorCmd(app))
	root.AddCommand(newPolicyCmd(app))
	root.AddCommand(newLogsCmd(app))
	root.AddCommand(newRunCmd(app))
	root.AddCommand(newSetupCmd(app))
	root.AddCommand(newAllowCmd(app))
	root.AddCommand(newDenyCmd(app))
	root.AddCommand(newImportCmd(app))
	root.AddCommand(newStatusCmd(app))
	root.AddCommand(newCleanCmd(app))
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
		Stdin:          os.Stdin,
		Terminal:       sysdep.OSTerminal{},
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
		FSType:         sysdep.OSFilesystemTyper{},
		Listeners:      sysdep.OSListenerScanner{},
		WatcherFactory: sysdep.OSFileWatcherFactory{},
		// Plain until the root PersistentPreRunE resolves --color; safe for any
		// path reached before that (e.g. bare `agent-creance`).
		OutStyle: style.Plain(),
		ErrStyle: style.Plain(),
	}
	if err := newRootCmd(app).ExecuteContext(context.Background()); err != nil {
		// Cobra already validated args; this is a runtime failure.
		_, _ = io.WriteString(app.Stderr, "error: "+err.Error()+"\n")
		return 1
	}
	return 0
}
