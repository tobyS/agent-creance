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
	// SecretResolver resolves host-side secret references (op:// / keychain:// /
	// env://) to in-memory values for credential injection (AC-0068); its
	// consumers — the proxy spawner (AC-0068c) and the credential CLI (AC-0068d) —
	// land in later phases.
	SecretResolver sysdep.SecretResolver
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

// Command group IDs for the root help listing. cobra prints subcommands under
// their group's Title (in AddGroup order) instead of one alphabetized list;
// every top-level command must carry one of these or cobra panics at runtime
// (checkCommandGroups). The groups are ordered by how a user meets the tool:
// the setup -> init -> run happy path first, then doctor for when it breaks,
// then the config-editing and inspection commands, then teardown. The
// auto-generated help/completion commands are routed into groupMaint via
// Set{Help,Completion}CommandGroupID below.
const (
	groupStart     = "start"
	groupTrouble   = "troubleshoot"
	groupConfigure = "configure"
	groupInspect   = "inspect"
	groupMaint     = "maintenance"
)

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
		Use:   "agent-creance",
		Short: "Run a coding agent inside an isolated, egress-filtered cage",
		Long: "agent-creance runs a coding agent (Claude Code or another) inside an isolated,\n" +
			"egress-filtered cage on macOS, composing agent-safehouse (filesystem and process\n" +
			"isolation) with mitmproxy (a TLS-terminating egress allowlist), configured by one\n" +
			".agent-creance.yaml file.\n" +
			"\n" +
			"Getting started — the happy path is setup -> init -> run:\n" +
			"\n" +
			"  agent-creance setup   # once per machine: trust the mitmproxy CA, install the\n" +
			"                        #   skill, and scaffold the global config\n" +
			"  agent-creance init    # once per project: write .agent-creance.yaml\n" +
			"  agent-creance run     # start the cage and your agent inside it\n" +
			"\n" +
			"setup runs once per machine, init once per project. If you skip them, run won't\n" +
			"fail with a stack trace — it refuses early with a pointer to whichever command\n" +
			"you still need.",
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

	// Order commands by importance (registration order), not alphabetically, so
	// the happy-path sequence reads setup -> init -> run at the top instead of
	// being re-sorted within its group. This is a package-global in cobra; it
	// also makes nested subcommand lists (e.g. policy show/explain/refresh) print
	// in registration order, which is the intended reading order there too.
	cobra.EnableCommandSorting = false

	root.AddGroup(
		&cobra.Group{ID: groupStart, Title: "Getting Started:"},
		&cobra.Group{ID: groupTrouble, Title: "Troubleshooting:"},
		&cobra.Group{ID: groupConfigure, Title: "Configure Egress & Cage:"},
		&cobra.Group{ID: groupInspect, Title: "Inspect:"},
		&cobra.Group{ID: groupMaint, Title: "Maintenance:"},
	)
	// addCmd registers a subcommand under a group, keeping the taxonomy in one
	// reviewable place rather than scattering GroupID across the factories.
	addCmd := func(cmd *cobra.Command, group string) {
		cmd.GroupID = group
		root.AddCommand(cmd)
	}
	// Getting started — the happy path, in sequence order (setup -> init -> run).
	addCmd(newSetupCmd(app), groupStart)
	addCmd(newInitCmd(app), groupStart)
	addCmd(newRunCmd(app), groupStart)
	// Troubleshooting — the first thing to reach for when run won't start.
	addCmd(newDoctorCmd(app), groupTrouble)
	// Configure — editing the project's egress rules and cage exposure.
	addCmd(newAllowCmd(app), groupConfigure)
	addCmd(newDenyCmd(app), groupConfigure)
	addCmd(newDomainCmd(app), groupConfigure)
	addCmd(newServiceCmd(app), groupConfigure)
	addCmd(newMountCmd(app), groupConfigure)
	addCmd(newIncludeCmd(app), groupConfigure)
	addCmd(newImportCmd(app), groupConfigure)
	// Inspect — read-only state and resolved policy.
	addCmd(newStatusCmd(app), groupInspect)
	addCmd(newLogsCmd(app), groupInspect)
	addCmd(newPolicyCmd(app), groupInspect)
	addCmd(newVersionCmd(app), groupInspect)
	// Maintenance — teardown (plus the auto-generated help/completion below).
	addCmd(newCleanCmd(app), groupMaint)

	root.SetHelpCommandGroupID(groupMaint)
	root.SetCompletionCommandGroupID(groupMaint)
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
		Stdin:     os.Stdin,
		Terminal:  sysdep.OSTerminal{},
		Tested:    buildinfo.TestedVersions,
		FS:        sysdep.OSFileSystem{},
		Paths:     sysdep.OSPathResolver{},
		Clock:     sysdep.OSClock{},
		HTTP:      sysdep.OSHTTPGetter{},
		Keychain:  sysdep.OSKeychain{},
		SecretResolver: sysdep.OSSecretResolver{
			Commander: sysdep.ExecCommander{},
			Keychain:  sysdep.OSKeychain{},
			Paths:     sysdep.OSPathResolver{},
		},
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
