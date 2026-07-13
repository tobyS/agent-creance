// Package profile generates the cage's Seatbelt (SBPL) --append-profile fragments.
//
// The primary fragment is the network profile: it narrows agent-safehouse's
// "network open by default" base to a deny-all baseline plus narrow per-port allows
// for whitelisted host services and the mitmproxy. A second, filesystem fragment
// (RenderCAReadFragment, AC-0034) re-opens read of exactly the one mitmproxy CA PEM
// so env-var-CA clients (node, python) trust the proxy in-cage — agent-safehouse's
// base denies ~/.mitmproxy, where the four injected CA env vars point.
//
// Rule form (per spikes S3/AC-0003 and S5/AC-0005): each allowed port is emitted as
//
//	(allow network-outbound (remote tcp "localhost:<port>"))
//
// Apple's Seatbelt rejects literal-IP host tokens — (remote tcp "127.0.0.1:<port>")
// does not compile ("host must be * or localhost") — and the literal `*` host token
// would permit external egress on that port. So the compiler uses the `localhost`
// token (which spans both IPv4 127.0.0.1 and IPv6 ::1) and lets the port be the
// discriminator; it must never emit `*:<port>`. The port-level guarantee is
// family-agnostic: a non-allowlisted port is refused over both v4 and v6.
//
// The fragment carries no (version 1)/(deny default) header — it is appended after
// safehouse's base, which already declares those. Order is load-bearing: (deny
// network*) first, then the specific allows, so Seatbelt's last-match-wins precedence
// reopens only the named ports.
package profile

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tobyS/agent-creance/internal/config"
)

const (
	minPort = 1
	maxPort = 65535
)

const (
	networkHeader = ";; agent-creance network.sb — appended after safehouse's base via --append-profile (AC-0023).\n" +
		";; Deny-all network baseline; re-open only allowlisted host-service ports. Generated; do not edit.\n"
	// DenyBaseline is the deny-all network line emitted first in every network.sb.
	// Exported so the AC-0033 cage-verification negative control can strip it to
	// build a deliberately-weakened profile without hardcoding the literal.
	DenyBaseline = "(deny network*)"
	proxyHeader  = ";; agent-creance proxy fragment — live ephemeral proxy port; regenerated per launch.\n" +
		";; Relies on network.sb's (deny network*) being appended BEFORE this fragment (AC-0023).\n"
	caHeader = ";; agent-creance ca.sb — appended after safehouse's base via --append-profile (AC-0034).\n" +
		";; Re-open read of exactly the one mitmproxy CA PEM so env-var-CA clients (node,\n" +
		";; python) trust the proxy in-cage; the sibling CA private key stays unreadable.\n" +
		";; Generated; do not edit.\n"
	keychainHeader = ";; agent-creance keychain.sb — appended after safehouse's base via --append-profile (AC-0045).\n" +
		";; The grant for the shared Claude Code-credentials login-Keychain item: securityd\n" +
		";; reachability, plus file-level RW on the login keychain db (and its -wal/-shm\n" +
		";; sidecars) and the AtomicFile .fl* lock files — the legacy SecKeychain stack\n" +
		";; (security CLI, keytar) opens, locks, and rewrites these client-side. Nothing broader.\n" +
		";; Generated; do not edit.\n"
	claudeStateHeader = ";; agent-creance claude.sb — appended after safehouse's base via --append-profile (AC-0045).\n" +
		";; v0.1 config-cage deferral (AC-0046): the caged agent uses the host's real Claude\n" +
		";; account state. ~/.claude is a --add-dirs RW mount; this fragment grants the\n" +
		";; file-level RW on ~/.claude.json* (the prefix regex also covers Claude's sibling\n" +
		";; writes such as .claude.json.backup). Generated; do not edit.\n"
	configROHeader = ";; agent-creance config-ro.sb — appended after safehouse's base via --append-profile (AC-0053).\n" +
		";; Deny in-cage write of the project's source config + its include graph. The project\n" +
		";; is mounted read-write, and the run-session watcher recompiles the egress policy on\n" +
		";; any edit to these files; without this deny a prompt-injected agent could widen its\n" +
		";; own egress by editing them. Read stays allowed; only write is denied (last-match-\n" +
		";; wins over safehouse's RW grant). Generated; do not edit.\n"
	brokerHeader = ";; agent-creance broker.sb — appended after safehouse's base via --append-profile (AC-0069b).\n" +
		";; The credential broker serves every injected token on this unix socket. It is\n" +
		";; host-side only: the socket lives under ~/.cache/agent-creance, which is never\n" +
		";; mounted into the cage, and (deny network*) already covers a unix-socket connect.\n" +
		";; These denies are the belt to that suspenders — they survive a future change that\n" +
		";; mounts a broader path in, and they make the guarantee explicit rather than\n" +
		";; incidental. A cage that could reach this socket would be the IMDS-style token\n" +
		";; endpoint the whole design exists to avoid. Generated; do not edit.\n"
)

