package doctor

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/cred"
	"github.com/tobyS/agent-creance/internal/prereq"
	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/style"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

var update = flag.Bool("update", false, "regenerate golden files")

// versionResults is a fixed, representative version section shared by the golden
// fixtures (one exact, one patch-skew, mirroring prereq's own golden).
func versionResults() []prereq.Result {
	safehouse := prereq.Tool{Name: "agent-safehouse", Tested: "1.4.2"}
	mitm := prereq.Tool{Name: "mitmproxy", Tested: "12.0.1"}
	return []prereq.Result{
		{Tool: safehouse, Installed: true, Version: "1.4.2", Skew: prereq.SkewExact},
		{Tool: mitm, Installed: true, Version: "12.0.1", Skew: prereq.SkewExact},
	}
}

func goldenCases() map[string]Report {
	return map[string]Report{
		// Everything healthy: trusted CA, a running proxy with an agent, nothing
		// exposed, local filesystems.
		"healthy": {
			Version: versionResults(),
			CA:      CASection{State: StatusOK, Detail: "trusted"},
			Cred:    CredSection{State: StatusOK, Detail: "reachable"},
			Proxy: ProxySection{Diag: proxy.Diagnosis{
				LockPresent: true, ProxyPID: 111, Port: 8080, ProxyUp: true, LiveAgents: []int{222},
			}},
			Exposed: ExposedSection{State: StatusOK},
			FS:      FSSection{State: StatusOK},
		},
		// Actionable problems: untrusted CA + an un-fixed orphan, plus warnings.
		"problems": {
			Version: versionResults(),
			CA:      CASection{State: StatusProblem, Detail: "CA verification failed: the mitmproxy CA is not trusted. Re-run `agent-creance setup`."},
			Cred:    CredSection{State: StatusProblem, Detail: cred.Result{Status: cred.StatusLocked}.Message()},
			Proxy: ProxySection{Diag: proxy.Diagnosis{
				LockPresent: true, ProxyPID: 111, Port: 8080, ProxyUp: true, Orphan: true,
			}},
			Exposed: ExposedSection{State: StatusWarn, Listeners: []sysdep.Listener{
				{Command: "node", PID: 501, Address: "*:8080"},
				{Command: "sshd", PID: 77, Address: "*:22"},
			}},
			FS: FSSection{State: StatusWarn, Warnings: []FSWarning{
				{Label: "working directory", Path: "/Users/toby/proj", FSType: "smbfs", Reason: "network mount"},
				{Label: "state cache", Path: "/Users/toby/Library/Mobile Documents/com~apple~CloudDocs/.cache/agent-creance", FSType: "apfs", Reason: "iCloud Drive"},
			}},
		},
		// --fix cleaned the orphan; warnings only otherwise.
		"fixed": {
			Version: versionResults(),
			CA:      CASection{State: StatusWarn, Detail: "CA not generated — run `agent-creance setup`"},
			Cred:    CredSection{State: StatusWarn, Detail: cred.Result{Status: cred.StatusMissing}.Message()},
			Proxy: ProxySection{
				Diag:    proxy.Diagnosis{LockPresent: true, ProxyPID: 111, Port: 8080, ProxyUp: true, Orphan: true},
				Cleaned: &proxy.CleanResult{Cleaned: true, ProxyPID: 111},
			},
			Exposed: ExposedSection{State: StatusSkipped, Detail: "could not scan (lsof unavailable)"},
			FS:      FSSection{State: StatusOK},
		},
		// Stranded agents + no listeners scanned cleanly.
		"stranded": {
			Version: versionResults(),
			CA:      CASection{State: StatusOK, Detail: "trusted"},
			Cred:    CredSection{State: StatusOK, Detail: "reachable"},
			Proxy: ProxySection{Diag: proxy.Diagnosis{
				LockPresent: true, ProxyPID: 111, Port: 8080, ProxyUp: false, LiveAgents: []int{222, 333}, Stranded: true,
			}},
			Exposed: ExposedSection{State: StatusOK},
			FS:      FSSection{State: StatusOK},
		},
	}
}

