package profile

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/config"
)

// update regenerates the golden artifact: `go test ./... -update` (make golden).
var update = flag.Bool("update", false, "regenerate golden files")

// forbiddenLiterals are tokens the generated SBPL must never contain: the literal IPs
// do not compile (S3), and a bare `*:` host token would permit external egress (S3 §3).
var forbiddenLiterals = []string{"127.0.0.1", "::1", `"*:`}

func assertNoForbiddenLiterals(t *testing.T, out string) {
	t.Helper()
	if !strings.Contains(out, "localhost:") {
		t.Errorf("output is missing the required localhost: host token:\n%s", out)
	}
	for _, bad := range forbiddenLiterals {
		if strings.Contains(out, bad) {
			t.Errorf("output contains forbidden literal %q:\n%s", bad, out)
		}
	}
}

func TestRenderNetworkSB_Golden(t *testing.T) {
	got := RenderNetworkSB([]config.HostService{
		{Label: "mysql", Port: 3306},
		{Label: "redis", Port: 6379},
	})

	golden := filepath.Join("testdata", "network.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("network.sb mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderNetworkSB_Empty(t *testing.T) {
	got := RenderNetworkSB(nil)
	if !strings.Contains(got, DenyBaseline) {
		t.Errorf("empty render is missing the deny baseline:\n%s", got)
	}
	if strings.Contains(got, "(allow") {
		t.Errorf("empty render must emit no allow rules:\n%s", got)
	}
}

func TestRenderNetworkSB_DedupeByPort(t *testing.T) {
	got := RenderNetworkSB([]config.HostService{
		{Label: "mysql", Port: 3306},
		{Label: "mysql-alias", Port: 3306},
	})
	if n := strings.Count(got, "(allow"); n != 1 {
		t.Errorf("duplicate ports should collapse to one allow rule, got %d:\n%s", n, got)
	}
	// First label wins for the comment.
	if !strings.Contains(got, ";; mysql\n") {
		t.Errorf("expected first label to win the comment:\n%s", got)
	}
}

func TestRenderNetworkSB_NoForbiddenLiterals(t *testing.T) {
	got := RenderNetworkSB([]config.HostService{{Label: "mysql", Port: 3306}})
	assertNoForbiddenLiterals(t, got)
}

// TestRenderNetworkSB_LabelInjection feeds hostile labels (control chars and SBPL
// metacharacters) directly to the renderer, bypassing config validation, and asserts
// the label can never produce a line that is not its own trailing comment. This is the
// render-side defense behind AC-0058 / F1: each service must still emit exactly one
// allow line, and no injected (allow|deny) form may appear after the deny baseline.
func TestRenderNetworkSB_LabelInjection(t *testing.T) {
	hostile := []string{
		"x\n(allow network*)",
		"x\r(allow network*)",
		"a\tb",
		"x\x00y",
		`weird " \ ;; ( ) label`,
	}
	for _, label := range hostile {
		got := RenderNetworkSB([]config.HostService{{Label: label, Port: 3306}})

		// No control characters survive into the rendered fragment (newlines separate
		// lines and are produced only by the renderer itself, never by a label).
		for _, r := range got {
			if (r < 0x20 && r != '\n') || r == 0x7f {
				t.Errorf("label %q left a control character %q in the output:\n%s", label, r, got)
			}
		}
		// Every line is a comment, the deny baseline, or a single localhost allow line —
		// the label can only ever extend its own trailing comment, never start a new SBPL
		// form. (A substring like "(allow network*)" inside a ;; comment is inert.)
		allowLines := 0
		for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
			trimmed := strings.TrimLeft(line, " ")
			switch {
			case strings.HasPrefix(trimmed, ";;"):
			case trimmed == DenyBaseline:
			case strings.HasPrefix(trimmed, `(allow network-outbound (remote tcp "localhost:`):
				allowLines++
			default:
				t.Errorf("label %q produced an unexpected line %q:\n%s", label, line, got)
			}
		}
		if allowLines != 1 {
			t.Errorf("label %q produced %d allow lines, want 1:\n%s", label, allowLines, got)
		}
	}
}

// TestPathRenderers_QuoteEscaping pins the %q escaping on the path-bearing renderers so
// a refactor to %s (which would re-open the SBPL-injection class for paths) fails the
// build (AC-0058 / F15).
func TestPathRenderers_QuoteEscaping(t *testing.T) {
	cfg, err := RenderConfigReadOnlyFragment([]string{`/tmp/a"b`})
	if err != nil {
		t.Fatalf("RenderConfigReadOnlyFragment: %v", err)
	}
	if !strings.Contains(cfg, `(literal "/tmp/a\"b")`) {
		t.Errorf("config-ro fragment must %%q-escape the embedded quote:\n%s", cfg)
	}

	ca, err := RenderCAReadFragment(`/tmp/c"a/cert.pem`)
	if err != nil {
		t.Fatalf("RenderCAReadFragment: %v", err)
	}
	if !strings.Contains(ca, `\"`) {
		t.Errorf("ca fragment must %%q-escape the embedded quote:\n%s", ca)
	}
}

func TestRenderProxyFragment(t *testing.T) {
	tests := []struct {
		name    string
		port    int
		wantErr bool
	}{
		{"valid", 18081, false},
		{"min", 1, false},
		{"max", 65535, false},
		{"zero", 0, true},
		{"negative", -1, true},
		{"too high", 65536, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderProxyFragment(tt.port)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for port %d, got none (%q)", tt.port, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RenderProxyFragment(%d): %v", tt.port, err)
			}
			want := allowRule(tt.port)
			if n := strings.Count(got, "(allow"); n != 1 {
				t.Errorf("expected exactly one allow line, got %d:\n%s", n, got)
			}
			if !strings.Contains(got, want) {
				t.Errorf("output missing %q:\n%s", want, got)
			}
			// The proxy fragment must NOT restate the deny baseline as a rule line
			// (ordering contract); it may appear inside a header comment.
			for _, line := range strings.Split(got, "\n") {
				if strings.TrimSpace(line) == DenyBaseline {
					t.Errorf("proxy fragment must not restate the deny baseline:\n%s", got)
				}
			}
		})
	}
}

