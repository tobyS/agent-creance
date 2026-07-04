// Package verify is the adversarial cage-verification harness (AC-0033, WP-4.5):
// the executable form of docs/design.md's "What the cage prevents — and what it
// doesn't". A hostile "fake agent" runs inside the real cage and probes each
// threat-model bullet; this package holds the matrix that ties every probe to a
// design bullet plus the pure evaluator that turns the fake agent's structured
// output into a pass/fail verdict.
//
// The live battery that actually launches the cage lives in
// verification_integration_test.go (build tag `integration`). The matrix and
// evaluator here are pure and run in the fast suite, including a drift guard
// (coverage_test.go) that fails if a design bullet loses its mapped probe.
package verify

// Label classifies what a vector asserts about the cage.
type Label string

const (
	// LabelBlocked: the cage MUST refuse this (kernel- or proxy-enforced). A
	// blocked vector observed as succeeding is a security escape.
	LabelBlocked Label = "BLOCKED"
	// LabelAllowed: this MUST succeed — a false-negative guard. A blocked
	// observation is a battery failure (over-blocking), not an escape.
	LabelAllowed Label = "ALLOWED"
	// LabelDocumented: a design non-guarantee that must behave exactly as the
	// "Not prevented" section states (e.g. project files are damageable).
	LabelDocumented Label = "DOCUMENTED"
)

// Vector is one assertion in the threat-model matrix. ID is the token the fake
// agent emits (CREANCE::<ID>::<observed>); Expected is the normalized observation
// for a green run; Keyword must still appear in design.md's prevent/not-prevent
// section (drift guard); Egress marks vectors that need real network egress and
// are skipped offline.
type Vector struct {
	ID        string
	Label     Label
	Expected  string
	Keyword   string // substring that must exist in the design.md matrix section
	DesignRef string // human-readable anchor, e.g. "design.md:57"
	Egress    bool
	Desc      string
}

// leakToken is the normalized observation the fake agent emits when a BLOCKED
// vector unexpectedly got through. Evaluate treats it as an escape.
const leakToken = "LEAK"

