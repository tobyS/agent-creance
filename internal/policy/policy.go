// Package policy is the egress matcher: given an in-memory rule set and a request
// (host, path, method), it returns the cage's decision — allow, soft-deny, or
// hard-deny — plus the carried enforcement mode and the rule the decision is
// attributed to. It is deliberately pure: no filesystem, no clock, no OS. Reading
// rules off a compiled policy.json on disk is the compiler's job (AC-0013); this
// package matches an already-in-memory RuleSet.
//
// The decision model is fixed by docs/design.md ("Network refusal handling") and is
// exhaustive: a request that matches a deny_always rule is a hard-deny; otherwise a
// request that matches an allow rule is an allow (carrying that rule's mode);
// otherwise it is a soft-deny. There is no separate "default" — soft-deny *is* the
// default. deny_always shadows allow.
//
// This logic exists twice — here in Go (for `policy explain`) and in Python (for the
// mitmproxy enforcer, AC-0017). They must never disagree. The guardrail is the
// language-neutral corpus under testdata/decision-vectors/ (cross-cutting C1): plain
// JSON `(ruleset, request) -> expected {decision, mode, matched_rule}` vectors that
// both implementations run. No change to this matcher is complete without adding or
// updating vectors.
//
// Matching semantics (the contract the Python side must reproduce exactly):
//
//   - Host: the request host is canonicalized once at the matcher entry (Decide and
//     HostDisposition) — lowercased, a trailing ":port" stripped, a single trailing "."
//     stripped — so api.example.com, API.EXAMPLE.COM, api.example.com. and
//     api.example.com:443 decide identically and a host-level deny_always cannot be
//     evaded by spelling (the Python enforcer canonicalizes identically; the corpus
//     proves it). Matching is then case-insensitive: "*" matches any host; "*.suffix"
//     matches any host with at least one label before ".suffix" (the bare apex is NOT
//     matched); else exact equality. Rule patterns are validated at config load, not
//     canonicalized here.
//   - Path: prefix-by-default. Pattern and request path are trimmed of leading and
//     trailing "/" and split into segments. A pattern matches when its segments match
//     a *prefix* of the request's segments (remaining request segments are "under"
//     the prefix and still match). Within a segment, "*" matches any run of
//     characters; "?" and everything else are literal. A whole-segment "**" matches
//     zero or more segments (the only token that crosses "/"); "**" glued to other
//     characters degrades to "*". A rule with no paths (nil) matches any path; a
//     rule with paths matches if any one pattern matches.
//   - Method: a rule with no methods (nil) matches any method; otherwise the request
//     method must be a verbatim member of the list.
//   - Most-specific-wins: when several rules in the same list match, the reported
//     rule and carried mode come from the most specific one (host exact > *.suffix >
//     *; then path constrained > host-wide, more literal segments, more segments;
//     then method constrained > any), with a deterministic fallback for exact ties.
//     This is order-independent — allow/deny_always lists union across include: files.
//   - Passthrough blind spot: a path-scoped deny_always is *suppressed* when the
//     request's most-specific matching allow is mode: passthrough, because the proxy
//     never sees the path on a passthrough host (docs/design.md). A host-level
//     deny_always (no paths) still hard-denies a passthrough host.
package policy

import "github.com/tobyS/agent-creance/internal/config"

// Decision is the cage's verdict on a request. The three values are exhaustive.
const (
	DecisionAllow    = "allow"
	DecisionSoftDeny = "soft-deny"
	DecisionHardDeny = "hard-deny"
)

// Enforcement modes a rule may carry. These mirror config.ModeIntercept /
// config.ModePassthrough but are duplicated as plain strings so the corpus (and any
// Python reader) need not know about the config package.
const (
	ModeIntercept   = "intercept"
	ModePassthrough = "passthrough"
)

