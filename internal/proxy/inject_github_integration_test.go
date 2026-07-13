//go:build integration

// Package proxy integration test for credential injection against the REAL GitHub
// GraphQL API (AC-0068e, the Phase-1 flagship). Gated behind the `integration` build
// tag so `make test` never runs it. It drives every real seam on the injection path:
// OSSecretResolver resolves a live op:// / keychain:// / env:// reference, a real
// mitmproxy is spawned via SpawnWithSecret with the payload on inherited fd 3, and the
// enforcer addon overwrites the auth header on a request to api.github.com/graphql.
//
// This is the only test in the project that exercises a real upstream. It is the
// validation gate for the whole AC-0068 stack:
//
//   - a real, repo-scoped token authenticates a GraphQL call the cage forwards,
//   - the phantom the client sends is overwritten (the agent never holds the token),
//   - an unresolvable credential fails closed with 472 rather than leaking the phantom,
//   - a rejected token keeps its upstream 401 and gains X-Cage-Injected.
//
// It needs a fine-grained PAT scoped to the target repo with Metadata: Read,
// Issues: Read and write, and Contents: Read (see docs/design.md "Credential
// injection"). Point AC_TEST_GITHUB_TOKEN_REF at it.
//
// Run with:
//
//	AC_TEST_GITHUB_TOKEN_REF='op://Private/GitHub PAT/token' make test-integration
package proxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/policy"
	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

const (
	// ghCredential is the credential name the policy binds and the addon annotates with.
	ghCredential = "github"
	// ghPhantom stands in for the placeholder a caged `gh` would carry (via the config
	// env: block). Every request below sends it, so a passing assertion is also an
	// overwrite assertion: GitHub never sees this value.
	ghPhantom = "Bearer ghp_phantom_the_proxy_overwrites_this"
	// ghInvalidToken is well-formed enough to reach GitHub and be rejected there.
	ghInvalidToken = "ghp_invalid_token_for_agent_creance_testing"

	statusInjectionUnavailable = 472
	headerCageReason           = "X-Cage-Reason"
	headerCageInjected         = "X-Cage-Injected"
)

