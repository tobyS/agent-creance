package progress

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// glyphOK matches the doctor/prereq report glyph. Kept as a constant so the
// tests and the code agree on exact bytes.
const glyphOK = "✓"

// indent prefixes lines nested under a step, matching the two-space indent the
// report renderers use.
const indent = "  "

// Printer renders progress events as human-readable lines on one writer (run
// wires stderr). In interactive mode (the writer is a terminal) the current
// step line and the per-manifest lookup counter are rewritten in place with
// \r and space-padding — no ANSI escapes, the codebase uses none. In
// non-interactive mode (pipe, CI log) every write is an appended full line and
// the counter degrades to milestone lines at roughly 25/50/75%.
//
// Printer implements Reporter for the compiler-emitted events and adds the
// step-level methods (StepStart, StepDone, Line) the run command calls
// directly. Durations are measured through the injected sysdep.Clock. All
// methods must be called from a single goroutine.
type Printer struct {
	w           io.Writer
	clock       sysdep.Clock
	interactive bool

	// openWidth is the rune width the current in-place line occupies on
	// screen (0 = no open line). Rewrites pad with spaces up to this width to
	// cover residue from a longer previous render.
	openWidth   int
	counterOpen bool

	stepActive bool
	stepStart  time.Time
	stepDirty  bool // other output intervened since StepStart

	manifest       ManifestRef
	manifestStart  time.Time
	manifestCached bool
	lookups        int
	milestones     []int // pending milestone indices (non-interactive)
}

var _ Reporter = (*Printer)(nil)

// NewPrinter returns a Printer writing to w, timing via clock. interactive
// selects in-place \r rendering and should reflect whether w is a terminal.
func NewPrinter(w io.Writer, clock sysdep.Clock, interactive bool) *Printer {
	return &Printer{w: w, clock: clock, interactive: interactive}
}

// StepStart announces a step ("text…"). Interactive: the line stays open so
// StepDone can complete it in place unless other output intervenes first.
func (p *Printer) StepStart(text string) {
	p.interrupt()
	p.stepActive = true
	p.stepStart = p.clock.Now()
	p.stepDirty = false
	if p.interactive {
		p.openLine(text + "…")
	} else {
		fmt.Fprintln(p.w, text+"…")
	}
}

// StepDone completes the current step as "✓ text (duration)", rewriting the
// open step line in place when nothing intervened (interactive) and appending
// a line otherwise.
func (p *Printer) StepDone(text string) {
	line := fmt.Sprintf("%s %s (%s)", glyphOK, text, formatDuration(p.clock.Since(p.stepStart)))
	if p.interactive && !p.stepDirty && p.openWidth > 0 {
		p.replaceLine(line)
	} else {
		p.endOpenLine()
		fmt.Fprintln(p.w, line)
	}
	p.stepActive = false
}

// Line prints a plain announcement line (e.g. "Launching agent…").
func (p *Printer) Line(text string) {
	p.interrupt()
	fmt.Fprintln(p.w, text)
}

// Close terminates any open in-place line so a following writer (the error:
// line, the agent's own output) starts on a fresh line. Idempotent.
func (p *Printer) Close() {
	p.endOpenLine()
}

// BuildStart implements Reporter: the expectation message explaining why the
// compile will take time and that the cost is a one-time one.
func (p *Printer) BuildStart(manifests []ManifestRef) {
	p.interrupt()
	fmt.Fprintf(p.w, "%sinputs changed (first run or updated config/manifest) — fetching package metadata from %s; results are cached for future runs\n",
		indent, strings.Join(registryNames(manifests), "/"))
}

// ManifestStart implements Reporter.
func (p *Printer) ManifestStart(m ManifestRef) {
	p.interrupt()
	p.manifest = m
	p.manifestStart = p.clock.Now()
	p.manifestCached = false
	p.lookups = 0
	p.milestones = nil
}

// LookupsStart implements Reporter: opens the in-place counter (interactive)
// or announces the walk and arms the milestone indices (non-interactive).
func (p *Printer) LookupsStart(n int) {
	p.lookups = n
	if p.interactive {
		p.writeCounter(0, n)
		return
	}
	fmt.Fprintf(p.w, "%s%s: looking up %s…\n", indent, p.manifest.Path, countPackages(n))
	p.milestones = milestoneIndices(n)
}

