package audit_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/audit"
)

// rotated holds the older entries; current holds the newer ones. Summarize reads them
// in order as one logical stream.
const (
	rotatedFixture = `{"ts":"t1","method":"GET","url":"https://a/","decision":"allow","rule":{"list":"allow_always","index":0},"status":200}
{"ts":"t2","method":"GET","url":"https://b/","decision":"hard-deny","rule":{"list":"deny_always","index":1},"status":471}
{"ts":"t3","host":"api.anthropic.com","decision":"allow"}
`
	currentFixture = `{"ts":"t4","method":"POST","url":"https://c/","decision":"soft-deny","rule":null,"status":470}
{"ts":"t5","method":"GET","url":"https://d/","decision":"allow","rule":{"list":"allow_always","index":3},"status":200}

not-json-at-all
{"ts":"t6","host":"tunnel.example","decision":"hard-deny"}
`
)

func TestSummarize(t *testing.T) {
	s, err := audit.Summarize(strings.NewReader(rotatedFixture), strings.NewReader(currentFixture))
	require.NoError(t, err)

	require.Equal(t, audit.Summary{
		Total:       6,
		Allow:       3,
		SoftDeny:    1,
		HardDeny:    2,
		Intercepted: 4,
		Passthrough: 2,
		Unknown:     0,
		Malformed:   1,
	}, s)
}

func TestSummarizeEmpty(t *testing.T) {
	s, err := audit.Summarize(strings.NewReader(""))
	require.NoError(t, err)
	require.Equal(t, audit.Summary{}, s)
	require.Equal(t, "No audit entries yet.\n", s.Render())
}

func TestSummarizeUnknownDecision(t *testing.T) {
	s, err := audit.Summarize(strings.NewReader(`{"ts":"t","method":"GET","url":"https://x/","decision":"sideways","status":200}` + "\n"))
	require.NoError(t, err)
	require.Equal(t, 1, s.Unknown)
	require.Equal(t, 1, s.Total)
}

func TestSummaryRenderGolden(t *testing.T) {
	s, err := audit.Summarize(strings.NewReader(rotatedFixture), strings.NewReader(currentFixture))
	require.NoError(t, err)
	assertGolden(t, "summary.golden", s.Render())
}
