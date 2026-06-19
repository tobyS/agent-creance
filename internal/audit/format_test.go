package audit_test

import (
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/audit"
	"github.com/tobyS/agent-creance/internal/style"
)

func TestFormatEntryGolden(t *testing.T) {
	entries := []audit.Entry{
		{TS: "2026-06-06T10:22:01Z", Method: "GET", URL: "https://api.github.com/", Decision: "allow", Rule: &audit.Rule{List: "allow_always", Index: 2}, Status: 200},
		{TS: "2026-06-06T10:22:03Z", Method: "GET", URL: "https://evil.example/", Decision: "soft-deny", Status: 470},
		{TS: "2026-06-06T10:22:04Z", Method: "POST", URL: "https://w3schools.com/x", Decision: "hard-deny", Rule: &audit.Rule{List: "deny_always", Index: 0}, Status: 471},
		{TS: "2026-06-06T10:22:05Z", Host: "api.anthropic.com", Decision: "allow"},
		{TS: "2026-06-06T10:22:06Z", Host: "tunnel.example", Decision: "hard-deny"},
	}
	// Plain keeps the original golden name; color adds a _color sibling.
	modes := map[string]*style.Styler{"": style.Plain(), "_color": style.New(true)}
	for suffix, sty := range modes {
		var b strings.Builder
		for _, e := range entries {
			b.WriteString(audit.FormatEntry(e, sty))
			b.WriteByte('\n')
		}
		assertGolden(t, "format_lines"+suffix+".golden", b.String())
	}
}
