package configwatch

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const (
	testHome     = "/home/toby"
	projectPath  = "/proj/.agent-creance.yaml"
	testDebounce = 5 * time.Millisecond
)

// safeBuffer is a goroutine-safe io.Writer the loop goroutine writes feedback to
// while the test reads it.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// stubReload is a scripted ReloadFunc: it returns the i-th result for the i-th
// call, repeating the last one thereafter, and counts calls.
type stubReload struct {
	mu      sync.Mutex
	calls   int
	results []reloadResult
}

type reloadResult struct {
	changed bool
	summary string
	err     error
}

func (s *stubReload) fn(context.Context) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	s.calls++
	if i >= len(s.results) {
		i = len(s.results) - 1
	}
	r := s.results[i]
	return r.changed, r.summary, r.err
}

func (s *stubReload) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// harness bundles a started Watcher with the fakes a test drives and asserts.
type harness struct {
	w      *Watcher
	fw     *sysdeptest.FakeFileWatcher
	reload *stubReload
	out    *safeBuffer
	fsys   *sysdeptest.FakeFileSystem
}

// newHarness wires a Watcher over fake fs/path/watcher seams, seeded with the
// given path→YAML config files, and starts it.
func newHarness(t *testing.T, files map[string]string, results ...reloadResult) *harness {
	t.Helper()
	fsys := sysdeptest.NewFakeFileSystem()
	for p, body := range files {
		fsys.Files[p] = []byte(body)
	}
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = testHome

	loader := config.NewLoader(fsys, paths)
	factory := sysdeptest.NewFakeFileWatcherFactory()
	reload := &stubReload{results: results}
	out := &safeBuffer{}

	w := New(loader, factory, reload.fn, out, WithDebounce(testDebounce))
	require.NoError(t, w.Start(context.Background(), projectPath))
	t.Cleanup(func() { _ = w.Stop() })

	return &harness{w: w, fw: factory.Watcher, reload: reload, out: out, fsys: fsys}
}

func (h *harness) send(name string, op sysdep.FileOp) {
	h.fw.EventsCh <- sysdep.FileEvent{Name: name, Op: op}
}

func TestStart_WatchesProjectDir(t *testing.T) {
	h := newHarness(t, map[string]string{
		projectPath: "network:\n  egress:\n    allow:\n      - host: react.dev\n",
	})
	assert.Equal(t, []string{"/proj"}, h.fw.Added(), "watches the project file's dir")
}

func TestReloadOnWrite(t *testing.T) {
	h := newHarness(t, map[string]string{
		projectPath: "network:\n  egress:\n    allow:\n      - host: react.dev\n",
	}, reloadResult{changed: true, summary: "3 allow, 1 deny"})

	h.send(projectPath, sysdep.FileWrite)

	require.Eventually(t, func() bool { return h.reload.count() == 1 }, time.Second, time.Millisecond)
	assert.Contains(t, h.out.String(), "✓ config reloaded (3 allow, 1 deny)")
}

func TestDebounceCoalesces(t *testing.T) {
	h := newHarness(t, map[string]string{
		projectPath: "network:\n  egress:\n    allow:\n      - host: react.dev\n",
	}, reloadResult{changed: true, summary: "ok"})

	for i := 0; i < 5; i++ {
		h.send(projectPath, sysdep.FileWrite)
	}

	require.Eventually(t, func() bool { return h.reload.count() == 1 }, time.Second, time.Millisecond)
	// A burst collapses to exactly one recompile.
	require.Never(t, func() bool { return h.reload.count() > 1 }, 50*time.Millisecond, 5*time.Millisecond)
}

func TestIgnoresChmodAndUnwatched(t *testing.T) {
	h := newHarness(t, map[string]string{
		projectPath: "network:\n  egress:\n    allow:\n      - host: react.dev\n",
	}, reloadResult{changed: true, summary: "ok"})

	h.send(projectPath, sysdep.FileChmod)           // wrong op
	h.send("/proj/unrelated.txt", sysdep.FileWrite) // not in include graph

	require.Never(t, func() bool { return h.reload.count() > 0 }, 50*time.Millisecond, 5*time.Millisecond)
}

