package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse_Generators(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want []Generator
	}{
		{
			name: "bare strings",
			yaml: "network:\n  egress:\n    generators:\n      - package_json\n      - composer_json\n",
			want: []Generator{{Type: "package_json"}, {Type: "composer_json"}},
		},
		{
			name: "object form with path",
			yaml: "network:\n  egress:\n    generators:\n" +
				"      - type: package_json\n        path: apps/web/package.json\n",
			want: []Generator{{Type: "package_json", Path: "apps/web/package.json"}},
		},
		{
			name: "object form without path",
			yaml: "network:\n  egress:\n    generators:\n      - type: composer_json\n",
			want: []Generator{{Type: "composer_json"}},
		},
		{
			name: "mixed bare and object forms",
			yaml: "network:\n  egress:\n    generators:\n" +
				"      - package_json\n" +
				"      - type: package_json\n        path: apps/api/package.json\n",
			want: []Generator{
				{Type: "package_json"},
				{Type: "package_json", Path: "apps/api/package.json"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(cfg.Network.Egress.Generators, tc.want) {
				t.Errorf("Generators = %#v, want %#v", cfg.Network.Egress.Generators, tc.want)
			}
		})
	}
}

func TestParse_GeneratorErrors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string // substring expected in the error
	}{
		{
			name: "unknown key in object form",
			yaml: "network:\n  egress:\n    generators:\n      - type: package_json\n        manifest: x\n",
			want: `unknown key "manifest"`,
		},
		{
			name: "missing type",
			yaml: "network:\n  egress:\n    generators:\n      - path: apps/web/package.json\n",
			want: `missing "type"`,
		},
		{
			name: "sequence (not name or mapping)",
			yaml: "network:\n  egress:\n    generators:\n      - [package_json]\n",
			want: "must be a name or a {type, path} mapping",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// TestParse_StrictDecodeWithObjectGenerators guards the yaml.Node-capture assumption:
// capturing generators as raw nodes must not disable KnownFields strictness for the
// rest of the document — an unknown top-level key is still rejected.
func TestParse_StrictDecodeWithObjectGenerators(t *testing.T) {
	yaml := "bogus_top_level: true\n" +
		"network:\n  egress:\n    generators:\n" +
		"      - type: package_json\n        path: apps/web/package.json\n"
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected strict-decode error for unknown top-level key")
	}
	if !strings.Contains(err.Error(), "bogus_top_level") {
		t.Errorf("error = %q, want it to mention the unknown key", err.Error())
	}
}
