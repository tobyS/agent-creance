// Package audit is the read side of the egress audit log. The mitmproxy enforcer
// (internal/proxy/enforcer) writes a compact JSONL log — egress.jsonl, rotated to
// egress.jsonl.1 — out-of-tree under the project's state dir; this package parses,
// summarizes, and tails it for `agent-creance logs`.
//
// Two pieces with deliberately different shapes:
//
//   - A pure core (Entry, ParseLine, Summarize, FormatEntry, Summary) that operates
//     on io.Reader / []byte, so it is hermetically table- and golden-testable with no
//     filesystem.
//   - A concrete I/O layer (Dump, SummarizeFiles, Follow) that opens the real files
//     and — for Follow — watches them with fsnotify. This layer touches os/fsnotify
//     directly rather than going through the internal/sysdep seam, because a live tail
//     needs real kernel file-change events that no in-memory fake can produce. The
//     deviation is scoped to this package and the pure core stays seam-free.
package audit

import (
	"encoding/json"
	"fmt"
)

// The decision vocabulary written by the enforcer (internal/proxy/enforcer/policy.py
// DECISION_*). Exactly these three values appear in the "decision" field.
const (
	DecisionAllow    = "allow"
	DecisionSoftDeny = "soft-deny"
	DecisionHardDeny = "hard-deny"
)

// Rule identifies which policy list and index decided an intercepted request. It is
// null in the log for a soft-deny (no list matched).
type Rule struct {
	List  string `json:"list"`
	Index int    `json:"index"`
}

// Entry is one decoded audit-log line. The enforcer emits two shapes:
//
//   - intercepted (TLS-terminated allow/soft-deny/hard-deny): ts, method, url,
//     decision, rule, status.
//   - passthrough (ignored/tunneled connection, host-only): ts, host, decision.
//
// The omitempty tags mean a decoded Entry round-trips back to whichever shape it came
// from, and IsPassthrough distinguishes the two by the presence of host.
type Entry struct {
	TS       string `json:"ts"`
	Method   string `json:"method,omitempty"`
	URL      string `json:"url,omitempty"`
	Host     string `json:"host,omitempty"`
	Decision string `json:"decision"`
	Rule     *Rule  `json:"rule,omitempty"`
	Status   int    `json:"status,omitempty"`
}

// IsPassthrough reports whether the entry is a host-only passthrough record (carrying
// host and no method/url/status), as opposed to an intercepted request.
func (e Entry) IsPassthrough() bool { return e.Host != "" }

// ParseLine decodes one JSONL line into an Entry. A malformed line yields a wrapped
// error so callers can skip-and-count rather than abort the whole stream.
func ParseLine(line []byte) (Entry, error) {
	var e Entry
	if err := json.Unmarshal(line, &e); err != nil {
		return Entry{}, fmt.Errorf("parse audit entry: %w", err)
	}
	return e, nil
}
