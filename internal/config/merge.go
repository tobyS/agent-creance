package config

import "reflect"

// merge layers over onto base and returns the combined Config. The semantics follow
// docs/design.md:151 ("later files override earlier ones for scalar fields, while
// allow:/deny_always: lists union additively") extended per the AC-0008 checkpoint:
//
//   - Scalars (agent.workdir) and the agent.command argv: over wins when it sets the
//     field, else base is kept. command is replaced, never concatenated — joining two
//     argv slices would produce a nonsensical command line.
//   - List fields (safehouse dirs, enable, host_services, egress generators/allow/
//     deny_always) union: base's entries followed by over's, then exact duplicates are
//     dropped keeping the first occurrence (deterministic, order-stable).
//   - env merges key-wise with over winning on a collision.
//   - Include is not merged here: the loader resolves it away before/while merging.
//
// merge is pure (no I/O) and deterministic: the same inputs always yield a
// reflect.DeepEqual-identical result.
func merge(base, over Config) Config {
	return Config{
		Agent: Agent{
			Command: firstNonEmptySlice(over.Agent.Command, base.Agent.Command),
			Workdir: firstNonEmptyString(over.Agent.Workdir, base.Agent.Workdir),
		},
		Safehouse: Safehouse{
			AddDirsRW: dedupeStrings(concat(base.Safehouse.AddDirsRW, over.Safehouse.AddDirsRW)),
			AddDirsRO: dedupeStrings(concat(base.Safehouse.AddDirsRO, over.Safehouse.AddDirsRO)),
			Enable:    dedupeStrings(concat(base.Safehouse.Enable, over.Safehouse.Enable)),
		},
		Network: Network{
			HostServices: dedupeHostServices(concatHS(base.Network.HostServices, over.Network.HostServices)),
			Egress: Egress{
				Generators: dedupeStrings(concat(base.Network.Egress.Generators, over.Network.Egress.Generators)),
				Allow:      dedupeRules(concatRules(base.Network.Egress.Allow, over.Network.Egress.Allow)),
				DenyAlways: dedupeRules(concatRules(base.Network.Egress.DenyAlways, over.Network.Egress.DenyAlways)),
			},
		},
		Env: mergeEnv(base.Env, over.Env),
	}
}

// firstNonEmptyString returns over if it is non-empty, else base. This is the
// scalar-override rule: a later file that omits a scalar leaves the earlier value
// intact.
func firstNonEmptyString(over, base string) string {
	if over != "" {
		return over
	}
	return base
}

// firstNonEmptySlice returns over if it is non-empty, else base. Used for agent.command
// (replace, not union): an omitted command keeps the inherited one.
func firstNonEmptySlice(over, base []string) []string {
	if len(over) > 0 {
		return over
	}
	return base
}

func concat(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	out := make([]string, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}

func concatHS(a, b []HostService) []HostService {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	out := make([]HostService, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}

func concatRules(a, b []Rule) []Rule {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	out := make([]Rule, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}

// dedupeStrings returns xs with exact duplicates removed, keeping the first
// occurrence. It returns nil for an empty result so two empty merges compare equal
// under reflect.DeepEqual (the determinism guarantee).
func dedupeStrings(xs []string) []string {
	if len(xs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(xs))
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}

// dedupeHostServices removes exact duplicate host services, keeping the first
// occurrence. HostService is a comparable struct, so a set keyed on the value works.
func dedupeHostServices(xs []HostService) []HostService {
	if len(xs) == 0 {
		return nil
	}
	seen := make(map[HostService]bool, len(xs))
	out := make([]HostService, 0, len(xs))
	for _, x := range xs {
		if seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}

// dedupeRules removes exact duplicate rules, keeping the first occurrence. Rule
// carries *[]string fields (Paths/Methods), so equality goes through
// reflect.DeepEqual — which follows the pointers and treats a nil Paths as distinct
// from an empty (&[]string{}) one, preserving the omitted-vs-empty meaning the
// passthrough validation depends on. The lists are short (hand-authored allow/deny),
// so the O(n^2) scan is fine and avoids inventing a fragile string key.
func dedupeRules(rules []Rule) []Rule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]Rule, 0, len(rules))
	for _, r := range rules {
		dup := false
		for _, kept := range out {
			if reflect.DeepEqual(r, kept) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, r)
		}
	}
	return out
}

// mergeEnv overlays over onto a copy of base, with over winning on key collisions. It
// returns nil when the result is empty so equal-but-differently-typed empties (nil vs
// empty map) do not break reflect.DeepEqual comparisons.
func mergeEnv(base, over map[string]string) map[string]string {
	if len(base) == 0 && len(over) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}
