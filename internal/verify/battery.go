package verify

import (
	"fmt"
	"sort"
	"strings"
)

// probePrefix is the marker the fake agent prefixes to each structured result
// line: CREANCE::<vector-id>::<observed>.
const probePrefix = "CREANCE::"

// skipToken is what the fake agent emits for an egress vector when run without
// network egress (offline). It is neither a pass nor a failure.
const skipToken = "skip"

// VectorResult is the evaluated outcome of one vector.
type VectorResult struct {
	ID       string
	Label    Label
	Expected string
	Observed string
	OK       bool // observation matched Expected (or was skipped)
	Escape   bool // a BLOCKED vector that got through — a security regression
	Skipped  bool // egress vector skipped offline
	Missing  bool // the fake agent emitted no line for this vector
}

// Verdict is the whole-battery result.
type Verdict struct {
	Results []VectorResult
	Failed  bool // any vector did not behave as expected (and was not skipped)
	Escaped bool // any BLOCKED vector leaked — the harness's escape signal
}

// ParseProbeOutput extracts the CREANCE::<id>::<observed> lines from the fake
// agent's combined stdout/stderr. The last line for a given id wins. Observed
// may itself contain "::" (e.g. an error detail); only the first two delimiters
// are structural.
func ParseProbeOutput(out string) map[string]string {
	res := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, probePrefix) {
			continue
		}
		rest := strings.TrimPrefix(line, probePrefix)
		id, observed, ok := strings.Cut(rest, "::")
		if !ok || id == "" {
			continue
		}
		res[strings.TrimSpace(id)] = strings.TrimSpace(observed)
	}
	return res
}

// Evaluate turns the parsed observations into a Verdict against the Vectors
// matrix. A BLOCKED vector observed as a leak (or otherwise non-blocked success)
// sets Escaped; any vector that does not match its Expected outcome (and was not
// legitimately skipped) sets Failed.
func Evaluate(observed map[string]string) Verdict {
	var v Verdict
	for _, vec := range Vectors {
		obs, present := observed[vec.ID]
		r := VectorResult{ID: vec.ID, Label: vec.Label, Expected: vec.Expected, Observed: obs}

		switch {
		case !present:
			r.Missing = true
			r.OK = false
		case obs == skipToken:
			r.Skipped = true
			r.OK = true
		default:
			r.OK = obs == vec.Expected
			// A BLOCKED vector that explicitly leaked, or that reported any
			// success-like observation instead of its expected refusal, is an
			// escape. The fake agent emits leakToken for a clean leak; a proxy
			// BLOCKED vector that returned 200 is likewise a leak.
			if vec.Label == LabelBlocked && !r.OK && isLeak(obs) {
				r.Escape = true
			}
		}

		if r.Escape {
			v.Escaped = true
		}
		if !r.OK {
			v.Failed = true
		}
		v.Results = append(v.Results, r)
	}
	return v
}

// isLeak reports whether an observation from a BLOCKED vector indicates the
// blocked action actually got through (as opposed to a harness/tooling error).
func isLeak(obs string) bool {
	switch {
	case obs == leakToken:
		return true
	case obs == "200":
		return true
	case strings.HasPrefix(obs, "open"), strings.HasPrefix(obs, "connect-ok"):
		return true
	default:
		return false
	}
}

// Summary renders a stable, human-readable per-vector PASS/FAIL table for test
// logs (sorted by ID for determinism).
func (v Verdict) Summary() string {
	rs := append([]VectorResult(nil), v.Results...)
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })

	var b strings.Builder
	fmt.Fprintf(&b, "cage verification: %d vectors", len(rs))
	if v.Escaped {
		b.WriteString(" — ESCAPE DETECTED")
	} else if v.Failed {
		b.WriteString(" — FAILURES")
	} else {
		b.WriteString(" — all green")
	}
	b.WriteByte('\n')
	for _, r := range rs {
		status := "PASS"
		switch {
		case r.Escape:
			status = "ESCAPE"
		case r.Skipped:
			status = "SKIP"
		case r.Missing:
			status = "MISSING"
		case !r.OK:
			status = "FAIL"
		}
		fmt.Fprintf(&b, "  [%-6s] %-16s %-10s want=%-14s got=%s\n",
			status, r.ID, r.Label, r.Expected, dash(r.Observed))
	}
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
