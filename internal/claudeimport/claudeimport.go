// Package claudeimport reads a project's (or the user's) Claude Code
// configuration and extracts the pieces agent-creance can seed into an egress
// allowlist: allowed web domains and MCP server hosts. It is deliberately pure —
// it reads files through a sysdep.FileSystem and never touches the OS directly —
// so init/setup can call it and tests can drive it with the in-memory fake.
//
// What it collects (see Result):
//   - Web domains from settings permissions (WebFetch(domain:…)) and the Bash
//     sandbox network allowlist (sandbox.network.allowedDomains) → GET-only
//     intercept rules.
//   - Remote MCP servers (those with an https url) → passthrough rules; MCP
//     traffic is JSON-RPC over POST and may upgrade to SSE, and the bearer token
//     it carries is best left undecrypted, so the proxy passes it through.
//   - MCP servers whose url points at localhost → a host_services port, not
//     egress. Local stdio MCP servers (no url) are ignored: they speak over the
//     subprocess's stdio and have neither host nor port.
//
// Claude Code's JSON is third-party config, so it is decoded leniently (only the
// fields we need; unknown keys ignored), unlike agent-creance's own strict YAML.
package claudeimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// Result is the set of config entries extracted from Claude Code config. The web
// and MCP rules are kept separate because init offers them as independent import
// steps. Ports come from MCP servers bound to localhost.
type Result struct {
	WebRules []config.Rule        // GET-only intercept (WebFetch + sandbox domains)
	MCPRules []config.Rule        // passthrough (remote MCP hosts)
	Ports    []config.HostService // localhost MCP servers
}

// Empty reports whether nothing was collected.
func (r Result) Empty() bool {
	return len(r.WebRules) == 0 && len(r.MCPRules) == 0 && len(r.Ports) == 0
}

const (
	webReason = "imported from Claude Code settings"
	localhost = "localhost"
)

// Project collects entries from a project's Claude Code config: .claude/settings
// .json, .claude/settings.local.json, .mcp.json, and the per-project MCP block of
// ~/.claude.json (keyed on the project's absolute path). Missing files are not an
// error; a present-but-unreadable or malformed file becomes a warning and is
// skipped. The second return is the list of human-readable warnings.
func Project(fsys sysdep.FileSystem, paths sysdep.PathResolver, projectDir string) (Result, []string) {
	var warns []string

	domains := newOrderedSet()
	for _, name := range []string{"settings.json", "settings.local.json"} {
		path := filepath.Join(projectDir, ".claude", name)
		readDomains(fsys, path, domains, &warns)
	}

	// MCP: project .mcp.json (project scope), then the local-scope per-project
	// block of ~/.claude.json overrides by server name (Local > Project).
	servers := map[string]mcpServer{}
	mergeServers(servers, readMCPFile(fsys, filepath.Join(projectDir, ".mcp.json"), &warns))
	if home, err := paths.UserHomeDir(); err == nil {
		abs := projectDir
		if a, err := paths.Abs(projectDir); err == nil {
			abs = a
		}
		mergeServers(servers, readClaudeJSONProject(fsys, filepath.Join(home, ".claude.json"), abs, &warns))
	}

	return assemble(domains, servers, paths), warns
}

// Global collects entries from the user's Claude Code config:
// ~/.claude/settings.json, ~/.claude/settings.local.json, and the user-scope
// (top-level) mcpServers of ~/.claude.json.
func Global(fsys sysdep.FileSystem, paths sysdep.PathResolver) (Result, []string) {
	var warns []string
	home, err := paths.UserHomeDir()
	if err != nil {
		return Result{}, []string{fmt.Sprintf("resolve home dir: %v", err)}
	}

	domains := newOrderedSet()
	for _, name := range []string{"settings.json", "settings.local.json"} {
		readDomains(fsys, filepath.Join(home, ".claude", name), domains, &warns)
	}

	servers := map[string]mcpServer{}
	mergeServers(servers, readClaudeJSONUser(fsys, filepath.Join(home, ".claude.json"), &warns))

	return assemble(domains, servers, paths), warns
}

// assemble turns the collected domains and MCP servers into a Result, classifying
// each MCP server as a remote passthrough rule or a localhost port.
func assemble(domains *orderedSet, servers map[string]mcpServer, paths sysdep.PathResolver) Result {
	var res Result

	for _, host := range domains.values() {
		res.WebRules = append(res.WebRules, config.Rule{
			Host:    host,
			Methods: &[]string{"GET"},
			Mode:    config.ModeIntercept,
			Reason:  webReason,
		})
	}

	mcpHosts := newOrderedSet()
	portByPort := map[int]config.HostService{}
	var portOrder []int
	for _, name := range sortedKeys(servers) {
		srv := servers[name]
		if srv.URL == "" {
			continue // stdio server: no host, no port
		}
		raw, ok := expandEnv(srv.URL, paths.Getenv)
		if !ok {
			continue // unresolved ${VAR}: cannot determine host
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if isLocalhost(host) {
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				continue // a localhost MCP without an explicit port has nothing to map
			}
			if _, seen := portByPort[port]; !seen {
				portByPort[port] = config.HostService{Label: name, Port: port}
				portOrder = append(portOrder, port)
			}
			continue
		}
		if mcpHosts.add(host) {
			res.MCPRules = append(res.MCPRules, config.Rule{
				Host:   host,
				Mode:   config.ModePassthrough,
				Reason: fmt.Sprintf("imported from Claude Code MCP server %q", name),
			})
		}
	}

	sort.Ints(portOrder)
	for _, p := range portOrder {
		res.Ports = append(res.Ports, portByPort[p])
	}
	return res
}

