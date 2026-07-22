package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tobyS/agent-creance/internal/broker"
	"github.com/tobyS/agent-creance/internal/policy"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// resolveInjectionSecrets resolves every credential referenced by an inject rule in
// the compiled policy and returns the JSON broker.Payload the broker reads over the
// inherited fd (sysdep.SecretFD). It is the host-side, resolve-once-at-spawn step of
// credential injection: the long-lived secret (a static token, or a GitHub App key /
// OAuth2 refresh token for a minted credential) stays in op:///keychain:///env://,
// only the resolved value crosses the fd, and it never touches this process's argv,
// env, or disk. A minted credential's key material is delivered here once and the
// broker mints/refreshes from it (AC-0069a) — the key itself is never re-resolved
// (that would re-prompt Touch ID) and never reaches the cage.
//
// Best-effort by design: a credential that fails to resolve is reported via warn and
// omitted from the payload, so the proxy still starts and the caged agent gets a 472
// (injection-unavailable) for requests needing it — fail-closed, human-recoverable.
// An OAuth2 refresh token that is simply not stored yet (ErrSecretNotFound) warns with
// the `credential authorize` hint instead of a generic resolve failure. Returns nil
// when no rule injects. A resolved secret is never placed in the returned error or in
// a warn message.
func resolveInjectionSecrets(ctx context.Context, r sysdep.SecretResolver, compiled *policy.Compiled, warn func(string)) ([]byte, error) {
	if compiled == nil || r == nil {
		return nil, nil
	}
	names := injectedCredentialNames(compiled)
	if len(names) == 0 {
		return nil, nil
	}
	payload := make(broker.Payload, len(names))
	for _, name := range names {
		cred, ok := compiled.Credentials[name]
		if !ok {
			// The compiler's validateInjectRefs already rejects a dangling inject, so
			// this is defensive only.
			warn(fmt.Sprintf("credential %q referenced by inject is not defined; requests needing it will be refused (472)", name))
			continue
		}
		spec, ok := resolveCredentialSpec(ctx, r, name, cred, warn)
		if !ok {
			continue
		}
		payload[name] = spec
	}
	if len(payload) == 0 {
		return nil, nil
	}
	return json.Marshal(payload)
}

// resolveCredentialSpec resolves one credential (static or minted) into its delivery
// spec. ok is false (and a warn emitted) when the credential's secret material cannot
// be resolved — the credential is then omitted and 472s per request.
func resolveCredentialSpec(ctx context.Context, r sysdep.SecretResolver, name string, cred policy.Credential, warn func(string)) (broker.CredentialSpec, bool) {
	switch {
	case cred.GitHubApp != nil:
		pem, err := r.Resolve(ctx, cred.GitHubApp.Key)
		if err != nil {
			warn(fmt.Sprintf("credential %q app key could not be resolved (%v); requests needing it will be refused (472)", name, err))
			return broker.CredentialSpec{}, false
		}
		return broker.CredentialSpec{
			Kind: broker.KindGitHubApp,
			GitHubApp: &broker.GitHubAppSpec{
				PEM:         string(pem),
				ClientID:    cred.GitHubApp.ClientID,
				Repo:        cred.GitHubApp.Repo,
				Permissions: cred.GitHubApp.Permissions,
			},
		}, true

	case cred.OAuth2 != nil:
		rt, err := r.Resolve(ctx, cred.OAuth2.RefreshToken)
		if err != nil {
			if errors.Is(err, sysdep.ErrSecretNotFound) {
				warn(fmt.Sprintf("credential %q is not authorized yet — run 'agent-creance credential authorize %s'; requests needing it will be refused (472)", name, name))
			} else {
				warn(fmt.Sprintf("credential %q refresh token could not be resolved (%v); requests needing it will be refused (472)", name, err))
			}
			return broker.CredentialSpec{}, false
		}
		return broker.CredentialSpec{
			Kind: broker.KindOAuth2,
			OAuth2: &broker.OAuth2Spec{
				RefreshToken:  string(rt),
				ClientID:      cred.OAuth2.ClientID,
				TokenEndpoint: cred.OAuth2.TokenEndpoint,
				Scopes:        cred.OAuth2.Scopes,
			},
		}, true

	default: // static
		val, err := r.Resolve(ctx, cred.Source)
		if err != nil {
			warn(fmt.Sprintf("credential %q could not be resolved (%v); requests needing it will be refused (472)", name, err))
			return broker.CredentialSpec{}, false
		}
		return broker.CredentialSpec{Kind: broker.KindStatic, Token: string(val)}, true
	}
}

// injectedCredentialNames returns the distinct credential names referenced by any
// rule's inject field, across both the allow and deny sets (stable first-seen order).
func injectedCredentialNames(c *policy.Compiled) []string {
	seen := map[string]bool{}
	var names []string
	for _, set := range [][]policy.Rule{c.Allow, c.DenyAlways} {
		for _, ru := range set {
			if ru.Inject != "" && !seen[ru.Inject] {
				seen[ru.Inject] = true
				names = append(names, ru.Inject)
			}
		}
	}
	return names
}
