package status

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/style"
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
//
// With color disabled it uses text/tabwriter exactly as before (byte-identical
// plain output). With color enabled tabwriter cannot be used — it counts escape
// bytes as column width and would misalign — so a manual visible-width layout
// renders the same columns with the state word colored and the port dimmed.
func Render(r Report, sty *style.Styler) string {
	if len(r.Projects) == 0 {
		return "No active cages.\n"
	}
	if sty.Enabled() {
		return renderColor(r, sty)
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

// renderColor lays the table out by hand so escape bytes never reach a width
// computation: column widths come from the plain cell text, then the colored
// content is slotted in and padded by the plain width. The first three columns
// are padded (2-space gap, matching tabwriter); AGENTS is trailing, unpadded.
func renderColor(r Report, sty *style.Styler) string {
	const gap = 2
	projW, stateW, portW := len("PROJECT"), len("STATE"), len("PORT")
	for _, p := range r.Projects {
		projW = max(projW, len(project(p)))
		stateW = max(stateW, len(stateLabel(p.Diag)))
		portW = max(portW, len(portLabel(p.Diag)))
	}
	pad := func(s string, w int) string { return strings.Repeat(" ", w-len(s)+gap) }

	var b strings.Builder
	b.WriteString(sty.Header("PROJECT") + pad("PROJECT", projW))
	b.WriteString(sty.Header("STATE") + pad("STATE", stateW))
	b.WriteString(sty.Header("PORT") + pad("PORT", portW))
	b.WriteString(sty.Header("AGENTS") + "\n")
	for _, p := range r.Projects {
		proj, state, port := project(p), stateLabel(p.Diag), portLabel(p.Diag)
		b.WriteString(proj + pad(proj, projW))
		b.WriteString(colorState(state, sty) + pad(state, stateW))
		b.WriteString(sty.Dim(port) + pad(port, portW))
		fmt.Fprintf(&b, "%d\n", len(p.Diag.LiveAgents))
	}
	return b.String()
}

// colorState maps a state word to its semantic color (matching doctor's verdict
// colors): running green, orphan red, stranded yellow, down dimmed.
func colorState(state string, sty *style.Styler) string {
	switch state {
	case "running":
		return sty.OK(state)
	case "orphan":
		return sty.Bad(state)
	case "stranded":
		return sty.Warn(state)
	default:
		return sty.Dim(state)
	}
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
	case d.BrokerDown:
		// Running, but injected hosts 472 until the session restarts.
		return "broker-down"
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
