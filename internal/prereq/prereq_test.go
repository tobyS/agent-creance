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
		{Name: "agent-safehouse", VersionArgs: []string{"--version"}, Tested: "1.4.2", InstallHint: "brew install eugene1g/safehouse/agent-safehouse"},
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
	assert.Equal(t, "1.4.5", results[0].Version)
	assert.Equal(t, prereq.SkewPatch, results[0].Skew, "1.4.5 vs tested 1.4.2 is a patch skew")

	assert.True(t, results[1].Installed)
	assert.Equal(t, prereq.SkewExact, results[1].Skew)

	assert.Empty(t, prereq.Missing(results))
	assert.Empty(t, prereq.MissingInstructions(results))
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
