package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Sentinels for the loader's include-resolution failures (AC-0008). They are wrapped
// with the offending path/chain for the human-readable message and are
// errors.Is-checkable so tests (and callers) can branch on the failure kind without
// matching on message text.
var (
	// ErrIncludeCycle is returned when include: resolution revisits a file already on
	// the current resolution path (an A→B→A loop).
	ErrIncludeCycle = errors.New("config: include cycle")
	// ErrMaxIncludeDepth is returned when an include: chain nests deeper than
	// maxIncludeDepth, guarding against pathological (acyclic) chains.
	ErrMaxIncludeDepth = errors.New("config: include depth limit exceeded")
	// ErrIncludeOutOfScope is returned when an include: path resolves outside the
	// allowed scope: the declaring file's directory subtree or the global config
	// dir (~/.config). It stops a cloned, untrusted project's config from pulling in
	// arbitrary user-readable files (~/.ssh, ~/.aws, /etc/...) — an unintended
	// read-and-leak surface for a confinement tool (AC-0059, F8).
	ErrIncludeOutOfScope = errors.New("config: include path out of scope")
)

// ValidationError aggregates one or more human-readable problems with a config
// document. Its message is stable and free of Go type names so it can be
// golden-tested and so an operator can fix the config without reading source.
type ValidationError struct {
	Issues []string
}

func (e *ValidationError) add(format string, args ...any) {
	e.Issues = append(e.Issues, fmt.Sprintf(format, args...))
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	b.WriteString("invalid .agent-creance.yaml:\n")
	for _, issue := range e.Issues {
		fmt.Fprintf(&b, "  - %s\n", issue)
	}
	return strings.TrimRight(b.String(), "\n")
}

// fieldNotFound matches yaml.v3's strict-decode "unknown key" wording, capturing the
// optional line prefix and the offending key, and drops the trailing "in type
// <pkg>.<Type>" tail (which would leak an internal Go type name into the message).
var fieldNotFound = regexp.MustCompile(`^(line \d+: )?field (\S+) not found in type .*$`)

// reformat turns a yaml.v3 decode error into a stable *ValidationError. Strict
// "field not found" entries become `unknown key "<key>"`; other type errors are
// passed through (their wording references Go builtins, not our package types); a
// parse/syntax error becomes a single `invalid YAML: ...` issue.
func reformat(err error) error {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		ve := &ValidationError{}
		for _, e := range te.Errors {
			if m := fieldNotFound.FindStringSubmatch(e); m != nil {
				ve.add(`%sunknown key %q`, m[1], m[2])
				continue
			}
			ve.add("%s", e)
		}
		return ve
	}
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	return &ValidationError{Issues: []string{"invalid YAML: " + msg}}
}
