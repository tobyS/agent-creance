package cage_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/cage"
	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/state"
)

// update regenerates the golden artifact: `go test ./... -update` (make golden).
var update = flag.Bool("update", false, "regenerate golden files")

// fixtureInputs is the canonical Build input used by the golden + negative tests:
// the design doc's example safehouse block, a user env var, and a known port/home.
func fixtureInputs() cage.Inputs {
	return cage.Inputs{
		Config: &config.Config{
			Agent: config.Agent{
				Command: []string{"claude", "--dangerously-skip-permissions"},
				Workdir: ".",
			},
			Safehouse: config.Safehouse{
				AddDirsRW: []string{"."},
				AddDirsRO: []string{"~/.config/git"},
				Enable:    []string{"shell-init"},
			},
			Env: map[string]string{"MY_VAR": "hello"},
		},
		Layout: state.Layout{
			Canonical: "/proj",
			Hash:      "0123456789abcdef",
			Root:      "/home/test/.cache/agent-creance/projects/0123456789abcdef",
		},
		ProxyPort:  18081,
		HomeDir:    "/home/test",
		CACertPath: "/home/test/.mitmproxy/mitmproxy-ca-cert.pem",
	}
}

func TestBuildGolden(t *testing.T) {
	inv, err := cage.Build(fixtureInputs())
	require.NoError(t, err)

	got, err := json.MarshalIndent(inv, "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')

	path := filepath.Join("testdata", "invocation.golden.json")
	if *update {
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file; run with -update to create it")
	require.Equal(t, string(want), string(got))
}

// TestBuildBinary covers Invocation.Path: the resolved name from Inputs.Binary
// when set (run's prereq-verified name), the default Binary otherwise (the
// golden fixture leaves it unset, pinning the default).
func TestBuildBinary(t *testing.T) {
	in := fixtureInputs()
	inv, err := cage.Build(in)
	require.NoError(t, err)
	require.Equal(t, cage.Binary, inv.Path, "unset Inputs.Binary falls back to the default")

	in.Binary = "agent-safehouse"
	inv, err = cage.Build(in)
	require.NoError(t, err)
	require.Equal(t, "agent-safehouse", inv.Path, "resolved Inputs.Binary is honored")
}

func TestExpandPathViaArgs(t *testing.T) {
	// expandPath is unexported; exercise it through the public Build by varying the
	// RW mount dir and asserting the resulting --add-dirs value.
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"home tilde", "~", "/home/test"},
		{"under home", "~/.config/git", "/home/test/.config/git"},
		{"dot is project", ".", "/proj"},
		{"relative is project-rooted", "sub/dir", "/proj/sub/dir"},
		{"absolute passes through", "/abs/path", "/abs/path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := fixtureInputs()
			in.Config.Safehouse.AddDirsRW = []string{tc.in}
			inv, err := cage.Build(in)
			require.NoError(t, err)
			// The real ~/.claude is always appended as a RW mount (AC-0045).
			want := tc.want + ":" + filepath.Join(in.HomeDir, ".claude")
			require.Equal(t, want, argValue(t, inv.Args, "--add-dirs"))
		})
	}
}

// TestBuildMountsRealClaudeRW guards the v0.1 posture (AC-0045): the real ~/.claude
// is mounted read-write even when the user configured no add_dirs_rw, and
// CLAUDE_CONFIG_DIR is NOT set — redirecting it would change the Keychain service
// name Claude Code derives and break the shared-credential lookup.
func TestBuildMountsRealClaudeRW(t *testing.T) {
	realClaude := filepath.Join(fixtureInputs().HomeDir, ".claude")
	t.Run("empty AddDirsRW still mounts ~/.claude", func(t *testing.T) {
		in := fixtureInputs()
		in.Config.Safehouse.AddDirsRW = nil
		inv, err := cage.Build(in)
		require.NoError(t, err)
		require.Equal(t, realClaude, argValue(t, inv.Args, "--add-dirs"))
	})
	t.Run("~/.claude mounted alongside user dirs", func(t *testing.T) {
		in := fixtureInputs()
		inv, err := cage.Build(in)
		require.NoError(t, err)
		require.Contains(t, argValue(t, inv.Args, "--add-dirs"), realClaude)
	})
	t.Run("no CLAUDE_CONFIG_DIR redirect", func(t *testing.T) {
		in := fixtureInputs()
		inv, err := cage.Build(in)
		require.NoError(t, err)
		for _, kv := range inv.Env {
			require.False(t, strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR="),
				"CLAUDE_CONFIG_DIR must not be set in the cage env: %s", kv)
		}
		require.NotContains(t, argValue(t, inv.Args, "--env-pass"), "CLAUDE_CONFIG_DIR")
	})
}

func TestBuildEnvPrecedence(t *testing.T) {
	in := fixtureInputs()
	in.Config.Env = map[string]string{"HTTPS_PROXY": "http://evil.example:8080", "MY_VAR": "hello"}
	inv, err := cage.Build(in)
	require.NoError(t, err)

	require.Equal(t, "http://127.0.0.1:18081", envValue(t, inv.Env, "HTTPS_PROXY"),
		"computed proxy URL must override a user-set HTTPS_PROXY")
	require.Equal(t, "hello", envValue(t, inv.Env, "MY_VAR"),
		"unrelated user env var is preserved")
}

func TestBuildEnvPassMatchesEnvKeys(t *testing.T) {
	inv, err := cage.Build(fixtureInputs())
	require.NoError(t, err)

	names := strings.Split(argValue(t, inv.Args, "--env-pass"), ",")
	keys := make([]string, len(inv.Env))
	for i, kv := range inv.Env {
		keys[i] = kv[:strings.IndexByte(kv, '=')]
	}
	require.Equal(t, keys, names, "--env-pass names must equal the sorted Env keys")

	// The full S4 set + the user var are all present (no CLAUDE_CONFIG_DIR — AC-0045).
	for _, want := range []string{
		"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy",
		"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO",
		"MY_VAR",
	} {
		require.Contains(t, names, want)
	}
}

func TestBuildValidation(t *testing.T) {
	t.Run("empty command", func(t *testing.T) {
		in := fixtureInputs()
		in.Config.Agent.Command = nil
		_, err := cage.Build(in)
		require.Error(t, err)
	})
	t.Run("nil config", func(t *testing.T) {
		_, err := cage.Build(cage.Inputs{ProxyPort: 18081})
		require.Error(t, err)
	})
	for _, port := range []int{0, 70000, -1} {
		t.Run("bad port", func(t *testing.T) {
			in := fixtureInputs()
			in.ProxyPort = port
			_, err := cage.Build(in)
			require.Error(t, err)
		})
	}
}

// argValue returns the token following the first occurrence of flag in args.
func argValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	t.Fatalf("flag %q not found in args %v", flag, args)
	return ""
}

// envValue returns the value for key from a KEY=VALUE slice.
func envValue(t *testing.T, env []string, key string) string {
	t.Helper()
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return kv[len(key)+1:]
		}
	}
	t.Fatalf("env key %q not found in %v", key, env)
	return ""
}
