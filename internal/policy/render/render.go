// Package render is the policy *visibility* layer for `agent-creance policy show`
// and `policy explain` (AC-0015): pure functions that turn a compiled policy
// (internal/policy/compile's policy.Compiled artifact) into the annotated,
// golden-stable text — and JSON — an operator reads.
//
// It is deliberately pure (no filesystem, clock, or OS): the command in
// internal/cli compiles the policy and feeds the in-memory artifact here. For the
// `explain` decision it calls the *shared* matcher (policy.RuleSet.Decide) rather
// than reimplementing the logic — cross-cutting C1, so `explain` can never disagree
// with the proxy enforcer about what a URL does.
package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tobyS/agent-creance/internal/policy"
)

// Distinct flags appended to a rule line. Kept as constants so the code and the
// golden files agree on exact bytes. A passthrough rule is an audit blind spot
// (the proxy tunnels raw bytes, never sees path/method), which docs/design.md
// requires `policy show` to surface at a glance; lower-trust marks a host a
// stricter threat model could drop.
const (
	markerPassthrough = "⚠ passthrough (audit blind spot)"
	markerLowerTrust  = "⚠ lower-trust"

	// notePassthrough explains, in `explain`, why a passthrough allow is not
	// path/method-scoped — the same blind spot, from the single-URL angle.
	notePassthrough = "passthrough host — the proxy tunnels raw bytes; path and method are not inspected and auditing is host-level only."
)

// Show renders the resolved policy as annotated, aligned text: an ALLOW block then
// a DENY block, each rule tagged with its [source] and (for allow) its mode, with
// distinct markers for passthrough and lower-trust rules.
func Show(c policy.Compiled) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Resolved egress policy (%d allow, %d deny)\n", len(c.Allow), len(c.DenyAlways))

	b.WriteString("\nALLOW\n")
	if len(c.Allow) == 0 {
		b.WriteString("  (none)\n")
	} else {
		b.WriteString(renderAllow(c.Allow))
	}

	b.WriteString("\nDENY\n")
	if len(c.DenyAlways) == 0 {
		b.WriteString("  (none)\n")
	} else {
		b.WriteString(renderDeny(c.DenyAlways))
	}
	return b.String()
}

// ShowJSON re-emits the compiled artifact as indented JSON (with a trailing
// newline), for `policy show --json`.
func ShowJSON(c policy.Compiled) (string, error) {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render: marshal policy: %w", err)
	}
	return string(data) + "\n", nil
}