func TestInvalidEditKeepsLastGood(t *testing.T) {
	h := newHarness(t, map[string]string{
		projectPath: "network:\n  egress:\n    allow:\n      - host: react.dev\n",
	}, reloadResult{err: errors.New("config: bad.yaml: unknown key")})

	h.send(projectPath, sysdep.FileWrite)

	require.Eventually(t, func() bool {
		return strings.Contains(h.out.String(), "keeping last-good policy")
	}, time.Second, time.Millisecond)
	assert.Contains(t, h.out.String(), "unknown key")
	// A failed reload must not re-derive the watch set (no extra Add beyond start).
	assert.Equal(t, []string{"/proj"}, h.fw.Added())
}

func TestRecoverAfterInvalid(t *testing.T) {
	h := newHarness(t, map[string]string{
		projectPath: "network:\n  egress:\n    allow:\n      - host: react.dev\n",
	},
		reloadResult{err: errors.New("boom")},
		reloadResult{changed: true, summary: "fixed"},
	)

	h.send(projectPath, sysdep.FileWrite)
	require.Eventually(t, func() bool {
		return strings.Contains(h.out.String(), "keeping last-good policy")
	}, time.Second, time.Millisecond)

	h.send(projectPath, sysdep.FileWrite)
	require.Eventually(t, func() bool {
		return strings.Contains(h.out.String(), "✓ config reloaded (fixed)")
	}, time.Second, time.Millisecond)
}

func TestUnchangedPolicyReported(t *testing.T) {
	h := newHarness(t, map[string]string{
		projectPath: "network:\n  egress:\n    allow:\n      - host: react.dev\n",
	}, reloadResult{changed: false})

	h.send(projectPath, sysdep.FileWrite)
	require.Eventually(t, func() bool {
		return strings.Contains(h.out.String(), "policy unchanged")
	}, time.Second, time.Millisecond)
}

func TestIncludeAddedReconcilesWatchedDirs(t *testing.T) {
	h := newHarness(t, map[string]string{
		projectPath: "network:\n  egress:\n    allow:\n      - host: react.dev\n",
	}, reloadResult{changed: true, summary: "ok"})

	// Simulate the user's edit: the project now includes a fragment in a new dir.
	h.fsys.Files[projectPath] = []byte("include:\n  - /other/frag.yaml\n")
	h.fsys.Files["/other/frag.yaml"] = []byte("network:\n  egress:\n    allow:\n      - host: x.example\n")

	h.send(projectPath, sysdep.FileWrite)

	require.Eventually(t, func() bool {
		for _, d := range h.fw.Added() {
			if d == "/other" {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond, "newly-included dir should be watched")
}

func TestCleanStop(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	fsys.Files[projectPath] = []byte("network:\n  egress:\n    allow:\n      - host: react.dev\n")
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = testHome
	factory := sysdeptest.NewFakeFileWatcherFactory()
	reload := &stubReload{results: []reloadResult{{changed: true, summary: "ok"}}}

	w := New(config.NewLoader(fsys, paths), factory, reload.fn, &safeBuffer{}, WithDebounce(testDebounce))
	require.NoError(t, w.Start(context.Background(), projectPath))

	require.NoError(t, w.Stop())
	assert.True(t, factory.Watcher.Closed(), "watcher closed on Stop")
	// Stop is safe to call again.
	require.NoError(t, w.Stop())
}

func TestStartFailure_WatcherCreate(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	fsys.Files[projectPath] = []byte("network:\n  egress:\n    allow:\n      - host: react.dev\n")
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = testHome
	factory := &sysdeptest.FakeFileWatcherFactory{NewErr: errors.New("no fds")}

	w := New(config.NewLoader(fsys, paths), factory, (&stubReload{results: []reloadResult{{}}}).fn, &safeBuffer{})
	err := w.Start(context.Background(), projectPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create watcher")
}

func TestStartFailure_MissingProject(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem() // nothing on disk
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = testHome
	factory := sysdeptest.NewFakeFileWatcherFactory()

	w := New(config.NewLoader(fsys, paths), factory, (&stubReload{results: []reloadResult{{}}}).fn, &safeBuffer{})
	err := w.Start(context.Background(), projectPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enumerate config files")
}
