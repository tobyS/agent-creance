// Package githubapp mints GitHub App installation access tokens (AC-0069a): sign an
// RS256 JWT with the app private key, discover the installation for the target repo,
// and exchange the JWT for a repo-scoped installation token that lives ≤1h. The app
// private key never leaves the host — only the resolved PEM bytes reach this package,
// and only the minted (short-lived, repo-scoped) token is ever handed to the cage.
package githubapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/tobyS/agent-creance/internal/mint"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// apiBase is the GitHub REST API root. A constant (not configurable) — GitHub App
// minting is GitHub-specific; a GHES target would be a later, explicit change.
const apiBase = "https://api.github.com"

// JWT timing per GitHub's guidance: backdate iat 60s to absorb clock skew (the classic
// "'exp' too far in the future" failure) and set exp ~9min ahead (the max is 10min).
const (
	jwtBackdate = 60 * time.Second
	jwtLifetime = 9 * time.Minute
)

// Config is the resolved GitHub App minting input: the PEM bytes are already resolved
// from the secret reference host-side, the rest is non-secret config from the compiled
// policy.
type Config struct {
	PEM         []byte            // PKCS#1 (or PKCS#8) PEM app private key
	ClientID    string            // JWT issuer (the App's client ID)
	Repo        string            // owner/name
	Permissions map[string]string // down-scope cap, e.g. {contents: read}
}

// Minter mints installation tokens for one repo. The installation id is discovered
// once and cached for the minter's lifetime (it is stable per owner).
type Minter struct {
	cfg   Config
	http  sysdep.HTTPClient
	clock sysdep.Clock

	installationID int64 // 0 until discovered
}

var _ mint.Minter = (*Minter)(nil)

// New builds a GitHub App minter over the given HTTP and Clock seams.
func New(cfg Config, httpClient sysdep.HTTPClient, clock sysdep.Clock) *Minter {
	return &Minter{cfg: cfg, http: httpClient, clock: clock}
}

// Mint signs a fresh JWT, discovers the installation (once), and exchanges the JWT for
// a repo-scoped installation token. It returns the token and its expiry.
func (m *Minter) Mint(ctx context.Context) (string, time.Time, error) {
	jwtStr, err := m.signJWT()
	if err != nil {
		return "", time.Time{}, err
	}

	if m.installationID == 0 {
		id, err := m.discoverInstallation(ctx, jwtStr)
		if err != nil {
			return "", time.Time{}, err
		}
		m.installationID = id
	}

	return m.createToken(ctx, jwtStr)
}

// signJWT builds and signs the RS256 app JWT (iat−60s, exp+9min, iss=ClientID).
func (m *Minter) signJWT() (string, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM(m.cfg.PEM)
	if err != nil {
		return "", fmt.Errorf("githubapp: parse app private key: %w", err)
	}
	now := m.clock.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    m.cfg.ClientID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-jwtBackdate)),
		ExpiresAt: jwt.NewNumericDate(now.Add(jwtLifetime)),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("githubapp: sign app JWT: %w", err)
	}
	return signed, nil
}

type installationResp struct {
	ID int64 `json:"id"`
}

// discoverInstallation resolves the installation id for the target repo.
func (m *Minter) discoverInstallation(ctx context.Context, jwtStr string) (int64, error) {
	url := fmt.Sprintf("%s/repos/%s/installation", apiBase, m.cfg.Repo)
	status, body, err := m.http.Do(ctx, "GET", url, m.apiHeaders(jwtStr), nil)
	if err != nil {
		return 0, fmt.Errorf("githubapp: discover installation: %w", err)
	}
	if status != 200 {
		return 0, &APIError{Op: "discover installation", Method: "GET", URL: url, Status: status, Body: snippet(body)}
	}
	var ir installationResp
	if err := json.Unmarshal(body, &ir); err != nil {
		return 0, fmt.Errorf("githubapp: decode installation: %w", err)
	}
	if ir.ID == 0 {
		return 0, fmt.Errorf("githubapp: installation response carried no id")
	}
	return ir.ID, nil
}

type tokenReq struct {
	Repositories []string          `json:"repositories,omitempty"`
	Permissions  map[string]string `json:"permissions,omitempty"`
}

type tokenResp struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"` // RFC3339
}

// createToken exchanges the JWT for a repo-scoped installation token.
func (m *Minter) createToken(ctx context.Context, jwtStr string) (string, time.Time, error) {
	repoName := m.cfg.Repo
	if _, name, ok := strings.Cut(m.cfg.Repo, "/"); ok {
		repoName = name
	}
	reqBody, err := json.Marshal(tokenReq{
		Repositories: []string{repoName},
		Permissions:  m.cfg.Permissions,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("githubapp: encode token request: %w", err)
	}

	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", apiBase, m.installationID)
	status, body, err := m.http.Do(ctx, "POST", url, m.apiHeaders(jwtStr), reqBody)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("githubapp: create installation token: %w", err)
	}
	if status != 201 {
		return "", time.Time{}, &APIError{Op: "create installation token", Method: "POST", URL: url, Status: status, Body: snippet(body)}
	}
	var tr tokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", time.Time{}, fmt.Errorf("githubapp: decode token response: %w", err)
	}
	if tr.Token == "" {
		return "", time.Time{}, fmt.Errorf("githubapp: token response carried no token")
	}
	exp, err := time.Parse(time.RFC3339, tr.ExpiresAt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("githubapp: parse token expiry %q: %w", tr.ExpiresAt, err)
	}
	return tr.Token, exp, nil
}

// Revoke deletes the installation token, authenticated with the token itself
// (best-effort teardown; a failure is the caller's to swallow). The token is passed as
// the bearer credential and never placed in a returned error.
func (m *Minter) Revoke(ctx context.Context, token string) error {
	url := apiBase + "/installation/token"
	headers := map[string]string{
		"Authorization":        "Bearer " + token,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
	status, body, err := m.http.Do(ctx, "DELETE", url, headers, nil)
	if err != nil {
		return fmt.Errorf("githubapp: revoke installation token: %w", err)
	}
	if status != 204 {
		return &APIError{Op: "revoke installation token", Method: "DELETE", URL: url, Status: status, Body: snippet(body)}
	}
	return nil
}

// apiHeaders builds the standard GitHub REST headers with the app JWT as bearer.
func (m *Minter) apiHeaders(jwtStr string) map[string]string {
	return map[string]string{
		"Authorization":        "Bearer " + jwtStr,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
}

// APIError is a non-2xx GitHub response. It carries the operation, endpoint, and
// status plus a bounded snippet of GitHub's error body (never a minted token — a mint
// failure has no token, and revoke keeps the token out of the error).
type APIError struct {
	Op     string
	Method string
	URL    string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("githubapp: %s: %s %s returned %d: %s", e.Op, e.Method, e.URL, e.Status, e.Body)
}

// snippet bounds a response body for inclusion in an error message.
func snippet(b []byte) string {
	const limit = 256
	s := strings.TrimSpace(string(b))
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}
