package config

// edit.go adds the one mutating operation the config package supports: appending a
// single allow/deny Rule to a .agent-creance.yaml document (project, global, or the
// session-overlay) for the `agent-creance allow`/`deny` commands (AC-0030).
//
// The committed config is hand-authored and comment-rich (docs/design.md:299), so a
// decode→re-encode round-trip is the wrong tool: it would discard comments and
// reflow the file. Instead we parse only to *locate* the insertion point (yaml.Node
// carries Line/Column for every node) and splice the rendered rule in as text,
// leaving every other byte untouched. When the target key or its parent sections are
// absent or commented-out, we synthesize the minimal missing structure at the right
// indent.
//
// Two safety nets bound the splice's fragility: a duplicate check (an identical rule
// is a no-op) and a validation gate — the candidate bytes are re-parsed with the same
// strict Parse the compiler uses, and the resulting rule set must equal the original
// plus exactly the new rule, or AppendRule refuses to return it. A splice bug can
// therefore never reach disk as a silently-wrong policy.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// RuleList selects which egress list AppendRule targets.
type RuleList int

const (
	// AllowList is network.egress.allow (soft-allow rules).
	AllowList RuleList = iota
	// DenyList is network.egress.deny_always (hard-deny rules).
	DenyList
)

func (l RuleList) keyName() string {
	if l == DenyList {
		return "deny_always"
	}
	return "allow"
}

// AppendRule returns src with rule appended under network.egress.<list>, preserving
// every other byte (comments, blank lines, quoting, indentation). changed is false
// (and out == src) when an identical rule already exists. The candidate is validated
// by re-parsing and diffing the rule set; a parse error or unexpected diff returns a
// non-nil error and never a partial result.
func AppendRule(src []byte, list RuleList, rule Rule) (out []byte, changed bool, err error) {
	// The existing document must parse — we refuse to edit a config we can't read,
	// and we need its rule set for the duplicate check and the validation gate.
	before, err := Parse(src)
	if err != nil {
		return nil, false, fmt.Errorf("read existing config: %w", err)
	}
	if containsRule(listRules(before, list), rule) {
		return src, false, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, false, fmt.Errorf("parse config for editing: %w", err)
	}

	lines := strings.Split(string(src), "\n")
	insertAt, block := planInsert(&doc, lines, list, rule)

	merged := make([]string, 0, len(lines)+len(block))
	merged = append(merged, lines[:insertAt]...)
	merged = append(merged, block...)
	merged = append(merged, lines[insertAt:]...)
	candidate := []byte(strings.Join(merged, "\n"))

	if err := validateAppend(before, candidate, list, rule); err != nil {
		return nil, false, err
	}
	return candidate, true, nil
}

// planInsert finds the line index to splice at and renders the block of lines to
// insert. It walks as deep into network → egress → <list> as the existing structure
// goes and synthesizes only the missing suffix, indented to match.
func planInsert(doc *yaml.Node, lines []string, list RuleList, rule Rule) (int, []string) {
	anchors := collectAnchors(doc)
	root := rootMapping(doc)

	// Whole sections missing → build from scratch at end of file (a new top-level
	// network: key is safe precisely because none exists).
	if root == nil {
		return endOfFile(lines), renderNested(0, []string{"network", "egress", list.keyName()}, rule)
	}

	netKey, netVal := mappingChild(root, "network")
	if netKey == nil {
		return endOfFile(lines), renderNested(0, []string{"network", "egress", list.keyName()}, rule)
	}
	if !isMapping(netVal) {
		// network: present but empty/null — add egress + list beneath it.
		idx := endOfRegion(anchors, lines, netKey.Line, indentOf(netKey))
		return idx, renderNested(indentOf(netKey)+2, []string{"egress", list.keyName()}, rule)
	}

	egKey, egVal := mappingChild(netVal, "egress")
	if egKey == nil {
		idx := endOfRegion(anchors, lines, netKey.Line, indentOf(netKey))
		return idx, renderNested(indentOf(netKey)+2, []string{"egress", list.keyName()}, rule)
	}
	if !isMapping(egVal) {
		idx := endOfRegion(anchors, lines, egKey.Line, indentOf(egKey))
		return idx, renderNested(indentOf(egKey)+2, []string{list.keyName()}, rule)
	}

	listKey, listVal := mappingChild(egVal, list.keyName())
	if listKey == nil {
		idx := endOfRegion(anchors, lines, egKey.Line, indentOf(egKey))
		return idx, renderNested(indentOf(egKey)+2, []string{list.keyName()}, rule)
	}

	// The list already exists: append one item at the end of its region.
	itemIndent := indentOf(listKey) + 2
	if listVal.Kind == yaml.SequenceNode && len(listVal.Content) > 0 {
		itemIndent = leadingSpaces(lines[listVal.Content[0].Line-1])
	}
	idx := endOfRegion(anchors, lines, listKey.Line, indentOf(listKey))
	return idx, renderRuleItem(rule, itemIndent)
}

