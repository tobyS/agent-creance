// Package profile generates the cage's network Seatbelt (SBPL) profile: an
// --append-profile fragment that narrows agent-safehouse's "network open by default"
// base to a deny-all baseline plus narrow per-port allows for whitelisted host
// services and the mitmproxy.
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
	denyBaseline = "(deny network*)"
	proxyHeader  = ";; agent-creance proxy fragment — live ephemeral proxy port; regenerated per launch.\n" +
		";; Relies on network.sb's (deny network*) being appended BEFORE this fragment (AC-0023).\n"
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
	b.WriteString(denyBaseline)
	b.WriteByte('\n')
	for _, svc := range dedupeByPort(services) {
		b.WriteString(allowRule(svc.Port))
		if svc.Label != "" {
			b.WriteString("  ;; ")
			b.WriteString(svc.Label)
		}
		b.WriteByte('\n')
	}
	return b.String()
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