// LookupDone implements Reporter: rewrites the counter in place (interactive)
// or emits the next pending milestone line (non-interactive).
func (p *Printer) LookupDone(i, n int) {
	if p.interactive {
		p.writeCounter(i, n)
		return
	}
	for len(p.milestones) > 0 && i >= p.milestones[0] {
		fmt.Fprintf(p.w, "%s%s: %d/%d…\n", indent, p.manifest.Path, i, n)
		p.milestones = p.milestones[1:]
	}
}

// ManifestCached implements Reporter.
func (p *Printer) ManifestCached() {
	p.manifestCached = true
}

// ManifestDone implements Reporter: the per-manifest "✓ path: detail
// (duration)" line, replacing the open counter when one is on screen.
func (p *Printer) ManifestDone() {
	detail := countPackages(p.lookups)
	if p.manifestCached {
		detail = "rules cached"
	}
	line := fmt.Sprintf("%s%s %s: %s (%s)", indent, glyphOK, p.manifest.Path, detail,
		formatDuration(p.clock.Since(p.manifestStart)))
	if p.counterOpen {
		p.replaceLine(line)
	} else {
		fmt.Fprintln(p.w, line)
	}
}

// writeCounter opens or rewrites the in-place per-manifest counter line.
func (p *Printer) writeCounter(i, n int) {
	s := fmt.Sprintf("%s%s: looking up %d/%d packages…", indent, p.manifest.Path, i, n)
	if p.counterOpen {
		p.rewrite(s)
		return
	}
	p.interrupt()
	p.openLine(s)
	p.counterOpen = true
}

// interrupt makes room for output that is not an in-place update of the open
// line: it terminates the open line and marks the active step as dirty so its
// completion is appended rather than rewritten over the intervening lines.
func (p *Printer) interrupt() {
	if p.stepActive {
		p.stepDirty = true
	}
	p.endOpenLine()
}

func (p *Printer) openLine(s string) {
	fmt.Fprint(p.w, s)
	p.openWidth = utf8.RuneCountInString(s)
}

// rewrite redraws the open line in place, space-padding to cover residue from
// a longer previous render.
func (p *Printer) rewrite(s string) {
	w := utf8.RuneCountInString(s)
	pad := p.openWidth - w
	if pad < 0 {
		pad = 0
	}
	fmt.Fprint(p.w, "\r", s, strings.Repeat(" ", pad))
	if w > p.openWidth {
		p.openWidth = w
	}
}

// replaceLine rewrites the open line with s and terminates it.
func (p *Printer) replaceLine(s string) {
	p.rewrite(s)
	fmt.Fprintln(p.w)
	p.openWidth = 0
	p.counterOpen = false
}

func (p *Printer) endOpenLine() {
	if p.openWidth > 0 {
		fmt.Fprintln(p.w)
		p.openWidth = 0
		p.counterOpen = false
	}
}

// registryNames maps the manifests' generator types to the registries they
// query, first-seen order, deduplicated.
func registryNames(manifests []ManifestRef) []string {
	var out []string
	seen := make(map[string]bool)
	for _, m := range manifests {
		name := registryName(m.Type)
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// registryName names the registry a generator type queries. The mapping is
// duplicated from the generator package on purpose: progress is a leaf package
// the generator itself imports, so it cannot import generator back. An unknown
// type falls back to its own name, which stays truthful if a generator is
// added without updating this table.
func registryName(typ string) string {
	switch typ {
	case "composer_json":
		return "packagist"
	case "package_json":
		return "npm"
	default:
		return typ
	}
}

// milestoneIndices returns the ~25/50/75% lookup indices for append-only
// progress. Small walks (n < 8) get none — the start and ✓ lines suffice.
func milestoneIndices(n int) []int {
	if n < 8 {
		return nil
	}
	var ms []int
	for q := 1; q <= 3; q++ {
		i := n * q / 4
		if i > 0 && i < n && (len(ms) == 0 || ms[len(ms)-1] != i) {
			ms = append(ms, i)
		}
	}
	return ms
}

func countPackages(n int) string {
	if n == 1 {
		return "1 package"
	}
	return fmt.Sprintf("%d packages", n)
}

// formatDuration rounds for terminal display: whole seconds from a minute up
// ("2m14s"), tenths below ("41.2s", "400ms", "0s").
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(100 * time.Millisecond).String()
}
