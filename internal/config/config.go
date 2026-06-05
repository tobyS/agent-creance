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
	Agent     Agent
	Safehouse Safehouse
	Include   []string
	Network   Network
	Env       map[string]string
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
// cosmetic; the address is always forced to 127.0.0.1 downstream (AC-0014), so it is
// not represented here.
type HostService struct {
	Label string
	Port  int
}

// Egress is the TLS-terminating egress policy: built-in generators plus explicit
// soft-allow and hard-deny rules.
type Egress struct {
	Generators []string
	Allow      []Rule
	DenyAlways []Rule
}

// Rule is one egress allow/deny entry. Paths and Methods are pointers so an omitted
// key (nil) is distinguishable from an explicit empty list — the passthrough
// validation rejects a rule that *sets* paths/methods, which length alone cannot
// detect.
type Rule struct {
	Host    string    `yaml:"host"`
	Paths   *[]string `yaml:"paths"`
	Methods *[]string `yaml:"methods"`
	Mode    string    `yaml:"mode"`
	Reason  string    `yaml:"reason"`
}

// Enforcement modes a Rule may carry.
const (
	ModeIntercept   = "intercept"
	ModePassthrough = "passthrough"
)

// rawConfig mirrors Config for strict decoding. It differs from the public types in
// exactly one place — host_services decodes as []string — and carries the yaml tags.
// It deliberately has no custom UnmarshalYAML so KnownFields stays effective.
type rawConfig struct {
	Agent     rawAgent          `yaml:"agent"`
	Safehouse rawSafehouse      `yaml:"safehouse"`
	Include   []string          `yaml:"include"`
	Network   rawNetwork        `yaml:"network"`
	Env       map[string]string `yaml:"env"`
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
	Generators []string `yaml:"generators"`
	Allow      []Rule   `yaml:"allow"`
	DenyAlways []Rule   `yaml:"deny_always"`
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
			Egress: Egress(raw.Network.Egress),
		},
		Env: raw.Env,
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

	defaultRuleModes(c.Network.Egress.Allow)
	defaultRuleModes(c.Network.Egress.DenyAlways)
}

func defaultRuleModes(rules []Rule) {
	for i := range rules {
		if rules[i].Mode == "" {
			rules[i].Mode = ModeIntercept
		}
	}
}
