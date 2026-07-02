package config

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// The credential value-template (AC-0068b) is a tiny substitution DSL, general
// enough for the common header shapes with one mechanism:
//
//   - Bearer {token}                  → "Bearer <token>"           (gold-plated v1)
//   - token {token}                   → "token <token>"           (gh REST)
//   - {token}                         → "<token>"                 (crates.io, Linear)
//   - Basic base64({user}:{token})    → "Basic <base64(user:token)>" (git/PyPI/GitLab)
//   - any custom string with {token}  → arbitrary header value    (x-api-key, …)
//
// The only substitutions are {token} (the resolved secret — a placeholder in every
// test here; the real value flows only at inject time, host-side, AC-0068c) and
// {user} (the Basic username sentinel). An optional single, non-nested base64( … )
// wrapper base64-encodes its already-substituted inner expression. This Go renderer
// is the reference/spec; the runtime injector ports the same semantics into the
// Python enforcer in AC-0068c.
const (
	tokenPlaceholder = "{token}"
	userPlaceholder  = "{user}"
	base64Open       = "base64("
)

// RenderCredentialValue renders a value-template to a header value, substituting the
// username sentinel and the (resolved) token and applying an optional base64(…)
// wrapper. It validates the template first, so a malformed template is a clear error
// rather than a silently-wrong header.
func RenderCredentialValue(template, username, token string) (string, error) {
	if err := validateTemplate(template, username); err != nil {
		return "", err
	}

	open := strings.Index(template, base64Open)
	if open < 0 {
		return substitutePlaceholders(template, username, token), nil
	}

	// validateTemplate guarantees exactly one base64( with a matching ), non-nested.
	rest := template[open+len(base64Open):]
	closeIdx := strings.Index(rest, ")")
	inner := rest[:closeIdx]
	prefix := template[:open]
	suffix := rest[closeIdx+1:]

	encoded := base64.StdEncoding.EncodeToString([]byte(substitutePlaceholders(inner, username, token)))
	return substitutePlaceholders(prefix, username, token) + encoded + substitutePlaceholders(suffix, username, token), nil
}

func substitutePlaceholders(s, username, token string) string {
	s = strings.ReplaceAll(s, userPlaceholder, username)
	s = strings.ReplaceAll(s, tokenPlaceholder, token)
	return s
}

// validateTemplate checks a value-template is well-formed without rendering a real
// secret: it must carry {token}; {user} requires a username; a base64(…) wrapper must
// be balanced, single, and non-nested; and no unknown {…} placeholder may appear.
func validateTemplate(template, username string) error {
	if !strings.Contains(template, tokenPlaceholder) {
		return fmt.Errorf("value-template %q must contain the %s placeholder", template, tokenPlaceholder)
	}
	if strings.Contains(template, userPlaceholder) && username == "" {
		return fmt.Errorf("value-template %q uses %s but the credential sets no username", template, userPlaceholder)
	}

	if open := strings.Index(template, base64Open); open >= 0 {
		rest := template[open+len(base64Open):]
		closeIdx := strings.Index(rest, ")")
		if closeIdx < 0 {
			return fmt.Errorf("value-template %q has an unbalanced %s (missing closing ')')", template, base64Open)
		}
		if strings.Contains(rest[:closeIdx], base64Open) || strings.Contains(rest[closeIdx+1:], base64Open) {
			return fmt.Errorf("value-template %q supports at most one %s…) wrapper", template, base64Open)
		}
	}

	stripped := strings.ReplaceAll(template, userPlaceholder, "")
	stripped = strings.ReplaceAll(stripped, tokenPlaceholder, "")
	if strings.ContainsAny(stripped, "{}") {
		return fmt.Errorf("value-template %q contains an unknown {…} placeholder (only %s and %s are supported)", template, userPlaceholder, tokenPlaceholder)
	}
	return nil
}
