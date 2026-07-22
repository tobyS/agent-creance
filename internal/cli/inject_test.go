package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/broker"
	"github.com/tobyS/agent-creance/internal/policy"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

func compiledWith(creds map[string]policy.Credential, allow []policy.Rule) *policy.Compiled {
	c := &policy.Compiled{Credentials: creds}
	c.Allow = allow
	return c
}

// unmarshalPayload decodes the fd-3 JSON into the structured broker.Payload.
func unmarshalPayload(t *testing.T, payload []byte) broker.Payload {
	t.Helper()
	var got broker.Payload
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("payload not a broker.Payload: %v (%s)", err, payload)
	}
	return got
}

func TestResolveInjectionSecretsResolvesReferencedCredentials(t *testing.T) {
	r := sysdeptest.NewFakeSecretResolver().WithSecret("op://vault/gh", "ghs_real")
	compiled := compiledWith(
		map[string]policy.Credential{"gh": {Source: "op://vault/gh", Template: "Bearer {token}"}},
		[]policy.Rule{{Host: "api.github.com", Inject: "gh"}},
	)

	var warnings []string
	payload, err := resolveInjectionSecrets(context.Background(), r, compiled, func(m string) { warnings = append(warnings, m) })
	if err != nil {
		t.Fatalf("resolveInjectionSecrets: %v", err)
	}
	got := unmarshalPayload(t, payload)
	if got["gh"].Kind != broker.KindStatic || got["gh"].Token != "ghs_real" {
		t.Errorf("payload[gh] = %+v, want static token ghs_real", got["gh"])
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

func TestResolveInjectionSecretsOmitsUnresolvableAndWarns(t *testing.T) {
	// "gh" resolves; "deploy" does not (unregistered → ErrSecretNotFound).
	r := sysdeptest.NewFakeSecretResolver().WithSecret("op://vault/gh", "ghs_real")
	compiled := compiledWith(
		map[string]policy.Credential{
			"gh":     {Source: "op://vault/gh", Template: "Bearer {token}"},
			"deploy": {Source: "op://vault/deploy", Template: "Bearer {token}"},
		},
		[]policy.Rule{
			{Host: "api.github.com", Inject: "gh"},
			{Host: "deploy.example.com", Inject: "deploy"},
		},
	)

	var warnings []string
	payload, err := resolveInjectionSecrets(context.Background(), r, compiled, func(m string) { warnings = append(warnings, m) })
	if err != nil {
		t.Fatalf("resolveInjectionSecrets: %v", err)
	}
	got := unmarshalPayload(t, payload)
	if _, ok := got["gh"]; !ok {
		t.Error("resolvable credential gh missing from payload")
	}
	if _, ok := got["deploy"]; ok {
		t.Error("unresolvable credential deploy must be omitted")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "deploy") {
		t.Errorf("want one warning naming deploy, got %v", warnings)
	}
	// Hygiene: no warning leaks a resolved token value.
	for _, w := range warnings {
		if strings.Contains(w, "ghs_real") {
			t.Errorf("warning leaked a secret: %q", w)
		}
	}
}

func TestResolveInjectionSecretsNoInjectRulesReturnsNil(t *testing.T) {
	r := sysdeptest.NewFakeSecretResolver()
	compiled := compiledWith(nil, []policy.Rule{{Host: "example.com"}}) // no inject
	payload, err := resolveInjectionSecrets(context.Background(), r, compiled, func(string) {})
	if err != nil {
		t.Fatalf("resolveInjectionSecrets: %v", err)
	}
	if payload != nil {
		t.Errorf("payload = %s, want nil when nothing injects", payload)
	}
	if len(r.Resolved) != 0 {
		t.Errorf("resolver queried %v, want no resolution when nothing injects", r.Resolved)
	}
}

func TestResolveInjectionSecretsAllUnresolvableReturnsNil(t *testing.T) {
	r := sysdeptest.NewFakeSecretResolver() // resolves nothing
	compiled := compiledWith(
		map[string]policy.Credential{"gh": {Source: "op://vault/gh", Template: "Bearer {token}"}},
		[]policy.Rule{{Host: "api.github.com", Inject: "gh"}},
	)
	var warnings []string
	payload, err := resolveInjectionSecrets(context.Background(), r, compiled, func(m string) { warnings = append(warnings, m) })
	if err != nil {
		t.Fatalf("resolveInjectionSecrets: %v", err)
	}
	if payload != nil {
		t.Errorf("payload = %s, want nil when no credential resolves", payload)
	}
	if len(warnings) != 1 {
		t.Errorf("want a warning for the unresolvable credential, got %v", warnings)
	}
}

// TestResolveInjectionSecretsMintedGitHubApp: a github_app credential resolves its key
// material into a github_app spec carrying the non-secret params.
func TestResolveInjectionSecretsMintedGitHubApp(t *testing.T) {
	r := sysdeptest.NewFakeSecretResolver().WithSecret("keychain://svc/ghapp-key", "-----BEGIN RSA PRIVATE KEY-----\n...\n-----END RSA PRIVATE KEY-----")
	compiled := compiledWith(
		map[string]policy.Credential{
			"gh-app": {
				Template: "Bearer {token}",
				GitHubApp: &policy.GitHubAppMint{
					Key:         "keychain://svc/ghapp-key",
					ClientID:    "Iv1.example",
					Repo:        "tobyS/agent-creance",
					Permissions: map[string]string{"contents": "read"},
				},
			},
		},
		[]policy.Rule{{Host: "api.github.com", Inject: "gh-app"}},
	)

	var warnings []string
	payload, err := resolveInjectionSecrets(context.Background(), r, compiled, func(m string) { warnings = append(warnings, m) })
	if err != nil {
		t.Fatalf("resolveInjectionSecrets: %v", err)
	}
	got := unmarshalPayload(t, payload)
	spec := got["gh-app"]
	if spec.Kind != broker.KindGitHubApp || spec.GitHubApp == nil {
		t.Fatalf("payload[gh-app] = %+v, want a github_app spec", spec)
	}
	if !strings.Contains(spec.GitHubApp.PEM, "BEGIN RSA PRIVATE KEY") {
		t.Errorf("resolved PEM not carried in the spec")
	}
	if spec.GitHubApp.ClientID != "Iv1.example" || spec.GitHubApp.Repo != "tobyS/agent-creance" {
		t.Errorf("non-secret params not carried: %+v", spec.GitHubApp)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

// TestResolveInjectionSecretsOAuth2Unauthorized: an oauth2 credential whose refresh
// token is not stored yet (ErrSecretNotFound) is omitted with the authorize hint.
func TestResolveInjectionSecretsOAuth2Unauthorized(t *testing.T) {
	r := sysdeptest.NewFakeSecretResolver() // resolves nothing → ErrSecretNotFound
	compiled := compiledWith(
		map[string]policy.Credential{
			"drive": {
				Template: "Bearer {token}",
				OAuth2: &policy.OAuth2Mint{
					RefreshToken:  "keychain://svc/drive-refresh",
					ClientID:      "1234.apps.googleusercontent.com",
					TokenEndpoint: "https://oauth2.googleapis.com/token",
				},
			},
		},
		[]policy.Rule{{Host: "www.googleapis.com", Inject: "drive"}},
	)

	var warnings []string
	payload, err := resolveInjectionSecrets(context.Background(), r, compiled, func(m string) { warnings = append(warnings, m) })
	if err != nil {
		t.Fatalf("resolveInjectionSecrets: %v", err)
	}
	if payload != nil {
		t.Errorf("payload = %s, want nil when the only credential is unauthorized", payload)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "credential authorize drive") {
		t.Errorf("want the authorize hint, got %v", warnings)
	}
}

// TestResolveInjectionSecretsOAuth2Authorized: a stored refresh token resolves into an
// oauth2 spec.
func TestResolveInjectionSecretsOAuth2Authorized(t *testing.T) {
	r := sysdeptest.NewFakeSecretResolver().WithSecret("keychain://svc/drive-refresh", "rt-stored")
	compiled := compiledWith(
		map[string]policy.Credential{
			"drive": {
				Template: "Bearer {token}",
				OAuth2: &policy.OAuth2Mint{
					RefreshToken:  "keychain://svc/drive-refresh",
					ClientID:      "1234.apps.googleusercontent.com",
					TokenEndpoint: "https://oauth2.googleapis.com/token",
					Scopes:        []string{"https://www.googleapis.com/auth/drive.file"},
				},
			},
		},
		[]policy.Rule{{Host: "www.googleapis.com", Inject: "drive"}},
	)

	payload, err := resolveInjectionSecrets(context.Background(), r, compiled, func(string) {})
	if err != nil {
		t.Fatalf("resolveInjectionSecrets: %v", err)
	}
	got := unmarshalPayload(t, payload)
	spec := got["drive"]
	if spec.Kind != broker.KindOAuth2 || spec.OAuth2 == nil {
		t.Fatalf("payload[drive] = %+v, want an oauth2 spec", spec)
	}
	if spec.OAuth2.RefreshToken != "rt-stored" {
		t.Errorf("resolved refresh token not carried: %+v", spec.OAuth2)
	}
}
