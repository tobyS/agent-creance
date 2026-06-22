package render_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/policy"
	"github.com/tobyS/agent-creance/internal/policy/render"
	"github.com/tobyS/agent-creance/internal/style"
)

// vector decodes one language-neutral decision-vector — the same corpus the matcher
// (AC-0010) and the Python enforcer (AC-0017) replay. Here it guards C1 from the
// rendering side: Explain must surface exactly the matcher's decision and matched
// rule, never a reimplementation.
type vector struct {
	Name     string         `json:"name"`
	Ruleset  policy.RuleSet `json:"ruleset"`
	Request  policy.Request `json:"request"`
	Expected struct {
		Decision string              `json:"decision"`
		Mode     string              `json:"mode"`
		Matched  *policy.MatchedRule `json:"matched_rule"`
		// HostDisposition is the optional CONNECT-stage expectation (AC-0058 / C3); it is
		// not relevant to Explain but must be declared so strict decoding accepts vectors
		// that carry it.
		HostDisposition *policy.HostDisposition `json:"host_disposition,omitempty"`
	} `json:"expected"`
}

// TestExplainMatchesMatcher replays every decision vector through Explain and asserts
// the rendered decision (and the named matched rule, or the soft-deny "none") agrees
// with policy.Decide — the C1 consistency check the ticket calls for.
func TestExplainMatchesMatcher(t *testing.T) {
	dir := filepath.Join("..", "testdata", "decision-vectors")
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

			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			var v vector
			require.NoError(t, dec.Decode(&v), "decoding %s", e.Name())

			want := v.Ruleset.Decide(v.Request)
			c := policy.Compiled{Version: policy.CompiledVersion, RuleSet: v.Ruleset}
			got := render.Explain(c, v.Request, style.Plain())

			// The decision line carries the verdict (and mode when present).
			if want.Mode != "" {
				require.Contains(t, got, fmt.Sprintf("Decision:  %s (%s)", want.Decision, want.Mode))
			} else {
				require.Contains(t, got, "Decision:  "+want.Decision)
			}

			// The matched rule the renderer names must be the matcher's.
			if want.Matched == nil {
				require.Contains(t, got, "Matched:   (none")
			} else {
				require.Contains(t, got, fmt.Sprintf("%s[%d]", want.Matched.List, want.Matched.Index))
			}
		})
	}
	require.Positive(t, count, "no decision vectors found in %s", dir)
}
