package audit_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/audit"
)

func TestParseLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    audit.Entry
		wantErr bool
	}{
		{
			name: "intercepted allow with rule",
			line: `{"ts":"2026-06-06T10:22:01Z","method":"GET","url":"https://api.github.com/","decision":"allow","rule":{"list":"allow_always","index":2},"status":200}`,
			want: audit.Entry{
				TS:       "2026-06-06T10:22:01Z",
				Method:   "GET",
				URL:      "https://api.github.com/",
				Decision: "allow",
				Rule:     &audit.Rule{List: "allow_always", Index: 2},
				Status:   200,
			},
		},
		{
			name: "soft-deny with null rule",
			line: `{"ts":"2026-06-06T10:22:03Z","method":"GET","url":"https://evil.example/","decision":"soft-deny","rule":null,"status":403}`,
			want: audit.Entry{
				TS:       "2026-06-06T10:22:03Z",
				Method:   "GET",
				URL:      "https://evil.example/",
				Decision: "soft-deny",
				Rule:     nil,
				Status:   403,
			},
		},
		{
			name: "passthrough host-only",
			line: `{"ts":"2026-06-06T10:22:05Z","host":"api.anthropic.com","decision":"allow"}`,
			want: audit.Entry{
				TS:       "2026-06-06T10:22:05Z",
				Host:     "api.anthropic.com",
				Decision: "allow",
			},
		},
		{
			name:    "malformed json",
			line:    `{"ts":"2026-`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := audit.ParseLine([]byte(tt.line))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestEntryIsPassthrough(t *testing.T) {
	require.True(t, audit.Entry{Host: "api.anthropic.com"}.IsPassthrough())
	require.False(t, audit.Entry{Method: "GET", URL: "https://x/"}.IsPassthrough())
}
