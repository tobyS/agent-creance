package config

// edit_safehouse.go extends the comment-preserving splice in edit.go to the
// safehouse.add_dirs_rw / add_dirs_ro lists, so `agent-creance mount add` can append
// a filesystem mount without reflowing the hand-authored config. It mirrors
// AppendHostService: parse to locate the insertion point, splice rendered text, and
// gate the result by re-parsing and diffing. Mounts are identified by their literal
// path string within a single list, so the same path already present is a no-op.
//
// Unlike host_services (block style in the design), add_dirs_* are canonically flow
// style (`add_dirs_rw: [.]`). Appending a block item after a flow line would be
// invalid YAML, so when the target list is flow (or empty/null) the key line is
// rewritten in place as a flow sequence reconstructed from the typed values plus the
// new entry; every other line — and so every comment — is left untouched. A populated
// block-style list is appended to item-by-item exactly like host_services.

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// dirListKey returns the safehouse mapping key for the rw/ro selector.
func dirListKey(rw bool) string {
	if rw {
		return "add_dirs_rw"
	}
	return "add_dirs_ro"
}

// AppendDir returns src with dir appended under safehouse.add_dirs_rw (rw == true) or
// safehouse.add_dirs_ro, preserving every other byte. changed is false (and out ==
// src) when the same path already exists in the target list. The candidate is
// validated by re-parsing and diffing; a parse error or unexpected diff returns a
// non-nil error and never a partial result.
func AppendDir(src []byte, dir string, rw bool) (out []byte, changed bool, err error) {
	before, err := Parse(src)
	if err != nil {
		return nil, false, fmt.Errorf("read existing config: %w", err)
	}
	if containsDir(dirList(before, rw), dir) {
		return src, false, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, false, fmt.Errorf("parse config for editing: %w", err)
	}

	lines := strings.Split(string(src), "\n")
	at, block, drop := planInsertDir(&doc, lines, before, dir, rw)

	merged := make([]string, 0, len(lines)+len(block))
	merged = append(merged, lines[:at]...)
	merged = append(merged, block...)
	merged = append(merged, lines[at+drop:]...)
	candidate := []byte(strings.Join(merged, "\n"))

	if err := validateAppendDir(before, candidate, dir, rw); err != nil {
		return nil, false, err
	}
	return candidate, true, nil
}

// planInsertDir finds where to splice the new mount and renders the block. It returns
// the 0-based line index to splice at, the lines to insert, and the number of existing
// lines to drop at that index (0 for a pure insert, >=1 when an existing flow key line
// is being rewritten).
func planInsertDir(doc *yaml.Node, lines []string, before *Config, dir string, rw bool) (at int, block []string, drop int) {
	key := dirListKey(rw)
	anchors := collectAnchors(doc)
	root := rootMapping(doc)

	if root == nil {
		return endOfFile(lines), renderNestedDir(0, []string{"safehouse", key}, dir), 0
	}
	shKey, shVal := mappingChild(root, "safehouse")
	if shKey == nil {
		return endOfFile(lines), renderNestedDir(0, []string{"safehouse", key}, dir), 0
	}
	if !isMapping(shVal) {
		idx := endOfRegion(anchors, lines, shKey.Line, indentOf(shKey))
		return idx, renderNestedDir(indentOf(shKey)+2, []string{key}, dir), 0
	}

	listKey, listVal := mappingChild(shVal, key)
	if listKey == nil {
		idx := endOfRegion(anchors, lines, shKey.Line, indentOf(shKey))
		return idx, renderNestedDir(indentOf(shKey)+2, []string{key}, dir), 0
	}

	// Populated block-style list: append one item at the end of its region, matching
	// the existing items' indent — comments and every other byte survive verbatim.
	if listVal != nil && listVal.Kind == yaml.SequenceNode && listVal.Style != yaml.FlowStyle && len(listVal.Content) > 0 {
		itemIndent := leadingSpaces(lines[listVal.Content[0].Line-1])
		idx := endOfRegion(anchors, lines, listKey.Line, indentOf(listKey))
		return idx, renderDirItem(dir, itemIndent), 0
	}

	// Flow style (empty or populated), or null/empty: rewrite the key line(s) as a flow
	// sequence reconstructed from the current typed values plus the new entry. An inline
	// comment on the rewritten line (e.g. `add_dirs_rw: [.]  # note`) is carried over so
	// it is not lost with the line.
	keyIndent := indentOf(listKey)
	items := append(append([]string{}, dirList(before, rw)...), dir)
	rewritten := []string{flowDirLine(keyIndent, key, items, lineComment(listKey, listVal))}
	last := maxLine(listVal)
	if last < listKey.Line {
		last = listKey.Line
	}
	return listKey.Line - 1, rewritten, last - listKey.Line + 1
}

