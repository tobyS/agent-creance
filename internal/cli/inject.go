package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tobyS/agent-creance/internal/policy"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// resolveInjectionSecrets resolves every credential referenced by an inject rule in
// the compiled policy to its raw token via the resolver, returning the JSON payload
// {credential-name: raw-token} the enforcer addon reads over the inherited fd
// (sysdep.SecretFD). It is the host-side, resolve-once-at-spawn step of credential
// injection: the long-lived secret stays in op:///keychain:///env://, only the
// resolved value crosses the fd, and it never touches this process's argv, env, or
// disk.
//
// Best-effort by design: a credential that fails to resolve is reported via warn and
// omitted from the payload, so the proxy still starts and the addon answers 472
// (injection-unavailable) for requests needing it — fail-closed, human-recoverable.
// Returns nil when no rule injects. The resolved token is never placed in the
// returned error or in a warn message (the resolver guarantees its own errors carry
// no secret value).
func resolveInjectionSecrets(ctx context.Context, r sysdep.SecretResolver, compiled *policy.Compiled, warn func(string)) ([]byte, error) {
	if compiled == nil || r == nil {
		return nil, nil
	}
	names := injectedCredentialNames(compiled)
	if len(names) == 0 {
		return nil, nil
	}
	tokens := make(map[string]string, len(names))
	for _, name := range names {
		cred, ok := compiled.Credentials[name]
		if !ok {
			// The compiler's validateInjectRefs already rejects a dangling inject, so
			// this is defensive only.
			warn(fmt.Sprintf("credential %q referenced by inject is not defined; requests needing it will be refused (472)", name))
			continue
		}
		val, err := r.Resolve(ctx, cred.Source)
		if err != nil {
			warn(fmt.Sprintf("credential %q could not be resolved (%v); requests needing it will be refused (472)", name, err))
			continue
		}
		tokens[name] = string(val)
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	return json.Marshal(tokens)
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
