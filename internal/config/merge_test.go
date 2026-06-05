package config

import (
	"reflect"
	"testing"
)

func strs(xs ...string) *[]string { return &xs }

func TestMerge_ScalarOverride(t *testing.T) {
	base := Config{Agent: Agent{Workdir: "/base"}}
	over := Config{Agent: Agent{Workdir: "/over"}}
	if got := merge(base, over).Agent.Workdir; got != "/over" {
		t.Errorf("Workdir = %q, want /over (over wins)", got)
	}
	// An omitted scalar in over keeps base's value.
	if got := merge(base, Config{}).Agent.Workdir; got != "/base" {
		t.Errorf("Workdir = %q, want /base (over omits, base kept)", got)
	}
}

func TestMerge_CommandReplacesNotConcatenated(t *testing.T) {
	base := Config{Agent: Agent{Command: []string{"claude"}}}
	over := Config{Agent: Agent{Command: []string{"claude", "--print"}}}
	got := merge(base, over).Agent.Command
	want := []string{"claude", "--print"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Command = %v, want %v (replace, not concat)", got, want)
	}
	// Omitted command in over inherits base's.
	if got := merge(base, Config{}).Agent.Command; !reflect.DeepEqual(got, []string{"claude"}) {
		t.Errorf("Command = %v, want [claude] (inherited)", got)
	}
}

func TestMerge_StringListUnionDedupe(t *testing.T) {
	base := Config{Safehouse: Safehouse{
		AddDirsRW: []string{".", "~/shared"},
		Enable:    []string{"shell-init"},
	}}
	over := Config{Safehouse: Safehouse{
		AddDirsRW: []string{"~/shared", "/extra"}, // ~/shared duplicates base
		Enable:    []string{"git"},
	}}
	got := merge(base, over).Safehouse
	wantRW := []string{".", "~/shared", "/extra"} // first occurrence kept, order stable
	if !reflect.DeepEqual(got.AddDirsRW, wantRW) {
		t.Errorf("AddDirsRW = %v, want %v", got.AddDirsRW, wantRW)
	}
	wantEnable := []string{"shell-init", "git"}
	if !reflect.DeepEqual(got.Enable, wantEnable) {
		t.Errorf("Enable = %v, want %v", got.Enable, wantEnable)
	}
}

func TestMerge_GeneratorsAndHostServicesUnionDedupe(t *testing.T) {
	base := Config{Network: Network{
		HostServices: []HostService{{"mysql", 3306}},
		Egress:       Egress{Generators: []string{"package_json"}},
	}}
	over := Config{Network: Network{
		HostServices: []HostService{{"mysql", 3306}, {"redis", 6379}}, // mysql dup
		Egress:       Egress{Generators: []string{"package_json", "composer_json"}},
	}}
	got := merge(base, over).Network
	wantHS := []HostService{{"mysql", 3306}, {"redis", 6379}}
	if !reflect.DeepEqual(got.HostServices, wantHS) {
		t.Errorf("HostServices = %v, want %v", got.HostServices, wantHS)
	}
	wantGen := []string{"package_json", "composer_json"}
	if !reflect.DeepEqual(got.Egress.Generators, wantGen) {
		t.Errorf("Generators = %v, want %v", got.Egress.Generators, wantGen)
	}
}

func TestMerge_RuleUnionDedupe(t *testing.T) {
	r := func(host string, paths *[]string) Rule {
		return Rule{Host: host, Paths: paths, Mode: ModeIntercept}
	}
	base := Config{Network: Network{Egress: Egress{
		Allow: []Rule{r("api.github.com", strs("/x"))},
	}}}
	over := Config{Network: Network{Egress: Egress{
		Allow: []Rule{
			r("api.github.com", strs("/x")), // exact dup → collapses
			r("react.dev", nil),
		},
	}}}
	got := merge(base, over).Network.Egress.Allow
	want := []Rule{r("api.github.com", strs("/x")), r("react.dev", nil)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Allow = %+v, want %+v", got, want)
	}
}

func TestMerge_RuleDedupeIsPointerAware(t *testing.T) {
	// nil Paths (omitted) must NOT collapse with an empty-but-present Paths: the
	// passthrough validation depends on the distinction, so merge must preserve it.
	nilPaths := Rule{Host: "react.dev", Paths: nil, Mode: ModeIntercept}
	emptyPaths := Rule{Host: "react.dev", Paths: strs(), Mode: ModeIntercept}
	base := Config{Network: Network{Egress: Egress{Allow: []Rule{nilPaths}}}}
	over := Config{Network: Network{Egress: Egress{Allow: []Rule{emptyPaths}}}}
	got := merge(base, over).Network.Egress.Allow
	if len(got) != 2 {
		t.Fatalf("len(Allow) = %d, want 2 (nil and empty Paths are distinct)", len(got))
	}
}

func TestMerge_EnvKeyWiseOverWins(t *testing.T) {
	base := Config{Env: map[string]string{"A": "1", "B": "base"}}
	over := Config{Env: map[string]string{"B": "over", "C": "3"}}
	got := merge(base, over).Env
	want := map[string]string{"A": "1", "B": "over", "C": "3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Env = %v, want %v", got, want)
	}
	// Both empty → nil (not an empty map), for DeepEqual stability.
	if got := merge(Config{}, Config{}).Env; got != nil {
		t.Errorf("Env = %v, want nil", got)
	}
}

func TestMerge_Deterministic(t *testing.T) {
	base := Config{
		Safehouse: Safehouse{AddDirsRW: []string{".", "x"}},
		Network: Network{Egress: Egress{
			Allow: []Rule{{Host: "a.com", Mode: ModeIntercept}},
		}},
		Env: map[string]string{"A": "1"},
	}
	over := Config{
		Safehouse: Safehouse{AddDirsRW: []string{"x", "y"}},
		Network: Network{Egress: Egress{
			Allow: []Rule{{Host: "b.com", Mode: ModeIntercept}},
		}},
		Env: map[string]string{"B": "2"},
	}
	first := merge(base, over)
	second := merge(base, over)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("merge not deterministic:\n first=%+v\nsecond=%+v", first, second)
	}
}
