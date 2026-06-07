// Package doctor orchestrates `agent-creance doctor`: it runs every diagnostic
// (version compatibility, CA trust, proxy health, exposed host services, filesystem
// reliability) and renders a deterministic report. The checks are status-as-data —
// each returns a finding, never aborting the others — so a missing tool degrades to
// a warning rather than crashing the command. Render is pure (golden-tested); the
// side-effecting Checker lives in doctor.go.
package doctor

import (
	"fmt"
	"strings"

	"github.com/tobyS/agent-creance/internal/prereq"
	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// Status glyphs, kept as constants so the golden files and the code agree on exact
// bytes (mirrors internal/prereq).
const (
	glyphOK   = "✓"
	glyphWarn = "⚠"
	glyphMiss = "✗"
)

// Status is a per-section verdict.
type Status int

const (
	// StatusOK is healthy. StatusWarn is a non-fatal warning (exit 0). StatusProblem
	// is an actionable failure (exit non-zero). StatusSkipped means the check could
	// not run (also non-fatal).
	StatusOK Status = iota
	StatusWarn
	StatusProblem
	StatusSkipped
)

// Report is the full doctor result as data; Render turns it into text and
// Actionable derives the exit verdict.
type Report struct {
	Version []prereq.Result // version-compatibility results (rendered via prereq.Report)
	Missing string          // prereq.MissingInstructions(Version); "" when nothing missing
	CA      CASection
	Proxy   ProxySection
	Exposed ExposedSection
	FS      FSSection
}

// CASection is the live CA-trust finding. Detail is the text after the glyph.
type CASection struct {
	State  Status // OK=trusted, Problem=untrusted (actionable), Warn=not-generated/could-not-verify
	Detail string
}

// ProxySection is the current project's proxy health. The rendered line and the
// actionable verdict are both derived from Diag (+ Cleaned) so there is one source
// of truth.
type ProxySection struct {
	Diag    proxy.Diagnosis
	Cleaned *proxy.CleanResult // non-nil when --fix attempted a cleanup
}

// ExposedSection lists host services bound to all interfaces (0.0.0.0 / ::).
type ExposedSection struct {
	State     Status // OK=none, Warn=some listed, Skipped=scan failed
	Listeners []sysdep.Listener
	Detail    string // reason when Skipped
}

// FSSection warns when state/working dirs are on flock-unreliable filesystems.
type FSSection struct {
	State    Status // OK=all reliable, Warn=warnings present
	Warnings []FSWarning
}

// FSWarning is one flock-unreliable path.
type FSWarning struct {
	Label  string // "working directory" / "state cache"
	Path   string
	FSType string // statfs f_fstypename
	Reason string // "network mount" / "iCloud Drive"
}

// Actionable returns the labels of remaining actionable problems — missing
// prerequisites, an untrusted CA, and an orphan proxy that was NOT cleaned. An
// empty result means the command should exit 0.
func (r Report) Actionable() []string {
	var probs []string
	if len(prereq.Missing(r.Version)) > 0 {
		probs = append(probs, "missing prerequisites")
	}
	if r.CA.State == StatusProblem {
		probs = append(probs, "untrusted CA")
	}
	if r.Proxy.Diag.Orphan && (r.Proxy.Cleaned == nil || !r.Proxy.Cleaned.Cleaned) {
		probs = append(probs, "orphan proxy")
	}
	return probs
}

// Render produces the full deterministic doctor report: Version, CA trust, Proxy,
// Exposed host services, Filesystem reliability, then the missing-prerequisite
// install block when applicable.
func Render(r Report) string {
	var b strings.Builder
	b.WriteString(prereq.Report(r.Version))

	b.WriteString("\nCA trust:\n")
	line(&b, caGlyph(r.CA.State), r.CA.Detail)

	b.WriteString("\nProxy (this project):\n")
	renderProxy(&b, r.Proxy)

	b.WriteString("\nExposed host services:\n")
	renderExposed(&b, r.Exposed)

	b.WriteString("\nFilesystem reliability:\n")
	renderFS(&b, r.FS)

	if r.Missing != "" {
		b.WriteString("\n")
		b.WriteString(r.Missing)
	}
	return b.String()
}

func line(b *strings.Builder, glyph, text string) {
	fmt.Fprintf(b, "  %s %s\n", glyph, text)
}

func caGlyph(s Status) string {
	switch s {
	case StatusOK:
		return glyphOK
	case StatusProblem:
		return glyphMiss
	default:
		return glyphWarn
	}
}

func renderProxy(b *strings.Builder, sec ProxySection) {
	d := sec.Diag
	switch {
	case !d.LockPresent:
		line(b, glyphOK, "no proxy state")
	case sec.Cleaned != nil && sec.Cleaned.Cleaned:
		line(b, glyphOK, fmt.Sprintf("cleaned orphan proxy (pid %d)", sec.Cleaned.ProxyPID))
	case d.Orphan:
		line(b, glyphMiss, fmt.Sprintf("orphan proxy (pid %d, port %d) — no live agents; run `doctor --fix`", d.ProxyPID, d.Port))
	case d.Stranded:
		line(b, glyphWarn, fmt.Sprintf("%d attached agent(s) but proxy not reachable on recorded port %d; relaunch them", len(d.LiveAgents), d.Port))
	case d.ProxyUp:
		line(b, glyphOK, fmt.Sprintf("proxy running (pid %d, port %d), %d agent(s) attached", d.ProxyPID, d.Port, len(d.LiveAgents)))
	default:
		line(b, glyphOK, "no active proxy")
	}
}

func renderExposed(b *strings.Builder, sec ExposedSection) {
	switch sec.State {
	case StatusOK:
		line(b, glyphOK, "none on 0.0.0.0")
	case StatusSkipped:
		line(b, glyphWarn, sec.Detail)
	default:
		for _, l := range sec.Listeners {
			line(b, glyphWarn, fmt.Sprintf("%s (pid %d) listening on %s", l.Command, l.PID, l.Address))
		}
	}
}

func renderFS(b *strings.Builder, sec FSSection) {
	if sec.State == StatusOK {
		line(b, glyphOK, "ok")
		return
	}
	for _, w := range sec.Warnings {
		line(b, glyphWarn, fmt.Sprintf("%s (%s) is on %s (%s); file locks may be unreliable", w.Label, w.Path, w.FSType, w.Reason))
	}
}
