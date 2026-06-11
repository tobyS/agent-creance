package prereq

import (
	"fmt"
	"strings"
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
// with a golden file.
func Report(results []Result) string {
	var b strings.Builder
	b.WriteString("Version compatibility:\n")

	// Compute alignment widths so the columns line up regardless of tool-name
	// or version length.
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
		fmt.Fprintf(&b, "  %-*s  %-*s   %s\n", nameW, name, instW, inst, statusField(r))
	}

	b.WriteString("\n")
	b.WriteString("  agent-creance was tested against specific versions of its dependencies.\n")
	b.WriteString("  Other versions may work, but behavior is not guaranteed. If you hit\n")
	b.WriteString("  unexpected issues, try pinning to the tested versions or open an\n")
	b.WriteString("  issue with your version combination.\n")
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

// statusField renders the trailing glyph + label column.
func statusField(r Result) string {
	if !r.Installed {
		return glyphMiss + " missing"
	}
	switch r.Skew {
	case SkewExact:
		return glyphOK
	case SkewUnparseable:
		// No version to compare — say so plainly rather than warning.
		return glyphOK + " (version unparsed)"
	default:
		return fmt.Sprintf("%s %s", glyphWarn, r.Skew)
	}
}
