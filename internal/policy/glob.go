package policy

import "strings"

// canonicalHost canonicalizes a request host before matching: lowercase, strip a
// trailing ":port" (only the unambiguous host:port form — an IPv6 literal's colons are
// left alone), then strip a single trailing "." (the FQDN root). Applied once at the
// matcher entry (Decide / HostDisposition) so api.example.com, API.EXAMPLE.COM,
// api.example.com. and api.example.com:443 decide identically and a host-level
// deny_always cannot be evaded by spelling (AC-0058 / C1). Rule patterns are validated
// at config load, not canonicalized here. Must stay byte-identical to policy.py's
// canonical_host.
func canonicalHost(host string) string {
	host = strings.ToLower(host)
	if i := strings.LastIndex(host, ":"); i != -1 && strings.Count(host, ":") == 1 && isAllDigits(host[i+1:]) {
		host = host[:i]
	}
	return strings.TrimSuffix(host, ".")
}

// isAllDigits reports whether s is non-empty and all ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// matchHost reports whether host (a request host) satisfies pattern. Comparison is
// case-insensitive. "*" matches any host; "*.suffix" matches any host ending in
// ".suffix" with at least one label before it (the bare apex is excluded, since the
// leading dot of the suffix cannot be satisfied by the apex); otherwise the match is
// exact.
func matchHost(pattern, host string) bool {
	pattern = strings.ToLower(pattern)
	host = strings.ToLower(host)

	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		// suffix includes the leading dot, e.g. ".medium.com"; HasSuffix then
		// requires at least one label before it, so "medium.com" (apex) does not
		// match "*.medium.com".
		return strings.HasSuffix(host, pattern[1:])
	}
	return pattern == host
}

// matchPath reports whether path (a request path) is covered by pattern under the
// prefix-by-default segment semantics documented on the package. pattern and path are
// normalized (leading/trailing "/" trimmed, split on "/") and the pattern must match a
// prefix of the path's segments.
func matchPath(pattern, path string) bool {
	return matchSegments(splitPath(pattern), splitPath(path))
}

// splitPath normalizes a path or pattern into segments: it trims a leading and
// trailing "/", then splits on "/". The empty string (root, "/") yields no segments.
func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// matchSegments reports whether pat matches a prefix of path. A "**" pattern segment
// consumes zero or more path segments; any other segment matches exactly one path
// segment via matchSegmentGlob. Once pat is exhausted the match succeeds regardless of
// remaining path segments (prefix-by-default).
func matchSegments(pat, path []string) bool {
	if len(pat) == 0 {
		return true
	}
	if pat[0] == "**" {
		// Consume zero segments, or consume one and retry the same "**".
		if matchSegments(pat[1:], path) {
			return true
		}
		return len(path) > 0 && matchSegments(pat, path[1:])
	}
	if len(path) == 0 {
		return false
	}
	return matchSegmentGlob(pat[0], path[0]) && matchSegments(pat[1:], path[1:])
}

// matchSegmentGlob matches a single pattern segment against a single path segment.
// "*" matches any (possibly empty) run of characters; every other character
// (including "?") is literal. A segment that is not exactly "**" but contains "**"
// simply has each "*" treated independently, so glued "**" degrades to "*".
func matchSegmentGlob(pat, seg string) bool {
	// Classic two-pointer wildcard match with backtracking on the last '*'.
	var (
		pi, si       int
		star         = -1
		starMatchEnd int
	)
	for si < len(seg) {
		switch {
		case pi < len(pat) && pat[pi] == '*':
			star = pi
			starMatchEnd = si
			pi++
		case pi < len(pat) && pat[pi] == seg[si]:
			pi++
			si++
		case star >= 0:
			pi = star + 1
			starMatchEnd++
			si = starMatchEnd
		default:
			return false
		}
	}
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	return pi == len(pat)
}

// matchMethod reports whether method satisfies a rule's method list. A nil list
// matches any method; otherwise method must be a verbatim member.
func matchMethod(methods []string, method string) bool {
	if methods == nil {
		return true
	}
	for _, m := range methods {
		if m == method {
			return true
		}
	}
	return false
}

// specificity is a rule's tie-break key for most-specific-wins, compared field by
// field with higher meaning more specific. matchedPattern is the path pattern that
// actually matched (empty when the rule is host-wide), so a rule's specificity
// reflects the pattern responsible for the match.
type specificity struct {
	hostRank     int    // exact 2, *.suffix 1, * 0
	pathScoped   int    // 1 if the rule constrains paths, else 0
	literalSegs  int    // count of non-wildcard segments in the matched pattern
	totalSegs    int    // segment count of the matched pattern
	methodScoped int    // 1 if the rule constrains methods, else 0
	canonical    string // deterministic fallback for exact ties
}

func ruleSpecificity(r Rule, matchedPattern string) specificity {
	s := specificity{
		hostRank:  hostRank(r.Host),
		canonical: r.Host + "|" + strings.Join(r.Paths, ",") + "|" + strings.Join(r.Methods, ",") + "|" + r.Mode,
	}
	if r.Paths != nil {
		s.pathScoped = 1
		segs := splitPath(matchedPattern)
		s.totalSegs = len(segs)
		for _, seg := range segs {
			if !strings.ContainsAny(seg, "*") {
				s.literalSegs++
			}
		}
	}
	if r.Methods != nil {
		s.methodScoped = 1
	}
	return s
}

func hostRank(host string) int {
	switch {
	case host == "*":
		return 0
	case strings.HasPrefix(host, "*."):
		return 1
	default:
		return 2
	}
}

// moreSpecific reports whether a is strictly more specific than b. The canonical
// string is the final, deterministic tie-break (smaller wins), so the ordering is
// total and independent of rule declaration order.
func moreSpecific(a, b specificity) bool {
	switch {
	case a.hostRank != b.hostRank:
		return a.hostRank > b.hostRank
	case a.pathScoped != b.pathScoped:
		return a.pathScoped > b.pathScoped
	case a.literalSegs != b.literalSegs:
		return a.literalSegs > b.literalSegs
	case a.totalSegs != b.totalSegs:
		return a.totalSegs > b.totalSegs
	case a.methodScoped != b.methodScoped:
		return a.methodScoped > b.methodScoped
	default:
		return a.canonical < b.canonical
	}
}
