package policy

// Decide evaluates req against the rule set and returns the cage's verdict. The
// algorithm follows docs/design.md ("Network refusal handling") with the passthrough
// blind-spot refinement:
//
//  1. Find the most-specific matching allow; the host is "passthrough" iff that
//     allow carries mode: passthrough.
//  2. Evaluate deny_always. A host-level deny (no paths) hard-denies even a
//     passthrough host. A path-scoped deny hard-denies only when the host is not
//     passthrough (on a passthrough host the proxy never sees the path). The most
//     specific *eligible* matching deny is reported.
//  3. Otherwise: an allow match is an allow (carrying its mode); no match is the
//     implicit soft-deny.
func (rs RuleSet) Decide(req Request) Result {
	allowIdx, allowOK := bestMatch(rs.Allow, req, nil)
	isPassthrough := allowOK && rs.Allow[allowIdx].Mode == ModePassthrough

	// deny_always shadows allow. On a passthrough host only host-level denies are
	// eligible, because a path-scoped deny cannot be enforced without seeing the path.
	eligible := func(Rule) bool { return true }
	if isPassthrough {
		eligible = func(r Rule) bool { return r.Paths == nil }
	}
	if denyIdx, ok := bestMatch(rs.DenyAlways, req, eligible); ok {
		r := rs.DenyAlways[denyIdx]
		return Result{
			Decision: DecisionHardDeny,
			Mode:     r.Mode,
			Matched:  &MatchedRule{List: "deny_always", Index: denyIdx},
		}
	}

	if allowOK {
		return Result{
			Decision: DecisionAllow,
			Mode:     rs.Allow[allowIdx].Mode,
			Matched:  &MatchedRule{List: "allow", Index: allowIdx},
		}
	}
	return Result{Decision: DecisionSoftDeny}
}

// bestMatch returns the index of the most-specific rule in rules that matches req and
// passes the eligible predicate (nil eligible means all rules are eligible). The
// second return is false when nothing matches.
func bestMatch(rules []Rule, req Request, eligible func(Rule) bool) (int, bool) {
	best := -1
	var bestSpec specificity
	for i, r := range rules {
		if eligible != nil && !eligible(r) {
			continue
		}
		pattern, ok := ruleMatches(r, req)
		if !ok {
			continue
		}
		spec := ruleSpecificity(r, pattern)
		if best == -1 || moreSpecific(spec, bestSpec) {
			best, bestSpec = i, spec
		}
	}
	return best, best != -1
}

// ruleMatches reports whether r matches req on all of host, method, and path, and
// returns the matched path pattern (the most specific one when the rule lists
// several, "" when the rule is host-wide) for use in specificity scoring.
func ruleMatches(r Rule, req Request) (string, bool) {
	if !matchHost(r.Host, req.Host) {
		return "", false
	}
	if !matchMethod(r.Methods, req.Method) {
		return "", false
	}
	if r.Paths == nil {
		return "", true // host-wide: matches any path
	}

	matched := ""
	found := false
	for _, p := range r.Paths {
		if !matchPath(p, req.Path) {
			continue
		}
		if !found || moreSpecific(ruleSpecificity(r, p), ruleSpecificity(r, matched)) {
			matched, found = p, true
		}
	}
	return matched, found
}