// renderAllow renders the allow block with two-pass column alignment over the
// [source] tag, mode, and host columns (the same manual-width idiom as
// prereq.Report — no tabwriter in this codebase).
func renderAllow(rules []policy.Rule) string {
	tagW, modeW, hostW := 0, 0, 0
	for _, r := range rules {
		tagW = max(tagW, len(tag(r.Source)))
		modeW = max(modeW, len(mode(r.Mode)))
		hostW = max(hostW, len(r.Host))
	}

	var b strings.Builder
	for _, r := range rules {
		fmt.Fprintf(&b, "  %-*s  %-*s  %-*s  %s",
			tagW, tag(r.Source), modeW, mode(r.Mode), hostW, r.Host, pathField(r.Paths))
		if m := methodField(r.Methods); m != "" {
			b.WriteString("  " + m)
		}
		for _, mk := range markers(r) {
			b.WriteString("  " + mk)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderDeny renders the deny block. Deny rules carry no meaningful mode column
// (a deny_always is enforced at the host level regardless), so it is omitted; the
// trailing (reason: …) is what matters.
func renderDeny(rules []policy.Rule) string {
	tagW, hostW := 0, 0
	for _, r := range rules {
		tagW = max(tagW, len(tag(r.Source)))
		hostW = max(hostW, len(r.Host))
	}

	var b strings.Builder
	for _, r := range rules {
		fmt.Fprintf(&b, "  %-*s  %-*s  %s", tagW, tag(r.Source), hostW, r.Host, pathField(r.Paths))
		if m := methodField(r.Methods); m != "" {
			b.WriteString("  " + m)
		}
		if r.Reason != "" {
			fmt.Fprintf(&b, "  (reason: %s)", r.Reason)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Explain runs the shared matcher (C1) on req against c and renders the decision,
// the carried enforcement mode, and the matched rule + its source. A soft-deny has
// no matching rule; a passthrough allow gets an explanatory note.
func Explain(c policy.Compiled, req policy.Request) string {
	res := c.Decide(req)

	var b strings.Builder
	fmt.Fprintf(&b, "Request:   %s %s %s\n", req.Host, req.Method, reqPath(req.Path))
	if res.Mode != "" {
		fmt.Fprintf(&b, "Decision:  %s (%s)\n", res.Decision, res.Mode)
	} else {
		fmt.Fprintf(&b, "Decision:  %s\n", res.Decision)
	}

	if res.Matched == nil {
		b.WriteString("Matched:   (none — not in the allowlist)\n")
		return b.String()
	}

	r := matchedRule(c, res.Matched)
	fmt.Fprintf(&b, "Matched:   %s[%d]  %s  %s %s",
		res.Matched.List, res.Matched.Index, tag(r.Source), r.Host, pathField(r.Paths))
	if m := methodField(r.Methods); m != "" {
		b.WriteString("  " + m)
	}
	if r.Reason != "" {
		fmt.Fprintf(&b, "  (reason: %s)", r.Reason)
	}
	b.WriteString("\n")

	if res.Decision == policy.DecisionAllow && res.Mode == policy.ModePassthrough {
		fmt.Fprintf(&b, "Note:      %s\n", notePassthrough)
	}
	return b.String()
}

// explainOutput is the `policy explain --json` shape: the request, the decision and
// carried mode, and the resolved matched rule (null for a soft-deny).
type explainOutput struct {
	Request  policy.Request `json:"request"`
	Decision string         `json:"decision"`
	Mode     string         `json:"mode,omitempty"`
	Matched  *matchedOutput `json:"matched"`
}

// matchedOutput flattens the matched rule's list/index identity together with the
// rule's own fields (embedded policy.Rule carries the host/paths/.../source tags).
type matchedOutput struct {
	List  string `json:"list"`
	Index int    `json:"index"`
	policy.Rule
}

// ExplainJSON returns the structured explanation for `policy explain --json`.
func ExplainJSON(c policy.Compiled, req policy.Request) (string, error) {
	res := c.Decide(req)
	out := explainOutput{Request: req, Decision: res.Decision, Mode: res.Mode}
	if res.Matched != nil {
		out.Matched = &matchedOutput{
			List:  res.Matched.List,
			Index: res.Matched.Index,
			Rule:  matchedRule(c, res.Matched),
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render: marshal explanation: %w", err)
	}
	return string(data) + "\n", nil
}

// matchedRule resolves a MatchedRule's (list, index) back to the rule it names.
func matchedRule(c policy.Compiled, m *policy.MatchedRule) policy.Rule {
	if m.List == "deny_always" {
		return c.DenyAlways[m.Index]
	}
	return c.Allow[m.Index]
}

// tag renders a rule's source as the bracketed [source] annotation. The compiler
// always stamps a source on artifact rules; a bare rule (no source) is labelled
// [unknown] defensively.
func tag(source string) string {
	if source == "" {
		return "[unknown]"
	}
	return "[" + source + "]"
}

// mode renders a rule's enforcement mode, defaulting a missing one to intercept so
// the column never shows a blank (the compiler defaults it too).
func mode(m string) string {
	if m == "" {
		return policy.ModeIntercept
	}
	return m
}

// pathField renders the path column: the literal "(any path)" for a host-wide rule,
// else the rule's path patterns.
func pathField(paths []string) string {
	if len(paths) == 0 {
		return "(any path)"
	}
	return strings.Join(paths, ", ")
}

// methodField renders the optional "(GET, POST)" method column, empty when the rule
// matches any method.
func methodField(methods []string) string {
	if len(methods) == 0 {
		return ""
	}
	return "(" + strings.Join(methods, ", ") + ")"
}

// markers returns the distinct trailing flags for a rule (passthrough, lower-trust).
func markers(r policy.Rule) []string {
	var out []string
	if r.Mode == policy.ModePassthrough {
		out = append(out, markerPassthrough)
	}
	if r.LowerTrust {
		out = append(out, markerLowerTrust)
	}
	return out
}

// reqPath renders a request path, normalising an empty path to "/".
func reqPath(p string) string {
	if p == "" {
		return "/"
	}
	return p
}
