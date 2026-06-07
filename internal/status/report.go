package status

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/tobyS/agent-creance/internal/proxy"
)

// Report is the full `status` result as data; Render turns it into the table.
type Report struct {
	// Projects are the projects with recorded proxy state, sorted by Hash.
	Projects []ProjectStatus
}

// ProjectStatus is one project's row: its identity plus the proxy diagnosis.
type ProjectStatus struct {
	// Hash is the project state-dir name (used as the display fallback when the
	// lock has no recorded canonical path).
	Hash string
	// Diag is the read-only proxy diagnosis (state, port, live agents, path).
	Diag proxy.Diagnosis
}

// Render produces the deterministic status table: one row per project showing the
// project directory (the recorded canonical path, falling back to the state-dir
// hash), its state, the proxy port, and the live attached-agent count. When no
// project has running proxy state it prints a single "No active cages." line.
func Render(r Report) string {
	if len(r.Projects) == 0 {
		return "No active cages.\n"
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tSTATE\tPORT\tAGENTS")
	for _, p := range r.Projects {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\n",
			project(p), stateLabel(p.Diag), portLabel(p.Diag), len(p.Diag.LiveAgents))
	}
	_ = w.Flush()
	return b.String()
}

// project is the display identity: the recorded project directory when known,
// else the opaque state-dir hash.
func project(p ProjectStatus) string {
	if p.Diag.CanonicalPath != "" {
		return p.Diag.CanonicalPath
	}
	return p.Hash
}

// stateLabel maps a Diagnosis to a single-word state, matching the lifecycle's own
// notion of "up". The order mirrors doctor's renderProxy precedence.
func stateLabel(d proxy.Diagnosis) string {
	switch {
	case d.Orphan:
		return "orphan"
	case d.Stranded:
		return "stranded"
	case d.ProxyUp:
		return "running"
	default:
		return "down"
	}
}

// portLabel shows the port for a reachable proxy and "-" otherwise (a stranded or
// down proxy is not listening on the recorded port).
func portLabel(d proxy.Diagnosis) string {
	if d.ProxyUp {
		return strconv.Itoa(d.Port)
	}
	return "-"
}