// renderNestedDir synthesises the chain of missing mapping keys (each two spaces
// deeper) ending in a flow-style single-item list `key: [dir]`, matching the design's
// canonical add_dirs_* formatting.
func renderNestedDir(baseIndent int, keys []string, dir string) []string {
	var out []string
	for i := 0; i < len(keys)-1; i++ {
		out = append(out, strings.Repeat(" ", baseIndent+i*2)+keys[i]+":")
	}
	last := keys[len(keys)-1]
	lastIndent := baseIndent + (len(keys)-1)*2
	out = append(out, strings.Repeat(" ", lastIndent)+last+": "+flowSeq([]string{dir}))
	return out
}

// renderDirItem renders one add_dirs_* entry as a block sequence item "- <scalar>".
func renderDirItem(dir string, indent int) []string {
	return []string{strings.Repeat(" ", indent) + "- " + scalar(dir)}
}

// flowDirLine renders a flow-style add_dirs_* key line `<key>: [items...]` at keyIndent,
// re-appending an inline comment when one was carried from the original line. Shared by
// the append (AppendDir) and remove (RemoveDir) flow-rewrite paths.
func flowDirLine(keyIndent int, key string, items []string, comment string) string {
	line := strings.Repeat(" ", keyIndent) + key + ": " + flowSeq(items)
	if comment != "" {
		line += "  " + comment
	}
	return line
}

// lineComment returns the first non-empty yaml LineComment (including its leading "#")
// from the key or value node, or "" when neither carries one.
func lineComment(keyNode, valNode *yaml.Node) string {
	if keyNode != nil && keyNode.LineComment != "" {
		return keyNode.LineComment
	}
	if valNode != nil && valNode.LineComment != "" {
		return valNode.LineComment
	}
	return ""
}

// maxLine returns the largest Line number anywhere in the node subtree (0 if none),
// used to find the last source line a flow value occupies.
func maxLine(n *yaml.Node) int {
	if n == nil {
		return 0
	}
	m := n.Line
	for _, c := range n.Content {
		if l := maxLine(c); l > m {
			m = l
		}
	}
	return m
}

// validateAppendDir re-parses the candidate and asserts the only change versus before
// is dir appended to the target add_dirs list: both egress lists, host_services and the
// other add_dirs list are identical, and the target list is the old slice plus exactly
// dir, in order.
func validateAppendDir(before *Config, candidate []byte, dir string, rw bool) error {
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
	if !sameIncludes(dirList(before, !rw), dirList(after, !rw)) {
		return fmt.Errorf("edit unexpectedly changed the other add_dirs list")
	}
	want := append(append([]string{}, dirList(before, rw)...), dir)
	if !sameIncludes(want, dirList(after, rw)) {
		return fmt.Errorf("edit did not append the mount as expected")
	}
	return nil
}

func dirList(c *Config, rw bool) []string {
	if rw {
		return c.Safehouse.AddDirsRW
	}
	return c.Safehouse.AddDirsRO
}

func containsDir(dirs []string, dir string) bool {
	for _, d := range dirs {
		if d == dir {
			return true
		}
	}
	return false
}
