package prereq_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/prereq"
)

// update is the conventional "-update" flag for golden tests. Run
//
//	go test ./internal/prereq -run TestReport -update
//
// to regenerate the .golden files after an intentional format change, then eyeball
// the git diff. This is the Go-community equivalent of `jest --updateSnapshot`,
// but the golden files are plain bytes you review like any other file.
var update = flag.Bool("update", false, "regenerate golden files")

// goldenResults builds a fixed, representative set of check results so the
// rendered report is deterministic and worth pinning.
func goldenResults() []prereq.Result {
	safehouse := prereq.Tool{Name: "agent-safehouse", Binaries: []string{"safehouse", "agent-safehouse"}, Tested: "1.4.2"}
	mitm := prereq.Tool{Name: "mitmproxy", Tested: "12.0.1"}
	missing := prereq.Tool{Name: "some-future-tool", Tested: "3.0.0"}
	return []prereq.Result{
		// Resolved under the non-canonical executable name → "via" annotation.
		{Tool: safehouse, Installed: true, ResolvedName: "safehouse", Version: "1.4.5", Skew: prereq.SkewPatch},
		{Tool: mitm, Installed: true, ResolvedName: "mitmproxy", Version: "12.0.1", Skew: prereq.SkewExact},
		{Tool: missing, Installed: false},
	}
}

func TestReport(t *testing.T) {
	got := prereq.Report(goldenResults())
	golden := filepath.Join("testdata", "doctor_report.golden")

	if *update {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
		return
	}

	want, err := os.ReadFile(golden)
	require.NoError(t, err, "missing golden file; run with -update to create it")
	require.Equal(t, string(want), got)
}
