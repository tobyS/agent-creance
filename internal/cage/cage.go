// Package cage constructs the agent-safehouse invocation for a compiled config:
// the argv (mount/capability flags, the two ordered --append-profile fragments,
// --env-pass, and the wrapped agent command) plus the environment to set on the
// safehouse child process (proxy routing, CA trust, the redirected
// CLAUDE_CONFIG_DIR, and the user's config.env).
//
// The argv+env construction (Build) is a pure function of (config, port, paths):
// it performs no I/O, so it is golden-testable without launching anything. The two
// unavoidable side effects — seeding the ephemeral CLAUDE_CONFIG_DIR and writing
// the launch-time proxy-port Seatbelt fragment — live on Builder.Prepare, behind
// the sysdep seams.
//
// This package composes Safehouse; it never execs, forwards signals, or manages
// lifecycle (that is AC-0024 / AC-0025). It also deliberately never mounts the real
// ~/.claude: the OAuth credential is read from the Keychain via the .sb ACL, and
// the agent's executable config is redirected to an ephemeral, sanitized dir so a
// prompt-injected agent cannot plant a hook/MCP/skill that survives into a later,
// un-caged Claude run (see docs/design.md, "The proxy and the credential story").
package cage

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/profile"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// Binary is the agent-safehouse executable name (resolved on PATH by the caller
// that execs it; this package only constructs the invocation).
const Binary = "safehouse"

// Port bounds for defence in depth — the live port originates from the proxy lock
// file, not config, but Build range-checks it anyway.
const (
	minPort = 1
	maxPort = 65535
)

// Inputs is the complete, pre-resolved input to Build. No I/O happens in Build;
// the home dir and CA path are resolved upstream by Builder.Resolve.
type Inputs struct {
	Config     *config.Config
	Layout     state.Layout
	ProxyPort  int
	HomeDir    string // resolved value of ~ (from PathResolver.UserHomeDir)
	CACertPath string // the mitmproxy CA PEM: ~/.mitmproxy/mitmproxy-ca-cert.pem
}

// Invocation is the constructed safehouse command: a pure function of Inputs.
type Invocation struct {
	Path string   // == Binary
	Args []string // safehouse flags, then "--", then the agent command
	Env  []string // extra KEY=VALUE pairs (sorted) to set on the safehouse process
}

// Build assembles the safehouse argv + env from already-resolved Inputs. It is a
// pure function: deterministic, no I/O. The env is delivered via --env-pass —
// safehouse forwards the named vars from its own (the caller-set) environment into
// the cage on top of its sanitized defaults — so the caller merges Invocation.Env
// onto the inherited environment of the safehouse process before exec.
func Build(in Inputs) (Invocation, error) {
	if in.Config == nil {
		return Invocation{}, errors.New("cage: nil config")
	}
	if len(in.Config.Agent.Command) == 0 {
		return Invocation{}, errors.New("cage: agent.command is empty; nothing to run in the cage")
	}
	if in.ProxyPort < minPort || in.ProxyPort > maxPort {
		return Invocation{}, fmt.Errorf("cage: proxy port %d out of range %d-%d", in.ProxyPort, minPort, maxPort)
	}

	sh := in.Config.Safehouse
	var args []string

	// Mount dirs: colon-separated paths, ~ and relative ("."/sub) expanded here
	// (config keeps them verbatim by design). The real ~/.claude is never added.
	if len(sh.AddDirsRW) > 0 {
		args = append(args, "--add-dirs", expandColonList(sh.AddDirsRW, in))
	}
	if len(sh.AddDirsRO) > 0 {
		args = append(args, "--add-dirs-ro", expandColonList(sh.AddDirsRO, in))
	}
	// Capabilities: comma-separated, =-form (matches docs/design.md and --help).
	if len(sh.Enable) > 0 {
		args = append(args, "--enable="+strings.Join(sh.Enable, ","))
	}
	if wd := in.Config.Agent.Workdir; wd != "" {
		args = append(args, "--workdir", expandPath(wd, in.HomeDir, in.Layout.Canonical))
	}

	// Two append-profiles, in order: the cached deny-all baseline, then the
	// launch-time proxy-port allow fragment (which relies on the baseline preceding
	// it — the ordering contract in profile.RenderProxyFragment).
	args = append(args, "--append-profile", in.Layout.NetworkSB())
	args = append(args, "--append-profile", in.Layout.ProxyProfileSB())

	env := buildEnv(in)
	keys := sortedKeys(env)
	args = append(args, "--env-pass", strings.Join(keys, ","))

	args = append(args, "--")
	args = append(args, in.Config.Agent.Command...)

	return Invocation{Path: Binary, Args: args, Env: kvSlice(env, keys)}, nil
}

// Builder resolves seam-backed inputs and performs cage's two side effects. The
// pure argv+env construction stays in the free function Build.
type Builder struct {
	fs    sysdep.FileSystem
	paths sysdep.PathResolver
}

// New returns a Builder backed by the given seams (mirrors proxy.NewExtractor).
func New(fsys sysdep.FileSystem, paths sysdep.PathResolver) *Builder {
	return &Builder{fs: fsys, paths: paths}
}

