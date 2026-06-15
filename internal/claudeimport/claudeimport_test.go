package claudeimport

import (
	"io/fs"
	"reflect"
	"testing"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const (
	home    = "/home/dev"
	project = "/home/dev/proj"
)

func newFS() *sysdeptest.FakeFileSystem { return sysdeptest.NewFakeFileSystem() }

func newPaths() *sysdeptest.FakePathResolver {
	p := sysdeptest.NewFakePathResolver()
	p.HomeDir = home
	p.Cwd = project
	return p
}

func getHosts(rules []config.Rule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Host)
	}
	return out
}

func TestProjectWebDomains(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]string // filename under .claude → contents
		want     []string
	}{
		{
			name: "webfetch domains",
			settings: map[string]string{
				"settings.json": `{"permissions":{"allow":[
					"WebFetch(domain:example.com)",
					"WebFetch(domain:*.docs.io)",
					"Bash(npm run lint)"
				]}}`,
			},
			want: []string{"*.docs.io", "example.com"},
		},
		{
			name: "bare star skipped",
			settings: map[string]string{
				"settings.json": `{"permissions":{"allow":["WebFetch(domain:*)","WebFetch(domain:keep.me)"]}}`,
			},
			want: []string{"keep.me"},
		},
		{
			name: "sandbox domains unioned and trailing dot stripped",
			settings: map[string]string{
				"settings.json": `{"permissions":{"allow":["WebFetch(domain:a.com)"]},"sandbox":{"network":{"allowedDomains":["b.com.","A.com"]}}}`,
			},
			want: []string{"a.com", "b.com"},
		},
		{
			name: "local overlay unions with shared",
			settings: map[string]string{
				"settings.json":       `{"permissions":{"allow":["WebFetch(domain:shared.com)"]}}`,
				"settings.local.json": `{"permissions":{"allow":["WebFetch(domain:local.com)"]}}`,
			},
			want: []string{"local.com", "shared.com"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := newFS()
			for name, body := range tc.settings {
				fsys.Files[project+"/.claude/"+name] = []byte(body)
			}
			res, warns := Project(fsys, newPaths(), project)
			if len(warns) != 0 {
				t.Fatalf("unexpected warnings: %v", warns)
			}
			if got := getHosts(res.WebRules); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hosts = %v, want %v", got, tc.want)
			}
			for _, r := range res.WebRules {
				if r.Mode != config.ModeIntercept {
					t.Errorf("web rule %s mode = %q, want intercept", r.Host, r.Mode)
				}
				if r.Methods == nil || !reflect.DeepEqual(*r.Methods, []string{"GET"}) {
					t.Errorf("web rule %s methods = %v, want [GET]", r.Host, r.Methods)
				}
			}
		})
	}
}

func TestProjectMCP(t *testing.T) {
	fsys := newFS()
	fsys.Files[project+"/.mcp.json"] = []byte(`{"mcpServers":{
		"remote":   {"type":"http","url":"https://mcp.example.com/mcp"},
		"sse":      {"type":"sse","url":"https://sse.example.com/sse"},
		"stdio":    {"type":"stdio","command":"/usr/bin/server"},
		"local":    {"type":"http","url":"http://localhost:8181/mcp"},
		"loopback": {"url":"http://127.0.0.1:9000"}
	}}`)

	res, warns := Project(fsys, newPaths(), project)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	wantRemote := []string{"mcp.example.com", "sse.example.com"}
	if got := getHosts(res.MCPRules); !reflect.DeepEqual(got, wantRemote) {
		t.Fatalf("remote MCP hosts = %v, want %v", got, wantRemote)
	}
	for _, r := range res.MCPRules {
		if r.Mode != config.ModePassthrough {
			t.Errorf("MCP rule %s mode = %q, want passthrough", r.Host, r.Mode)
		}
		if r.Methods != nil || r.Paths != nil {
			t.Errorf("MCP rule %s must not carry methods/paths (passthrough)", r.Host)
		}
	}

	wantPorts := []config.HostService{{Label: "local", Port: 8181}, {Label: "loopback", Port: 9000}}
	if !reflect.DeepEqual(res.Ports, wantPorts) {
		t.Fatalf("ports = %v, want %v", res.Ports, wantPorts)
	}
}

func TestProjectMCPLocalOverridesProject(t *testing.T) {
	fsys := newFS()
	fsys.Files[project+"/.mcp.json"] = []byte(`{"mcpServers":{"svc":{"type":"http","url":"https://shared.example.com"}}}`)
	fsys.Files[home+"/.claude.json"] = []byte(`{"projects":{"` + project + `":{"mcpServers":{"svc":{"type":"http","url":"https://local.example.com"}}}}}`)

	res, _ := Project(fsys, newPaths(), project)
	want := []string{"local.example.com"}
	if got := getHosts(res.MCPRules); !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts = %v, want %v (local scope must override project)", got, want)
	}
}

func TestProjectMCPEnvExpansion(t *testing.T) {
	fsys := newFS()
	fsys.Files[project+"/.mcp.json"] = []byte(`{"mcpServers":{
		"resolved":   {"type":"http","url":"https://${MCP_HOST}/mcp"},
		"defaulted":  {"type":"http","url":"https://${UNSET:-fallback.example.com}/mcp"},
		"unresolved": {"type":"http","url":"https://${MISSING}/mcp"}
	}}`)
	paths := newPaths()
	paths.Env["MCP_HOST"] = "resolved.example.com"

	res, _ := Project(fsys, paths, project)
	want := []string{"fallback.example.com", "resolved.example.com"}
	if got := getHosts(res.MCPRules); !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts = %v, want %v (unresolved var must be skipped)", got, want)
	}
}

func TestProjectMissingFilesNoWarn(t *testing.T) {
	res, warns := Project(newFS(), newPaths(), project)
	if !res.Empty() {
		t.Fatalf("expected empty result, got %+v", res)
	}
	if len(warns) != 0 {
		t.Fatalf("absent files must not warn, got %v", warns)
	}
}

func TestProjectMalformedJSONWarns(t *testing.T) {
	fsys := newFS()
	fsys.Files[project+"/.claude/settings.json"] = []byte(`{not json`)
	_, warns := Project(fsys, newPaths(), project)
	if len(warns) != 1 {
		t.Fatalf("expected 1 warning, got %v", warns)
	}
}

func TestProjectUnreadableFileWarns(t *testing.T) {
	fsys := newFS()
	fsys.Errs[project+"/.claude/settings.json"] = fs.ErrPermission
	_, warns := Project(fsys, newPaths(), project)
	if len(warns) != 1 {
		t.Fatalf("expected 1 warning for unreadable file, got %v", warns)
	}
}

func TestGlobalScope(t *testing.T) {
	fsys := newFS()
	fsys.Files[home+"/.claude/settings.json"] = []byte(`{"permissions":{"allow":["WebFetch(domain:global.com)"]}}`)
	fsys.Files[home+"/.claude.json"] = []byte(`{"mcpServers":{"u":{"type":"http","url":"https://user.mcp.com"}}}`)

	res, warns := Global(fsys, newPaths())
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if got := getHosts(res.WebRules); !reflect.DeepEqual(got, []string{"global.com"}) {
		t.Fatalf("web hosts = %v", got)
	}
	if got := getHosts(res.MCPRules); !reflect.DeepEqual(got, []string{"user.mcp.com"}) {
		t.Fatalf("mcp hosts = %v", got)
	}
}
