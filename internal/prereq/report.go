package prereq

import (
	"fmt"
	"strings"

	"github.com/tobyS/agent-creance/internal/style"
)

// Status glyphs for the doctor report. Kept as constants so the golden file and
// the code agree on exact bytes.
const (
	glyphOK   = "✓"
	glyphWarn = "⚠"
	glyphMiss = "✗"
)

// Report renders the full "Version compatibility" block for `agent-creance
// doctor`. Unlike run/setup, doctor surfaces *every* mismatch, including
// patch-level skew (design.md). The output is deterministic so it can be pinned
// with a golden file. sty colors the status glyphs and dims the secondary
// detail; a disabled styler reproduces the original bytes exactly.
func Report(results []Result, sty *style.Styler) string {
	var b strings.Builder
	b.WriteString(sty.Header("Version compatibility:") + "\n")

	// Compute alignment widths from the PLAIN fields so the columns line up
	// regardless of color: escapes have zero on-screen width, so padding is
	// derived from the uncolored strings and the colored content is slotted in.
	nameW, instW := 0, 0
	for _, r := range results {
		if n := len(r.Tool.Name) + 1; n > nameW { // +1 for the trailing colon
			nameW = n
		}
		if n := len(installedField(r)); n > instW {
			instW = n
		}
	}

	for _, r := range results {
		name := r.Tool.Name + ":"
		inst := installedField(r)
		fmt.Fprintf(&b, "  %s%s  %s%s   %s\n",
			name, strings.Repeat(" ", nameW-len(name)),
			installedFieldColored(r, sty), strings.Repeat(" ", instW-len(inst)),
			statusField(r, sty))
	}

	b.WriteString("\n")
	for _, ln := range []string{
		"  agent-creance was tested against specific versions of its dependencies.",
		"  Other versions may work, but behavior is not guaranteed. If you hit",
		"  unexpected issues, try pinning to the tested versions or open an",
		"  issue with your version combination.",
	} {
		b.WriteString(sty.Dim(ln) + "\n")
	}
	return b.String()
}

// installedField renders the "installed X, tested against Y" middle column.
// When the tool was found under a different executable name than its canonical
// label (e.g. agent-safehouse installed as "safehouse"), the resolved name is
// annotated so the user can see which binary satisfied the check.
func installedField(r Result) string {
	if !r.Installed {
		return "not installed"
	}
	installed := r.Version
	if installed == "" {
		installed = "unknown"
	}
	if r.ResolvedName != "" && r.ResolvedName != r.Tool.Name {
		installed += " via " + r.ResolvedName
	}
	return fmt.Sprintf("installed %s, tested against %s", installed, r.Tool.Tested)
}

// installedFieldColored is installedField with the "tested against Y" detail
// dimmed. It produces the same visible width as installedField (escapes are
// zero-width), so the column math above stays correct.
func installedFieldColored(r Result, sty *style.Styler) string {
	if !r.Installed {
		return "not installed"
	}
	installed := r.Version
	if installed == "" {
		installed = "unknown"
	}
	if r.ResolvedName != "" && r.ResolvedName != r.Tool.Name {
		installed += " via " + r.ResolvedName
	}
	return "installed " + installed + ", " + sty.Dim("tested against "+r.Tool.Tested)
}

// statusField renders the trailing glyph + label column. The glyph is colored
// by verdict (green ok / yellow skew / red missing).
func statusField(r Result, sty *style.Styler) string {
	if !r.Installed {
		return sty.Bad(glyphMiss) + " missing"
	}
	switch r.Skew {
	case SkewExact:
		return sty.OK(glyphOK)
	case SkewUnparseable:
		// No version to compare — say so plainly rather than warning.
		return sty.OK(glyphOK) + " (version unparsed)"
	default:
		return fmt.Sprintf("%s %s", sty.Warn(glyphWarn), r.Skew)
	}
}
