package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// testKey generates an RSA key and returns it with its PKCS#1 PEM encoding.
func testKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, pemBytes
}

const (
	installURL = apiBase + "/repos/tobyS/agent-creance/installation"
	tokenURL   = apiBase + "/app/installations/42/access_tokens"
)

func newMinter(t *testing.T, http *sysdeptest.FakeHTTPClient, clock *sysdeptest.FakeClock) (*Minter, *rsa.PrivateKey) {
	t.Helper()
	key, pemBytes := testKey(t)
	m := New(Config{
		PEM:         pemBytes,
		ClientID:    "Iv1.example",
		Repo:        "tobyS/agent-creance",
		Permissions: map[string]string{"contents": "read", "issues": "write"},
	}, http, clock)
	return m, key
}

func TestMint_HappyPath(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	clock := sysdeptest.NewFakeClock(now)
	exp := now.Add(time.Hour)
	http := sysdeptest.NewFakeHTTPClient().
		WithResponse("GET", installURL, 200, []byte(`{"id":42}`)).
		WithResponse("POST", tokenURL, 201, []byte(`{"token":"ghs_minted","expires_at":"`+exp.Format(time.RFC3339)+`"}`))

	m, key := newMinter(t, http, clock)
	tok, gotExp, err := m.Mint(context.Background())
	require.NoError(t, err)
	require.Equal(t, "ghs_minted", tok)
	require.True(t, gotExp.Equal(exp), "expiry parsed")

	// Two requests: install discovery then token creation.
	require.Len(t, http.Requests, 2)
	get := http.Requests[0]
	require.Equal(t, "GET", get.Method)
	require.Equal(t, installURL, get.URL)

	// The JWT bearer verifies against the public key, with the correct claims.
	auth := get.Headers["Authorization"]
	require.True(t, strings.HasPrefix(auth, "Bearer "))
	claims := parseVerify(t, strings.TrimPrefix(auth, "Bearer "), &key.PublicKey)
	require.Equal(t, "Iv1.example", claims.Issuer)
	require.Equal(t, now.Add(-jwtBackdate).Unix(), claims.IssuedAt.Unix(), "iat backdated 60s")
	require.Equal(t, now.Add(jwtLifetime).Unix(), claims.ExpiresAt.Unix(), "exp +9min")

	// The mint POST body carries the single repo name (unqualified) + permissions.
	post := http.Requests[1]
	require.Equal(t, "POST", post.Method)
	var body tokenReq
	require.NoError(t, json.Unmarshal(post.Body, &body))
	require.Equal(t, []string{"agent-creance"}, body.Repositories)
	require.Equal(t, map[string]string{"contents": "read", "issues": "write"}, body.Permissions)
}

// parseVerify parses a signed JWT with RS256 verification and returns its registered
// claims.
func parseVerify(t *testing.T, tokenStr string, pub *rsa.PublicKey) *jwt.RegisteredClaims {
	t.Helper()
	claims := &jwt.RegisteredClaims{}
	tok, err := jwt.ParseWithClaims(tokenStr, claims, func(tok *jwt.Token) (any, error) {
		require.Equal(t, "RS256", tok.Method.Alg())
		return pub, nil
	})
	require.NoError(t, err)
	require.True(t, tok.Valid)
	return claims
}

func TestMint_InstallationCachedAcrossMints(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	clock := sysdeptest.NewFakeClock(now)
	exp := now.Add(time.Hour).Format(time.RFC3339)
	http := sysdeptest.NewFakeHTTPClient().
		WithResponse("GET", installURL, 200, []byte(`{"id":42}`)).
		WithResponse("POST", tokenURL, 201, []byte(`{"token":"t","expires_at":"`+exp+`"}`))
	m, _ := newMinter(t, http, clock)

	_, _, err := m.Mint(context.Background())
	require.NoError(t, err)
	_, _, err = m.Mint(context.Background())
	require.NoError(t, err)

	// Installation discovered once; the second Mint reuses the cached id.
	gets := 0
	for _, r := range http.Requests {
		if r.Method == "GET" {
			gets++
		}
	}
	require.Equal(t, 1, gets, "installation discovered once, then cached")
}

func TestMint_Non2xxErrorHasNoToken(t *testing.T) {
	now := time.Now()
	http := sysdeptest.NewFakeHTTPClient().
		WithResponse("GET", installURL, 200, []byte(`{"id":42}`)).
		WithResponse("POST", tokenURL, 422, []byte(`{"message":"permission not granted"}`))
	m := New(Config{PEM: mustPEM(t), ClientID: "Iv1.x", Repo: "tobyS/agent-creance"}, http, sysdeptest.NewFakeClock(now))

	_, _, err := m.Mint(context.Background())
	require.Error(t, err)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 422, apiErr.Status)
	require.NotContains(t, err.Error(), "ghs_", "error must not leak a token")
}

func TestRevoke_IssuesDelete(t *testing.T) {
	http := sysdeptest.NewFakeHTTPClient().
		WithResponse("DELETE", apiBase+"/installation/token", 204, nil)
	m := New(Config{PEM: mustPEM(t), ClientID: "Iv1.x", Repo: "tobyS/agent-creance"}, http, sysdeptest.NewFakeClock(time.Now()))

	require.NoError(t, m.Revoke(context.Background(), "ghs_secret"))
	last := http.LastRequest()
	require.Equal(t, "DELETE", last.Method)
	require.Equal(t, apiBase+"/installation/token", last.URL)
	require.Equal(t, "Bearer ghs_secret", last.Headers["Authorization"])
}

func TestRevoke_Non204IsErrorWithoutToken(t *testing.T) {
	http := sysdeptest.NewFakeHTTPClient().
		WithResponse("DELETE", apiBase+"/installation/token", 404, []byte(`{"message":"not found"}`))
	m := New(Config{PEM: mustPEM(t), ClientID: "Iv1.x", Repo: "tobyS/agent-creance"}, http, sysdeptest.NewFakeClock(time.Now()))

	err := m.Revoke(context.Background(), "ghs_secret")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "ghs_secret", "revoke error must not leak the token")
}

// mustPEM returns a throwaway PKCS#1 PEM for tests that do not verify the JWT.
func mustPEM(t *testing.T) []byte {
	t.Helper()
	_, pemBytes := testKey(t)
	return pemBytes
}
