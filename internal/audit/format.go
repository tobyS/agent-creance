package audit

import "fmt"

// decisionWidth aligns the decision column. The widest decision ("soft-deny" /
// "hard-deny") is 9 characters.
const decisionWidth = 9

// FormatEntry renders one entry as a single human-readable line for `logs` and
// `logs --follow`. Intercepted requests show method/url/status; passthrough records
// show the host only (no method/url/status exist for an ignored connection).
func FormatEntry(e Entry) string {
	if e.IsPassthrough() {
		return fmt.Sprintf("%s  %-*s  %s (passthrough)", e.TS, decisionWidth, e.Decision, e.Host)
	}
	return fmt.Sprintf("%s  %-*s  %s %s -> %d", e.TS, decisionWidth, e.Decision, e.Method, e.URL, e.Status)
}
