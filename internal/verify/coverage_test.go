package verify

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// designMatrixSection returns the text of docs/design.md's
// "## What the cage prevents — and what it doesn't" section — from that header
// up to (but not including) the next top-level "## " header.
func designMatrixSection(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../docs/design.md")
	require.NoError(t, err, "design.md must be readable from the verify package")
	text := string(raw)

	const header = "## What the cage prevents"
	start := strings.Index(text, header)
	require.GreaterOrEqual(t, start, 0, "design.md lost its threat-model section header")

	rest := text[start+len(header):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// TestThreatModelMappingHasNoDrift is the anti-drift guard (AC-0033 "Harness
// integrity"): every matrix vector's Keyword must still appear in design.md's
// prevent/not-prevent section. If a bullet is reworded or removed, this fails and
// forces the matrix (and the harness) to be re-pinned to the design.
func TestThreatModelMappingHasNoDrift(t *testing.T) {
	section := designMatrixSection(t)
	for _, vec := range Vectors {
		// ALLOWED vectors are false-negative guards — the complement of the
		// prevent/not-prevent bullets, grounded in the proxy mechanism described
		// elsewhere (e.g. design.md:46), not in this matrix section. Only BLOCKED
		// and DOCUMENTED vectors correspond to a threat-model bullet here.
		if vec.Label == LabelAllowed {
			continue
		}
		assert.Containsf(t, section, vec.Keyword,
			"vector %s (%s) is mapped to %q via keyword %q, which is no longer in the "+
				"design threat-model section — re-pin the matrix to the current design",
			vec.ID, vec.Label, vec.DesignRef, vec.Keyword)
	}
}

// TestEveryThreatModelBulletIsCovered asserts each honesty bullet of the
// "Not prevented" list (the DOCUMENTED non-guarantees the harness must exercise)
// has a representative keyword in the matrix, so a design promise can't exist
// without a probe behind it.
func TestEveryThreatModelBulletIsCovered(t *testing.T) {
	section := designMatrixSection(t)
	// Keywords that must be present BOTH in the design section and in at least one
	// matrix vector — the load-bearing prevent/not-prevent concepts AC-0033 gates.
	required := []string{
		"outside `./`", // host files outside the project
		"mitmproxy",    // only egress path
		"localhost",    // host-service port precision
		"inherited",    // child-process inheritance
		"non-allowlisted",
		"DNS tunneling",
		"rm -rf",             // project files damageable (DOCUMENTED)
		"passthrough",        // least-observable egress (DOCUMENTED)
		"config-persistence", // ephemeral config dir closes persistence
	}
	keywords := map[string]bool{}
	for _, vec := range Vectors {
		keywords[vec.Keyword] = true
	}
	for _, k := range required {
		assert.Containsf(t, section, k, "design.md no longer mentions %q", k)
		assert.Truef(t, keywords[k], "no matrix vector maps the %q threat-model concept", k)
	}
}
