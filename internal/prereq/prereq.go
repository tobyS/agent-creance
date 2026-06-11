// Package prereq checks that agent-creance's external prerequisites
// (agent-safehouse, mitmproxy) are installed and reports how their installed
// versions compare to the versions agent-creance was tested against.
//
// This is the first real vertical slice through the app: it exercises the
// sysdep seam (so it's unit-testable without the tools installed), pure
// classification logic (table-driven tests), and human-facing report formatting
// (golden-file tests).
package prereq

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tobyS/agent-creance/internal/buildinfo"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// Tool describes one external prerequisite: how to find it, how to ask its
// version, and which version we tested against.
type Tool struct {
	// Name is the tool's canonical display label (and its TestedVersions key).
	// It is also the executable name when Binaries is empty.
	Name string
	// Binaries are the executable names the tool may be installed under, in
	// preference order — the first name found on PATH wins. Empty means the
	// tool is only ever installed as Name.
	Binaries []string
	// VersionArgs are passed to the tool to make it print its version.
	VersionArgs []string
	// Tested is the version agent-creance was validated against.
	Tested string
	// InstallHint is shown when the tool is missing.
	InstallHint string
}

// binaries returns the ordered lookup candidates (the Binaries list, or just
// Name for single-name tools).
func (t Tool) binaries() []string {
	if len(t.Binaries) > 0 {
		return t.Binaries
	}
	return []string{t.Name}
}

// DefaultTools is the v0.1 prerequisite set, with tested versions sourced from
// buildinfo. It's a function (not a package var) so it reads buildinfo at call
// time and stays easy to override in tests.
func DefaultTools(tested map[string]string) []Tool {
	return []Tool{
		{
			Name:        buildinfo.ToolSafehouse,
			Binaries:    buildinfo.SafehouseBinaries,
			VersionArgs: []string{"--version"},
			Tested:      tested[buildinfo.ToolSafehouse],
			InstallHint: "brew install eugene1g/safehouse/agent-safehouse",
		},
		{
			Name:        buildinfo.ToolMitmproxy,
			VersionArgs: []string{"--version"},
			Tested:      tested[buildinfo.ToolMitmproxy],
			InstallHint: "brew install mitmproxy",
		},
	}
}

// Result is the outcome of checking one Tool.
type Result struct {
	Tool      Tool
	Installed bool
	// ResolvedName is the executable name (from Tool's candidates) that was
	// found on PATH — the name callers must exec. Empty when not installed.
	ResolvedName string
	// Version is the raw extracted version string (empty if missing/unknown).
	Version string
	Skew    Skew
}

// Check inspects every tool using the injected Commander. It never returns an
// error: a missing tool is data (Result.Installed == false), not a failure —
// callers decide whether missing is fatal (run) or merely reported (doctor).
func Check(ctx context.Context, cmd sysdep.Commander, tools []Tool) []Result {
	results := make([]Result, 0, len(tools))
	for _, t := range tools {
		r := Result{Tool: t}
		for _, name := range t.binaries() {
			if _, err := cmd.LookPath(name); err == nil {
				r.Installed = true
				r.ResolvedName = name
				break
			}
		}
		if !r.Installed {
			results = append(results, r)
			continue
		}
		out, err := cmd.Output(ctx, r.ResolvedName, t.VersionArgs...)
		if err != nil {
			// Tool exists but wouldn't report a version: treat as unparseable
			// (benefit of the doubt) rather than failing the whole check.
			r.Skew = SkewUnparseable
			results = append(results, r)
			continue
		}
		banner := strings.TrimSpace(string(out))
		if pv := parseVersion(banner); pv.ok {
			r.Version = fmt.Sprintf("%d.%d.%d", pv.major, pv.minor, pv.patch)
		}
		r.Skew = classify(banner, t.Tested)
		results = append(results, r)
	}
	return results
}

// ResolvedBinary returns the executable name the check resolved for the tool
// labelled name (Tool.Name), and whether that tool was found installed. Callers
// that exec the tool (run's cage launch) use this so the binary they start is
// exactly the one the check verified.
func ResolvedBinary(results []Result, name string) (string, bool) {
	for _, r := range results {
		if r.Tool.Name == name && r.Installed {
			return r.ResolvedName, true
		}
	}
	return "", false
}

// Missing returns the names of tools that are not installed, sorted for stable
// output. Used by `run` to refuse-and-suggest before doing anything else.
func Missing(results []Result) []string {
	var names []string
	for _, r := range results {
		if !r.Installed {
			names = append(names, r.Tool.Name)
		}
	}
	sort.Strings(names)
	return names
}

// MissingInstructions formats the refuse-and-suggest block shown when one or
// more prerequisites are absent (design.md, "Prerequisites and version
// handling"). Returns "" when nothing is missing.
func MissingInstructions(results []Result) string {
	var missing []Result
	for _, r := range results {
		if !r.Installed {
			missing = append(missing, r)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("agent-creance requires the following tools, which are not installed:\n\n")
	// Align the install hints in a column for readability.
	width := 0
	for _, r := range missing {
		if n := len(r.Tool.Name); n > width {
			width = n
		}
	}
	for _, r := range missing {
		fmt.Fprintf(&b, "  - %-*s %s\n", width+1, r.Tool.Name+":", r.Tool.InstallHint)
	}
	b.WriteString("\nInstall the missing tool(s) and run `agent-creance setup`.\n")
	return b.String()
}