func TestRenderProxyFragment_NoForbiddenLiterals(t *testing.T) {
	got, err := RenderProxyFragment(18081)
	if err != nil {
		t.Fatalf("RenderProxyFragment: %v", err)
	}
	assertNoForbiddenLiterals(t, got)
}

func TestRenderCAReadFragment_Golden(t *testing.T) {
	// A fixed, host-independent path keeps the golden stable.
	got, err := RenderCAReadFragment("/home/test/.mitmproxy/mitmproxy-ca-cert.pem")
	if err != nil {
		t.Fatalf("RenderCAReadFragment: %v", err)
	}

	golden := filepath.Join("testdata", "ca.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("ca.sb mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderCAReadFragment_GrantsOnlyTheOnePEM(t *testing.T) {
	got, err := RenderCAReadFragment("/home/test/.mitmproxy/mitmproxy-ca-cert.pem")
	if err != nil {
		t.Fatalf("RenderCAReadFragment: %v", err)
	}
	// Exactly one file-read* (data) grant, on the cert only — the private key
	// (mitmproxy-ca.pem) in the same dir must NOT be read-data-granted.
	if n := strings.Count(got, "file-read*"); n != 1 {
		t.Errorf("expected exactly one file-read* grant, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, `(allow file-read* (literal "/home/test/.mitmproxy/mitmproxy-ca-cert.pem"))`) {
		t.Errorf("missing the cert read grant:\n%s", got)
	}
	// The parent dir gets metadata-only (for traversal), never file-read* (contents).
	if !strings.Contains(got, `(allow file-read-metadata (literal "/home/test/.mitmproxy"))`) {
		t.Errorf("missing the parent-dir metadata grant:\n%s", got)
	}
	if strings.Contains(got, `file-read* (literal "/home/test/.mitmproxy")`) {
		t.Errorf("must not grant read-data on the whole .mitmproxy dir:\n%s", got)
	}
}

func TestRenderCAReadFragment_Errors(t *testing.T) {
	if _, err := RenderCAReadFragment(""); err == nil {
		t.Error("empty path: want error, got nil")
	}
	if _, err := RenderCAReadFragment("relative/ca.pem"); err == nil {
		t.Error("relative path: want error, got nil")
	}
}

func TestRenderKeychainFragment_Golden(t *testing.T) {
	// A fixed, host-independent home keeps the golden stable.
	got, err := RenderKeychainFragment("/home/test")
	if err != nil {
		t.Fatalf("RenderKeychainFragment: %v", err)
	}

	golden := filepath.Join("testdata", "keychain.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("keychain.sb mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestRenderKeychainFragment_ExactlyTheS2Grant pins least privilege: the fragment
// is the S2 spike's two allowances and nothing broader — no read grants, no
// subpath, no rules beyond the one mach-lookup and the one scoped file-write.
func TestRenderKeychainFragment_ExactlyTheS2Grant(t *testing.T) {
	got, err := RenderKeychainFragment("/home/test")
	if err != nil {
		t.Fatalf("RenderKeychainFragment: %v", err)
	}
	if n := strings.Count(got, "(allow"); n != 2 {
		t.Errorf("expected exactly two allow rules, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, `(allow mach-lookup (global-name "com.apple.SecurityServer"))`) {
		t.Errorf("missing the securityd mach-lookup grant:\n%s", got)
	}
	if !strings.Contains(got, `(allow file-write* (regex #"^/home/test/Library/Keychains/login\.keychain-db"))`) {
		t.Errorf("missing the scoped keychain-db write grant:\n%s", got)
	}
	for _, bad := range []string{"file-read", "subpath", "file*"} {
		if strings.Contains(got, bad) {
			t.Errorf("keychain fragment must not contain %q:\n%s", bad, got)
		}
	}
}

func TestRenderClaudeStateFragment_Golden(t *testing.T) {
	got, err := RenderClaudeStateFragment("/home/test")
	if err != nil {
		t.Fatalf("RenderClaudeStateFragment: %v", err)
	}

	golden := filepath.Join("testdata", "claude.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("claude.sb mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestRenderClaudeStateFragment_FileLevelOnly pins least privilege: RW is anchored
// at the ~/.claude.json prefix (file-level, never a subpath/dir-wide grant), and
// the home dir itself gets metadata only.
func TestRenderClaudeStateFragment_FileLevelOnly(t *testing.T) {
	got, err := RenderClaudeStateFragment("/home/test")
	if err != nil {
		t.Fatalf("RenderClaudeStateFragment: %v", err)
	}
	if !strings.Contains(got, `(allow file-read* file-write* (regex #"^/home/test/\.claude\.json"))`) {
		t.Errorf("missing the anchored .claude.json RW grant:\n%s", got)
	}
	if !strings.Contains(got, `(allow file-read-metadata (literal "/home/test"))`) {
		t.Errorf("missing the home-dir metadata grant:\n%s", got)
	}
	if strings.Contains(got, "subpath") {
		t.Errorf("claude fragment must not grant a subpath:\n%s", got)
	}
	if n := strings.Count(got, "(allow"); n != 2 {
		t.Errorf("expected exactly two allow rules, got %d:\n%s", n, got)
	}
	// The regex must be anchored so it cannot match e.g. /home/test2/.claude.json.
	if !strings.Contains(got, `#"^/home/test/\.claude\.json"`) {
		t.Errorf("the RW regex must be anchored at the home prefix:\n%s", got)
	}
}

// TestHomeFragments_RegexEscaping: a home dir containing regex metacharacters must
// be escaped so the dot matches literally, not as a wildcard.
func TestHomeFragments_RegexEscaping(t *testing.T) {
	kc, err := RenderKeychainFragment("/home/j.doe")
	if err != nil {
		t.Fatalf("RenderKeychainFragment: %v", err)
	}
	if !strings.Contains(kc, `#"^/home/j\.doe/Library/Keychains/login\.keychain-db"`) {
		t.Errorf("keychain regex must escape the home-dir dot:\n%s", kc)
	}
	cs, err := RenderClaudeStateFragment("/home/j.doe")
	if err != nil {
		t.Fatalf("RenderClaudeStateFragment: %v", err)
	}
	if !strings.Contains(cs, `#"^/home/j\.doe/\.claude\.json"`) {
		t.Errorf("claude-state regex must escape the home-dir dot:\n%s", cs)
	}
	// The literal (non-regex) home grant stays unescaped.
	if !strings.Contains(cs, `(allow file-read-metadata (literal "/home/j.doe"))`) {
		t.Errorf("metadata literal must use the raw path:\n%s", cs)
	}
}

func TestHomeFragments_Errors(t *testing.T) {
	for _, home := range []string{"", "relative/home"} {
		if _, err := RenderKeychainFragment(home); err == nil {
			t.Errorf("RenderKeychainFragment(%q): want error, got nil", home)
		}
		if _, err := RenderClaudeStateFragment(home); err == nil {
			t.Errorf("RenderClaudeStateFragment(%q): want error, got nil", home)
		}
	}
}

func TestRenderConfigReadOnlyFragment_Golden(t *testing.T) {
	// Fixed, host-independent paths keep the golden stable: the project config plus
	// one in-project include and one out-of-project (home) include.
	got, err := RenderConfigReadOnlyFragment([]string{
		"/home/test/proj/.agent-creance.yaml",
		"/home/test/proj/team.yaml",
		"/home/test/.config/agent-creance.yaml",
	})
	if err != nil {
		t.Fatalf("RenderConfigReadOnlyFragment: %v", err)
	}

	golden := filepath.Join("testdata", "config-ro.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("config-ro.sb mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderConfigReadOnlyFragment_DeniesWriteOnly(t *testing.T) {
	got, err := RenderConfigReadOnlyFragment([]string{"/p/.agent-creance.yaml"})
	if err != nil {
		t.Fatalf("RenderConfigReadOnlyFragment: %v", err)
	}
	if !strings.Contains(got, `(deny file-write* (literal "/p/.agent-creance.yaml"))`) {
		t.Errorf("missing the deny-write rule:\n%s", got)
	}
	// Read must stay allowed: the agent still needs to read its own config.
	if strings.Contains(got, "file-read") {
		t.Errorf("must not touch read permissions:\n%s", got)
	}
}

func TestRenderConfigReadOnlyFragment_Dedupes(t *testing.T) {
	got, err := RenderConfigReadOnlyFragment([]string{"/p/a.yaml", "/p/a.yaml"})
	if err != nil {
		t.Fatalf("RenderConfigReadOnlyFragment: %v", err)
	}
	if n := strings.Count(got, "/p/a.yaml"); n != 1 {
		t.Errorf("duplicate path not deduped, got %d occurrences:\n%s", n, got)
	}
}

func TestRenderConfigReadOnlyFragment_Errors(t *testing.T) {
	if _, err := RenderConfigReadOnlyFragment([]string{"relative.yaml"}); err == nil {
		t.Error("relative path: want error, got nil")
	}
}
