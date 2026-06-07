package status

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/proxy"
)

var update = flag.Bool("update", false, "regenerate golden files")

func goldenCases() map[string]Report {
	return map[string]Report{
		// No project has running proxy state.
		"empty": {},
		// A single healthy running cage with two agents, shown by its path.
		"running": {Projects: []ProjectStatus{
			{Hash: "a1b2c3d4e5f60718", Diag: proxy.Diagnosis{
				LockPresent: true, ProxyPID: 111, Port: 8080, ProxyUp: true,
				LiveAgents: []int{222, 333}, CanonicalPath: "/Users/toby/code/proj",
			}},
		}},
		// An orphan: proxy up, no live agents.
		"orphan": {Projects: []ProjectStatus{
			{Hash: "9f8e7d6c5b4a3021", Diag: proxy.Diagnosis{
				LockPresent: true, ProxyPID: 444, Port: 8081, ProxyUp: true, Orphan: true,
				CanonicalPath: "/Users/toby/code/other",
			}},
		}},
		// Stranded: live agents, proxy not reachable on the recorded port.
		"stranded": {Projects: []ProjectStatus{
			{Hash: "0011223344556677", Diag: proxy.Diagnosis{
				LockPresent: true, ProxyPID: 555, Port: 8082, ProxyUp: false,
				LiveAgents: []int{666}, Stranded: true, CanonicalPath: "/Users/toby/code/wedged",
			}},
		}},
		// Several projects, including one whose lock predates canonical_path (no
		// path → hash fallback) and one recorded-but-down proxy.
		"mixed": {Projects: []ProjectStatus{
			{Hash: "1111111111111111", Diag: proxy.Diagnosis{
				LockPresent: true, ProxyPID: 111, Port: 8080, ProxyUp: true,
				LiveAgents: []int{222}, CanonicalPath: "/Users/toby/code/alpha",
			}},
			{Hash: "2222222222222222", Diag: proxy.Diagnosis{
				LockPresent: true, ProxyPID: 333, Port: 8081, ProxyUp: true, Orphan: true,
			}}, // no recorded path ⇒ hash fallback
			{Hash: "3333333333333333", Diag: proxy.Diagnosis{
				LockPresent: true, ProxyPID: 444, Port: 8082, ProxyUp: false,
				CanonicalPath: "/Users/toby/code/gamma",
			}}, // recorded but dead ⇒ down
		}},
	}
}

func TestRender(t *testing.T) {
	for name, rep := range goldenCases() {
		t.Run(name, func(t *testing.T) {
			got := Render(rep)
			golden := filepath.Join("testdata", "render_"+name+".golden")
			if *update {
				require.NoError(t, os.MkdirAll("testdata", 0o755))
				require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
				return
			}
			want, err := os.ReadFile(golden)
			require.NoError(t, err, "missing golden file; run with -update to create it")
			require.Equal(t, string(want), got)
		})
	}
}