// Rule is one egress allow/deny entry as the matcher sees it. Unlike config.Rule it
// uses plain []string (nil == "key omitted") and json tags, so it decodes straight
// from the language-neutral decision-vector corpus and carries no yaml/`*[]string`
// baggage from the loader.
//
// Source and LowerTrust are populated by the compiler (AC-0013) for the on-disk
// policy.json and are *ignored by Decide* — they exist so `policy show` can render an
// annotated artifact. They are absent from the decision-vector corpus (omitempty).
//
// Inject and InCage are the auth axis (AC-0068b): Inject names a credentials: entry
// the proxy will resolve and inject (AC-0068c); InCage marks a host whose auth headers
// the proxy must not touch. Like Source/LowerTrust they are annotations *ignored by
// Decide* (they act on an already-allowed request, not on the allow/deny decision), so
// they never enter the decision-vector corpus and cannot change matcher parity.
type Rule struct {
	Host       string   `json:"host"`
	Paths      []string `json:"paths,omitempty"`
	Methods    []string `json:"methods,omitempty"`
	Mode       string   `json:"mode,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Inject     string   `json:"inject,omitempty"`
	InCage     bool     `json:"in_cage,omitempty"`
	Source     string   `json:"source,omitempty"`
	LowerTrust bool     `json:"lower_trust,omitempty"`
}

// RuleSet is the compiled, in-memory policy the matcher evaluates: the unioned
// soft-allow and hard-deny lists.
type RuleSet struct {
	Allow      []Rule `json:"allow,omitempty"`
	DenyAlways []Rule `json:"deny_always,omitempty"`
}

// CompiledVersion is the policy.json schema version — the cross-language contract with
// the Python enforcer. Bump only on a breaking change to the artifact shape.
const CompiledVersion = 1

// Credential is one compiled credentials: entry (AC-0068b) as the proxy will read it:
// a reference the enforcer resolves host-side and injects, never a resolved value. It
// mirrors config.Credential but carries json tags for the artifact. Source is an
// op:// / keychain:// / env:// reference; Header is the target header; Template is the
// value-template; Username is the Basic sentinel. GitHubApp/OAuth2 are the minted
// forms (AC-0069a) — still reference-only: Key/RefreshToken are secret *references*,
// the rest is non-secret minting config. No secret value ever appears here.
//
// The Python enforcer ignores the minting blocks entirely: the token it injects
// comes from the broker by credential name, resolved (static) or minted (AC-0069a)
// host-side, and it renders through Header/Template/Username regardless of form.
type Credential struct {
	Source    string         `json:"source,omitempty"`
	Header    string         `json:"header,omitempty"`
	Template  string         `json:"template"`
	Username  string         `json:"username,omitempty"`
	GitHubApp *GitHubAppMint `json:"github_app,omitempty"`
	OAuth2    *OAuth2Mint    `json:"oauth2,omitempty"`
}

// GitHubAppMint is the compiled github_app: block (reference-only): Key is a secret
// reference to the PKCS#1 PEM app private key, the rest is non-secret config.
type GitHubAppMint struct {
	Key         string            `json:"key"`
	ClientID    string            `json:"client_id"`
	Repo        string            `json:"repo"`
	Permissions map[string]string `json:"permissions,omitempty"`
}

// OAuth2Mint is the compiled oauth2: block (reference-only): RefreshToken is a secret
// reference, the rest is non-secret config.
type OAuth2Mint struct {
	RefreshToken  string   `json:"refresh_token"`
	ClientID      string   `json:"client_id"`
	TokenEndpoint string   `json:"token_endpoint,omitempty"`
	Scopes        []string `json:"scopes,omitempty"`
}

// CredentialsFromConfig converts the config credentials map into the compiled form
// (reference-only, no resolution). It returns nil for an empty input so the artifact
// omits the block entirely.
func CredentialsFromConfig(in map[string]config.Credential) map[string]Credential {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]Credential, len(in))
	for name, c := range in {
		cc := Credential{
			Source:   c.Source,
			Header:   c.Header,
			Template: c.Template,
			Username: c.Username,
		}
		if c.GitHubApp != nil {
			cc.GitHubApp = &GitHubAppMint{
				Key:         c.GitHubApp.Key,
				ClientID:    c.GitHubApp.ClientID,
				Repo:        c.GitHubApp.Repo,
				Permissions: c.GitHubApp.Permissions,
			}
		}
		if c.OAuth2 != nil {
			cc.OAuth2 = &OAuth2Mint{
				RefreshToken:  c.OAuth2.RefreshToken,
				ClientID:      c.OAuth2.ClientID,
				TokenEndpoint: c.OAuth2.TokenEndpoint,
				Scopes:        c.OAuth2.Scopes,
			}
		}
		out[name] = cc
	}
	return out
}

// Compiled is the on-disk policy.json the compiler (AC-0013) writes and the proxy
// enforcer reads: a versioned, input-hash-keyed RuleSet whose rules carry source
// annotations. The embedded RuleSet promotes allow/deny_always to the top level, so the
// artifact serializes as {version, input_hash, allow, deny_always}, plus the optional
// credentials: block (AC-0068b, references only). InputHash keys the compiler's
// regeneration cache; the enforcer ignores it.
type Compiled struct {
	Version     int                   `json:"version"`
	InputHash   string                `json:"input_hash"`
	Credentials map[string]Credential `json:"credentials,omitempty"`
	RuleSet
}

// Request is the single egress attempt being decided.
type Request struct {
	Host   string `json:"host"`
	Path   string `json:"path"`
	Method string `json:"method"`
}

// MatchedRule identifies the rule a decision is attributed to: which list, and its
// index within that list. It is nil for a soft-deny (nothing matched). The list/index
// form is language-neutral — it survives two rules sharing a host, where a host
// string alone would be ambiguous.
type MatchedRule struct {
	List  string `json:"list"` // "allow" or "deny_always"
	Index int    `json:"index"`
}

// Result is the matcher's verdict: the decision, the carried enforcement mode (the
// matched rule's mode; "" for a soft-deny), and the matched rule (nil for a
// soft-deny).
type Result struct {
	Decision string       `json:"decision"`
	Mode     string       `json:"mode"`
	Matched  *MatchedRule `json:"matched_rule"`
}

// HostDisposition is what the proxy can decide about a host at CONNECT / TLS
// ClientHello time, when only the host is known (no path or method). It mirrors the
// Python enforcer's host_disposition (internal/proxy/enforcer/policy.py) and is driven
// by the same decision-vector corpus (the host_disposition expectation), so the two
// CONNECT-stage implementations are provably consistent (AC-0058 / C3).
//
//   - Passthrough: tunnel without terminating TLS. True iff the top host-rank tier of
//     matching allows is entirely passthrough (any intercept in that tier forces TLS
//     termination so per-request path/method rules can apply — a mixed host resolves to
//     intercept).
//   - DenyReason: set ("" when none) when a host-level deny_always (no paths) matches;
//     the most host-specific one wins. Such a deny is enforced at CONNECT even on a
//     passthrough host (the tunnel is refused), since it cannot be enforced once tunnelled.
type HostDisposition struct {
	Passthrough bool   `json:"passthrough"`
	DenyReason  string `json:"deny_reason"`
}

// HostDisposition decides the CONNECT-stage disposition of host. It canonicalizes the
// host at entry exactly as Decide does, then applies the host-only projection of the
// decision model. The Decide rule (most-specific-allow) is the canonical one; this is
// its host-granular approximation for the stage where no path/method is visible yet.
func (rs RuleSet) HostDisposition(host string) HostDisposition {
	host = canonicalHost(host)

	denyReason := ""
	bestDenyRank := -1
	for _, r := range rs.DenyAlways {
		if r.Paths == nil && matchHost(r.Host, host) {
			if rank := hostRank(r.Host); rank > bestDenyRank {
				bestDenyRank, denyReason = rank, r.Reason
			}
		}
	}

	topRank := -1
	for _, r := range rs.Allow {
		if matchHost(r.Host, host) {
			if hr := hostRank(r.Host); hr > topRank {
				topRank = hr
			}
		}
	}
	passthrough := topRank >= 0
	if passthrough {
		for _, r := range rs.Allow {
			if matchHost(r.Host, host) && hostRank(r.Host) == topRank && r.Mode != ModePassthrough {
				passthrough = false
				break
			}
		}
	}

	return HostDisposition{Passthrough: passthrough, DenyReason: denyReason}
}

// FromConfig converts a validated config.Egress into a matcher RuleSet, dereferencing
// the loader's *[]string Paths/Methods (a nil pointer stays a nil slice — "key
// omitted"). It is the bridge a later `policy explain` / compiler (AC-0013) uses to
// build a RuleSet from parsed config.
func FromConfig(e config.Egress) RuleSet {
	return RuleSet{
		Allow:      rulesFromConfig(e.Allow),
		DenyAlways: rulesFromConfig(e.DenyAlways),
	}
}

func rulesFromConfig(in []config.Rule) []Rule {
	if len(in) == 0 {
		return nil
	}
	out := make([]Rule, len(in))
	for i, r := range in {
		out[i] = RuleFromConfig(r)
	}
	return out
}

// RuleFromConfig converts one config.Rule into a matcher Rule, dereferencing the
// loader's *[]string Paths/Methods (a nil pointer stays a nil slice — "key omitted").
// Source is left empty for the caller (the compiler) to stamp with provenance.
func RuleFromConfig(r config.Rule) Rule {
	out := Rule{
		Host:   r.Host,
		Mode:   r.Mode,
		Reason: r.Reason,
		Inject: r.Inject,
		InCage: r.InCage,
	}
	if r.Paths != nil {
		out.Paths = *r.Paths
	}
	if r.Methods != nil {
		out.Methods = *r.Methods
	}
	return out
}
