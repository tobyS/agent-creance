package config

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// validate checks cross-field schema constraints, recording every problem on verr
// (it does not stop at the first). It runs after applyDefaults, so every rule Mode is
// already non-empty.
func (c *Config) validate(verr *ValidationError) {
	validateRules(c.Network.Egress.Allow, "allow", verr)
	validateRules(c.Network.Egress.DenyAlways, "deny_always", verr)
}

// validateRules checks one allow/deny list. The mode is validated uniformly across
// both lists: the Rule shape is uniform, so a stray mode: on a deny rule is better
// caught than silently ignored.
func validateRules(rules []Rule, list string, verr *ValidationError) {
	for i, r := range rules {
		ref := ruleRef(i, r)
		if r.Host == "" {
			verr.add("egress %s rule %s is missing a host", list, ref)
		}
		switch r.Mode {
		case ModeIntercept:
			// Fully intercepted: paths/methods are allowed (and optional).
		case ModePassthrough:
			// A passthrough host is a raw CONNECT tunnel with no TLS termination, so
			// path/method matching is impossible — carrying them is a config error,
			// not a silent no-op (docs/design.md "Per-host enforcement modes").
			if r.Paths != nil {
				verr.add("egress %s rule %s uses mode: passthrough, which cannot carry paths", list, ref)
			}
			if r.Methods != nil {
				verr.add("egress %s rule %s uses mode: passthrough, which cannot carry methods", list, ref)
			}
		default:
			verr.add("egress %s rule %s has unknown mode %q (want %q or %q)", list, ref, r.Mode, ModeIntercept, ModePassthrough)
		}
	}
}

// ruleRef names a rule for an error message: by host when it has one, else by its
// 1-based position in the list.
func ruleRef(i int, r Rule) string {
	if r.Host != "" {
		return fmt.Sprintf("for host %q", r.Host)
	}
	return fmt.Sprintf("#%d", i+1)
}

// parseGenerator parses one network.egress.generators entry from its raw yaml node.
// A scalar is the bare form (`- package_json`) → Generator{Type: <scalar>}. A mapping
// is the object form (`- {type:, path:}`); its keys are validated here (KnownFields
// does not reach into a captured yaml.Node) — only "type" (required, non-empty) and
// "path" (optional) are allowed, anything else is an error citing the line. The
// generator name's validity (Known) is checked later, by the compiler.
func parseGenerator(node *yaml.Node) (Generator, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Value == "" {
			return Generator{}, fmt.Errorf("egress generators line %d: empty generator name", node.Line)
		}
		return Generator{Type: node.Value}, nil
	case yaml.MappingNode:
		var g Generator
		var sawType bool
		// MappingNode content is a flat [key, value, key, value, …] list.
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			val := node.Content[i+1]
			switch key.Value {
			case "type":
				g.Type = val.Value
				sawType = true
			case "path":
				g.Path = val.Value
			default:
				return Generator{}, fmt.Errorf("egress generators line %d: unknown key %q (want %q or %q)", key.Line, key.Value, "type", "path")
			}
		}
		if !sawType || g.Type == "" {
			return Generator{}, fmt.Errorf("egress generators line %d: missing %q", node.Line, "type")
		}
		return g, nil
	default:
		return Generator{}, fmt.Errorf("egress generators line %d: a generator must be a name or a {type, path} mapping", node.Line)
	}
}

// parseHostService parses a "label:port" entry into a typed HostService. The label
// is cosmetic but must be non-empty; the port must be a number in 1-65535.
func parseHostService(s string) (HostService, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return HostService{}, fmt.Errorf("host_services entry %q is not in label:port form", s)
	}
	label, portStr := s[:i], s[i+1:]
	if label == "" {
		return HostService{}, fmt.Errorf("host_services entry %q has an empty label", s)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return HostService{}, fmt.Errorf("host_services entry %q has a non-numeric port %q", s, portStr)
	}
	if port < 1 || port > 65535 {
		return HostService{}, fmt.Errorf("host_services entry %q has port %d out of range 1-65535", s, port)
	}
	return HostService{Label: label, Port: port}, nil
}
