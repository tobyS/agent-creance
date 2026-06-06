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
			require.Equal(t, tc.want, argValue(t, inv.Args, "--add-dirs"))
		})
	}
}

func TestBuildNeverMountsRealClaude(t *testing.T) {
	in := fixtureInputs()
	inv, err := cage.Build(in)
	require.NoError(t, err)

	realClaude := filepath.Join(in.HomeDir, ".claude")
	// No --add-dirs* value may reference the real ~/.claude.
	for i := 0; i+1 < len(inv.Args); i++ {
		if inv.Args[i] == "--add-dirs" || inv.Args[i] == "--add-dirs-ro" {
			require.NotContains(t, inv.Args[i+1], realClaude,
				"mount flag %s must not reference the real ~/.claude", inv.Args[i])
		}
	}
	// CLAUDE_CONFIG_DIR must point under the state root, not ~/.claude.
	ccd := envValue(t, inv.Env, "CLAUDE_CONFIG_DIR")
	require.Equal(t, in.Layout.ClaudeConfigDir(), ccd)
	require.True(t, strings.HasPrefix(ccd, in.Layout.Root), "CLAUDE_CONFIG_DIR not under state root")
	require.NotContains(t, ccd, realClaude)
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

	// The full S4 set + CLAUDE_CONFIG_DIR + the user var are all present.
	for _, want := range []string{
		"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy",
		"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO",
		"CLAUDE_CONFIG_DIR", "MY_VAR",
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
