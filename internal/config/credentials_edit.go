package config

// credentials_edit.go adds the append/remove operations for the top-level
// credentials: mapping block, backing `agent-creance credential add/remove`
// (AC-0068d). The existing edit.go/remove.go writers target sequences
// (allow/deny_always, host_services, add_dirs); a name→entry *mapping* is a new
// shape, so it gets its own navigator and renderer here.
//
// Like every config mutation it is a comment-preserving text splice: parse only to
// locate the insertion/removal point, splice rendered lines in (or out) as text, and
// gate the candidate by re-parsing and diffing so a splice bug can never reach disk.

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// AppendCredential returns src with a credentials: entry named name added,
// preserving every other byte. changed is false (and out == src) when a credential
// of that name already exists. The candidate is validated by re-parsing and diffing
// the credential set (and asserting no egress rule changed); a parse error or
// unexpected diff returns a non-nil error and never a partial result. Only a
// reference is stored — never a resolved secret value.
func AppendCredential(src []byte, name string, cred Credential) (out []byte, changed bool, err error) {
	before, err := Parse(src)
	if err != nil {
		return nil, false, fmt.Errorf("read existing config: %w", err)
	}
	if _, ok := before.Credentials[name]; ok {
		return src, false, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, false, fmt.Errorf("parse config for editing: %w", err)
	}

	lines := strings.Split(string(src), "\n")
	insertAt, block := planCredentialInsert(&doc, lines, name, cred)

	merged := make([]string, 0, len(lines)+len(block))
	merged = append(merged, lines[:insertAt]...)
	merged = append(merged, block...)
	merged = append(merged, lines[insertAt:]...)
	candidate := []byte(strings.Join(merged, "\n"))

	if err := validateAppendCredential(before, candidate, name, cred); err != nil {
		return nil, false, err
	}
	return candidate, true, nil
}

// RemoveCredential returns src with the credentials: entry named name removed,
// preserving every other byte. Returns ErrNotFound when no such credential exists.
func RemoveCredential(src []byte, name string) (out []byte, changed bool, err error) {
	before, err := Parse(src)
	if err != nil {
		return nil, false, fmt.Errorf("read existing config: %w", err)
	}
	if _, ok := before.Credentials[name]; !ok {
		return src, false, ErrNotFound
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, false, fmt.Errorf("parse config for editing: %w", err)
	}
	credVal := credentialsNode(&doc)
	keyNode, valNode := credentialEntry(credVal, name)
	if keyNode == nil {
		return nil, false, fmt.Errorf("config: internal: credentials node mismatch")
	}

	lines := strings.Split(string(src), "\n")
	candidate := []byte(strings.Join(spliceLines(lines, keyNode.Line-1, maxLine(valNode), nil), "\n"))

	if err := validateRemoveCredential(before, candidate, name); err != nil {
		return nil, false, err
	}
	return candidate, true, nil
}

// planCredentialInsert finds the line index to splice at and renders the block of
// lines to insert. It walks as deep into credentials: as the existing structure goes
// and synthesizes only the missing suffix at the right indent.
func planCredentialInsert(doc *yaml.Node, lines []string, name string, cred Credential) (int, []string) {
	anchors := collectAnchors(doc)
	root := rootMapping(doc)
	if root == nil {
		return endOfFile(lines), renderCredentialBlock(0, name, cred)
	}

	credKey, credVal := mappingChild(root, "credentials")
	if credKey == nil {
		return endOfFile(lines), renderCredentialBlock(0, name, cred)
	}
	if !isMapping(credVal) {
		// credentials: present but empty/null — add the first entry beneath it.
		idx := endOfRegion(anchors, lines, credKey.Line, indentOf(credKey))
		return idx, renderCredentialEntry(indentOf(credKey)+2, name, cred)
	}

	// The mapping already exists: append one entry at the end of its region, matching
	// the existing entries' indent.
	entryIndent := indentOf(credKey) + 2
	if len(credVal.Content) > 0 {
		entryIndent = leadingSpaces(lines[credVal.Content[0].Line-1])
	}
	idx := endOfRegion(anchors, lines, credKey.Line, indentOf(credKey))
	return idx, renderCredentialEntry(entryIndent, name, cred)
}

