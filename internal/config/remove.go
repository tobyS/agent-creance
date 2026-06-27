package config

// remove.go adds the removal counterpart to the append operations in edit.go and
// friends, for `agent-creance domain/service/mount remove`. Like the appends it is a
// comment-preserving text splice: parse only to locate the element's source lines,
// drop (or, for a single path, re-render) just those lines, and gate the result by
// re-parsing and diffing. Surrounding comments and blank lines — including a standalone
// comment that visually leads the *next* element — are left untouched: an element's
// span is bounded by maxLine of its own node subtree, never by the next sibling's start,
// so removal only ever deletes the element's own lines.
//
// Removing an entry that is not present is reported as ErrNotFound (not a silent no-op),
// so the CLI can exit non-zero (AC-0067).

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// RemoveRule removes a rule from network.egress.<list>. With path == "", the whole rule
// whose host matches is removed. With a non-empty path, that path is removed from the
// matching rule's paths; if it was the rule's only path the whole rule is removed
// (a rule scoped to zero paths is meaningless and an empty paths list would silently
// widen it to host-wide, AC-0067). Returns ErrNotFound when no rule matches (host
// absent, or the path absent from that host's rule).
func RemoveRule(src []byte, list RuleList, host, path string) (out []byte, changed bool, err error) {
	before, err := Parse(src)
	if err != nil {
		return nil, false, fmt.Errorf("read existing config: %w", err)
	}
	rules := listRules(before, list)
	idx, removeWhole, modified, found := findRuleIndex(rules, host, path)
	if !found {
		return src, false, ErrNotFound
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
	end := maxLine(item) // 1-based last line == 0-based exclusive end
	var replacement []string
	if !removeWhole {
		replacement = renderRuleItem(modified, leadingSpaces(lines[start]))
	}

	candidate := []byte(strings.Join(spliceLines(lines, start, end, replacement), "\n"))

	expected := expectedAfterRuleRemoval(rules, idx, removeWhole, modified)
	if err := validateRemoveRule(before, candidate, list, expected); err != nil {
		return nil, false, err
	}
	return candidate, true, nil
}

// findRuleIndex locates the rule to act on. When path == "" it matches the first rule
// with the given host (whole-rule removal). When path != "" it matches the first rule
// with that host whose paths contain path, returning removeWhole when path is that
// rule's only path, otherwise a modified copy with the path dropped.
func findRuleIndex(rules []Rule, host, path string) (idx int, removeWhole bool, modified Rule, found bool) {
	for i, r := range rules {
		if r.Host != host {
			continue
		}
		if path == "" {
			return i, true, Rule{}, true
		}
		if r.Paths == nil {
			continue
		}
		pos := indexOfString(*r.Paths, path)
		if pos < 0 {
			continue
		}
		if len(*r.Paths) == 1 {
			return i, true, Rule{}, true
		}
		np := append(append([]string{}, (*r.Paths)[:pos]...), (*r.Paths)[pos+1:]...)
		m := r
		m.Paths = &np
		return i, false, m, true
	}
	return 0, false, Rule{}, false
}

func expectedAfterRuleRemoval(before []Rule, idx int, removeWhole bool, modified Rule) []Rule {
	out := append([]Rule{}, before[:idx]...)
	if !removeWhole {
		out = append(out, modified)
	}
	return append(out, before[idx+1:]...)
}

func validateRemoveRule(before *Config, candidate []byte, list RuleList, expected []Rule) error {
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
	if !sameHostServices(before.Network.HostServices, after.Network.HostServices) {
		return fmt.Errorf("edit unexpectedly changed host_services")
	}
	if !sameIdentities(expected, listRules(after, list)) {
		return fmt.Errorf("edit did not remove the rule from %s as expected", list.keyName())
	}
	return nil
}

// RemoveHostService removes the network.host_services entry with the given port (the
// label is cosmetic). Returns ErrNotFound when no entry has that port.
func RemoveHostService(src []byte, port int) (out []byte, changed bool, err error) {
	before, err := Parse(src)
	if err != nil {
		return nil, false, fmt.Errorf("read existing config: %w", err)
	}
	idx := indexOfPort(before.Network.HostServices, port)
	if idx < 0 {
		return src, false, ErrNotFound
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, false, fmt.Errorf("parse config for editing: %w", err)
	}
	listVal := hostServicesNode(&doc)
	if listVal == nil || listVal.Kind != yaml.SequenceNode || idx >= len(listVal.Content) {
		return nil, false, fmt.Errorf("config: internal: host_services node mismatch")
	}

	lines := strings.Split(string(src), "\n")
	item := listVal.Content[idx]
	candidate := []byte(strings.Join(spliceLines(lines, item.Line-1, maxLine(item), nil), "\n"))

	after, err := Parse(candidate)
	if err != nil {
		return nil, false, fmt.Errorf("edit produced invalid config: %w", err)
	}
	if !sameIdentities(listRules(before, AllowList), listRules(after, AllowList)) ||
		!sameIdentities(listRules(before, DenyList), listRules(after, DenyList)) {
		return nil, false, fmt.Errorf("edit unexpectedly changed an egress list")
	}
	want := removeHostServiceAt(before.Network.HostServices, idx)
	if !sameHostServices(want, after.Network.HostServices) {
		return nil, false, fmt.Errorf("edit did not remove the host service as expected")
	}
	return candidate, true, nil
}

// RemoveDir removes dir from safehouse.add_dirs_rw and add_dirs_ro. A path present in
// both lists is detached from both (AC-0067). removedRW/removedRO report which list(s)
// it was in so the caller can word its message. Returns ErrNotFound when dir is in
// neither list.
func RemoveDir(src []byte, dir string) (out []byte, removedRW, removedRO, changed bool, err error) {
	cur := src
	cur, removedRW, err = removeDirOnce(cur, dir, true)
	if err != nil {
		return nil, false, false, false, err
	}
	cur, removedRO, err = removeDirOnce(cur, dir, false)
	if err != nil {
		return nil, false, false, false, err
	}
	if !removedRW && !removedRO {
		return src, false, false, false, ErrNotFound
	}
	return cur, removedRW, removedRO, true, nil
}

// removeDirOnce removes dir from a single add_dirs list, re-parsing src fresh so line
// numbers are consistent. removed is false (and out == src) when dir is not in that
// list — RemoveDir turns "in neither list" into ErrNotFound.
func removeDirOnce(src []byte, dir string, rw bool) (out []byte, removed bool, err error) {
	before, err := Parse(src)
	if err != nil {
		return nil, false, fmt.Errorf("read existing config: %w", err)
	}
	dirs := dirList(before, rw)
	pos := indexOfString(dirs, dir)
	if pos < 0 {
		return src, false, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, false, fmt.Errorf("parse config for editing: %w", err)
	}
	listKey, listVal := safehouseListNode(&doc, rw)
	if listKey == nil || listVal == nil {
		return nil, false, fmt.Errorf("config: internal: %s node mismatch", dirListKey(rw))
	}

	lines := strings.Split(string(src), "\n")
	var candidate []byte
	if listVal.Kind == yaml.SequenceNode && listVal.Style != yaml.FlowStyle && len(listVal.Content) > 0 {
		// Block style: drop the one item's line(s).
		item := listVal.Content[pos]
		candidate = []byte(strings.Join(spliceLines(lines, item.Line-1, maxLine(item), nil), "\n"))
	} else {
		// Flow style: rewrite the key line with the entry removed.
		remaining := append(append([]string{}, dirs[:pos]...), dirs[pos+1:]...)
		keyIndent := indentOf(listKey)
		line := flowDirLine(keyIndent, dirListKey(rw), remaining, lineComment(listKey, listVal))
		last := maxLine(listVal)
		if last < listKey.Line {
			last = listKey.Line
		}
		candidate = []byte(strings.Join(spliceLines(lines, listKey.Line-1, last, []string{line}), "\n"))
	}

	after, err := Parse(candidate)
	if err != nil {
		return nil, false, fmt.Errorf("edit produced invalid config: %w", err)
	}
	want := append(append([]string{}, dirs[:pos]...), dirs[pos+1:]...)
	if !sameIncludes(want, dirList(after, rw)) {
		return nil, false, fmt.Errorf("edit did not remove the mount as expected")
	}
	if !sameIncludes(dirList(before, !rw), dirList(after, !rw)) {
		return nil, false, fmt.Errorf("edit unexpectedly changed the other add_dirs list")
	}
	return candidate, true, nil
}

// --- splice + navigation helpers -------------------------------------------

// spliceLines returns lines with [start, end) replaced by replacement (0-based; end
// exclusive). replacement may be nil to delete the span.
func spliceLines(lines []string, start, end int, replacement []string) []string {
	merged := make([]string, 0, len(lines)-(end-start)+len(replacement))
	merged = append(merged, lines[:start]...)
	merged = append(merged, replacement...)
	return append(merged, lines[end:]...)
}

func egressListNode(doc *yaml.Node, list RuleList) *yaml.Node {
	root := rootMapping(doc)
	if root == nil {
		return nil
	}
	_, netVal := mappingChild(root, "network")
	if !isMapping(netVal) {
		return nil
	}
	_, egVal := mappingChild(netVal, "egress")
	if !isMapping(egVal) {
		return nil
	}
	_, listVal := mappingChild(egVal, list.keyName())
	return listVal
}

func hostServicesNode(doc *yaml.Node) *yaml.Node {
	root := rootMapping(doc)
	if root == nil {
		return nil
	}
	_, netVal := mappingChild(root, "network")
	if !isMapping(netVal) {
		return nil
	}
	_, hsVal := mappingChild(netVal, "host_services")
	return hsVal
}

func safehouseListNode(doc *yaml.Node, rw bool) (keyNode, valNode *yaml.Node) {
	root := rootMapping(doc)
	if root == nil {
		return nil, nil
	}
	_, shVal := mappingChild(root, "safehouse")
	if !isMapping(shVal) {
		return nil, nil
	}
	return mappingChild(shVal, dirListKey(rw))
}

func indexOfString(ss []string, target string) int {
	for i, s := range ss {
		if s == target {
			return i
		}
	}
	return -1
}

func indexOfPort(services []HostService, port int) int {
	for i, hs := range services {
		if hs.Port == port {
			return i
		}
	}
	return -1
}

func removeHostServiceAt(services []HostService, idx int) []HostService {
	out := append([]HostService{}, services[:idx]...)
	return append(out, services[idx+1:]...)
}
