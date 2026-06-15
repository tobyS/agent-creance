package config

// edit_hostservice.go extends the comment-preserving splice in edit.go to the
// network.host_services list, so `agent-creance import` can fold detected local
// ports into a hand-authored config without reflowing it. It mirrors AppendRule:
// parse to locate the insertion point, splice rendered text, and gate the result
// by re-parsing and diffing. host_services entries are identified by port (the
// label is cosmetic), so a port already present is a no-op.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// RenderRule renders one allow/deny rule as YAML sequence-item lines indented by
// indent spaces — the same rendering AppendRule splices in. init/setup use it to
// emit imported rules into a freshly generated config.
func RenderRule(rule Rule, indent int) []string { return renderRuleItem(rule, indent) }

// RenderHostService renders one network.host_services entry ("- label:port") at
// indent spaces.
func RenderHostService(hs HostService, indent int) []string { return renderHostServiceItem(hs, indent) }

// AppendHostService returns src with hs appended under network.host_services,
// preserving every other byte. changed is false (and out == src) when an entry
// with the same port already exists. The candidate is validated by re-parsing and
// diffing; a parse error or unexpected diff returns a non-nil error.
func AppendHostService(src []byte, hs HostService) (out []byte, changed bool, err error) {
	before, err := Parse(src)
	if err != nil {
		return nil, false, fmt.Errorf("read existing config: %w", err)
	}
	if containsPort(before.Network.HostServices, hs.Port) {
		return src, false, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, false, fmt.Errorf("parse config for editing: %w", err)
	}

	lines := strings.Split(string(src), "\n")
	insertAt, block := planInsertHostService(&doc, lines, hs)

	merged := make([]string, 0, len(lines)+len(block))
	merged = append(merged, lines[:insertAt]...)
	merged = append(merged, block...)
	merged = append(merged, lines[insertAt:]...)
	candidate := []byte(strings.Join(merged, "\n"))

	if err := validateAppendHostService(before, candidate, hs); err != nil {
		return nil, false, err
	}
	return candidate, true, nil
}

// planInsertHostService finds the line index to splice at and renders the block to
// insert, walking as deep into network → host_services as the existing structure
// goes and synthesizing only the missing suffix.
func planInsertHostService(doc *yaml.Node, lines []string, hs HostService) (int, []string) {
	anchors := collectAnchors(doc)
	root := rootMapping(doc)

	if root == nil {
		return endOfFile(lines), renderNestedHostService(0, []string{"network", "host_services"}, hs)
	}
	netKey, netVal := mappingChild(root, "network")
	if netKey == nil {
		return endOfFile(lines), renderNestedHostService(0, []string{"network", "host_services"}, hs)
	}
	if !isMapping(netVal) {
		idx := endOfRegion(anchors, lines, netKey.Line, indentOf(netKey))
		return idx, renderNestedHostService(indentOf(netKey)+2, []string{"host_services"}, hs)
	}

	hsKey, hsVal := mappingChild(netVal, "host_services")
	if hsKey == nil {
		idx := endOfRegion(anchors, lines, netKey.Line, indentOf(netKey))
		return idx, renderNestedHostService(indentOf(netKey)+2, []string{"host_services"}, hs)
	}

	itemIndent := indentOf(hsKey) + 2
	if hsVal.Kind == yaml.SequenceNode && len(hsVal.Content) > 0 {
		itemIndent = leadingSpaces(lines[hsVal.Content[0].Line-1])
	}
	idx := endOfRegion(anchors, lines, hsKey.Line, indentOf(hsKey))
	return idx, renderHostServiceItem(hs, itemIndent)
}

// renderNestedHostService wraps renderHostServiceItem in the chain of missing
// mapping keys, each two spaces deeper, starting at baseIndent.
func renderNestedHostService(baseIndent int, keys []string, hs HostService) []string {
	var out []string
	for i, k := range keys {
		out = append(out, strings.Repeat(" ", baseIndent+i*2)+k+":")
	}
	return append(out, renderHostServiceItem(hs, baseIndent+len(keys)*2)...)
}

// renderHostServiceItem renders one host_services entry as "- label:port". The
// label/port form is a plain scalar (our labels are simple identifiers), matching
// the design's hand-authored example.
func renderHostServiceItem(hs HostService, indent int) []string {
	return []string{strings.Repeat(" ", indent) + "- " + hs.Label + ":" + strconv.Itoa(hs.Port)}
}

// validateAppendHostService re-parses the candidate and asserts the only change
// versus before is hs added to host_services: both egress rule lists are identical
// and host_services is the old set plus exactly hs.
func validateAppendHostService(before *Config, candidate []byte, hs HostService) error {
	after, err := Parse(candidate)
	if err != nil {
		return fmt.Errorf("edit produced invalid config: %w", err)
	}
	if !sameIdentities(listRules(before, AllowList), listRules(after, AllowList)) {
		return fmt.Errorf("edit unexpectedly changed the allow list")
	}
	if !sameIdentities(listRules(before, DenyList), listRules(after, DenyList)) {
		return fmt.Errorf("edit unexpectedly changed the deny_always list")
	}
	want := append(append([]HostService{}, before.Network.HostServices...), hs)
	if !sameHostServices(want, after.Network.HostServices) {
		return fmt.Errorf("edit did not append the host service as expected")
	}
	return nil
}

func containsPort(services []HostService, port int) bool {
	for _, hs := range services {
		if hs.Port == port {
			return true
		}
	}
	return false
}

func sameHostServices(a, b []HostService) bool {
	if len(a) != len(b) {
		return false
	}
	sa := append([]HostService(nil), a...)
	sb := append([]HostService(nil), b...)
	less := func(s []HostService) func(i, j int) bool {
		return func(i, j int) bool {
			if s[i].Port != s[j].Port {
				return s[i].Port < s[j].Port
			}
			return s[i].Label < s[j].Label
		}
	}
	sort.Slice(sa, less(sa))
	sort.Slice(sb, less(sb))
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}
