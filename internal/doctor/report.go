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
	"github.com/tobyS/agent-creance/internal/style"
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
	Cred    CredSection
	Proxy   ProxySection
	Exposed ExposedSection
	FS      FSSection
}

// CASection is the live CA-trust finding. Detail is the text after the glyph.
type CASection struct {
	State  Status // OK=trusted, Problem=untrusted (actionable), Warn=not-generated/could-not-verify
	Detail string
}

// CredSection is the host Claude-credential finding. Detail is the text after the glyph
// (cred.Result.Message() for the non-OK cases, "reachable" for OK).
type CredSection struct {
	State  Status // OK=reachable, Problem=locked/file-fallback (actionable), Warn=missing/could-not-check
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
// prerequisites, an untrusted CA, an unavailable credential (locked keychain or
// unsupported file-based credential), and an orphan proxy that was NOT cleaned. An
// empty result means the command should exit 0.
func (r Report) Actionable() []string {
	var probs []string
	if len(prereq.Missing(r.Version)) > 0 {
		probs = append(probs, "missing prerequisites")
	}
	if r.CA.State == StatusProblem {
		probs = append(probs, "untrusted CA")
	}
	if r.Cred.State == StatusProblem {
		probs = append(probs, "credential unavailable")
	}
	if r.Proxy.Diag.Orphan && (r.Proxy.Cleaned == nil || !r.Proxy.Cleaned.Cleaned) {
		probs = append(probs, "orphan proxy")
	}
	return probs
}

// Render produces the full deterministic doctor report: Version, CA trust, Proxy,
// Exposed host services, Filesystem reliability, then the missing-prerequisite
// install block when applicable.
func Render(r Report, sty *style.Styler) string {
	var b strings.Builder
	b.WriteString(prereq.Report(r.Version, sty))

	b.WriteString("\n" + sty.Header("CA trust:") + "\n")
	line(&b, stateGlyph(r.CA.State, sty), r.CA.Detail)

	b.WriteString("\n" + sty.Header("Credential:") + "\n")
	line(&b, stateGlyph(r.Cred.State, sty), r.Cred.Detail)

	b.WriteString("\n" + sty.Header("Proxy (this project):") + "\n")
	renderProxy(&b, r.Proxy, sty)

	b.WriteString("\n" + sty.Header("Exposed host services:") + "\n")
	renderExposed(&b, r.Exposed, sty)

	b.WriteString("\n" + sty.Header("Filesystem reliability:") + "\n")
	renderFS(&b, r.FS, sty)

	if r.Missing != "" {
		b.WriteString("\n")
		b.WriteString(r.Missing)
	}
	return b.String()
}

func line(b *strings.Builder, glyph, text string) {
	fmt.Fprintf(b, "  %s %s\n", glyph, text)
}

// stateGlyph maps a section Status to its styled glyph. Shared by the CA and
// Credential sections (and any other simple State+Detail finding): OK→✓, Problem→✗,
// Warn/Skipped→⚠.
func stateGlyph(s Status, sty *style.Styler) string {
	switch s {
	case StatusOK:
		return sty.OK(glyphOK)
	case StatusProblem:
		return sty.Bad(glyphMiss)
	default:
		return sty.Warn(glyphWarn)
	}
}

func renderProxy(b *strings.Builder, sec ProxySection, sty *style.Styler) {
	d := sec.Diag
	switch {
	case !d.LockPresent:
		line(b, sty.OK(glyphOK), "no proxy state")
	case sec.Cleaned != nil && sec.Cleaned.Cleaned:
		line(b, sty.OK(glyphOK), "cleaned orphan proxy "+sty.Dim(fmt.Sprintf("(pid %d)", sec.Cleaned.ProxyPID)))
	case d.Orphan:
		line(b, sty.Bad(glyphMiss), "orphan proxy "+sty.Dim(fmt.Sprintf("(pid %d, port %d)", d.ProxyPID, d.Port))+" — no live agents; run `doctor --fix`")
	case d.Stranded:
		line(b, sty.Warn(glyphWarn), fmt.Sprintf("%d attached agent(s) but proxy not reachable on recorded port ", len(d.LiveAgents))+sty.Dim(fmt.Sprintf("%d", d.Port))+"; relaunch them")
	case d.BrokerDown:
		// The cage still runs and non-injected hosts still work; only injected ones
		// answer 472. Worth saying out loud, because a 472 on its own cannot tell the
		// user a dead broker from a locked secret store.
		line(b, sty.Warn(glyphWarn), "proxy running "+sty.Dim(fmt.Sprintf("(pid %d, port %d)", d.ProxyPID, d.Port))+
			" but the credential broker "+sty.Dim(fmt.Sprintf("(pid %d)", d.BrokerPID))+
			" is gone — injected hosts answer 472; restart the session")
	case d.ProxyUp:
		line(b, sty.OK(glyphOK), "proxy running "+sty.Dim(fmt.Sprintf("(pid %d, port %d)", d.ProxyPID, d.Port))+fmt.Sprintf(", %d agent(s) attached", len(d.LiveAgents)))
	default:
		line(b, sty.OK(glyphOK), "no active proxy")
	}
}

func renderExposed(b *strings.Builder, sec ExposedSection, sty *style.Styler) {
	switch sec.State {
	case StatusOK:
		line(b, sty.OK(glyphOK), "none on 0.0.0.0")
	case StatusSkipped:
		line(b, sty.Warn(glyphWarn), sec.Detail)
	default:
		for _, l := range sec.Listeners {
			line(b, sty.Warn(glyphWarn), fmt.Sprintf("%s ", l.Command)+sty.Dim(fmt.Sprintf("(pid %d)", l.PID))+fmt.Sprintf(" listening on %s", l.Address))
		}
	}
}

func renderFS(b *strings.Builder, sec FSSection, sty *style.Styler) {
	if sec.State == StatusOK {
		line(b, sty.OK(glyphOK), "ok")
		return
	}
	for _, w := range sec.Warnings {
		line(b, sty.Warn(glyphWarn), fmt.Sprintf("%s ", w.Label)+sty.Dim("("+w.Path+")")+fmt.Sprintf(" is on %s ", w.FSType)+sty.Dim("("+w.Reason+")")+"; file locks may be unreliable")
	}
}
