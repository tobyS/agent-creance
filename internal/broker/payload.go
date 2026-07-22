package broker

import (
	"github.com/tobyS/agent-creance/internal/mint"
	"github.com/tobyS/agent-creance/internal/mint/githubapp"
	"github.com/tobyS/agent-creance/internal/mint/oauth2mint"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// The fd-3 payload the CLI writes and the broker reads (AC-0069a). It replaces the
// flat {name: token} map with a per-credential spec so a minted credential can carry
// its resolved key material plus its non-secret minting parameters.
//
// This is a Go→Go contract inside one binary: the broker is a re-exec of the same
// agent-creance, so the writer (internal/cli.resolveInjectionSecrets) and the reader
// (internal/cli.loadPayload) are always the same build — no cross-language or
// cross-version framing to version. Secret material (Token / PEM / RefreshToken) is
// resolved host-side on the spawn path (Touch-ID intact) and delivered here once; the
// broker never re-resolves.
const (
	KindStatic    = "static"
	KindGitHubApp = "github_app"
	KindOAuth2    = "oauth2"
)

// CredentialSpec is one credential's delivery record: a static token, or the resolved
// inputs for a minter.
type CredentialSpec struct {
	Kind      string         `json:"kind"`
	Token     string         `json:"token,omitempty"`      // KindStatic
	GitHubApp *GitHubAppSpec `json:"github_app,omitempty"` // KindGitHubApp
	OAuth2    *OAuth2Spec    `json:"oauth2,omitempty"`     // KindOAuth2
}

// GitHubAppSpec carries the resolved app private key (PEM) plus the non-secret
// minting parameters.
type GitHubAppSpec struct {
	PEM         string            `json:"pem"`
	ClientID    string            `json:"client_id"`
	Repo        string            `json:"repo"`
	Permissions map[string]string `json:"permissions,omitempty"`
}

// OAuth2Spec carries the resolved refresh token plus the non-secret minting
// parameters. RefreshTokenRef is the original secret *reference* (e.g. a keychain://
// item), so the broker can persist a provider-rotated refresh token back to where it
// came from (RFC 6749 §6; defensive — Google does not rotate today).
type OAuth2Spec struct {
	RefreshToken    string   `json:"refresh_token"`
	RefreshTokenRef string   `json:"refresh_token_ref,omitempty"`
	ClientID        string   `json:"client_id"`
	TokenEndpoint   string   `json:"token_endpoint"`
	Scopes          []string `json:"scopes,omitempty"`
}

// Payload maps a credential name to its delivery spec.
type Payload map[string]CredentialSpec

// MinterFor returns the minter for a minted spec (github_app / oauth2), or (nil,
// false) for a static credential (which needs no minter — its token is served
// directly). It is the spec→minter factory the broker uses to drive the refresh loop.
func MinterFor(spec CredentialSpec, httpClient sysdep.HTTPClient, clock sysdep.Clock) (mint.Minter, bool) {
	switch spec.Kind {
	case KindGitHubApp:
		if spec.GitHubApp == nil {
			return nil, false
		}
		return githubapp.New(githubapp.Config{
			PEM:         []byte(spec.GitHubApp.PEM),
			ClientID:    spec.GitHubApp.ClientID,
			Repo:        spec.GitHubApp.Repo,
			Permissions: spec.GitHubApp.Permissions,
		}, httpClient, clock), true
	case KindOAuth2:
		if spec.OAuth2 == nil {
			return nil, false
		}
		return oauth2mint.New(oauth2mint.Config{
			RefreshToken:  spec.OAuth2.RefreshToken,
			ClientID:      spec.OAuth2.ClientID,
			TokenEndpoint: spec.OAuth2.TokenEndpoint,
			Scopes:        spec.OAuth2.Scopes,
		}, httpClient, clock), true
	default:
		return nil, false
	}
}