func TestRender(t *testing.T) {
	// Plain keeps the original golden name (so the byte-identical files prove
	// plain parity); color adds a _color sibling.
	modes := map[string]*style.Styler{"": style.Plain(), "_color": style.New(true)}
	for name, rep := range goldenCases() {
		for suffix, sty := range modes {
			t.Run(name+suffix, func(t *testing.T) {
				got := Render(rep, sty)
				golden := filepath.Join("testdata", "render_"+name+suffix+".golden")
				if *update {
					require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
					return
				}
				want, err := os.ReadFile(golden)
				require.NoError(t, err, "missing golden file; run with -update to create it")
				require.Equal(t, string(want), got)
			})
		}
	}
}

func TestClassifyFS(t *testing.T) {
	cases := []struct {
		name       string
		info       sysdep.FSInfo
		path       string
		wantWarn   bool
		wantReason string
	}{
		{"local apfs ok", sysdep.FSInfo{Name: "apfs", Local: true}, "/Users/toby/proj", false, ""},
		{"smb network", sysdep.FSInfo{Name: "smbfs", Local: false}, "/Volumes/share/proj", true, "network mount"},
		{"nfs network", sysdep.FSInfo{Name: "nfs", Local: false}, "/mnt/nfs", true, "network mount"},
		{"icloud apfs by path", sysdep.FSInfo{Name: "apfs", Local: true}, "/Users/toby/Library/Mobile Documents/com~apple~CloudDocs/proj", true, "iCloud Drive"},
		{"icloud takes precedence over local", sysdep.FSInfo{Name: "apfs", Local: true}, "/x/Library/Mobile Documents/y", true, "iCloud Drive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warn, reason := classifyFS(tc.info, tc.path)
			assert.Equal(t, tc.wantWarn, warn)
			assert.Equal(t, tc.wantReason, reason)
		})
	}
}

func TestActionable(t *testing.T) {
	missingTool := prereq.Result{Tool: prereq.Tool{Name: "mitmproxy"}, Installed: false}

	t.Run("clean", func(t *testing.T) {
		r := Report{Version: versionResults(), CA: CASection{State: StatusOK}}
		assert.Empty(t, r.Actionable())
	})
	t.Run("untrusted CA", func(t *testing.T) {
		r := Report{CA: CASection{State: StatusProblem}}
		assert.Equal(t, []string{"untrusted CA"}, r.Actionable())
	})
	t.Run("missing prereq", func(t *testing.T) {
		r := Report{Version: []prereq.Result{missingTool}}
		assert.Equal(t, []string{"missing prerequisites"}, r.Actionable())
	})
	t.Run("credential unavailable", func(t *testing.T) {
		r := Report{Cred: CredSection{State: StatusProblem}}
		assert.Equal(t, []string{"credential unavailable"}, r.Actionable())
	})
	t.Run("orphan not cleaned", func(t *testing.T) {
		r := Report{Proxy: ProxySection{Diag: proxy.Diagnosis{Orphan: true}}}
		assert.Equal(t, []string{"orphan proxy"}, r.Actionable())
	})
	t.Run("orphan cleaned is not actionable", func(t *testing.T) {
		r := Report{Proxy: ProxySection{
			Diag:    proxy.Diagnosis{Orphan: true},
			Cleaned: &proxy.CleanResult{Cleaned: true},
		}}
		assert.Empty(t, r.Actionable())
	})
	t.Run("warnings only are not actionable", func(t *testing.T) {
		r := Report{
			CA:      CASection{State: StatusWarn},
			Cred:    CredSection{State: StatusWarn},
			Exposed: ExposedSection{State: StatusWarn},
			FS:      FSSection{State: StatusWarn},
			Proxy:   ProxySection{Diag: proxy.Diagnosis{Stranded: true}},
		}
		assert.Empty(t, r.Actionable())
	})
}
