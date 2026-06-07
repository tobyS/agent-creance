package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/tobyS/agent-creance/internal/cli"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// testscript (from rogpeppe/go-internal — the same harness the Go toolchain
// itself uses to test the `go` command) runs little script files that invoke
// our CLI as if it were the real binary and assert on stdout/stderr/files.
//
// testscript.Main re-execs this test binary as a subprocess whenever a script
// calls "agent-creance", routing it to the registered func. The subprocess
// inherits the script's environment (including PATH), which is what lets the
// doctor script swap in fake prerequisites hermetically — no real
// agent-safehouse/mitmproxy needed. cli.Main returns an exit code, so we wrap it
// in os.Exit; testscript.Main itself terminates the process.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"agent-creance": func() { os.Exit(cli.Main()) },
	})
}

// TestScripts runs every *.txtar file under testdata/script. Each file is an
// isolated scenario with its own temp working directory.
func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
		Cmds: map[string]func(*testscript.TestScript, bool, []string){
			"seedaudit": seedAudit,
			"seedlock":  seedLock,
		},
		Setup: func(e *testscript.Env) error {
			// testscript copies the registered `agent-creance` command into a
			// bindir it prepends to PATH (see go-internal/testscript/exe.go). We
			// expose that dir as $CREANCE_BIN so a scenario can set a minimal
			// PATH that still finds the CLI but excludes the host's real
			// prerequisites — making the "tools missing" case hermetic instead
			// of depending on what happens to be installed.
			path := e.Getenv("PATH")
			bindir := path
			if i := strings.IndexByte(path, filepath.ListSeparator); i >= 0 {
				bindir = path[:i]
			}
			e.Setenv("CREANCE_BIN", bindir)
			return nil
		},
	})
}

// seedAudit writes a rotated (.1) + current audit log into the out-of-tree state dir
// the CLI will resolve for the script's working directory, so a scenario can exercise
// `logs`/`logs --summary` over real fixture content. The state path is hashed from the
// realpath of the project dir, so it can't be pre-seeded by a static path in the
// .txtar — this command reproduces the same resolution the CLI does (same state
// package, the script's HOME/XDG_CACHE_HOME, and the real realpath of $WORK) and writes
// there. Usage: seedaudit <rotatedSrc> <currentSrc> (both are files in the script dir).
func seedAudit(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) != 2 {
		ts.Fatalf("usage: seedaudit <rotatedSrc> <currentSrc>")
	}
	work := ts.Getenv("WORK")
	canonical, err := filepath.EvalSymlinks(work)
	if err != nil {
		ts.Fatalf("seedaudit: evalsymlinks %s: %v", work, err)
	}
	// Reproduce the CLI's `state.New(OSPathResolver).Resolve(".")` using the script's
	// environment: Abs(".") -> $WORK, EvalSymlinks -> its realpath, cache root from
	// XDG_CACHE_HOME/HOME.
	paths := sysdeptest.NewFakePathResolver()
	paths.Cwd = work
	paths.HomeDir = ts.Getenv("HOME")
	if xdg := ts.Getenv("XDG_CACHE_HOME"); xdg != "" {
		paths.Env["XDG_CACHE_HOME"] = xdg
	}
	paths.Symlinks = map[string]string{work: canonical}
	layout, err := state.New(paths).Resolve(".")
	if err != nil {
		ts.Fatalf("seedaudit: resolve layout: %v", err)
	}
	if err := os.MkdirAll(layout.Root, 0o755); err != nil {
		ts.Fatalf("seedaudit: mkdir %s: %v", layout.Root, err)
	}
	if err := os.WriteFile(layout.EgressJSONLRotated(), []byte(ts.ReadFile(args[0])), 0o600); err != nil {
		ts.Fatalf("seedaudit: write rotated: %v", err)
	}
	if err := os.WriteFile(layout.EgressJSONL(), []byte(ts.ReadFile(args[1])), 0o600); err != nil {
		ts.Fatalf("seedaudit: write current: %v", err)
	}
}

// seedLock writes a fixture proxy.lock into the out-of-tree state dir the CLI will
// resolve for the script's working directory, so a scenario can exercise
// `status`/`clean` over real lock content. Like seedaudit it reproduces the CLI's
// own path resolution (the path is hashed from the realpath of $WORK and cannot be
// pre-seeded by a static path in the .txtar). Usage: seedlock <srcfile> (a JSON
// proxy.lock in the script dir).
func seedLock(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) != 1 {
		ts.Fatalf("usage: seedlock <srcfile>")
	}
	work := ts.Getenv("WORK")
	canonical, err := filepath.EvalSymlinks(work)
	if err != nil {
		ts.Fatalf("seedlock: evalsymlinks %s: %v", work, err)
	}
	paths := sysdeptest.NewFakePathResolver()
	paths.Cwd = work
	paths.HomeDir = ts.Getenv("HOME")
	if xdg := ts.Getenv("XDG_CACHE_HOME"); xdg != "" {
		paths.Env["XDG_CACHE_HOME"] = xdg
	}
	paths.Symlinks = map[string]string{work: canonical}
	layout, err := state.New(paths).Resolve(".")
	if err != nil {
		ts.Fatalf("seedlock: resolve layout: %v", err)
	}
	if err := os.MkdirAll(layout.Root, 0o755); err != nil {
		ts.Fatalf("seedlock: mkdir %s: %v", layout.Root, err)
	}
	if err := os.WriteFile(layout.ProxyLock(), []byte(ts.ReadFile(args[0])), 0o600); err != nil {
		ts.Fatalf("seedlock: write lock: %v", err)
	}
}
