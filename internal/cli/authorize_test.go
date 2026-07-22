package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

func TestGeneratePKCE_S256(t *testing.T) {
	verifier, challenge, err := generatePKCE()
	require.NoError(t, err)
	// challenge = base64url(sha256(verifier)), unpadded.
	sum := sha256.Sum256([]byte(verifier))
	require.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), challenge)
	require.NotContains(t, challenge, "=", "challenge is unpadded base64url")
	require.NotContains(t, verifier, "=", "verifier is unpadded base64url")
	// Two calls differ.
	v2, _, _ := generatePKCE()
	require.NotEqual(t, verifier, v2)
}

func TestBuildGoogleAuthURL(t *testing.T) {
	u := buildGoogleAuthURL("cid", "http://127.0.0.1:5000",
		[]string{"https://www.googleapis.com/auth/drive.file"}, "chal", "state123")
	parsed, err := url.Parse(u)
	require.NoError(t, err)
	require.Equal(t, "accounts.google.com", parsed.Host)
	q := parsed.Query()
	require.Equal(t, "cid", q.Get("client_id"))
	require.Equal(t, "http://127.0.0.1:5000", q.Get("redirect_uri"))
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, "https://www.googleapis.com/auth/drive.file", q.Get("scope"))
	require.Equal(t, "chal", q.Get("code_challenge"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	require.Equal(t, "offline", q.Get("access_type"))
	require.Equal(t, "consent", q.Get("prompt"))
	require.Equal(t, "state123", q.Get("state"))
}

func TestExchangeAuthCode(t *testing.T) {
	const endpoint = "https://oauth2.googleapis.com/token"
	http := sysdeptest.NewFakeHTTPClient().
		WithResponse("POST", endpoint, 200, []byte(`{"refresh_token":"rt-new","access_token":"at"}`))

	rt, err := exchangeAuthCode(context.Background(), http, endpoint, "cid", "the-code", "the-verifier", "http://127.0.0.1:9")
	require.NoError(t, err)
	require.Equal(t, "rt-new", rt)

	form, err := url.ParseQuery(string(http.LastRequest().Body))
	require.NoError(t, err)
	require.Equal(t, "authorization_code", form.Get("grant_type"))
	require.Equal(t, "the-code", form.Get("code"))
	require.Equal(t, "the-verifier", form.Get("code_verifier"))
	require.Equal(t, "cid", form.Get("client_id"))
	require.Equal(t, "http://127.0.0.1:9", form.Get("redirect_uri"))
}

func TestExchangeAuthCode_Errors(t *testing.T) {
	const endpoint = "https://oauth2.googleapis.com/token"
	// Non-200.
	h := sysdeptest.NewFakeHTTPClient().WithResponse("POST", endpoint, 400, []byte(`{"error":"invalid_grant"}`))
	_, err := exchangeAuthCode(context.Background(), h, endpoint, "cid", "c", "v", "r")
	require.ErrorContains(t, err, "400")
	// Missing refresh_token.
	h = sysdeptest.NewFakeHTTPClient().WithResponse("POST", endpoint, 200, []byte(`{"access_token":"at"}`))
	_, err = exchangeAuthCode(context.Background(), h, endpoint, "cid", "c", "v", "r")
	require.ErrorContains(t, err, "no refresh_token")
}

func TestAuthorizeHandler(t *testing.T) {
	newReq := func(rawQuery string) *http.Request {
		return &http.Request{URL: &url.URL{RawQuery: rawQuery}}
	}
	// Well-formed callback yields the code.
	ch := make(chan authorizeCallback, 1)
	authorizeHandler("st", ch).ServeHTTP(newRecorder(), newReq("code=abc&state=st"))
	cb := <-ch
	require.NoError(t, cb.err)
	require.Equal(t, "abc", cb.code)

	// State mismatch is rejected.
	ch = make(chan authorizeCallback, 1)
	authorizeHandler("st", ch).ServeHTTP(newRecorder(), newReq("code=abc&state=WRONG"))
	require.ErrorContains(t, (<-ch).err, "state mismatch")

	// A provider error param is surfaced.
	ch = make(chan authorizeCallback, 1)
	authorizeHandler("st", ch).ServeHTTP(newRecorder(), newReq("error=access_denied&state=st"))
	require.ErrorContains(t, (<-ch).err, "denied")
}

func TestKeychainServiceAccount(t *testing.T) {
	svc, acct, err := keychainServiceAccount("keychain://agent-creance/drive-refresh")
	require.NoError(t, err)
	require.Equal(t, "agent-creance", svc)
	require.Equal(t, "drive-refresh", acct)

	// A service-only reference is valid.
	svc, acct, err = keychainServiceAccount("keychain://svc")
	require.NoError(t, err)
	require.Equal(t, "svc", svc)
	require.Empty(t, acct)

	// op:// / env:// are refused.
	_, _, err = keychainServiceAccount("op://vault/x")
	require.ErrorContains(t, err, "keychain://")
}

// newRecorder is a minimal http.ResponseWriter for the handler tests.
func newRecorder() http.ResponseWriter { return &recorder{h: http.Header{}} }

type recorder struct {
	h http.Header
}

func (r *recorder) Header() http.Header         { return r.h }
func (r *recorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *recorder) WriteHeader(int)             {}

// --- full-flow test: real loopback listener, simulated browser, no network ------

// callbackBrowser simulates the user's browser by firing the loopback redirect with a
// canned authorization code as soon as the auth URL is "opened".
type callbackBrowser struct {
	code string
}

func (b *callbackBrowser) Open(authURL string) error {
	u, err := url.Parse(authURL)
	if err != nil {
		return err
	}
	q := u.Query()
	redirect := q.Get("redirect_uri") + "?code=" + b.code + "&state=" + q.Get("state")
	go func() { _, _ = http.Get(redirect) }() //nolint:errcheck // fire-and-forget redirect
	return nil
}

func TestRunCredentialAuthorize_FullFlow(t *testing.T) {
	paths := sysdeptest.NewFakePathResolver()
	paths.Cwd = "/proj"
	paths.HomeDir = "/home/toby"
	fs := sysdeptest.NewFakeFileSystem()
	fs.Files[filepath.Join("/proj", ".agent-creance.yaml")] = []byte(
		"credentials:\n  drive:\n    template: \"Bearer {token}\"\n    oauth2:\n" +
			"      refresh_token: keychain://agent-creance/drive-refresh\n" +
			"      client_id: 1234.apps.googleusercontent.com\n")

	kc := sysdeptest.NewFakeKeychain()
	httpClient := sysdeptest.NewFakeHTTPClient().
		WithResponse("POST", "https://oauth2.googleapis.com/token", 200, []byte(`{"refresh_token":"rt-fresh"}`))

	app := &App{
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
		FS:         fs,
		Paths:      paths,
		Keychain:   kc,
		HTTPClient: httpClient,
		Browser:    &callbackBrowser{code: "auth-code-123"},
	}

	err := runCredentialAuthorize(context.Background(), app, ".", "drive")
	require.NoError(t, err)

	// The refresh token was stored at the credential's keychain reference, via stdin
	// (the fake records the secret; the real seam writes it to security's stdin).
	require.Len(t, kc.Stored, 1)
	require.Equal(t, "agent-creance", kc.Stored[0].Service)
	require.Equal(t, "drive-refresh", kc.Stored[0].Account)
	require.Equal(t, "rt-fresh", string(kc.Stored[0].Secret))
}

func TestRunCredentialAuthorize_Rejections(t *testing.T) {
	paths := sysdeptest.NewFakePathResolver()
	paths.Cwd = "/proj"
	paths.HomeDir = "/home/toby"
	fs := sysdeptest.NewFakeFileSystem()
	fs.Files[filepath.Join("/proj", ".agent-creance.yaml")] = []byte(
		"credentials:\n" +
			"  static:\n    source: op://vault/x\n    template: \"Bearer {token}\"\n" +
			"  opdrive:\n    template: \"Bearer {token}\"\n    oauth2:\n      refresh_token: op://vault/rt\n      client_id: cid\n")

	app := &App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, FS: fs, Paths: paths,
		Keychain: sysdeptest.NewFakeKeychain(), Browser: &sysdeptest.FakeBrowser{}}

	// Undefined credential.
	require.ErrorContains(t, runCredentialAuthorize(context.Background(), app, ".", "nope"), "not defined")
	// Non-oauth2 credential.
	require.ErrorContains(t, runCredentialAuthorize(context.Background(), app, ".", "static"), "not an oauth2 credential")
	// oauth2 with an op:// refresh_token ref (authorize can only write keychain://).
	require.ErrorContains(t, runCredentialAuthorize(context.Background(), app, ".", "opdrive"), "keychain://")
}
