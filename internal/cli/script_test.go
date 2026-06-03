package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/tobyS/agent-creance/internal/cli"
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
