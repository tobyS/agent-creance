package config

// setauth.go adds the upsert operation for the per-rule auth axis (inject/in_cage),
// backing `agent-creance allow --inject <name>` / `--in-cage` (AC-0068d). Unlike
// AppendRule — which treats an identity match (host + path set + method set) as a
// no-op — binding a credential to an already-allowed host must *update* that rule
// rather than silently do nothing. When no matching rule exists it falls back to
// AppendRule, so a fresh `allow <host> --inject <name>` still appends.
//
// Like the other config mutations it is a comment-preserving text splice: parse only
// to locate the matching sequence item, re-render just that item with the new auth
// axis, and gate the candidate by re-parsing and diffing.

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// SetRuleAuth ensures the rule identified by host + path set + method set carries the
// auth axis (Inject/InCage) from rule. If no such rule exists it is appended
// (AppendRule); if it exists, its inject/in_cage are updated in place
// (comment-preserving), and its reason is overwritten when rule.Reason is non-empty.
// changed is false (and out == src) when the target rule already carries exactly this
// auth axis and reason. A parse error or unexpected diff returns a non-nil error and
// never a partial result.
func SetRuleAuth(src []byte, list RuleList, rule Rule) (out []byte, changed bool, err error) {
	before, err := Parse(src)
	if err != nil {
		return nil, false, fmt.Errorf("read existing config: %w", err)
	}
	rules := listRules(before, list)
	idx := indexOfIdentity(rules, ruleIdentity(rule))
	if idx < 0 {
		return AppendRule(src, list, rule)
	}

	existing := rules[idx]
	updated := existing
	updated.Inject, updated.InCage = rule.Inject, rule.InCage
	if rule.Reason != "" {
		updated.Reason = rule.Reason
	}
	if updated.Inject == existing.Inject && updated.InCage == existing.InCage &&
		updated.Reason == existing.Reason {
		return src, false, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, false, fmt.Errorf("parse config for editing: %w", err)
	}
	listVal := egressListNode(&doc, list)
	if listVal == nil || listVal.Kind != yaml.SequenceNode || idx >= len(listVal.Content) {
		return nil, false, fmt.Errorf("config: internal: rule list node mismatch")
	}

	lines := strings.Split(string(src), "\n")
	item := listVal.Content[idx]
	start := item.Line - 1
	replacement := renderRuleItem(updated, leadingSpaces(lines[start]))
	candidate := []byte(strings.Join(spliceLines(lines, start, maxLine(item), replacement), "\n"))

	if err := validateSetRuleAuth(before, candidate, list, idx, updated); err != nil {
		return nil, false, err
	}
	return candidate, true, nil
}

// validateSetRuleAuth re-parses the candidate and asserts the only change versus
// before is the auth axis (and reason) of the rule at idx: the other list is
// identity-identical, this list keeps the same identities in the same order, and the
// rule at idx now carries the expected Inject/InCage/Reason. This is the gate that
// makes the in-place splice safe.
func validateSetRuleAuth(before *Config, candidate []byte, list RuleList, idx int, updated Rule) error {
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

	got := listRules(after, list)
	if !sameIdentities(listRules(before, list), got) {
		return fmt.Errorf("edit unexpectedly changed the %s rule set", list.keyName())
	}
	if idx >= len(got) {
		return fmt.Errorf("edit dropped the rule from %s", list.keyName())
	}
	r := got[idx]
	if ruleIdentity(r) != ruleIdentity(updated) {
		return fmt.Errorf("edit changed the identity of the bound %s rule", list.keyName())
	}
	if r.Inject != updated.Inject || r.InCage != updated.InCage || r.Reason != updated.Reason {
		return fmt.Errorf("edit did not record the auth axis (inject/in_cage) as expected")
	}
	return nil
}
