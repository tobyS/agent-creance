package config

// edit_include.go extends the comment-preserving splice in edit.go to the
// top-level include: list, so `agent-creance include` can add a config fragment
// to compose a layered config without reflowing the hand-authored file. It mirrors
// AppendRule: parse to locate the insertion point, splice rendered text, and gate
// the result by re-parsing and diffing. include entries are identified by their
// exact string (the loader applies no normalisation), so an entry already present
// verbatim is a no-op.

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// AppendInclude returns src with inc appended to the top-level include: list,
// preserving every other byte (comments, blank lines, quoting, indentation).
// changed is false (and out == src) when an entry equal to inc already exists. The
// candidate is validated by re-parsing and diffing; a parse error or unexpected
// diff returns a non-nil error and never a partial result.
func AppendInclude(src []byte, inc string) (out []byte, changed bool, err error) {
	before, err := Parse(src)
	if err != nil {
		return nil, false, fmt.Errorf("read existing config: %w", err)
	}
	if containsInclude(before.Include, inc) {
		return src, false, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, false, fmt.Errorf("parse config for editing: %w", err)
	}

	lines := strings.Split(string(src), "\n")
	at, block, replace := planInsertInclude(&doc, lines, inc)

	merged := make([]string, 0, len(lines)+len(block))
	merged = append(merged, lines[:at]...)
	merged = append(merged, block...)
	if replace {
		merged = append(merged, lines[at+1:]...) // drop the rewritten key line
	} else {
		merged = append(merged, lines[at:]...)
	}
	candidate := []byte(strings.Join(merged, "\n"))

	if err := validateAppendInclude(before, candidate, inc); err != nil {
		return nil, false, err
	}
	return candidate, true, nil
}

// planInsertInclude finds where to splice the new include and renders the block.
// include: is a top-level key whose items are plain string scalars, so navigation
// is one level shallower than AppendRule's. When the key exists but holds no items
// (a null `include:` or a flow `include: []`), the key line is rewritten to a block
// list carrying the new item (replace=true); otherwise the item is inserted at the
// end of the existing list's region.
func planInsertInclude(doc *yaml.Node, lines []string, inc string) (at int, block []string, replace bool) {
	anchors := collectAnchors(doc)
	root := rootMapping(doc)
	if root == nil {
		return endOfFile(lines), renderNestedInclude(0, inc), false
	}

	incKey, incVal := mappingChild(root, "include")
	if incKey == nil {
		return endOfFile(lines), renderNestedInclude(0, inc), false
	}

	// A populated block list: append one item at the end of its region, matching the
	// existing items' indent.
	if incVal.Kind == yaml.SequenceNode && len(incVal.Content) > 0 {
		itemIndent := leadingSpaces(lines[incVal.Content[0].Line-1])
		idx := endOfRegion(anchors, lines, incKey.Line, indentOf(incKey))
		return idx, renderIncludeItem(inc, itemIndent), false
	}

	// include: present but empty (null or flow []). Rewrite the key line to a block
	// list so the appended item is valid YAML.
	keyIndent := indentOf(incKey)
	rewritten := append([]string{strings.Repeat(" ", keyIndent) + "include:"}, renderIncludeItem(inc, keyIndent+2)...)
	return incKey.Line - 1, rewritten, true
}

// renderNestedInclude synthesises a fresh include: key at baseIndent with the entry
// as its first item, used when no include: key exists yet.
func renderNestedInclude(baseIndent int, inc string) []string {
	return append([]string{strings.Repeat(" ", baseIndent) + "include:"}, renderIncludeItem(inc, baseIndent+2)...)
}

// renderIncludeItem renders one include entry as "- <scalar>" at indent spaces. The
// shared scalar helper quotes paths that are not plainly safe (e.g. "~/x.yaml",
// "./x.yaml", anything with a slash).
func renderIncludeItem(inc string, indent int) []string {
	return []string{strings.Repeat(" ", indent) + "- " + scalar(inc)}
}

// validateAppendInclude re-parses the candidate and asserts the only change versus
// before is inc appended to include: both egress lists and host_services are
// identical and Include is the old slice plus exactly inc, in order.
func validateAppendInclude(before *Config, candidate []byte, inc string) error {
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
	if !sameHostServices(before.Network.HostServices, after.Network.HostServices) {
		return fmt.Errorf("edit unexpectedly changed host_services")
	}
	want := append(append([]string{}, before.Include...), inc)
	if !sameIncludes(want, after.Include) {
		return fmt.Errorf("edit did not append the include as expected")
	}
	return nil
}

func containsInclude(list []string, inc string) bool {
	for _, e := range list {
		if e == inc {
			return true
		}
	}
	return false
}

// sameIncludes compares two include lists order-sensitively: a new entry must be
// appended at the end, leaving the existing order intact.
func sameIncludes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
