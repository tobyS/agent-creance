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
	"github.com/tobyS/agent-creance/internal/policy/compile"
	"github.com/tobyS/agent-creance/internal/style"
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
func Show(c policy.Compiled, sty *style.Styler) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Resolved egress policy (%d allow, %d deny)\n", len(c.Allow), len(c.DenyAlways))

	b.WriteString("\n" + sty.Header("ALLOW") + "\n")
	if len(c.Allow) == 0 {
		b.WriteString("  (none)\n")
	} else {
		b.WriteString(renderAllow(c.Allow, sty))
	}

	b.WriteString("\n" + sty.Header("DENY") + "\n")
	if len(c.DenyAlways) == 0 {
		b.WriteString("  (none)\n")
	} else {
		b.WriteString(renderDeny(c.DenyAlways, sty))
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
func renderAllow(rules []policy.Rule, sty *style.Styler) string {
	tagW, modeW, hostW := 0, 0, 0
	for _, r := range rules {
		tagW = max(tagW, len(tag(r.Source)))
		modeW = max(modeW, len(mode(r.Mode)))
		hostW = max(hostW, len(r.Host))
	}

	var b strings.Builder
	for _, r := range rules {
		// The [source] tag is dimmed; its pad is computed from the plain width so
		// the escapes don't disturb the column (mode/host stay plain %-*s).
		tg := tag(r.Source)
		fmt.Fprintf(&b, "  %s%s  %-*s  %-*s  %s",
			sty.Dim(tg), strings.Repeat(" ", tagW-len(tg)),
			modeW, mode(r.Mode), hostW, r.Host, pathField(r.Paths))
		if m := methodField(r.Methods); m != "" {
			b.WriteString("  " + sty.Dim(m))
		}
		if a := authField(r); a != "" {
			b.WriteString("  " + sty.Dim(a))
		}
		for _, mk := range markers(r) {
			b.WriteString("  " + sty.Warn(mk))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// authField renders the auth axis (AC-0068b) for an allow rule: the credential it
// injects, or that it is marked in-cage. Empty when the rule uses neither (today's
// default). It is informational, so it is dimmed rather than warned.
func authField(r policy.Rule) string {
	switch {
	case r.Inject != "":
		return "inject:" + r.Inject
	case r.InCage:
		return "in-cage"
	default:
		return ""
	}
}

// renderDeny renders the deny block. Deny rules carry no meaningful mode column
// (a deny_always is enforced at the host level regardless), so it is omitted; the
// trailing (reason: …) is what matters.
func renderDeny(rules []policy.Rule, sty *style.Styler) string {
	tagW, hostW := 0, 0
	for _, r := range rules {
		tagW = max(tagW, len(tag(r.Source)))
		hostW = max(hostW, len(r.Host))
	}

	var b strings.Builder
	for _, r := range rules {
		tg := tag(r.Source)
		fmt.Fprintf(&b, "  %s%s  %-*s  %s",
			sty.Dim(tg), strings.Repeat(" ", tagW-len(tg)), hostW, r.Host, pathField(r.Paths))
		if m := methodField(r.Methods); m != "" {
			b.WriteString("  " + sty.Dim(m))
		}
		if r.Reason != "" {
			b.WriteString("  " + sty.Dim(fmt.Sprintf("(reason: %s)", r.Reason)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Explain runs the shared matcher (C1) on req against c and renders the decision,
// the carried enforcement mode, and the matched rule + its source. A soft-deny has
// no matching rule; a passthrough allow gets an explanatory note.
func Explain(c policy.Compiled, req policy.Request, sty *style.Styler) string {
	res := c.Decide(req)

	var b strings.Builder
	fmt.Fprintf(&b, "Request:   %s %s %s\n", req.Host, req.Method, reqPath(req.Path))
	if res.Mode != "" {
		fmt.Fprintf(&b, "Decision:  %s (%s)\n", colorDecision(res.Decision, sty), sty.Dim(res.Mode))
	} else {
		fmt.Fprintf(&b, "Decision:  %s\n", colorDecision(res.Decision, sty))
	}

	if res.Matched == nil {
		b.WriteString("Matched:   (none — not in the allowlist)\n")
		return b.String()
	}

	r := matchedRule(c, res.Matched)
	fmt.Fprintf(&b, "Matched:   %s[%d]  %s  %s %s",
		res.Matched.List, res.Matched.Index, sty.Dim(tag(r.Source)), r.Host, pathField(r.Paths))
	if m := methodField(r.Methods); m != "" {
		b.WriteString("  " + sty.Dim(m))
	}
	if r.Reason != "" {
		b.WriteString("  " + sty.Dim(fmt.Sprintf("(reason: %s)", r.Reason)))
	}
	b.WriteString("\n")

	if res.Decision == policy.DecisionAllow && res.Mode == policy.ModePassthrough {
		fmt.Fprintf(&b, "Note:      %s\n", sty.Dim(notePassthrough))
	}
	return b.String()
}

// colorDecision colors a decision verdict: allow green, soft-deny yellow,
// hard-deny red.
func colorDecision(decision string, sty *style.Styler) string {
	switch decision {
	case policy.DecisionAllow:
		return sty.OK(decision)
	case policy.DecisionHardDeny:
		return sty.Bad(decision)
	case policy.DecisionSoftDeny:
		return sty.Warn(decision)
	default:
		return decision
	}
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

// Refresh renders the outcome of `policy refresh`: a per-generator line (packages
// considered + registry cache entries cleared) and the rule counts of the recompiled
// policy. With no generators configured it says so plainly — refresh still recompiles.
func Refresh(r compile.RefreshResult, sty *style.Styler) string {
	var b strings.Builder
	if len(r.Generators) == 0 {
		b.WriteString("No generators configured — nothing to refresh.\n")
	} else {
		b.WriteString(sty.Header("Refreshed generator metadata:") + "\n")
		nameW := 0
		for _, g := range r.Generators {
			nameW = max(nameW, len(g.Name))
		}
		for _, g := range r.Generators {
			fmt.Fprintf(&b, "  %-*s  %s  %s\n",
				nameW, g.Name,
				countN(g.Packages, "package", "packages"),
				sty.Dim("("+countN(g.CacheEntriesCleared, "cache entry", "cache entries")+" cleared)"))
		}
	}
	fmt.Fprintf(&b, "Recompiled policy: %d allow, %d deny.\n", r.AllowCount, r.DenyCount)
	return b.String()
}

// refreshOutput is the `policy refresh --json` shape: per-generator invalidation detail
// and the recompiled policy's rule counts.
type refreshOutput struct {
	Generators []generatorRefreshOutput `json:"generators"`
	AllowCount int                      `json:"allow_count"`
	DenyCount  int                      `json:"deny_count"`
}

type generatorRefreshOutput struct {
	Name                string `json:"name"`
	Packages            int    `json:"packages"`
	CacheEntriesCleared int    `json:"cache_entries_cleared"`
	OutputCacheCleared  bool   `json:"output_cache_cleared"`
}

// RefreshJSON returns the structured refresh report for `policy refresh --json`. The
// generators array is always present (empty, not null, when none are configured).
func RefreshJSON(r compile.RefreshResult) (string, error) {
	out := refreshOutput{
		Generators: make([]generatorRefreshOutput, 0, len(r.Generators)),
		AllowCount: r.AllowCount,
		DenyCount:  r.DenyCount,
	}
	for _, g := range r.Generators {
		out.Generators = append(out.Generators, generatorRefreshOutput{
			Name:                g.Name,
			Packages:            g.Packages,
			CacheEntriesCleared: g.CacheEntriesCleared,
			OutputCacheCleared:  g.OutputCacheCleared,
		})
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render: marshal refresh: %w", err)
	}
	return string(data) + "\n", nil
}

// countN renders "<n> <unit>" with singular/plural agreement (1 package, 2 packages).
func countN(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
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