// endOfRegion returns the line index (0-based) at which to insert so the new content
// lands at the end of the mapping whose owning key sits at ownerLine/ownerIndent. The
// region ends at the first structural node deeper in the document that is no more
// indented than the owner (a sibling or shallower key), or at EOF. Trailing blank
// lines are skipped so the insert hugs the last real content.
func endOfRegion(anchors []anchor, lines []string, ownerLine, ownerIndent int) int {
	boundary := len(lines) + 1 // 1-based; default past EOF
	for _, a := range anchors {
		if a.line > ownerLine && a.indent <= ownerIndent && a.line < boundary {
			boundary = a.line
		}
	}
	idx := boundary - 1 // 1-based line → 0-based index to insert before
	if idx > len(lines) {
		idx = len(lines)
	}
	return backOverBlanks(lines, idx)
}

// endOfFile returns the insertion index at the end of the file, skipping a trailing
// empty element (from a file that ends in a newline) and any blank lines so a
// synthesized top-level block hugs the last real content.
func endOfFile(lines []string) int {
	return backOverBlanks(lines, len(lines))
}

func backOverBlanks(lines []string, idx int) int {
	for idx > 0 && strings.TrimSpace(lines[idx-1]) == "" {
		idx--
	}
	return idx
}

// renderNested wraps renderRuleItem in the chain of mapping keys that are missing,
// each two spaces deeper than the last, starting at baseIndent.
func renderNested(baseIndent int, keys []string, rule Rule) []string {
	var out []string
	for i, k := range keys {
		out = append(out, strings.Repeat(" ", baseIndent+i*2)+k+":")
	}
	return append(out, renderRuleItem(rule, baseIndent+len(keys)*2)...)
}

// renderRuleItem renders one rule as a YAML sequence item at the given indent. Empty
// fields are omitted; paths/methods use flow style to match the design's example
// (paths: ["/repos/foo/"]).
func renderRuleItem(rule Rule, indent int) []string {
	pad := strings.Repeat(" ", indent)
	out := []string{pad + "- host: " + scalar(rule.Host)}
	if rule.Paths != nil && len(*rule.Paths) > 0 {
		out = append(out, pad+"  paths: "+flowSeq(*rule.Paths))
	}
	if rule.Methods != nil && len(*rule.Methods) > 0 {
		out = append(out, pad+"  methods: "+flowSeq(*rule.Methods))
	}
	if rule.Mode != "" && rule.Mode != ModeIntercept {
		out = append(out, pad+"  mode: "+scalar(rule.Mode))
	}
	if rule.Inject != "" {
		out = append(out, pad+"  inject: "+scalar(rule.Inject))
	}
	if rule.InCage {
		out = append(out, pad+"  in_cage: true")
	}
	if rule.Reason != "" {
		out = append(out, pad+"  reason: "+strconv.Quote(rule.Reason))
	}
	return out
}

func flowSeq(items []string) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = scalar(it)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// scalar renders a string as a YAML flow scalar, quoting it (Go double-quote syntax,
// which YAML's double-quoted style accepts) unless it is plainly safe unquoted. Hosts
// like "*" or "*.medium.com" and any path therefore get quoted.
func scalar(s string) string {
	if s != "" && isPlainSafe(s) {
		return s
	}
	return strconv.Quote(s)
}