// renderCredentialBlock renders a fresh top-level credentials: key plus one entry.
func renderCredentialBlock(baseIndent int, name string, cred Credential) []string {
	out := []string{strings.Repeat(" ", baseIndent) + "credentials:"}
	return append(out, renderCredentialEntry(baseIndent+2, name, cred)...)
}

// renderCredentialEntry renders one credentials: entry (name: mapping) at the given
// indent. header is omitted when it is empty or the Authorization default; username
// is omitted when empty. source and template are always present.
func renderCredentialEntry(indent int, name string, cred Credential) []string {
	pad := strings.Repeat(" ", indent)
	out := []string{pad + scalar(name) + ":"}
	out = append(out, pad+"  source: "+scalar(cred.Source))
	out = append(out, pad+"  template: "+scalar(cred.Template))
	if cred.Header != "" && cred.Header != DefaultCredentialHeader {
		out = append(out, pad+"  header: "+scalar(cred.Header))
	}
	if cred.Username != "" {
		out = append(out, pad+"  username: "+scalar(cred.Username))
	}
	return out
}

func validateAppendCredential(before *Config, candidate []byte, name string, cred Credential) error {
	after, err := Parse(candidate)
	if err != nil {
		return fmt.Errorf("edit produced invalid config: %w", err)
	}
	if !sameIdentities(listRules(before, AllowList), listRules(after, AllowList)) ||
		!sameIdentities(listRules(before, DenyList), listRules(after, DenyList)) {
		return fmt.Errorf("edit unexpectedly changed an egress list")
	}
	want := cloneCredentials(before.Credentials)
	want[name] = cred
	defaultCredentialHeaders(want) // mirror the parse-time header default
	if !sameCredentials(after.Credentials, want) {
		return fmt.Errorf("edit did not add credential %q as expected", name)
	}
	return nil
}

func validateRemoveCredential(before *Config, candidate []byte, name string) error {
	after, err := Parse(candidate)
	if err != nil {
		return fmt.Errorf("edit produced invalid config: %w", err)
	}
	if !sameIdentities(listRules(before, AllowList), listRules(after, AllowList)) ||
		!sameIdentities(listRules(before, DenyList), listRules(after, DenyList)) {
		return fmt.Errorf("edit unexpectedly changed an egress list")
	}
	want := cloneCredentials(before.Credentials)
	delete(want, name)
	if !sameCredentials(after.Credentials, want) {
		return fmt.Errorf("edit did not remove credential %q as expected", name)
	}
	return nil
}

// --- credentials navigation + comparison helpers ---------------------------

func credentialsNode(doc *yaml.Node) *yaml.Node {
	root := rootMapping(doc)
	if root == nil {
		return nil
	}
	_, credVal := mappingChild(root, "credentials")
	return credVal
}

// credentialEntry returns the key and value nodes for the entry named name in a
// credentials mapping node, or (nil, nil) when absent.
func credentialEntry(credVal *yaml.Node, name string) (keyNode, valNode *yaml.Node) {
	if credVal == nil || credVal.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(credVal.Content); i += 2 {
		if credVal.Content[i].Value == name {
			return credVal.Content[i], credVal.Content[i+1]
		}
	}
	return nil, nil
}

func cloneCredentials(in map[string]Credential) map[string]Credential {
	out := make(map[string]Credential, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// sameCredentials reports whether a and b hold the same name→Credential entries. A
// nil and an empty map are treated as equal (removing the last credential yields a
// nil map on re-parse).
func sameCredentials(a, b map[string]Credential) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || va != vb {
			return false
		}
	}
	return true
}
