// Package cage constructs the agent-safehouse invocation for a compiled config:
// the argv (mount/capability flags, the ordered --append-profile fragments,
// --env-pass, and the wrapped agent command) plus the environment to set on the
// safehouse child process (proxy routing, CA trust, and the user's config.env).
//
// The argv+env construction (Build) is a pure function of (config, port, paths):
// it performs no I/O, so it is golden-testable without launching anything. The
// unavoidable side effects — ensuring the ~/.claude mount target exists and
// writing the launch-time Seatbelt fragments — live on Builder.Prepare, behind
// the sysdep seams.
//
// This package composes Safehouse; it never execs, forwards signals, or manages
// lifecycle (that is AC-0024 / AC-0025). Per the v0.1 posture (AC-0045), it
// mounts the real ~/.claude (and grants ~/.claude.json via claude.sb) read-write
// and does NOT redirect CLAUDE_CONFIG_DIR, so the caged agent uses the host's
// account state and the plain shared Keychain credential (reached via the
// keychain.sb grant). The accepted cost — a prompt-injected agent can plant a
// hook/MCP/skill that fires on a later un-caged Claude run — is documented in
// docs/design.md ("The proxy and the credential story") and tracked as AC-0046.
package cage

import (
	_ "embed"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tobyS/agent-creance/internal/buildinfo"
	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/profile"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// briefingMD is the launch-time cage briefing appended to claude's system prompt
// (AC-0047): the agent learns up front that 470/471 responses are cage policy —
// body-blind clients like WebFetch hide the structured refusal, and an unbriefed
// agent misreads the bare status as the site blocking it. A sibling file so the
// prose is reviewable as markdown.
//
//go:embed briefing.md
var briefingMD string

// Binary is the default agent-safehouse executable name — the preferred
// candidate from buildinfo.SafehouseBinaries (resolved on PATH by the caller
// that execs it; this package only constructs the invocation). Callers that
// have already resolved the installed name (run's prereq check) pass it via
// Inputs.Binary instead.
var Binary = buildinfo.SafehouseBinaries[0]

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
	// Binary is the safehouse executable to invoke — the name the prereq check
	// resolved on PATH, so the launch can never exec a name the check didn't
	// verify. Empty falls back to the default Binary (tests, integration).
	Binary string
	// ExtraDirsRO are additional read-only mounts resolved at launch (e.g. local
	// plugin-marketplace source dirs detected from the Claude config, AC-0056).
	// They are already absolute/canonical, so expandPath leaves them untouched;
	// they are kept separate from Config.Safehouse.AddDirsRO because they are not
	// user config but a derived, per-launch grant.
	ExtraDirsRO []string
	// ConfigFiles is the resolved include graph (the project config plus every
	// transitively-included file and the global baseline), as canonical absolute
	// paths. Prepare emits a Seatbelt fragment denying in-cage write of these so a
	// caged agent cannot edit the config the run-session watcher hot-reloads
	// (AC-0053). Set at launch by run; empty in tests yields a header-only fragment.
	ConfigFiles []string
}

