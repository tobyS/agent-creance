// White-box tests (package config) for the AC-0068b credential-injection schema:
// the credentials: block, the auth-axis rule fields (inject/in_cage), the header
// default, strict unknown-key rejection, and key-wise credential merge.
package config

import (
	"reflect"
	"strings"
	"testing"
)

const credentialsYAML = `
network:
  egress:
    allow:
      - host: api.github.com
        paths: ["/graphql"]
        methods: [POST]
        inject: github-token
      - host: s3.eu-central-1.amazonaws.com
        in_cage: true

credentials:
  github-token:
    source: op://Private/GitHub PAT/token
    template: "Bearer {token}"
`

func TestParse_CredentialsAndAuthAxis(t *testing.T) {
	cfg, err := Parse([]byte(credentialsYAML))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	allow := cfg.Network.Egress.Allow
	if len(allow) != 2 {
		t.Fatalf("len(Allow) = %d, want 2", len(allow))
	}
	if allow[0].Inject != "github-token" {
		t.Errorf("Allow[0].Inject = %q, want %q", allow[0].Inject, "github-token")
	}
	if allow[0].InCage {
		t.Errorf("Allow[0].InCage = true, want false")
	}
	if !allow[1].InCage {
		t.Errorf("Allow[1].InCage = false, want true")
	}
	if allow[1].Inject != "" {
		t.Errorf("Allow[1].Inject = %q, want empty", allow[1].Inject)
	}

	cred, ok := cfg.Credentials["github-token"]
	if !ok {
		t.Fatalf("credentials[github-token] missing; got %v", cfg.Credentials)
	}
	if cred.Source != "op://Private/GitHub PAT/token" {
		t.Errorf("Source = %q", cred.Source)
	}
	if cred.Template != "Bearer {token}" {
		t.Errorf("Template = %q", cred.Template)
	}
	// Header was omitted → defaulted to Authorization in applyDefaults.
	if cred.Header != DefaultCredentialHeader {
		t.Errorf("Header = %q, want defaulted %q", cred.Header, DefaultCredentialHeader)
	}
}

func TestParse_CredentialUnknownKeyRejected(t *testing.T) {
	// KnownFields strictness must reach into a credentials entry: a mistyped key in a
	// security-policy file is a hole, so it fails closed rather than silently dropping.
	const y = `
credentials:
  tok:
    source: env://TOKEN
    template: "{token}"
    hedaer: X-Api-Key
`
	if _, err := Parse([]byte(y)); err == nil {
		t.Fatalf("Parse accepted an unknown key inside a credential entry; want error")
	}
}

func TestMergeCredentials_OverWins(t *testing.T) {
	base := map[string]Credential{
		"shared":    {Source: "op://base/item/field", Template: "Bearer {token}"},
		"only-base": {Source: "env://BASE", Template: "{token}"},
	}
	over := map[string]Credential{
		"shared":    {Source: "op://over/item/field", Template: "token {token}"},
		"only-over": {Source: "env://OVER", Template: "{token}"},
	}

	got := mergeCredentials(base, over)
	want := map[string]Credential{
		"shared":    {Source: "op://over/item/field", Template: "token {token}"},
		"only-base": {Source: "env://BASE", Template: "{token}"},
		"only-over": {Source: "env://OVER", Template: "{token}"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeCredentials = %#v, want %#v", got, want)
	}
}

func TestMergeCredentials_EmptyIsNil(t *testing.T) {
	if got := mergeCredentials(nil, nil); got != nil {
		t.Errorf("mergeCredentials(nil, nil) = %#v, want nil", got)
	}
}

func TestValidateEffective(t *testing.T) {
	// Undefined inject is a hard error.
	bad := &Config{
		Network: Network{Egress: Egress{Allow: []Rule{{Host: "h", Inject: "missing"}}}},
	}
	if _, err := bad.ValidateEffective(); err == nil {
		t.Errorf("undefined inject: want error, got nil")
	}

	// A referenced credential whose template lacks {user} despite a username, plus a
	// dangling credential, produce two warnings and no error.
	cfg := &Config{
		Network: Network{Egress: Egress{Allow: []Rule{{Host: "h", Inject: "used"}}}},
		Credentials: map[string]Credential{
			"used":   {Source: "env://X", Template: "{token}", Username: "u"},
			"unused": {Source: "env://Y", Template: "{token}"},
		},
	}
	warns, err := cfg.ValidateEffective()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 2 {
		t.Fatalf("warnings = %v, want 2", warns)
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, `"unused" is defined but never injected`) {
		t.Errorf("missing dangling-credential warning in %v", warns)
	}
	if !strings.Contains(joined, `"used" sets a username`) {
		t.Errorf("missing username-without-placeholder warning in %v", warns)
	}
}
