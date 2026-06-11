package prereq_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/prereq"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// Note the package name: prereq_test, not prereq. Go lets a _test.go file live
// in a sibling "_test" package so the test consumes the package as an external
// caller would — it can only touch the exported API, which keeps tests honest
// about the public surface. (Whitebox tests that need internals, like
// version_test.go, use the plain `package prereq` instead.)

func testTools() []prereq.Tool {
	return []prereq.Tool{
		{Name: "agent-safehouse", Binaries: []string{"safehouse", "agent-safehouse"}, VersionArgs: []string{"--version"}, Tested: "1.4.2", InstallHint: "brew install eugene1g/safehouse/agent-safehouse"},
		{Name: "mitmproxy", VersionArgs: []string{"--version"}, Tested: "12.0.1", InstallHint: "brew install mitmproxy"},
	}
}

func TestCheck_AllInstalled(t *testing.T) {
	// The fake stands in for the OS: no real agent-safehouse/mitmproxy needed.
	cmd := sysdeptest.NewFakeCommander().
		WithTool("agent-safehouse", "/usr/local/bin/agent-safehouse", "agent-safehouse 1.4.5").
		WithTool("mitmproxy", "/opt/homebrew/bin/mitmproxy", "Mitmproxy: 12.0.1")

	results := prereq.Check(context.Background(), cmd, testTools())
	require.Len(t, results, 2)

	// testify's require.* stops the test on failure; assert.* records and
	// continues. Use require for preconditions, assert for the actual checks.
	assert.True(t, results[0].Installed)
	assert.Equal(t, "agent-safehouse", results[0].ResolvedName, "fallback name resolves when the preferred one is absent")
	assert.Equal(t, "1.4.5", results[0].Version)
	assert.Equal(t, prereq.SkewPatch, results[0].Skew, "1.4.5 vs tested 1.4.2 is a patch skew")

	assert.True(t, results[1].Installed)
	assert.Equal(t, "mitmproxy", results[1].ResolvedName)
	assert.Equal(t, prereq.SkewExact, results[1].Skew)

	assert.Empty(t, prereq.Missing(results))
	assert.Empty(t, prereq.MissingInstructions(results))
}

// TestCheck_BinaryNameResolution covers the dual-name lookup: either executable
// name satisfies the safehouse prerequisite, the preferred name wins when both
// are installed, and the version query runs against the resolved name.
func TestCheck_BinaryNameResolution(t *testing.T) {
	safehouseOnly := testTools()[:1]
	tests := []struct {
		name         string
		installed    []string // names registered on the fake's PATH
		wantResolved string
	}{
		{"preferred name only", []string{"safehouse"}, "safehouse"},
		{"fallback name only", []string{"agent-safehouse"}, "agent-safehouse"},
		{"both installed, preferred wins", []string{"safehouse", "agent-safehouse"}, "safehouse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := sysdeptest.NewFakeCommander()
			for _, name := range tt.installed {
				cmd.WithTool(name, "/opt/homebrew/bin/"+name, "Agent Safehouse 1.4.2")
			}

			results := prereq.Check(context.Background(), cmd, safehouseOnly)
			require.Len(t, results, 1)
			assert.True(t, results[0].Installed)
			assert.Equal(t, tt.wantResolved, results[0].ResolvedName)
			assert.Equal(t, "1.4.2", results[0].Version, "version banner queried via the resolved name")
			assert.Equal(t, prereq.SkewExact, results[0].Skew)

			bin, ok := prereq.ResolvedBinary(results, "agent-safehouse")
			assert.True(t, ok)
			assert.Equal(t, tt.wantResolved, bin)
		})
	}
}

func TestResolvedBinary_MissingOrUnknown(t *testing.T) {
	// Nothing installed: the tool result exists but is not installed.
	results := prereq.Check(context.Background(), sysdeptest.NewFakeCommander(), testTools())

	_, ok := prereq.ResolvedBinary(results, "agent-safehouse")
	assert.False(t, ok, "uninstalled tool must not resolve")
	_, ok = prereq.ResolvedBinary(results, "no-such-tool")
	assert.False(t, ok, "unknown tool name must not resolve")
}

func TestCheck_OneMissing(t *testing.T) {
	cmd := sysdeptest.NewFakeCommander().
		WithTool("mitmproxy", "/opt/homebrew/bin/mitmproxy", "Mitmproxy: 12.0.1")
	// agent-safehouse is intentionally absent from the fake.

	results := prereq.Check(context.Background(), cmd, testTools())

	assert.Equal(t, []string{"agent-safehouse"}, prereq.Missing(results))
	instr := prereq.MissingInstructions(results)
	assert.Contains(t, instr, "agent-safehouse")
	assert.Contains(t, instr, "brew install eugene1g/safehouse/agent-safehouse")
	// The healthy tool must not appear in the missing-instructions block.
	assert.NotContains(t, instr, "mitmproxy")
}

func TestCheck_InstalledButVersionFails(t *testing.T) {
	cmd := sysdeptest.NewFakeCommander()
	cmd.Paths["agent-safehouse"] = "/usr/local/bin/agent-safehouse"
	cmd.Errs["agent-safehouse"] = errors.New("exec failed")

	results := prereq.Check(context.Background(), cmd, testTools()[:1])
	require.Len(t, results, 1)
	assert.True(t, results[0].Installed, "tool is on PATH even though --version failed")
	assert.Equal(t, prereq.SkewUnparseable, results[0].Skew, "failed --version is given benefit of the doubt")
}
