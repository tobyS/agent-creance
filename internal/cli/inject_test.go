package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/policy"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

func compiledWith(creds map[string]policy.Credential, allow []policy.Rule) *policy.Compiled {
	c := &policy.Compiled{Credentials: creds}
	c.Allow = allow
	return c
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
	var got map[string]string
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, payload)
	}
	if got["gh"] != "ghs_real" {
		t.Errorf("payload[gh] = %q, want ghs_real", got["gh"])
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
	var got map[string]string
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
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