// Resolve turns the compiled config, project layout, and live proxy port into a
// pure Inputs. The only I/O is the home-dir lookup (needed to expand ~ and to
// locate the mitmproxy CA PEM).
func (b *Builder) Resolve(cfg *config.Config, layout state.Layout, port int) (Inputs, error) {
	home, err := b.paths.UserHomeDir()
	if err != nil {
		return Inputs{}, fmt.Errorf("cage: locate home dir: %w", err)
	}
	return Inputs{
		Config:     cfg,
		Layout:     layout,
		ProxyPort:  port,
		HomeDir:    home,
		CACertPath: caCertPath(home),
	}, nil
}

// caCertPath is the mitmproxy CA bundle the four CA env vars point at. mitmproxy
// generates it on first run; `agent-creance setup` installs it into the keychain.
func caCertPath(home string) string {
	return filepath.Join(home, ".mitmproxy", "mitmproxy-ca-cert.pem")
}

// Prepare performs cage's side effects before launch:
//
//   - Seeds the ephemeral CLAUDE_CONFIG_DIR with a minimal sanitized settings.json
//     ("{}" — nothing executable by construction). It seeds only when absent: the
//     redirected dir persists across launches under projects/<hash>/claude, so the
//     in-cage agent's own session state (onboarding, theme) is preserved. The
//     security property comes from never copying the real ~/.claude, not from
//     wiping this dir.
//   - (Re)writes the launch-time proxy-port Seatbelt fragment to ProxyProfileSB.
//     Always overwritten because the port is ephemeral and changes per launch.
//
// network.sb is NOT written here — it is the compile step's output (internal/
// profile), guaranteed present before cage runs.
func (b *Builder) Prepare(in Inputs) error {
	dir := in.Layout.ClaudeConfigDir()
	if err := b.fs.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cage: create config dir %q: %w", dir, err)
	}
	settings := filepath.Join(dir, "settings.json")
	if _, err := b.fs.Stat(settings); errors.Is(err, fs.ErrNotExist) {
		if err := b.fs.WriteFile(settings, []byte("{}\n"), 0o600); err != nil {
			return fmt.Errorf("cage: seed %q: %w", settings, err)
		}
	} else if err != nil {
		return fmt.Errorf("cage: stat %q: %w", settings, err)
	}

	frag, err := profile.RenderProxyFragment(in.ProxyPort)
	if err != nil {
		return fmt.Errorf("cage: render proxy fragment: %w", err)
	}
	dst := in.Layout.ProxyProfileSB()
	if err := b.fs.WriteFile(dst, []byte(frag), 0o600); err != nil {
		return fmt.Errorf("cage: write proxy fragment %q: %w", dst, err)
	}
	return nil
}

// buildEnv computes the environment to inject into the cage. Precedence: the user's
// config.env first, then the computed vars overwrite it — so a user cannot disable
// egress filtering or the config redirect by setting, e.g., HTTPS_PROXY themselves.
//
// The proxy + CA set is the one confirmed by spike S4: all proxy vars (upper and
// lower case) point at the loopback proxy; all four CA vars point at the single
// mitmproxy CA PEM. go is intentionally not special-cased (on macOS it trusts the
// CA only via the keychain), and ALL_PROXY is intentionally omitted.
func buildEnv(in Inputs) map[string]string {
	env := make(map[string]string, len(in.Config.Env)+12)
	for k, v := range in.Config.Env {
		env[k] = v
	}

	proxyURL := "http://127.0.0.1:" + strconv.Itoa(in.ProxyPort)
	const noProxy = "localhost,127.0.0.1,::1"
	env["HTTP_PROXY"] = proxyURL
	env["http_proxy"] = proxyURL
	env["HTTPS_PROXY"] = proxyURL
	env["https_proxy"] = proxyURL
	env["NO_PROXY"] = noProxy
	env["no_proxy"] = noProxy

	for _, k := range []string{"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO"} {
		env[k] = in.CACertPath
	}

	// Redirect executable config to the ephemeral, sanitized state-dir config.
	env["CLAUDE_CONFIG_DIR"] = in.Layout.ClaudeConfigDir()

	return env
}

// expandPath resolves a config path: "~"/"~/x" against home; absolute paths are
// cleaned and pass through; relative paths (including ".") resolve against the
// canonical project directory.
func expandPath(p, home, projectDir string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	case filepath.IsAbs(p):
		return filepath.Clean(p)
	default:
		return filepath.Join(projectDir, p)
	}
}

// expandColonList expands each path and joins with ":" (the safehouse --add-dirs*
// separator).
func expandColonList(paths []string, in Inputs) string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = expandPath(p, in.HomeDir, in.Layout.Canonical)
	}
	return strings.Join(out, ":")
}

// sortedKeys returns the env map's keys sorted, for deterministic --env-pass and
// Env ordering.
func sortedKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// kvSlice renders the env map as KEY=VALUE pairs in the given key order.
func kvSlice(env map[string]string, keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
