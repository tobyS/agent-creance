package broker

// The broker wire protocol: newline-delimited JSON over a unix socket, one
// request and one response per connection.
//
// One request per connection is deliberate. The enforcer opens a connection for
// each injected request, so there is no framing state to desynchronise and no way
// for a rotation (AC-0069a swapping the token mid-session) to race a request that
// is already in flight: a request either got the old token before the swap or the
// new one after it, never a torn mixture.
//
//	→ {"credential":"gh"}
//	← {"token":"ghs_…","expires_at":"2026-07-13T11:00:00Z"}
//	← {"error":"unknown_credential"}
//	← {"error":"expired"}
//
// expires_at is omitted for a credential that does not expire (every statically
// resolved reference today). Both error kinds mean the same thing to the addon —
// answer 472, the human-recoverable "the credential could not be resolved"
// refusal — but they are distinguished on the wire so the audit trail and the
// operator can tell "you never configured this" from "the refresh loop fell
// behind".
const (
	// ErrUnknownCredential means the broker holds no token under that name.
	ErrUnknownCredential = "unknown_credential"
	// ErrExpired means the broker holds a token whose expiry has passed. Only
	// minted credentials (AC-0069a) can produce this.
	ErrExpired = "expired"
)

// Request asks for the current token of one credential, by the name the compiled
// policy binds to a rule's inject field.
type Request struct {
	Credential string `json:"credential"`
}

// Response carries either a token or an error kind, never both.
type Response struct {
	Token     string `json:"token,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"` // RFC3339; omitted ⇒ no expiry
	Error     string `json:"error,omitempty"`
}
