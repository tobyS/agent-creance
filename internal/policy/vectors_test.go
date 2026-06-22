package policy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// vector is one language-neutral decision-vector: a rule set and a request, with the
// decision the matcher must produce. The same JSON files are consumed by the Python
// enforcer's tests (AC-0017) — this is the C1 parity contract — so the on-disk shape
// is decoded here into test-only structs rather than exposed as a Go API.
type vector struct {
	Name     string      `json:"name"`
	Ruleset  RuleSet     `json:"ruleset"`
	Request  Request     `json:"request"`
	Expected expectation `json:"expected"`
}

type expectation struct {
	Decision string       `json:"decision"`
	Mode     string       `json:"mode"`
	Matched  *MatchedRule `json:"matched_rule"`
	// HostDisposition is optional: present only on vectors that also pin the
	// CONNECT-stage host-only decision (AC-0058 / C3). A pointer so older vectors omit it.
	HostDisposition *HostDisposition `json:"host_disposition,omitempty"`
}

// TestDecisionVectors runs every JSON vector under testdata/decision-vectors/ through
// Decide and asserts the matcher reproduces the expected decision, mode, and matched
// rule. It fails if the corpus is empty, so an accidentally-deleted corpus cannot pass
// silently.
func TestDecisionVectors(t *testing.T) {
	dir := filepath.Join("testdata", "decision-vectors")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	count := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		count++
		t.Run(strings.TrimSuffix(e.Name(), ".json"), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			require.NoError(t, err)

			// Decode strictly: a mistyped key in a security-policy fixture is a bug,
			// not a silent drop (the JSON analog of the loader's KnownFields stance).
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			var v vector
			require.NoError(t, dec.Decode(&v), "decoding %s", e.Name())

			got := v.Ruleset.Decide(v.Request)
			require.Equal(t, v.Expected.Decision, got.Decision, "decision")
			require.Equal(t, v.Expected.Mode, got.Mode, "mode")
			require.Equal(t, v.Expected.Matched, got.Matched, "matched_rule")

			// Vectors that pin the CONNECT-stage host decision also replay it (C3).
			if v.Expected.HostDisposition != nil {
				gotHD := v.Ruleset.HostDisposition(v.Request.Host)
				require.Equal(t, *v.Expected.HostDisposition, gotHD, "host_disposition")
			}
		})
	}

	require.Positive(t, count, "no decision vectors found in %s", dir)
}
