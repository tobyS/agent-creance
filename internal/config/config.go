// Package config parses one .agent-creance.yaml document into a validated, typed
// Config. It is deliberately pure: it never touches the filesystem and does not
// resolve include: directives or merge layered configs (that is AC-0008). Its only
// inputs are bytes; its only outputs are a *Config or a human-readable error.
//
// Decoding is strict: an unknown or misspelled key is an error rather than a silent
// drop. For a security-policy file a silently-ignored key (e.g. a mistyped
// deny_always) would be a hole, so the loader fails closed.
//
// Why a raw mirror (rawConfig) instead of decoding straight into Config: yaml.v3's
// strict-decode (KnownFields) is defeated by any custom UnmarshalYAML in the type
// tree (go-yaml issue #642). To parse host_services entries ("label:port") into a
// typed shape *and* keep strict checking on every nested mapping, we decode into a
// plain-struct mirror that has no custom unmarshalers, then convert — applying the
// mode default and parsing host_services in a separate pass.
package config

import (
	"bytes"
	"errors"
	"io"

	"gopkg.in/yaml.v3"
)

// Config is the typed, validated representation of one .agent-creance.yaml document.
// Every top-level section is optional: a project config may carry only deltas and a
// global baseline may be include:-only.
type Config struct {
	Agent       Agent
	Safehouse   Safehouse
	Include     []string
	Network     Network
	Env         map[string]string
	Credentials map[string]Credential

	// Warnings holds non-fatal problems found during effective-config validation
	// (see ValidateEffective) — e.g. a defined-but-never-injected credential. It is
	// populated by the Loader on the merged config and surfaced to the user; it is
	// never a parse input and is not carried by merge.
	Warnings []string
}

// Agent is how the caged coding agent is launched.
type Agent struct {
	Command []string
	Workdir string
}

// Safehouse holds the flags forwarded to agent-safehouse. Paths are kept verbatim
// (any ~ expansion is the safehouse compiler's job, a later phase).
type Safehouse struct {
	AddDirsRW []string
	AddDirsRO []string
	Enable    []string
}

// Network groups the in-cage host services and the egress allow/deny policy.
type Network struct {
	HostServices []HostService
	Egress       Egress
}

// HostService is one "label:port" entry reachable from inside the cage. Label is
// cosmetic; the generated Seatbelt rule keys on the port via the `localhost` host
// token (AC-0014 / internal/profile), so the address is not represented here.
type HostService struct {
	Label string
	Port  int
}

// Egress is the TLS-terminating egress policy: built-in generators plus explicit
// soft-allow and hard-deny rules.
type Egress struct {
	Generators []Generator
	Allow      []Rule
	DenyAlways []Rule
}

// Generator is one entry in network.egress.generators. Type is the generator name
// (e.g. "package_json"); Path is the optional manifest path it reads, relative to the
// project root. A bare-string entry (`- package_json`) decodes to a Generator with an
// empty Path, which the compiler resolves to the generator's root manifest. The
// object form (`- {type: package_json, path: apps/web/package.json}`) lets a monorepo
// list the same type once per package. Generator is a comparable struct so merge can
// dedupe it by (Type, Path) value.
type Generator struct {
	Type string
	Path string
}

// Rule is one egress allow/deny entry. Paths and Methods are pointers so an omitted
// key (nil) is distinguishable from an explicit empty list — the passthrough
// validation rejects a rule that *sets* paths/methods, which length alone cannot
// detect.
//
// Inject and InCage are the auth axis (AC-0068), orthogonal to the transport axis
// (Mode) and meaningful only on intercepted hosts: Inject names a credentials: entry
// the proxy will later resolve host-side and inject (overwrite + fail-closed;
// AC-0068c); InCage marks that the proxy must never add/strip/modify any auth header
// because the agent authenticates with a credential it holds in-cage (the honest,
// lint-able marker for cases injection cannot serve — SigV4, op, SDK-minted). At most
// one of them may be set. AC-0068b models and validates them; no injection happens.
type Rule struct {
	Host    string    `yaml:"host"`
	Paths   *[]string `yaml:"paths"`
	Methods *[]string `yaml:"methods"`
	Mode    string    `yaml:"mode"`
	Inject  string    `yaml:"inject"`
	InCage  bool      `yaml:"in_cage"`
	Reason  string    `yaml:"reason"`
}

// Enforcement modes a Rule may carry.
const (
	ModeIntercept   = "intercept"
	ModePassthrough = "passthrough"
)

// DefaultCredentialHeader is the header a Credential targets when its Header field is
// left empty — the overwhelmingly common case (Bearer / token / Basic all ride on
// Authorization). Custom-header services (x-api-key, PRIVATE-TOKEN, …) set Header.
const DefaultCredentialHeader = "Authorization"

