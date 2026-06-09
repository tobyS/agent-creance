// White-box tests (package config) so we can assert the defaults pass and the
// host_services parsing, which are internal behaviours. Loader/round-trip lives here;
// rendered error messages are golden-tested in validate_test.go.
package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParse_ExampleRoundTrips(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "example.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	cfg, err := Parse(data)
	if err != nil {
		// Strict decoding means a parse with no error is itself the proof that every
		// meaningful field maps to a struct field (no silent loss).
		t.Fatalf("Parse(example.yaml) returned error: %v", err)
	}

	wantCmd := []string{"claude", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(cfg.Agent.Command, wantCmd) {
		t.Errorf("Agent.Command = %v, want %v", cfg.Agent.Command, wantCmd)
	}
	if cfg.Agent.Workdir != "." {
		t.Errorf("Agent.Workdir = %q, want %q", cfg.Agent.Workdir, ".")
	}

	if !reflect.DeepEqual(cfg.Safehouse.AddDirsRW, []string{"."}) {
		t.Errorf("Safehouse.AddDirsRW = %v", cfg.Safehouse.AddDirsRW)
	}
	if !reflect.DeepEqual(cfg.Safehouse.AddDirsRO, []string{"~/.config/git"}) {
		t.Errorf("Safehouse.AddDirsRO = %v", cfg.Safehouse.AddDirsRO)
	}
	if !reflect.DeepEqual(cfg.Safehouse.Enable, []string{"shell-init"}) {
		t.Errorf("Safehouse.Enable = %v", cfg.Safehouse.Enable)
	}

	if !reflect.DeepEqual(cfg.Include, []string{".agent-creance/team-shared.yaml"}) {
		t.Errorf("Include = %v", cfg.Include)
	}

	wantHS := []HostService{{"mysql", 3306}, {"redis", 6379}, {"mailpit", 1025}}
	if !reflect.DeepEqual(cfg.Network.HostServices, wantHS) {
		t.Errorf("Network.HostServices = %v, want %v", cfg.Network.HostServices, wantHS)
	}

	if !reflect.DeepEqual(cfg.Network.Egress.Generators, []Generator{{Type: "package_json"}, {Type: "composer_json"}}) {
		t.Errorf("Egress.Generators = %v", cfg.Network.Egress.Generators)
	}

	allow := cfg.Network.Egress.Allow
	if len(allow) != 1 {
		t.Fatalf("len(Allow) = %d, want 1", len(allow))
	}
	if allow[0].Host != "api.github.com" {
		t.Errorf("Allow[0].Host = %q", allow[0].Host)
	}
	if got := derefSlice(allow[0].Paths); !reflect.DeepEqual(got, []string{"/repos/tobyS/this-project/"}) {
		t.Errorf("Allow[0].Paths = %v", got)
	}
	if got := derefSlice(allow[0].Methods); !reflect.DeepEqual(got, []string{"GET", "POST"}) {
		t.Errorf("Allow[0].Methods = %v", got)
	}
	if allow[0].Mode != ModeIntercept {
		t.Errorf("Allow[0].Mode = %q, want default %q", allow[0].Mode, ModeIntercept)
	}

	deny := cfg.Network.Egress.DenyAlways
	if len(deny) != 3 {
		t.Fatalf("len(DenyAlways) = %d, want 3", len(deny))
	}
	if deny[0].Host != "w3schools.com" || deny[0].Reason == "" {
		t.Errorf("DenyAlways[0] = %+v", deny[0])
	}
	if deny[1].Host != "*.medium.com" {
		t.Errorf("DenyAlways[1].Host = %q", deny[1].Host)
	}
	if got := derefSlice(deny[1].Paths); !reflect.DeepEqual(got, []string{"/@*"}) {
		t.Errorf("DenyAlways[1].Paths = %v", got)
	}
	if deny[2].Host != "*" {
		t.Errorf("DenyAlways[2].Host = %q", deny[2].Host)
	}
	if got := derefSlice(deny[2].Paths); !reflect.DeepEqual(got, []string{"**/.env", "**/.git/config"}) {
		t.Errorf("DenyAlways[2].Paths = %v", got)
	}

	if cfg.Env["GIT_AUTHOR_NAME"] != "Toby (caged)" {
		t.Errorf("Env[GIT_AUTHOR_NAME] = %q", cfg.Env["GIT_AUTHOR_NAME"])
	}
}

func TestParse_EmptyDocument(t *testing.T) {
	for _, in := range []string{"", "   \n", "# just a comment\n"} {
		cfg, err := Parse([]byte(in))
		if err != nil {
			t.Errorf("Parse(%q) error = %v, want nil", in, err)
			continue
		}
		if !reflect.DeepEqual(*cfg, Config{}) {
			t.Errorf("Parse(%q) = %+v, want zero Config", in, *cfg)
		}
	}
}

func TestParse_ModeDefaultAndPassthrough(t *testing.T) {
	cfg, err := Parse([]byte(`
network:
  egress:
    allow:
      - host: react.dev
      - host: api.anthropic.com
        mode: passthrough
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	allow := cfg.Network.Egress.Allow
	if allow[0].Mode != ModeIntercept {
		t.Errorf("Allow[0].Mode = %q, want default %q", allow[0].Mode, ModeIntercept)
	}
	if allow[0].Paths != nil || allow[0].Methods != nil {
		t.Errorf("omitted paths/methods should be nil, got %v / %v", allow[0].Paths, allow[0].Methods)
	}
	if allow[1].Mode != ModePassthrough {
		t.Errorf("Allow[1].Mode = %q, want %q", allow[1].Mode, ModePassthrough)
	}
}

func derefSlice(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}
