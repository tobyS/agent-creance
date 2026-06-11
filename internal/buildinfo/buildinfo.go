// Package buildinfo holds compile-time constants and version metadata for the
// agent-creance binary.
//
// Two distinct kinds of "version" live here:
//
//   - The agent-creance version itself (Version/Commit/Date). These are stamped
//     in at build time via -ldflags (see the Makefile's `build` target). When you
//     `go run` or `go test` without ldflags they fall back to "dev".
//   - The *tested-against* versions of our external prerequisites (agent-safehouse
//     and mitmproxy). The design pins these as constants bumped per release so the
//     doctor/run commands can warn on version skew. See design.md, "Prerequisites
//     and version handling".
package buildinfo

// These are overridden at build time with:
//
//	go build -ldflags "-X github.com/tobyS/agent-creance/internal/buildinfo.Version=0.1.0 ..."
//
// In Go, a package-level `var` can be set by the linker; a `const` cannot. That's
// why these are vars, not consts.
var (
	// Version is the agent-creance release version.
	Version = "dev"
	// Commit is the git SHA the binary was built from.
	Commit = "none"
	// Date is the build timestamp.
	Date = "unknown"
)

// Canonical tool names: the display labels for our external prerequisites and
// the keys of TestedVersions. Every place that names a tool (prereq registry,
// version command, report rows) goes through these so the labels can't drift.
const (
	ToolSafehouse = "agent-safehouse"
	ToolMitmproxy = "mitmproxy"
)

// SafehouseBinaries lists the executable names agent-safehouse may be
// installed under, in preference order — the first name found on PATH wins.
// The brew formula (eugene1g/safehouse/agent-safehouse) installs the binary as
// plain "safehouse"; other installs may use the project name. The prereq check
// resolves against this list and the cage launches the resolved name, so the
// two can never disagree.
var SafehouseBinaries = []string{"safehouse", "agent-safehouse"}

// TestedVersions records the external-tool versions this release of
// agent-creance was validated against. The doctor command compares the
// installed versions against these and classifies any skew. Bump these
// whenever you re-test against newer upstreams.
var TestedVersions = map[string]string{
	ToolSafehouse: "0.10.1",
	ToolMitmproxy: "12.2.3",
}
