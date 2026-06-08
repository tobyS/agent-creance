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
