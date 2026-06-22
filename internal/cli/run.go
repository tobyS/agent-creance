package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/buildinfo"
	"github.com/tobyS/agent-creance/internal/cage"
	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/configwatch"
	"github.com/tobyS/agent-creance/internal/cred"
	"github.com/tobyS/agent-creance/internal/pluginmkt"
	compile "github.com/tobyS/agent-creance/internal/policy/compile"
	"github.com/tobyS/agent-creance/internal/prereq"
	"github.com/tobyS/agent-creance/internal/profile"
	"github.com/tobyS/agent-creance/internal/progress"
	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/setupcheck"
	"github.com/tobyS/agent-creance/internal/state"
)

// configFile is the project config filename run resolves in the working directory.
const configFile = ".agent-creance.yaml"

// newRunCmd implements `agent-creance run` — the headline command. It checks
// prerequisites and that setup has run, then compiles the project's policy and
// profile, starts (or attaches to) the egress proxy, and execs the agent inside
// the cage, forwarding signals and decrementing the proxy refcount on exit.
//
// It is deliberately thin orchestration over already-tested subsystems: prereq,
// setupcheck, cred, the policy/profile compilers, proxy.Manager (refcount), and
// cage.Builder/Runner. The order of operations is the design's load-bearing
// contract (docs/design.md, "Commands" + "Multi-agent lifecycle").
func newRunCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Start the agent inside the egress-filtered cage",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRun(cmd.Context(), app, ".")
		},
	}
}

