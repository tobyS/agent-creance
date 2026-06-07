package status_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/status"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// lockJSON mirrors the proxy package's unexported lockState wire format so the
// scanner test can seed proxy.lock contents.
type lockJSON struct {
	ProxyPID      int    `json:"proxy_pid"`
	Port          int    `json:"port"`
	PolicyHash    string `json:"policy_hash"`
	Agents        []int  `json:"agents"`
	CanonicalPath string `json:"canonical_path"`
}

type scanHarness struct {
	scanner  *status.Scanner
	fs       *sysdeptest.FakeFileSystem
	flock    *sysdeptest.FakeFlock
	proc     *sysdeptest.FakeProcessManager
	ports    *sysdeptest.FakePortAllocator
	projects string // the resolved projects/ root
}

func newScanHarness(t *testing.T) *scanHarness {
	t.Helper()
	fs := sysdeptest.NewFakeFileSystem()
	flock := sysdeptest.NewFakeFlock()
	proc := sysdeptest.NewFakeProcessManager()
	ports := sysdeptest.NewFakePortAllocator()

	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = "/home/u" // no XDG ⇒ /home/u/.cache
	resolver := state.New(paths)
	projects, err := resolver.ProjectsRoot()
	require.NoError(t, err)

	return &scanHarness{
		scanner: &status.Scanner{
			Manager:  proxy.NewManager(fs, flock, proc, ports, nil),
			Resolver: resolver,
			FS:       fs,
		},
		fs: fs, flock: flock, proc: proc, ports: ports, projects: projects,
	}
}

// seed registers a project dir and its proxy.lock contents at the resolved hash.
func (h *scanHarness) seed(t *testing.T, hash string, ls lockJSON) {
	t.Helper()
	root := filepath.Join(h.projects, hash)
	require.NoError(t, h.fs.MkdirAll(root, 0o755))
	data, err := json.Marshal(ls)
	require.NoError(t, err)
	h.flock.Contents[filepath.Join(root, "proxy.lock")] = data
}

func TestScanEmptyWhenNoProjectsRoot(t *testing.T) {
	h := newScanHarness(t) // projects/ never created
	rep, err := h.scanner.Scan()
	require.NoError(t, err)
	assert.Empty(t, rep.Projects)
}

func TestScanListsRunningOrphanStrandedAndSkipsCleared(t *testing.T) {
	h := newScanHarness(t)

	// running: proxy alive + listening + a live agent.
	h.seed(t, "1111111111111111", lockJSON{ProxyPID: 11, Port: 8080, Agents: []int{12}, CanonicalPath: "/code/alpha"})
	h.proc.AlivePIDs[11] = true
	h.proc.AlivePIDs[12] = true
	h.ports.Listening[8080] = true

	// orphan: proxy alive + listening but its only agent is dead.
	h.seed(t, "2222222222222222", lockJSON{ProxyPID: 21, Port: 8081, Agents: []int{99}, CanonicalPath: "/code/beta"})
	h.proc.AlivePIDs[21] = true
	h.ports.Listening[8081] = true

	// stranded: live agent, proxy not listening.
	h.seed(t, "3333333333333333", lockJSON{ProxyPID: 31, Port: 8082, Agents: []int{32}, CanonicalPath: "/code/gamma"})
	h.proc.AlivePIDs[31] = true
	h.proc.AlivePIDs[32] = true
	// 8082 not listening

	// cleared/zeroed lock ⇒ LockPresent false ⇒ skipped.
	h.seed(t, "4444444444444444", lockJSON{})

	rep, err := h.scanner.Scan()
	require.NoError(t, err)
	require.Len(t, rep.Projects, 3, "the cleared-lock project must be skipped")

	// Sorted by hash.
	assert.Equal(t, "1111111111111111", rep.Projects[0].Hash)
	assert.True(t, rep.Projects[0].Diag.ProxyUp)
	assert.Equal(t, []int{12}, rep.Projects[0].Diag.LiveAgents)
	assert.Equal(t, "/code/alpha", rep.Projects[0].Diag.CanonicalPath)

	assert.Equal(t, "2222222222222222", rep.Projects[1].Hash)
	assert.True(t, rep.Projects[1].Diag.Orphan)

	assert.Equal(t, "3333333333333333", rep.Projects[2].Hash)
	assert.True(t, rep.Projects[2].Diag.Stranded)
}

func TestScanIgnoresNonDirEntries(t *testing.T) {
	h := newScanHarness(t)
	require.NoError(t, h.fs.MkdirAll(h.projects, 0o755))
	// A stray file directly under projects/ must not be treated as a project.
	require.NoError(t, h.fs.WriteFile(filepath.Join(h.projects, "README"), []byte("x"), 0o644))

	rep, err := h.scanner.Scan()
	require.NoError(t, err)
	assert.Empty(t, rep.Projects)
}
