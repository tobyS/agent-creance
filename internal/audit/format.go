package audit

import (
	"fmt"
	"strings"

	"github.com/tobyS/agent-creance/internal/style"
)

// decisionWidth aligns the decision column. The widest decision ("soft-deny" /
// "hard-deny") is 9 characters.
const decisionWidth = 9

// FormatEntry renders one entry as a single human-readable line for `logs` and
// `logs --follow`. Intercepted requests show method/url/status; passthrough records
// show the host only (no method/url/status exist for an ignored connection). sty
// dims the timestamp and colors the decision by verdict; a disabled styler emits
// the original bytes.
func FormatEntry(e Entry, sty *style.Styler) string {
	// Pad from the plain decision width so the escapes don't widen the column.
	dec := colorDecision(e.Decision, sty) + strings.Repeat(" ", decisionWidth-len(e.Decision))
	if e.IsPassthrough() {
		return fmt.Sprintf("%s  %s  %s (passthrough)", sty.Dim(e.TS), dec, e.Host)
	}
	return fmt.Sprintf("%s  %s  %s %s -> %d", sty.Dim(e.TS), dec, e.Method, e.URL, e.Status)
}

// colorDecision colors an audit decision verdict: allow green, soft-deny yellow,
// hard-deny red.
func colorDecision(decision string, sty *style.Styler) string {
	switch decision {
	case DecisionAllow:
		return sty.OK(decision)
	case DecisionSoftDeny:
		return sty.Warn(decision)
	case DecisionHardDeny:
		return sty.Bad(decision)
	default:
		return decision
	}
}