// allowRule renders one outbound allow for the loopback at the given port. The host
// token is always `localhost` (the only buildable loopback token; see package doc).
func allowRule(port int) string {
	return fmt.Sprintf("(allow network-outbound (remote tcp %q))", "localhost:"+strconv.Itoa(port))
}

// RenderNetworkSB renders the network.sb append fragment: the deny-all baseline plus
// one outbound allow per host service, deduped by port (first label wins for the
// trailing comment) and otherwise in input order. With no services it is just the
// header and the deny baseline. The result ends with a trailing newline.
func RenderNetworkSB(services []config.HostService) string {
	var b strings.Builder
	b.WriteString(networkHeader)
	b.WriteString(DenyBaseline)
	b.WriteByte('\n')
	for _, svc := range dedupeByPort(services) {
		b.WriteString(allowRule(svc.Port))
		if svc.Label != "" {
			b.WriteString("  ;; ")
			b.WriteString(sanitizeLabel(svc.Label))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// sanitizeLabel neutralizes a host-service label before it is written into network.sb
// after a ";; " comment marker. Config parsing already rejects control characters
// (internal/config.parseHostService), but this render-side pass is defense in depth: a
// control character — most dangerously a newline — would terminate the comment line and
// let the remainder render as a live SBPL form after the (deny network*) baseline,
// re-opening egress (last-match-wins). Any control char is replaced with a space so the
// label can only ever extend its own comment (AC-0058 / F1).
func sanitizeLabel(label string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, label)
}

// RenderProxyFragment renders the launch-time proxy-port allow line for the live,
// ephemeral proxy port. It deliberately omits its own (deny network*): it relies on
// network.sb being appended before it (the ordering contract for AC-0023). The port is
// range-checked as defence in depth (it originates from the lock file, not config).
func RenderProxyFragment(port int) (string, error) {
	if port < minPort || port > maxPort {
		return "", fmt.Errorf("profile: proxy port %d out of range %d-%d", port, minPort, maxPort)
	}
	return proxyHeader + allowRule(port) + "\n", nil
}

// RenderBrokerDenyFragment renders the broker.sb append fragment: an explicit deny
// of the credential broker's unix socket, both as a connect target and as a file.
//
// On macOS a unix-socket connect(2) is governed by the network-outbound operation
// filtered by path literal (the form Chromium's own sandbox profile uses), so the
// deny has to name the operation, not just the file. sockPath must be absolute; it
// is sanitized like a host-service label, so a control character cannot terminate
// the comment block and smuggle a live SBPL form past the deny (AC-0058 / F1).
func RenderBrokerDenyFragment(sockPath string) (string, error) {
	if sockPath == "" {
		return "", fmt.Errorf("profile: empty broker socket path")
	}
	if !filepath.IsAbs(sockPath) {
		return "", fmt.Errorf("profile: broker socket path %q is not absolute", sockPath)
	}
	clean := sanitizeLabel(sockPath)
	var b strings.Builder
	b.WriteString(brokerHeader)
	fmt.Fprintf(&b, "(deny network-outbound (literal %q))\n", clean)
	fmt.Fprintf(&b, "(deny file-read* file-write* (literal %q))\n", clean)
	return b.String(), nil
}

// RenderCAReadFragment renders the ca.sb append fragment: a read grant for exactly the
// one mitmproxy CA PEM, plus a metadata-only grant on its parent directory so the file
// is reachable for open() under safehouse's (deny default) base. The metadata grant
// exposes the directory's entries' existence — NOT the contents of any sibling file, so
// the CA private key (mitmproxy-ca.pem) in the same dir stays unreadable. caCertPath
// must be an absolute, symlink-resolved path (Seatbelt literals match the resolved path;
// see internal/cage Prepare). The result ends with a trailing newline.
func RenderCAReadFragment(caCertPath string) (string, error) {
	if caCertPath == "" {
		return "", fmt.Errorf("profile: empty CA cert path")
	}
	if !filepath.IsAbs(caCertPath) {
		return "", fmt.Errorf("profile: CA cert path %q is not absolute", caCertPath)
	}
	dir := filepath.Dir(caCertPath)
	var b strings.Builder
	b.WriteString(caHeader)
	fmt.Fprintf(&b, "(allow file-read-metadata (literal %q))\n", dir)
	fmt.Fprintf(&b, "(allow file-read* (literal %q))\n", caCertPath)
	return b.String(), nil
}

// RenderKeychainFragment renders the keychain.sb append fragment: the grant that
// lets the caged agent read and refresh the one shared login-Keychain credential
// item (spike S2, 2026-06-04-s2-keychain.md; completed 2026-07-04 — the spike's
// baseline profile allowed ALL file reads, which masked the legacy stack's
// client-side read/lock needs):
//
//   - (allow mach-lookup (global-name "com.apple.SecurityServer")) — securityd
//     reachability; unlock/decrypt are brokered, and the modern SecItem API
//     needs nothing more;
//   - (allow file-read* file-write* (regex #"^<home>/Library/Keychains/(login\.keychain-db|\.fl)"))
//     — the legacy SecKeychain stack (/usr/bin/security, keytar) opens the
//     keychain db client-side: it reads the db bytes to find the search-list
//     entry and the item, rewrites the db atomically (create+rename+unlink of
//     login.keychain-db and its -wal/-shm sidecars — the first name prefix), and
//     creates+flocks the AtomicFile ".fl<hash>" lock files in the same directory
//     (the second prefix). Metadata-only read is not enough; each piece was
//     verified necessary in-cage by the AC-0033 battery's kc-read/kc-write
//     vectors.
//
// home must be an absolute, symlink-resolved home directory (Seatbelt regexes
// match the kernel-resolved path; see internal/cage Prepare). The path is
// regex-escaped before interpolation. The result ends with a trailing newline.
func RenderKeychainFragment(home string) (string, error) {
	if err := validateHome(home); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(keychainHeader)
	b.WriteString(`(allow mach-lookup (global-name "com.apple.SecurityServer"))` + "\n")
	fmt.Fprintf(&b, "(allow file-read* file-write* (regex #\"^%s/Library/Keychains/(login\\.keychain-db|\\.fl)\"))\n",
		sbplRegexEscape(home))
	return b.String(), nil
}

// RenderClaudeStateFragment renders the claude.sb append fragment: file-level
// read-write on ~/.claude.json and its prefix-named siblings (.claude.json.backup
// etc.), so the caged agent uses the host's real account state (the v0.1
// config-cage deferral, AC-0046). The metadata-only grant on the home dir literal
// makes the file reachable for open() under safehouse's deny-default base — it
// exposes the existence of home's entries, never the contents of any sibling
// file (same trade as ca.sb's parent-dir grant). home must be an absolute,
// symlink-resolved home directory. The result ends with a trailing newline.
func RenderClaudeStateFragment(home string) (string, error) {
	if err := validateHome(home); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(claudeStateHeader)
	fmt.Fprintf(&b, "(allow file-read-metadata (literal %q))\n", home)
	fmt.Fprintf(&b, "(allow file-read* file-write* (regex #\"^%s/\\.claude\\.json\"))\n",
		sbplRegexEscape(home))
	return b.String(), nil
}

// RenderConfigReadOnlyFragment renders the config-ro.sb append fragment: one
// (deny file-write* (literal "<path>")) per resolved config file, so the caged
// agent can read but not modify the project config or any file in its include
// graph (AC-0053). Each path must be absolute (Seatbelt literals match the
// kernel-resolved path, so callers pass symlink-resolved paths — the loader's
// ResolveFiles already canonicalises them). Paths are deduplicated, preserving
// first-seen order. A non-absolute path is rejected. With no paths the result is
// just the header. The result ends with a trailing newline.
func RenderConfigReadOnlyFragment(paths []string) (string, error) {
	var b strings.Builder
	b.WriteString(configROHeader)
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			return "", fmt.Errorf("profile: config path %q is not absolute", p)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		fmt.Fprintf(&b, "(deny file-write* (literal %q))\n", p)
	}
	return b.String(), nil
}

// validateHome rejects the home-dir inputs the fragment renderers cannot safely
// interpolate: empty or non-absolute paths.
func validateHome(home string) error {
	if home == "" {
		return fmt.Errorf("profile: empty home dir")
	}
	if !filepath.IsAbs(home) {
		return fmt.Errorf("profile: home dir %q is not absolute", home)
	}
	return nil
}

// sbplRegexEscape escapes a literal path for interpolation into an SBPL regex so
// metacharacters in directory names (e.g. /Users/j.doe) match literally.
func sbplRegexEscape(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch r {
		case '\\', '.', '+', '*', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// dedupeByPort drops repeated ports, preserving first-seen order, so two host_services
// entries on the same port (e.g. differing only by label) yield a single allow rule.
func dedupeByPort(services []config.HostService) []config.HostService {
	seen := make(map[int]bool, len(services))
	out := make([]config.HostService, 0, len(services))
	for _, svc := range services {
		if seen[svc.Port] {
			continue
		}
		seen[svc.Port] = true
		out = append(out, svc)
	}
	return out
}