// Invocation is the constructed safehouse command: a pure function of Inputs.
type Invocation struct {
	Path string   // Inputs.Binary, or the default Binary when unset
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
	// (config keeps them verbatim by design).
	//
	// The real ~/.claude is always mounted read-write (AC-0045, the v0.1 config-cage
	// deferral): the caged agent uses the host's global Claude config — skills, hooks,
	// settings — and its session state persists there, exactly as un-caged Claude
	// does. ~/.claude.json (outside this dir) is granted by the claude.sb fragment.
	// Prepare ensures the dir exists before Build's invocation is exec'd.
	rw := append(append([]string{}, sh.AddDirsRW...), filepath.Join(in.HomeDir, ".claude"))
	args = append(args, "--add-dirs", expandColonList(rw, in))
	// Read-only mounts: the user's configured add_dirs_ro plus any launch-resolved
	// extras (local plugin-marketplace dirs, AC-0056).
	ro := append(append([]string{}, sh.AddDirsRO...), in.ExtraDirsRO...)
	if len(ro) > 0 {
		args = append(args, "--add-dirs-ro", expandColonList(ro, in))
	}
	// Capabilities: comma-separated, =-form (matches docs/design.md and --help).
	if len(sh.Enable) > 0 {
		args = append(args, "--enable="+strings.Join(sh.Enable, ","))
	}
	if wd := in.Config.Agent.Workdir; wd != "" {
		args = append(args, "--workdir", expandPath(wd, in.HomeDir, in.Layout.Canonical))
	}

	// Append-profiles, in order: the cached deny-all network baseline, then the
	// launch-time proxy-port allow fragment (which relies on the baseline preceding
	// it — the ordering contract in profile.RenderProxyFragment), then the
	// filesystem/mach fragments (order-independent of the network pair;
	// last-match-wins over safehouse's (deny default) base): the CA read grant
	// (AC-0034), the S2 keychain grant, and the ~/.claude.json grant (AC-0045).
	args = append(args, "--append-profile", in.Layout.NetworkSB())
	args = append(args, "--append-profile", in.Layout.ProxyProfileSB())
	args = append(args, "--append-profile", in.Layout.CAProfileSB())
	args = append(args, "--append-profile", in.Layout.KeychainProfileSB())
	args = append(args, "--append-profile", in.Layout.ClaudeProfileSB())
	// Appended last so its config-write denies win (last-match) over the project's
	// read-write mount: the agent may read but not edit the hot-reloaded config.
	args = append(args, "--append-profile", in.Layout.ConfigProfileSB())
	args = append(args, "--append-profile", in.Layout.BrokerProfileSB())

	env := buildEnv(in)
	keys := sortedKeys(env)
	args = append(args, "--env-pass", strings.Join(keys, ","))

	args = append(args, "--")
	args = append(args, in.Config.Agent.Command...)

	// Cage briefing (AC-0047), injected only when the agent is claude itself:
	// agent.command is arbitrary user config (wrappers, other agents), and an
	// unknown flag would break anything that isn't claude. Appended system-prompt
	// text reaches the main thread only — subagents don't inherit it — which is
	// why the briefing text tells the agent to relay the notice into subagent
	// task prompts.
	if filepath.Base(in.Config.Agent.Command[0]) == "claude" {
		args = append(args, "--append-system-prompt", strings.TrimSpace(briefingMD))
	}

	bin := in.Binary
	if bin == "" {
		bin = Binary
	}
	return Invocation{Path: bin, Args: args, Env: kvSlice(env, keys)}, nil
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
//   - Ensures the real ~/.claude exists (0700) — it is Build's always-on RW mount
//     (AC-0045) and must be present before safehouse is exec'd. Normally `setup`'s
//     skill install (or Claude itself) created it long ago; this is a first-run
//     safety net, never a seed: the dir's contents are the host's, untouched.
//   - (Re)writes the launch-time proxy-port Seatbelt fragment to ProxyProfileSB.
//     Always overwritten because the port is ephemeral and changes per launch.
//   - (Re)writes the CA read-grant Seatbelt fragment to CAProfileSB (AC-0034). The
//     CA path is symlink-resolved first because Seatbelt literals match the kernel's
//     resolved path (macOS firmlinks make /Users/... and /System/Volumes/Data/Users/...
//     the same file); EvalSymlinks failing means the CA does not exist yet (setup not
//     run), in which case the unresolved path is used as a best-effort literal.
//   - (Re)writes the S2 keychain grant to KeychainProfileSB and the ~/.claude.json
//     grant to ClaudeProfileSB (AC-0045), with the home dir symlink-resolved for
//     the same firmlink reason (best-effort fallback to the unresolved path).
//
// network.sb is NOT written here — it is the compile step's output (internal/
// profile), guaranteed present before cage runs.
func (b *Builder) Prepare(in Inputs) error {
	claudeDir := filepath.Join(in.HomeDir, ".claude")
	if err := b.fs.MkdirAll(claudeDir, 0o700); err != nil {
		return fmt.Errorf("cage: create %q: %w", claudeDir, err)
	}

	// The five profile fragments below all live directly under Layout.Root;
	// create it up front rather than relying on an earlier caller (proxy.Attach /
	// compile) having done so.
	if err := b.fs.MkdirAll(in.Layout.Root, 0o700); err != nil {
		return fmt.Errorf("cage: create %q: %w", in.Layout.Root, err)
	}

	frag, err := profile.RenderProxyFragment(in.ProxyPort)
	if err != nil {
		return fmt.Errorf("cage: render proxy fragment: %w", err)
	}
	dst := in.Layout.ProxyProfileSB()
	if err := b.fs.WriteFile(dst, []byte(frag), 0o600); err != nil {
		return fmt.Errorf("cage: write proxy fragment %q: %w", dst, err)
	}

	caPath := in.CACertPath
	if resolved, err := b.paths.EvalSymlinks(caPath); err == nil {
		caPath = resolved
	}
	caFrag, err := profile.RenderCAReadFragment(caPath)
	if err != nil {
		return fmt.Errorf("cage: render CA fragment: %w", err)
	}
	caDst := in.Layout.CAProfileSB()
	if err := b.fs.WriteFile(caDst, []byte(caFrag), 0o600); err != nil {
		return fmt.Errorf("cage: write CA fragment %q: %w", caDst, err)
	}

	home := in.HomeDir
	if resolved, err := b.paths.EvalSymlinks(home); err == nil {
		home = resolved
	}
	kcFrag, err := profile.RenderKeychainFragment(home)
	if err != nil {
		return fmt.Errorf("cage: render keychain fragment: %w", err)
	}
	kcDst := in.Layout.KeychainProfileSB()
	if err := b.fs.WriteFile(kcDst, []byte(kcFrag), 0o600); err != nil {
		return fmt.Errorf("cage: write keychain fragment %q: %w", kcDst, err)
	}
	csFrag, err := profile.RenderClaudeStateFragment(home)
	if err != nil {
		return fmt.Errorf("cage: render claude-state fragment: %w", err)
	}
	csDst := in.Layout.ClaudeProfileSB()
	if err := b.fs.WriteFile(csDst, []byte(csFrag), 0o600); err != nil {
		return fmt.Errorf("cage: write claude-state fragment %q: %w", csDst, err)
	}

	// Deny in-cage write of the source config + include graph (AC-0053). The paths
	// are already canonical (loader.ResolveFiles symlink-resolves them), matching
	// the kernel-resolved paths Seatbelt literals compare against.
	cfgFrag, err := profile.RenderConfigReadOnlyFragment(in.ConfigFiles)
	if err != nil {
		return fmt.Errorf("cage: render config read-only fragment: %w", err)
	}
	cfgDst := in.Layout.ConfigProfileSB()
	if err := b.fs.WriteFile(cfgDst, []byte(cfgFrag), 0o600); err != nil {
		return fmt.Errorf("cage: write config read-only fragment %q: %w", cfgDst, err)
	}

	// Deny the credential broker's socket (AC-0069b). Written unconditionally, even
	// for a project that injects nothing: the deny is what guarantees the cage can
	// never reach a broker, and making it conditional on the current config would
	// make that guarantee depend on the config the agent can see.
	brFrag, err := profile.RenderBrokerDenyFragment(in.Layout.BrokerSock())
	if err != nil {
		return fmt.Errorf("cage: render broker deny fragment: %w", err)
	}
	brDst := in.Layout.BrokerProfileSB()
	if err := b.fs.WriteFile(brDst, []byte(brFrag), 0o600); err != nil {
		return fmt.Errorf("cage: write broker deny fragment %q: %w", brDst, err)
	}
	return nil
}

// buildEnv computes the environment to inject into the cage. Precedence: the user's
// config.env first, then the computed vars overwrite it — so a user cannot disable
// egress filtering by setting, e.g., HTTPS_PROXY themselves.
//
// The proxy + CA set is the one confirmed by spike S4: all proxy vars (upper and
// lower case) point at the loopback proxy; all four CA vars point at the single
// mitmproxy CA PEM. That PEM is made readable in-cage by the CA read-grant fragment
// (AC-0034; see Prepare / profile.RenderCAReadFragment) — without it ~/.mitmproxy is
// denied and env-var-CA clients (node, python) cannot trust the proxy. go is
// intentionally not special-cased (on macOS it trusts the CA only via the keychain),
// and ALL_PROXY is intentionally omitted.
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

	// CLAUDE_CONFIG_DIR is deliberately NOT set (AC-0045): the caged agent uses the
	// real ~/.claude, and redirecting the var would change the Keychain service name
	// Claude Code derives, breaking the shared-credential lookup. A host-set value
	// cannot leak in either — safehouse only forwards the --env-pass keys.

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