// Credential is one entry in the top-level credentials: block: a named, indirected
// reference the proxy will later resolve host-side and inject (AC-0068c). It carries
// only a reference, never a value — the resolved secret never appears here or in the
// compiled policy.json.
//
//   - Source is an AC-0068a reference: op:// / keychain:// / env://.
//   - Header is the target header name; empty defaults to DefaultCredentialHeader.
//   - Template is the value-template (see template.go): Bearer {token} | token
//     {token} | {token} | Basic base64({user}:{token}) | any custom string with a
//     {token} placeholder.
//   - Username is the sentinel used only by the Basic base64({user}:{token}) form
//     (git x-access-token, PyPI __token__, GitLab oauth2, Jira email).
type Credential struct {
	Source   string `yaml:"source"`
	Header   string `yaml:"header"`
	Template string `yaml:"template"`
	Username string `yaml:"username"`
}

// rawConfig mirrors Config for strict decoding. It differs from the public types in
// exactly one place — host_services decodes as []string — and carries the yaml tags.
// It deliberately has no custom UnmarshalYAML so KnownFields stays effective.
type rawConfig struct {
	Agent       rawAgent              `yaml:"agent"`
	Safehouse   rawSafehouse          `yaml:"safehouse"`
	Include     []string              `yaml:"include"`
	Network     rawNetwork            `yaml:"network"`
	Env         map[string]string     `yaml:"env"`
	Credentials map[string]Credential `yaml:"credentials"`
}

type rawAgent struct {
	Command []string `yaml:"command"`
	Workdir string   `yaml:"workdir"`
}

type rawSafehouse struct {
	AddDirsRW []string `yaml:"add_dirs_rw"`
	AddDirsRO []string `yaml:"add_dirs_ro"`
	Enable    []string `yaml:"enable"`
}

type rawNetwork struct {
	HostServices []string  `yaml:"host_services"`
	Egress       rawEgress `yaml:"egress"`
}

type rawEgress struct {
	// Generators is captured as raw nodes so a list entry may be either a bare
	// scalar (`- package_json`) or a mapping (`- {type:, path:}`). yaml.Node carries
	// no custom UnmarshalYAML, so top-level KnownFields strictness is preserved; the
	// inner keys are validated in the parseGenerator post-decode pass.
	Generators []yaml.Node `yaml:"generators"`
	Allow      []Rule      `yaml:"allow"`
	DenyAlways []Rule      `yaml:"deny_always"`
}

// Parse decodes one .agent-creance.yaml document strictly, applies defaults, and
// validates it. It returns the typed config, or an error whose message is stable and
// human-readable (a *ValidationError for schema problems). It never touches the
// filesystem and does not resolve include: directives.
func Parse(data []byte) (*Config, error) {
	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			// An empty document is a valid (empty) config.
			return &Config{}, nil
		}
		return nil, reformat(err)
	}

	cfg := Config{
		Agent:     Agent(raw.Agent),
		Safehouse: Safehouse(raw.Safehouse),
		Include:   raw.Include,
		Network: Network{
			Egress: Egress{
				// Generators is filled in applyDefaults (post-decode parse of the
				// raw nodes); Allow/DenyAlways copy straight across.
				Allow:      raw.Network.Egress.Allow,
				DenyAlways: raw.Network.Egress.DenyAlways,
			},
		},
		Env:         raw.Env,
		Credentials: raw.Credentials,
	}

	verr := &ValidationError{}
	cfg.applyDefaults(raw, verr)
	cfg.validate(verr)
	if len(verr.Issues) > 0 {
		return nil, verr
	}
	return &cfg, nil
}

// applyDefaults fills in defaulted fields (rule Mode → intercept) and parses the raw
// host_services strings into typed HostService values, recording any parse problems
// on verr.
func (c *Config) applyDefaults(raw rawConfig, verr *ValidationError) {
	for _, s := range raw.Network.HostServices {
		hs, err := parseHostService(s)
		if err != nil {
			verr.add("%s", err.Error())
			continue
		}
		c.Network.HostServices = append(c.Network.HostServices, hs)
	}

	for _, node := range raw.Network.Egress.Generators {
		g, err := parseGenerator(&node)
		if err != nil {
			verr.add("%s", err.Error())
			continue
		}
		c.Network.Egress.Generators = append(c.Network.Egress.Generators, g)
	}

	defaultRuleModes(c.Network.Egress.Allow)
	defaultRuleModes(c.Network.Egress.DenyAlways)

	defaultCredentialHeaders(c.Credentials)
}

func defaultRuleModes(rules []Rule) {
	for i := range rules {
		if rules[i].Mode == "" {
			rules[i].Mode = ModeIntercept
		}
	}
}

// defaultCredentialHeaders fills an empty credential Header with the Authorization
// default in place, so validation and the compiled policy always see a concrete
// header name.
func defaultCredentialHeaders(creds map[string]Credential) {
	for name, cred := range creds {
		if cred.Header == "" {
			cred.Header = DefaultCredentialHeader
			creds[name] = cred
		}
	}
}