func TestInjectGitHubGraphQLRealUpstream(t *testing.T) {
	ref, owner, name := requireGitHubInjectPrereqs(t)

	// Resolve the live secret up front, in the test goroutine: a skip decision must not
	// run inside proxy.Attach's Secrets closure (t.Skip calls runtime.Goexit, which
	// would unwind Attach rather than the test). The closures below hand Attach a
	// precomputed payload, so the real spawn-path delivery is still what is exercised.
	token := resolveLiveToken(t, ref)

	t.Run("real token authenticates and the agent's header is overwritten", func(t *testing.T) {
		port := startInjectProxy(t, secretPayload(t, map[string]string{ghCredential: token}))

		resp, body := postGraphQL(t, port, owner, name)

		require.Equal(t, http.StatusOK, resp.StatusCode,
			"real token should authenticate; body: %s", body)

		// The client sent ghPhantom. GitHub answered anyway, so the proxy replaced the
		// header before forwarding — the overwrite guarantee, proven against a real
		// upstream rather than an echo origin (AC-0068e criteria 1 and 2).
		var out struct {
			Data struct {
				Repository struct {
					Name string `json:"name"`
				} `json:"repository"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(body, &out))
		require.Equal(t, name, out.Data.Repository.Name,
			"the injected token should be scoped to %s/%s", owner, name)
		require.Empty(t, resp.Header.Get(headerCageInjected),
			"a successful request is not annotated")
	})

	t.Run("unresolvable credential fails closed with 472", func(t *testing.T) {
		// An empty secret map is what resolveInjectionSecrets produces when the store is
		// locked or the reference is wrong: the credential is warned and omitted, and the
		// addon must refuse rather than forward the phantom (AC-0068e criterion 3a).
		port := startInjectProxy(t, secretPayload(t, map[string]string{}))

		resp, body := postGraphQL(t, port, owner, name)

		require.Equal(t, statusInjectionUnavailable, resp.StatusCode)
		require.Equal(t, "injection-unavailable", resp.Header.Get(headerCageReason))
		require.Equal(t, ghCredential, resp.Header.Get(headerCageInjected))

		var refusal struct {
			Error        string `json:"error"`
			Credential   string `json:"credential"`
			HowToProceed string `json:"how_to_proceed"`
		}
		require.NoError(t, json.Unmarshal(body, &refusal))
		require.Equal(t, "agent_cage_injection_unavailable", refusal.Error)
		require.Equal(t, ghCredential, refusal.Credential)
		require.NotEmpty(t, refusal.HowToProceed)
	})

	t.Run("rejected token keeps its upstream 401 and is annotated", func(t *testing.T) {
		// The proxy must not invent a status when the credential itself is bad — the
		// upstream owns it — but must name the credential so the agent blames the
		// injected token, not its phantom (AC-0068e criterion 3b).
		port := startInjectProxy(t, secretPayload(t, map[string]string{ghCredential: ghInvalidToken}))

		resp, _ := postGraphQL(t, port, owner, name)

		require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"GitHub, not the proxy, owns the status for a rejected credential")
		require.Equal(t, ghCredential, resp.Header.Get(headerCageInjected))
		require.Empty(t, resp.Header.Get(headerCageReason),
			"an upstream rejection is not a cage refusal")
	})
}

// requireGitHubInjectPrereqs skips unless a real mitmproxy, a real secret reference, and
// a trusted mitmproxy CA are all present. It returns the reference and the target repo.
func requireGitHubInjectPrereqs(t *testing.T) (ref, owner, name string) {
	t.Helper()

	if _, err := exec.LookPath("mitmdump"); err != nil {
		t.Skip("mitmdump not installed; skipping real-GitHub injection test")
	}

	ref = os.Getenv("AC_TEST_GITHUB_TOKEN_REF")
	if ref == "" {
		t.Skip("set AC_TEST_GITHUB_TOKEN_REF to a readable op:// / keychain:// / env:// " +
			"reference for a fine-grained PAT to exercise this test")
	}

	// The Manager spawns mitmdump without --set confdir, so it uses the default
	// ~/.mitmproxy CA — the same one `agent-creance setup` trusts. Mirrors
	// sysdep/tlsprober_integration_test.go.
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	if _, err := os.Stat(filepath.Join(home, ".mitmproxy", caCertFile)); err != nil {
		t.Skip("no mitmproxy CA at ~/.mitmproxy; run `agent-creance setup` first")
	}

	repo := os.Getenv("AC_TEST_GITHUB_REPO")
	if repo == "" {
		repo = "tobyS/agent-creance"
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		t.Fatalf("AC_TEST_GITHUB_REPO must be owner/name, got %q", repo)
	}
	return ref, owner, name
}

const caCertFile = "mitmproxy-ca-cert.pem"

// resolveLiveToken resolves ref through the production resolver, wired exactly as
// internal/cli/cli.go wires it. The resolved value is never printed, not even on failure.
func resolveLiveToken(t *testing.T, ref string) string {
	t.Helper()

	r := sysdep.OSSecretResolver{
		Commander: sysdep.ExecCommander{},
		Keychain:  sysdep.OSKeychain{},
		Paths:     sysdep.OSPathResolver{},
	}
	secret, err := r.Resolve(context.Background(), ref)
	switch {
	case errors.Is(err, sysdep.ErrSecretToolMissing):
		t.Skip("the secret tool for this reference is not installed (e.g. the 1Password CLI)")
	case errors.Is(err, sysdep.ErrKeychainLocked):
		t.Skip("the secret store is locked; unlock it to exercise this test")
	case errors.Is(err, sysdep.ErrSecretNotFound):
		t.Skipf("could not resolve %s (signed in? reference correct?)", ref)
	}
	require.NoError(t, err)
	require.NotEmpty(t, secret, "expected a non-empty resolved secret") // never print it
	return string(secret)
}

// secretPayload builds the {credential-name: token} JSON the addon reads from fd 3.
func secretPayload(t *testing.T, secrets map[string]string) func(context.Context) ([]byte, error) {
	t.Helper()
	payload, err := json.Marshal(secrets)
	require.NoError(t, err)
	return func(context.Context) ([]byte, error) { return payload, nil }
}

// startInjectProxy spawns a real mitmproxy for a fresh, isolated project whose policy
// injects ghCredential into api.github.com/graphql, and returns its port. Each call gets
// its own state dir so Attach always takes the SPAWN path — the reuse path deliberately
// never re-resolves secrets, so a shared proxy would silently reuse the first payload.
func startInjectProxy(t *testing.T, secrets func(context.Context) ([]byte, error)) int {
	t.Helper()

	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	paths := sysdep.OSPathResolver{}
	lay, err := state.New(paths).Resolve(t.TempDir())
	require.NoError(t, err)

	enforcerPy, err := proxy.NewExtractor(sysdep.OSFileSystem{}, paths).Extract()
	require.NoError(t, err)

	compiled := policy.Compiled{
		Version: policy.CompiledVersion,
		Credentials: map[string]policy.Credential{
			ghCredential: {
				Source:   os.Getenv("AC_TEST_GITHUB_TOKEN_REF"), // a reference; never a value
				Header:   "Authorization",
				Template: "Bearer {token}",
			},
		},
		RuleSet: policy.RuleSet{
			Allow: []policy.Rule{{
				Host:    "api.github.com",
				Paths:   []string{"/graphql"},
				Methods: []string{http.MethodPost},
				Mode:    config.ModeIntercept,
				Inject:  ghCredential,
			}},
		},
	}
	raw, err := json.Marshal(compiled)
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(lay.Root, 0o755))
	require.NoError(t, os.WriteFile(lay.PolicyJSON(), raw, 0o644))
	require.NoError(t, os.WriteFile(lay.SessionOverlay(), []byte("once: []\n"), 0o644))

	mgr := proxy.NewManager(sysdep.OSFileSystem{}, sysdep.OSFlock{}, sysdep.OSProcessManager{},
		sysdep.OSPortAllocator{}, sysdep.OSUnixSocket{}, sysdep.OSSleeper{}, os.Stderr)

	att, err := mgr.Attach(context.Background(), proxy.StartConfig{
		Layout:     lay,
		EnforcerPy: enforcerPy,
		PolicyHash: "ac-0068e",
		SelfPID:    os.Getpid(),
		Secrets:    secrets,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Detach(lay, os.Getpid()) })

	require.Eventually(t, func() bool {
		return sysdep.OSPortAllocator{}.Probe(att.Port)
	}, 10*time.Second, 100*time.Millisecond, "proxy did not come up")

	return att.Port
}

// postGraphQL issues the repository probe through the proxy, always carrying the phantom
// Authorization header a caged `gh` would send.
func postGraphQL(t *testing.T, port int, owner, name string) (*http.Response, []byte) {
	t.Helper()

	query := fmt.Sprintf(`{"query":"query { repository(owner: \"%s\", name: \"%s\") { name } }"}`, owner, name)
	req, err := http.NewRequest(http.MethodPost, "https://api.github.com/graphql", strings.NewReader(query))
	require.NoError(t, err)
	req.Header.Set("Authorization", ghPhantom)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agent-creance-integration-test") // GitHub requires one

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(&url.URL{
				Scheme: "http",
				Host:   "127.0.0.1:" + strconv.Itoa(port),
			}),
			TLSClientConfig: &tls.Config{RootCAs: mitmCAPool(t), MinVersion: tls.VersionTLS12},
		},
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	require.NoError(t, err)
	return resp, body
}

// mitmCAPool trusts only the mitmproxy CA — the proxy re-signs api.github.com's leaf.
func mitmCAPool(t *testing.T) *x509.CertPool {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	pem, err := os.ReadFile(filepath.Join(home, ".mitmproxy", caCertFile))
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(pem), "mitmproxy CA is not a valid PEM")
	return pool
}
