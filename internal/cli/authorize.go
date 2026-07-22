package cli

// authorize.go implements `agent-creance credential authorize NAME` (AC-0069a): a
// one-time, host-side, out-of-cage OAuth2 consent flow that obtains a refresh token
// via the RFC 8252 native-app loopback redirect with PKCE and stores it in the
// keychain, so the broker can later mint short-lived access tokens from it. The app
// (refresh) token never enters the cage; consent happens in the user's own browser.
//
// The flow: generate a PKCE verifier/challenge and a state nonce; start a loopback
// listener on 127.0.0.1:<random port>; open the Google consent URL in the browser;
// receive the redirect, validate state, exchange the code + verifier for tokens at
// the token endpoint; store the refresh token at the credential's keychain reference.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// googleAuthEndpoint is the Google OAuth2 authorization endpoint (distinct from the
// token endpoint, which is per-credential config). This ticket targets Google only.
const googleAuthEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"

// authorizeTimeout bounds the whole consent flow, so an abandoned browser tab does
// not wedge the command forever.
const authorizeTimeout = 3 * time.Minute

func newCredentialAuthorizeCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "authorize NAME",
		Short: "Complete OAuth2 consent for a credential and store its refresh token",
		Long: "Run the one-time OAuth2 consent for an oauth2 credential named NAME: it opens your\n" +
			"browser to the provider's consent screen (loopback redirect + PKCE, RFC 8252), then\n" +
			"stores the resulting refresh token in the keychain at the credential's refresh_token\n" +
			"reference. The refresh token never enters the cage — the broker uses it host-side to\n" +
			"mint short-lived access tokens. The credential's refresh_token must be a keychain://\n" +
			"reference (authorize can only write there).",
		Example: "  # Authorize a Google Drive credential added with add-oauth2\n" +
			"  agent-creance credential authorize drive",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCredentialAuthorize(cmd.Context(), app, ".", args[0])
		},
	}
}

func runCredentialAuthorize(ctx context.Context, app *App, dir, name string) error {
	cfg, err := config.NewLoader(app.FS, app.Paths).Load(filepath.Join(dir, configFile))
	if err != nil {
		return err
	}
	cred, ok := cfg.Credentials[name]
	if !ok {
		return fmt.Errorf("credential %q is not defined; add it with 'agent-creance credential add-oauth2 %s'", name, name)
	}
	if cred.OAuth2 == nil {
		return fmt.Errorf("credential %q is not an oauth2 credential; authorize only applies to oauth2 credentials", name)
	}
	service, account, err := keychainServiceAccount(cred.OAuth2.RefreshToken)
	if err != nil {
		return fmt.Errorf("credential %q refresh_token %q: %w", name, cred.OAuth2.RefreshToken, err)
	}

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return err
	}
	state, err := randomURLToken(24)
	if err != nil {
		return err
	}

	// A loopback listener on an ephemeral port is the RFC 8252 native-app redirect.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start loopback listener: %w", err)
	}
	defer func() { _ = ln.Close() }()
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)

	authURL := buildGoogleAuthURL(cred.OAuth2.ClientID, redirectURI, cred.OAuth2.Scopes, challenge, state)

	resultCh := make(chan authorizeCallback, 1)
	srv := &http.Server{Handler: authorizeHandler(state, resultCh)}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	if err := app.Browser.Open(authURL); err != nil {
		fmt.Fprintf(app.Stderr, "warning: could not open a browser automatically: %v\n", err)
	}
	fmt.Fprintf(app.Stdout, "Opening your browser to authorize %q.\nIf it did not open, visit:\n\n  %s\n\nWaiting for the redirect…\n", name, authURL)

	ctx, cancel := context.WithTimeout(ctx, authorizeTimeout)
	defer cancel()

	var cb authorizeCallback
	select {
	case cb = <-resultCh:
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for the browser redirect after %s (was consent completed?)", authorizeTimeout)
	}
	if cb.err != nil {
		return cb.err
	}

	refreshToken, err := exchangeAuthCode(ctx, app.HTTPClient, cred.OAuth2.TokenEndpoint, cred.OAuth2.ClientID, cb.code, verifier, redirectURI)
	if err != nil {
		return err
	}
	if err := app.Keychain.SetGenericPassword(service, account, []byte(refreshToken)); err != nil {
		return fmt.Errorf("store refresh token in the keychain: %w", err)
	}

	fmt.Fprintf(app.Stdout, "\nAuthorized %q — refresh token stored at keychain://%s.\n"+
		"The next 'agent-creance run' will mint access tokens from it.\n", name, keychainRefDisplay(service, account))
	return nil
}