// runRun is the testable body of the run command: dir is the project directory
// ("." in production), taken as a parameter so unit tests can drive it against
// the sysdep fakes.
func runRun(ctx context.Context, app *App, dir string) error {
	// 1. Prerequisites. A missing tool is a hard failure with an actionable block
	//    (no stack trace); a version skew never blocks (design.md "version handling").
	results := prereq.Check(ctx, app.Commander, prereq.DefaultTools(app.Tested))
	if instructions := prereq.MissingInstructions(results); instructions != "" {
		fmt.Fprint(app.Stdout, instructions)
		return fmt.Errorf("%d prerequisite(s) missing", len(prereq.Missing(results)))
	}
	warnVersionSkew(app, results)

	// 2. Setup precondition: the mitmproxy CA must be trusted and the skill present.
	//    Refuse with a pointer to `agent-creance setup` rather than failing later.
	setupRes, err := setupcheck.Verify(app.Keychain, app.FS, app.Paths)
	if err != nil {
		return fmt.Errorf("verify setup: %w", err)
	}
	if !setupRes.OK() {
		fmt.Fprintln(app.Stdout, setupRes.Message())
		return fmt.Errorf("setup incomplete")
	}

	// 3. Credential precondition: the Anthropic OAuth token must be in the Keychain
	//    (file-based / missing / locked all refuse up front, design.md).
	credRes, err := cred.Detect(app.Keychain, app.FS, app.Paths)
	if err != nil {
		return fmt.Errorf("detect credential: %w", err)
	}
	if !credRes.OK() {
		fmt.Fprintln(app.Stdout, credRes.Message())
		return fmt.Errorf("credential unavailable")
	}

	// 4. Resolve the project's out-of-tree state layout (canonical path → identity).
	layout, err := state.New(app.Paths).Resolve(dir)
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}

	// Progress goes to stderr (run's stdout belongs to the agent session; same
	// convention as git/curl). All announced steps finish before the agent owns
	// the terminal; the deferred Close terminates a half-drawn \r line on error
	// paths so the `error:` line starts fresh.
	prog := progress.NewPrinter(app.Stderr, app.Clock, app.Terminal.IsStderrTerminal(), app.ErrStyle)
	defer prog.Close()

	// 5. Cache-aware policy compile: skipped when inputs are unchanged. The input
	//    hash is recorded in the proxy lock so a policy change triggers a hot-reload.
	//    The printer doubles as the compiler's progress reporter (expectation
	//    message, per-manifest lookup counters).
	prog.StepStart("Compiling egress policy")
	compiler, err := compile.New(app.FS, app.Paths, app.Clock, app.HTTP, prog)
	if err != nil {
		return fmt.Errorf("init compiler: %w", err)
	}
	polRes, err := compiler.Compile(ctx, dir)
	if err != nil {
		return fmt.Errorf("compile policy: %w", err)
	}
	if polRes.Skipped {
		prog.StepDone("Egress policy up to date (cached)")
	} else {
		prog.StepDone(fmt.Sprintf("Egress policy compiled: %d allow, %d deny", polRes.AllowCount, polRes.DenyCount))
	}

	// 6. Load the merged config (needed to build the Safehouse invocation).
	cfg, err := config.NewLoader(app.FS, app.Paths).Load(filepath.Join(dir, configFile))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 7. (Re)generate network.sb — the deny-all baseline. Exempt from the policy
	//    cache by design; cheap, always regenerated.
	prog.StepStart("Compiling sandbox profile")
	if _, err := profile.New(app.FS, app.Paths).Compile(dir); err != nil {
		return fmt.Errorf("compile profile: %w", err)
	}
	prog.StepDone("Sandbox profile compiled")

	// 8. Extract the mitmproxy enforcer addon (idempotent).
	enforcerPy, err := proxy.NewExtractor(app.FS, app.Paths).Extract()
	if err != nil {
		return fmt.Errorf("extract enforcer: %w", err)
	}

	// 9. Start or attach the refcounted proxy. Detach is deferred immediately so
	//    every exit path decrements; the last agent out kills the proxy and purges
	//    the session overlay (proxy.Manager owns that logic).
	mgr := proxy.NewManager(app.FS, app.Flock, app.ProcessManager, app.PortAllocator, app.Sleeper, app.Stderr)
	selfPID := os.Getpid()
	prog.StepStart("Starting egress proxy")
	att, err := mgr.Attach(ctx, proxy.StartConfig{
		Layout:     layout,
		EnforcerPy: enforcerPy,
		PolicyHash: polRes.InputHash,
		SelfPID:    selfPID,
	})
	if err != nil {
		return fmt.Errorf("start proxy: %w", err)
	}
	prog.StepDone(fmt.Sprintf("Egress proxy ready on port %d", att.Port))
	defer func() {
		if derr := mgr.Detach(layout, selfPID); derr != nil {
			fmt.Fprintf(app.Stderr, "warning: proxy teardown: %v\n", derr)
		}
	}()

	cfgPath := filepath.Join(dir, configFile)

	// 10. Build the Safehouse invocation: resolve inputs, write the launch-time
	//     proxy-port fragment + seed the ephemeral config dir (Prepare), assemble argv.
	builder := cage.New(app.FS, app.Paths)
	in, err := builder.Resolve(cfg, layout, att.Port)
	if err != nil {
		return fmt.Errorf("resolve cage inputs: %w", err)
	}
	// Launch exactly the binary the prereq check resolved (step 1 guarantees it
	// is installed), so check and exec can never disagree on the name.
	in.Binary, _ = prereq.ResolvedBinary(results, buildinfo.ToolSafehouse)
	// Grant local Claude plugin-marketplace source dirs into the cage read-only so
	// caged Claude Code can load them (AC-0056). Advisory: detection problems warn,
	// never block.
	in.ExtraDirsRO = pluginMarketplaceDirs(app, in.HomeDir, layout.Canonical)
	// Resolve the include graph so the cage can deny in-cage write of every config
	// file the run-session watcher hot-reloads (AC-0053). Fail closed: launching
	// without this deny would let the caged agent widen its own egress by editing
	// the config the watcher recompiles.
	in.ConfigFiles, err = config.NewLoader(app.FS, app.Paths).ResolveFiles(cfgPath)
	if err != nil {
		return fmt.Errorf("resolve config files: %w", err)
	}
	if err := builder.Prepare(in); err != nil {
		return fmt.Errorf("prepare cage: %w", err)
	}
	inv, err := cage.Build(in)
	if err != nil {
		return fmt.Errorf("build cage invocation: %w", err)
	}

	// 10b. Watch the source config + its include graph for the duration of the
	//      session. A hand-edit recompiles policy.json, which the enforcer's mtime
	//      poll then applies to the live cage — no restart. Advisory: a watcher
	//      failure warns but never blocks the run; an invalid edit keeps the
	//      last-good policy (the compiler never overwrites policy.json on a failed
	//      compile). The deferred Stop runs before the deferred Detach (LIFO), so
	//      the watcher is gone before the proxy is torn down.
	reload := func(ctx context.Context) (bool, string, error) {
		c, err := compile.New(app.FS, app.Paths, app.Clock, app.HTTP, nil /*silent*/)
		if err != nil {
			return false, "", err
		}
		res, err := c.Compile(ctx, dir)
		if err != nil {
			return false, "", err
		}
		if res.Skipped {
			return false, "", nil
		}
		return true, fmt.Sprintf("%d allow, %d deny", res.AllowCount, res.DenyCount), nil
	}
	watcher := configwatch.New(config.NewLoader(app.FS, app.Paths), app.WatcherFactory, reload, app.Stderr)
	if err := watcher.Start(ctx, cfgPath); err != nil {
		fmt.Fprintf(app.Stderr, "%s config hot-reload unavailable: %v\n", app.ErrStyle.Warn("⚠"), err)
	} else {
		defer func() { _ = watcher.Stop() }()
	}

	// 11. Run the agent in its own process group, forwarding signals; blocks until
	//     the whole group is reaped, so the deferred Detach runs strictly after.
	prog.Line("Launching agent…")
	if err := cage.NewRunner(app.ProcessGroup).Run(ctx, inv); err != nil {
		return fmt.Errorf("run agent: %w", err)
	}
	return nil
}