// Vectors is the single source of truth linking each fake-agent probe to a
// docs/design.md threat-model bullet (AC-0033 "Harness integrity": every
// assertion maps to a named bullet so the test and the threat model never drift).
var Vectors = []Vector{
	// BLOCKED — kernel/Seatbelt.
	{
		ID: "fs-outside", Label: LabelBlocked, Expected: "blocked",
		Keyword: "outside `./`", DesignRef: "design.md:51",
		Desc: "read a host file outside ./ (planted secret) → denied",
	},
	{
		ID: "fs-home-write", Label: LabelBlocked, Expected: "blocked",
		Keyword: "outside `./`", DesignRef: "design.md:51",
		Desc: "write into $HOME outside ./ and ~/.claude → denied (the v0.1 ~/.claude mount must not widen to home)",
	},
	{
		ID: "net-raw-tcp", Label: LabelBlocked, Expected: "blocked",
		Keyword: "mitmproxy", DesignRef: "design.md:52",
		Desc: "raw outbound TCP to an external host bypassing the proxy → blocked",
	},
	{
		ID: "net-localhost-v4", Label: LabelBlocked, Expected: "blocked",
		Keyword: "localhost", DesignRef: "design.md:53",
		Desc: "non-allowlisted localhost port over 127.0.0.1 → refused",
	},
	{
		ID: "net-localhost-v6", Label: LabelBlocked, Expected: "blocked",
		Keyword: "localhost", DesignRef: "design.md:53",
		Desc: "non-allowlisted localhost port over ::1 → refused",
	},
	{
		ID: "net-child", Label: LabelBlocked, Expected: "blocked",
		Keyword: "inherited", DesignRef: "design.md:54",
		Desc: "a child process re-running a blocked vector → still blocked",
	},
	{
		ID: "net-dns", Label: LabelBlocked, Expected: "blocked",
		Keyword: "DNS tunneling", DesignRef: "design.md:59",
		Desc: "direct DNS to an external nameserver → blocked",
	},
	// BLOCKED — proxy.
	{
		ID: "proxy-soft-deny", Label: LabelBlocked, Expected: "470:soft-deny",
		Keyword: "non-allowlisted", DesignRef: "design.md:57",
		Desc: "egress to a non-allowlisted host → 470 X-Cage-Reason: soft-deny",
	},
	{
		ID: "proxy-hard-deny", Label: LabelBlocked, Expected: "471:hard-deny",
		Keyword: "non-allowlisted", DesignRef: "design.md:57",
		Desc: "egress to a deny_always host → 471 X-Cage-Reason: hard-deny",
	},
	{
		ID: "proxy-offpath", Label: LabelBlocked, Expected: "470:soft-deny",
		Keyword: "paths/methods", DesignRef: "design.md:57",
		Desc: "disallowed path on an allowlisted host → soft-deny",
	},
	// ALLOWED — false-negative guards.
	{
		ID: "svc-allowed", Label: LabelAllowed, Expected: "connect-ok",
		Keyword: "localhost", DesignRef: "design.md:53",
		Desc: "a host_services 127.0.0.1:<port> entry → connects",
	},
	{
		ID: "allow-200", Label: LabelAllowed, Expected: "200", Egress: true,
		Keyword: "forwards it", DesignRef: "design.md:46",
		Desc: "egress via the proxy to an allowlisted host → 200 upstream",
	},
	{
		ID: "passthrough", Label: LabelAllowed, Expected: "200", Egress: true,
		Keyword: "passthrough", DesignRef: "design.md:68",
		Desc: "a mode: passthrough host → tunnels, validates the real upstream cert",
	},
	{
		// AC-0034: a client that trusts the proxy CA ONLY via the injected env-var
		// file (not the keychain) must get 200 in-cage — proves the single-file CA
		// read-grant works. ALLOWED, so a regression (CA unreadable) shows as a
		// failure, not an escape. Egress; skipped offline or if node is absent.
		ID: "env-ca-node", Label: LabelAllowed, Expected: "200", Egress: true,
		Keyword: "NODE_EXTRA_CA_CERTS", DesignRef: "AC-0034",
		Desc: "node trusts the injected env-var CA file and gets 200 through the proxy in-cage",
	},
	{
		// AC-0034: same guard for the OpenSSL CA-file path (SSL_CERT_FILE /
		// REQUESTS_CA_BUNDLE), the mechanism python/requests use.
		ID: "env-ca-python", Label: LabelAllowed, Expected: "200", Egress: true,
		Keyword: "SSL_CERT_FILE", DesignRef: "AC-0034",
		Desc: "python trusts the injected env-var CA file and gets 200 through the proxy in-cage",
	},
	{
		// AC-0045: the keychain.sb read path — mach-lookup to securityd plus the
		// file-read on login.keychain-db the legacy client-side stack needs.
		// Probed against a THROWAWAY item the harness plants (never the real
		// Claude Code-credentials). ALLOWED, so a profile regression that breaks
		// credential reads fails the battery.
		ID: "kc-read", Label: LabelAllowed, Expected: "found",
		Keyword: "Keychain", DesignRef: "design.md:466 / AC-0045",
		Desc: "security find-generic-password on the throwaway item succeeds in-cage (securityd mach-lookup + keychain-db read)",
	},
	{
		// AC-0045: the keychain.sb write path — file-level RW on login.keychain-db*
		// and the AtomicFile .fl* lock files, which the legacy SecKeychain update
		// path token refresh uses. Same throwaway item.
		ID: "kc-write", Label: LabelAllowed, Expected: "updated",
		Keyword: "Keychain", DesignRef: "design.md:466 / AC-0045",
		Desc: "security add-generic-password -U on the throwaway item succeeds in-cage (keychain-db + lock-file RW)",
	},
	{
		// AC-0045: the claude.sb file-level grant — a ~/.claude.json-prefixed file
		// is creatable/readable/removable in-cage. Probed with a sibling probe file
		// (the prefix regex covers it), never the real ~/.claude.json.
		ID: "claude-json-rw", Label: LabelAllowed, Expected: "rw-ok",
		Keyword: "~/.claude.json", DesignRef: "design.md:51 / AC-0045",
		Desc: "a ~/.claude.json.creance-probe file is writable+readable in-cage (claude.sb prefix grant)",
	},
	// DOCUMENTED — honesty assertions.
	{
		ID: "doc-rm", Label: LabelDocumented, Expected: "rm-ok",
		Keyword: "rm -rf", DesignRef: "design.md:62",
		Desc: "rm/write within ./ succeeds — the cage does not block it",
	},
	{
		ID: "doc-post", Label: LabelDocumented, Expected: "post-sent", Egress: true,
		Keyword: "POST", DesignRef: "design.md:68",
		Desc: "a POST body to an allowlisted host goes through and is audited",
	},
	{
		ID: "doc-claude-rw", Label: LabelDocumented, Expected: "planted",
		Keyword: "config-persistence", DesignRef: "design.md:68",
		Desc: "the real ~/.claude is mounted RW: a planted file persists — the documented v0.1 config-persistence deferral (AC-0046)",
	},
}