// authorizeCallback is the outcome of the loopback redirect: the authorization code,
// or an error (a provider error param, or a state mismatch).
type authorizeCallback struct {
	code string
	err  error
}

// authorizeHandler serves the single loopback redirect. It validates state (CSRF),
// surfaces a provider error param, extracts the code, and shows the user a plain
// close-this-tab page. It sends exactly one result on ch.
func authorizeHandler(wantState string, ch chan<- authorizeCallback) http.Handler {
	var once bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if once {
			return
		}
		once = true
		q := r.URL.Query()

		send := func(cb authorizeCallback, page string) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<html><body><p>"+page+"</p></body></html>")
			ch <- cb
		}
		if e := q.Get("error"); e != "" {
			send(authorizeCallback{err: fmt.Errorf("authorization was denied or failed: %s", e)},
				"Authorization failed — you can close this tab and return to the terminal.")
			return
		}
		if q.Get("state") != wantState {
			send(authorizeCallback{err: errors.New("state mismatch on the OAuth2 redirect (possible CSRF); aborting")},
				"Authorization could not be verified — you can close this tab.")
			return
		}
		code := q.Get("code")
		if code == "" {
			send(authorizeCallback{err: errors.New("the OAuth2 redirect carried no authorization code")},
				"Authorization returned no code — you can close this tab.")
			return
		}
		send(authorizeCallback{code: code}, "Authorized — you can close this tab and return to the terminal.")
	})
}

// generatePKCE returns a PKCE verifier and its S256 challenge (RFC 7636):
// verifier = base64url(32 random bytes); challenge = base64url(sha256(verifier)).
// Both are unpadded base64url.
func generatePKCE() (verifier, challenge string, err error) {
	verifier, err = randomURLToken(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// randomURLToken returns nBytes of cryptographically random data as an unpadded
// base64url string.
func randomURLToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// buildGoogleAuthURL builds the Google authorization URL for the loopback flow.
// access_type=offline + prompt=consent ensure a refresh token is issued.
func buildGoogleAuthURL(clientID, redirectURI string, scopes []string, challenge, state string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("state", state)
	return googleAuthEndpoint + "?" + q.Encode()
}

type authTokenResp struct {
	RefreshToken string `json:"refresh_token"`
	AccessToken  string `json:"access_token"`
}

// exchangeAuthCode exchanges an authorization code + PKCE verifier for tokens at the
// token endpoint, returning the refresh token. A response without one is an error
// (Google issues one only with access_type=offline + prompt=consent, which
// buildGoogleAuthURL sets).
func exchangeAuthCode(ctx context.Context, httpClient sysdep.HTTPClient, tokenEndpoint, clientID, code, verifier, redirectURI string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("client_id", clientID)
	form.Set("redirect_uri", redirectURI)

	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"Accept":       "application/json",
	}
	status, body, err := httpClient.Do(ctx, "POST", tokenEndpoint, headers, []byte(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("exchange authorization code: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("token endpoint returned %d exchanging the authorization code: %s", status, snippetBody(body))
	}
	var tr authTokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if tr.RefreshToken == "" {
		return "", errors.New("the token response carried no refresh_token (consent must grant offline access)")
	}
	return tr.RefreshToken, nil
}

// keychainServiceAccount parses a keychain://service[/account] reference into its
// service and account, refusing any other scheme (authorize can only write to the
// keychain).
func keychainServiceAccount(ref string) (service, account string, err error) {
	if !strings.HasPrefix(ref, "keychain://") {
		return "", "", fmt.Errorf("authorize can only store into a keychain:// reference; change the credential's refresh_token to keychain://<service>[/<account>]")
	}
	rest := strings.TrimPrefix(ref, "keychain://")
	service, account, _ = strings.Cut(rest, "/")
	if service == "" {
		return "", "", errors.New("keychain reference needs a service name")
	}
	return service, account, nil
}

// keychainRefDisplay renders a service/account back into a keychain:// path for a
// success message.
func keychainRefDisplay(service, account string) string {
	if account == "" {
		return service
	}
	return service + "/" + account
}

// snippetBody bounds a response body for an error message.
func snippetBody(b []byte) string {
	const limit = 256
	s := strings.TrimSpace(string(b))
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}