// pluginMarketplaceDirs detects local Claude plugin-marketplace source dirs
// (pluginmkt.Detect) and returns those that must be granted into the cage
// read-only: the ones not already covered by a mounted root — the project dir
// (mounted RW) or ~/.claude (mounted RW, where git marketplaces and the plugin
// cache live). Detection problems are advisory: printed to stderr, never fatal
// (matching warnVersionSkew). run's stdout belongs to the agent, so the notice and
// warnings go to stderr. A one-line notice announces the granted dirs.
func pluginMarketplaceDirs(app *App, homeDir, projectDir string) []string {
	detected, warns := pluginmkt.Detect(app.FS, app.Paths)
	for _, w := range warns {
		fmt.Fprintf(app.Stderr, "%s plugin marketplace: %s\n", app.ErrStyle.Warn("⚠"), w)
	}

	claudeDir := filepath.Join(homeDir, ".claude")
	if resolved, err := app.Paths.EvalSymlinks(claudeDir); err == nil {
		claudeDir = resolved
	}
	roots := []string{projectDir, claudeDir}

	var grant []string
	for _, d := range detected {
		if withinAny(d, roots) {
			continue // already mounted read-write; no separate RO grant needed
		}
		grant = append(grant, d)
	}
	if len(grant) > 0 {
		fmt.Fprintf(app.Stderr, "granting read-only cage access to %d local plugin marketplace dir(s): %s\n",
			len(grant), strings.Join(grant, ", "))
	}
	return grant
}

// withinAny reports whether path equals, or is nested under, any of roots.
func withinAny(path string, roots []string) bool {
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// warnVersionSkew prints a single loud line per tool whose installed version is a
// minor/major skew from the tested version. Patch skew stays silent on run; only a
// missing tool blocks (handled earlier). Never returns an error — skew is advisory.
func warnVersionSkew(app *App, results []prereq.Result) {
	for _, r := range results {
		if r.Skew.Loud() {
			fmt.Fprintf(app.Stderr, "%s %s %s differs from tested %s %s\n",
				app.ErrStyle.Warn("⚠"), r.Tool.Name, r.Version, r.Tool.Tested,
				app.ErrStyle.Dim("("+r.Skew.String()+")"))
		}
	}
}