func isPlainSafe(s string) bool {
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		default:
			return false
		}
		if i == 0 && !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// validateAppend re-parses the candidate and asserts the only change versus before is
// rule added to list: the untouched list is identical and the target list is the old
// set plus exactly rule (including its reason). This is the gate that makes the
// textual splice safe.
func validateAppend(before *Config, candidate []byte, list RuleList, rule Rule) error {
	after, err := Parse(candidate)
	if err != nil {
		return fmt.Errorf("edit produced invalid config: %w", err)
	}

	other := AllowList
	if list == AllowList {
		other = DenyList
	}
	if !sameIdentities(listRules(before, other), listRules(after, other)) {
		return fmt.Errorf("edit unexpectedly changed the %s list", other.keyName())
	}

	want := append(append([]Rule{}, listRules(before, list)...), rule)
	got := listRules(after, list)
	if !sameIdentities(want, got) {
		return fmt.Errorf("edit did not append the rule to %s as expected", list.keyName())
	}
	for _, r := range got {
		if ruleIdentity(r) != ruleIdentity(rule) {
			continue
		}
		if r.Reason != rule.Reason {
			return fmt.Errorf("edit recorded the wrong reason for the appended rule")
		}
		if r.Inject != rule.Inject || r.InCage != rule.InCage {
			return fmt.Errorf("edit did not render the auth axis (inject/in_cage) for the appended rule")
		}
	}
	return nil
}

// --- rule identity helpers -------------------------------------------------

func listRules(c *Config, list RuleList) []Rule {
	if list == DenyList {
		return c.Network.Egress.DenyAlways
	}
	return c.Network.Egress.Allow
}

// ruleIdentity keys a rule on what makes it the "same" allow/deny for duplicate
// detection: host + path set + method set. Mode and reason are intentionally excluded
// (re-allowing a host is a no-op regardless of an added reason).
func ruleIdentity(r Rule) string {
	return r.Host + "\x00" + normSlice(r.Paths) + "\x00" + normSlice(r.Methods)
}

func normSlice(p *[]string) string {
	if p == nil {
		return "<nil>"
	}
	s := append([]string(nil), *p...)
	sort.Strings(s)
	return strings.Join(s, "\x01")
}

func containsRule(rules []Rule, target Rule) bool {
	return indexOfIdentity(rules, ruleIdentity(target)) >= 0
}

// indexOfIdentity returns the index of the first rule whose identity (host + path
// set + method set) equals id, or -1. SetRuleAuth uses it to decide between
// updating an existing rule in place and appending a new one.
func indexOfIdentity(rules []Rule, id string) int {
	for i, r := range rules {
		if ruleIdentity(r) == id {
			return i
		}
	}
	return -1
}

func sameIdentities(a, b []Rule) bool {
	if len(a) != len(b) {
		return false
	}
	ida := identities(a)
	idb := identities(b)
	for i := range ida {
		if ida[i] != idb[i] {
			return false
		}
	}
	return true
}

func identities(rules []Rule) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = ruleIdentity(r)
	}
	sort.Strings(out)
	return out
}

// --- yaml.Node navigation --------------------------------------------------

type anchor struct {
	line   int // 1-based
	indent int // 0-based column-1
}

func collectAnchors(n *yaml.Node) []anchor {
	var out []anchor
	var walk func(*yaml.Node)
	walk = func(node *yaml.Node) {
		if node == nil {
			return
		}
		if node.Line > 0 && node.Kind != yaml.DocumentNode {
			out = append(out, anchor{line: node.Line, indent: node.Column - 1})
		}
		for _, c := range node.Content {
			walk(c)
		}
	}
	walk(n)
	return out
}

func rootMapping(doc *yaml.Node) *yaml.Node {
	if doc == nil || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	return root
}

func mappingChild(m *yaml.Node, key string) (keyNode, valNode *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

func isMapping(n *yaml.Node) bool { return n != nil && n.Kind == yaml.MappingNode }

func indentOf(n *yaml.Node) int { return n.Column - 1 }

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}