// --- settings (web domains) ---

type settingsFile struct {
	Permissions struct {
		Allow []string `json:"allow"`
	} `json:"permissions"`
	Sandbox struct {
		Network struct {
			AllowedDomains []string `json:"allowedDomains"`
		} `json:"network"`
	} `json:"sandbox"`
}

// readDomains parses one settings file (if present) and adds its allowed web
// hosts to dst. WebFetch(domain:*) is skipped — a "*" host would allow all
// egress and defeat the cage.
func readDomains(fsys sysdep.FileSystem, path string, dst *orderedSet, warns *[]string) {
	var sf settingsFile
	if !readJSON(fsys, path, &sf, warns) {
		return
	}
	for _, rule := range sf.Permissions.Allow {
		if host, ok := webFetchDomain(rule); ok {
			dst.add(host)
		}
	}
	for _, d := range sf.Sandbox.Network.AllowedDomains {
		if host := normalizeHost(d); host != "" && host != "*" {
			dst.add(host)
		}
	}
}

// webFetchDomain extracts the host glob from a WebFetch(domain:HOST) permission
// rule. It returns ok=false for any other rule, a bare "*" host, or an empty
// host. The host glob (e.g. "*.example.com") is returned verbatim — the policy
// matcher already understands "*" and "*.suffix".
func webFetchDomain(rule string) (string, bool) {
	const prefix = "WebFetch(domain:"
	rule = strings.TrimSpace(rule)
	if !strings.HasPrefix(rule, prefix) || !strings.HasSuffix(rule, ")") {
		return "", false
	}
	host := normalizeHost(rule[len(prefix) : len(rule)-1])
	if host == "" || host == "*" {
		return "", false
	}
	return host, true
}

// normalizeHost lowercases a host pattern and strips a trailing dot, mirroring
// Claude Code's WebFetch matching ("example.com." == "example.com").
func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// --- MCP servers ---

type mcpServer struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Command string `json:"command"`
}

type mcpFile struct {
	MCPServers map[string]mcpServer `json:"mcpServers"`
}

type claudeJSON struct {
	MCPServers map[string]mcpServer `json:"mcpServers"`
	Projects   map[string]struct {
		MCPServers map[string]mcpServer `json:"mcpServers"`
	} `json:"projects"`
}

func readMCPFile(fsys sysdep.FileSystem, path string, warns *[]string) map[string]mcpServer {
	var mf mcpFile
	if !readJSON(fsys, path, &mf, warns) {
		return nil
	}
	return mf.MCPServers
}

func readClaudeJSONUser(fsys sysdep.FileSystem, path string, warns *[]string) map[string]mcpServer {
	var cj claudeJSON
	if !readJSON(fsys, path, &cj, warns) {
		return nil
	}
	return cj.MCPServers
}

func readClaudeJSONProject(fsys sysdep.FileSystem, path, absProjectDir string, warns *[]string) map[string]mcpServer {
	var cj claudeJSON
	if !readJSON(fsys, path, &cj, warns) {
		return nil
	}
	if p, ok := cj.Projects[absProjectDir]; ok {
		return p.MCPServers
	}
	return nil
}

// mergeServers copies src into dst, overriding by name (callers apply lower
// precedence first, then higher).
func mergeServers(dst, src map[string]mcpServer) {
	for name, srv := range src {
		dst[name] = srv
	}
}

// --- shared helpers ---

// readJSON reads and leniently decodes a JSON file into v. It returns false (and
// adds no warning) when the file is absent; a read or parse failure on a present
// file is recorded as a warning and also returns false.
func readJSON(fsys sysdep.FileSystem, path string, v any, warns *[]string) bool {
	data, err := fsys.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			*warns = append(*warns, fmt.Sprintf("read %s: %v", path, err))
		}
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		*warns = append(*warns, fmt.Sprintf("parse %s: %v", path, err))
		return false
	}
	return true
}

func isLocalhost(host string) bool {
	switch host {
	case localhost, "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// expandEnv resolves ${VAR} and ${VAR:-default} references using getenv. It
// returns ok=false if any referenced variable is unset and has no default, since
// the resulting string would not be a usable URL.
func expandEnv(s string, getenv func(string) string) (string, bool) {
	ok := true
	out := expandRefs(s, func(name, def string, hasDef bool) string {
		if v := getenv(name); v != "" {
			return v
		}
		if hasDef {
			return def
		}
		ok = false
		return ""
	})
	return out, ok
}

// expandRefs walks s replacing each ${...} reference via repl(name, default,
// hasDefault). A "$" not followed by "{" is left untouched.
func expandRefs(s string, repl func(name, def string, hasDef bool) string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '$' && i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end >= 0 {
				inner := s[i+2 : i+2+end]
				name, def, hasDef := inner, "", false
				if j := strings.Index(inner, ":-"); j >= 0 {
					name, def, hasDef = inner[:j], inner[j+2:], true
				}
				b.WriteString(repl(name, def, hasDef))
				i += 2 + end + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func sortedKeys(m map[string]mcpServer) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// orderedSet is a string set that yields its members sorted, used to dedupe hosts
// deterministically.
type orderedSet struct{ m map[string]bool }

func newOrderedSet() *orderedSet { return &orderedSet{m: map[string]bool{}} }

// add inserts host and reports whether it was newly added.
func (s *orderedSet) add(host string) bool {
	if host == "" || s.m[host] {
		return false
	}
	s.m[host] = true
	return true
}

func (s *orderedSet) values() []string {
	out := make([]string, 0, len(s.m))
	for k := range s.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
