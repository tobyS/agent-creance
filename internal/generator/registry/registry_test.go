package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const (
	testRoot = "/cache/agent-creance/registries"
	npmPkg   = "left-pad"
	npmURL   = "https://registry.npmjs.org/left-pad"
	npmPath  = testRoot + "/npm/" + npmPkg + ".json"
	npmDir   = testRoot + "/npm"
)

// baseTime is the frozen "now" the fake clock starts at.
var baseTime = time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)

// npmBody is a minimal valid packument with hoisted homepage + repository.
var npmBody = []byte(`{"homepage":"https://h.example/","repository":{"type":"git","url":"https://r.example/x.git"}}`)

func newNPMTest() (*Client, *sysdeptest.FakeFileSystem, *sysdeptest.FakeClock, *sysdeptest.FakeHTTPGetter) {
	fsys := sysdeptest.NewFakeFileSystem()
	clk := sysdeptest.NewFakeClock(baseTime)
	http := sysdeptest.NewFakeHTTPGetter()
	return NewNPM(fsys, clk, http, testRoot), fsys, clk, http
}

// seedCache writes a cacheEntry to the fake fs as if fetched at fetchedAt.
func seedCache(t *testing.T, fsys *sysdeptest.FakeFileSystem, path string, fetchedAt time.Time, md Metadata) {
	t.Helper()
	data, err := json.Marshal(cacheEntry{FetchedAt: fetchedAt, Metadata: md})
	require.NoError(t, err)
	fsys.Files[path] = data
}

func TestLookupCacheMissFetchesOnceAndWritesCache(t *testing.T) {
	c, fsys, clk, http := newNPMTest()
	http.WithResponse(npmURL, 200, npmBody)

	md, err := c.Lookup(context.Background(), npmPkg)
	require.NoError(t, err)
	require.Equal(t, "https://h.example/", md.Homepage)
	require.Equal(t, "https://r.example/x.git", md.Repository)

	// Exactly one network call.
	require.Equal(t, []string{npmURL}, http.Calls)

	// Cache file written atomically (parent dir created, perm 0o644, fetched_at = now).
	require.True(t, fsys.Dirs[npmDir], "cache dir created")
	require.Equal(t, fs.FileMode(0o644), fsys.Perms[npmPath])
	require.NotContains(t, fsys.Files, npmPath+".tmp", "temp file renamed away")

	var entry cacheEntry
	require.NoError(t, json.Unmarshal(fsys.Files[npmPath], &entry))
	require.Equal(t, clk.Now(), entry.FetchedAt)
	require.Equal(t, md, entry.Metadata)
}

func TestLookupFreshCacheHitDoesNotFetch(t *testing.T) {
	c, fsys, _, http := newNPMTest()
	want := Metadata{Homepage: "https://cached/", Repository: "https://cached.git"}
	seedCache(t, fsys, npmPath, baseTime, want) // fetched "now" → Since == 0

	md, err := c.Lookup(context.Background(), npmPkg)
	require.NoError(t, err)
	require.Equal(t, want, md)
	require.Empty(t, http.Calls, "fresh cache must not hit the network")
}

func TestLookupCacheJustUnder30DaysIsFresh(t *testing.T) {
	c, fsys, clk, http := newNPMTest()
	want := Metadata{Homepage: "https://cached/"}
	seedCache(t, fsys, npmPath, baseTime, want)

	clk.Advance(refreshInterval - time.Hour) // still within the window
	md, err := c.Lookup(context.Background(), npmPkg)
	require.NoError(t, err)
	require.Equal(t, want, md)
	require.Empty(t, http.Calls)
}

func TestLookupStaleCacheRefetches(t *testing.T) {
	c, fsys, clk, http := newNPMTest()
	seedCache(t, fsys, npmPath, baseTime, Metadata{Homepage: "https://old/"})
	http.WithResponse(npmURL, 200, npmBody)

	clk.Advance(refreshInterval + time.Hour) // past the window
	md, err := c.Lookup(context.Background(), npmPkg)
	require.NoError(t, err)
	require.Equal(t, "https://h.example/", md.Homepage)
	require.Equal(t, []string{npmURL}, http.Calls)

	// The rewritten cache carries the advanced fetched_at.
	var entry cacheEntry
	require.NoError(t, json.Unmarshal(fsys.Files[npmPath], &entry))
	require.Equal(t, clk.Now(), entry.FetchedAt)
}

func TestLookupUnparseableCacheRefetches(t *testing.T) {
	c, fsys, _, http := newNPMTest()
	fsys.Files[npmPath] = []byte("{ this is not valid json")
	http.WithResponse(npmURL, 200, npmBody)

	md, err := c.Lookup(context.Background(), npmPkg)
	require.NoError(t, err)
	require.Equal(t, "https://h.example/", md.Homepage)
	require.Equal(t, []string{npmURL}, http.Calls)
}

func TestLookupNotFoundReturnsErrNotFound(t *testing.T) {
	c, _, _, http := newNPMTest()
	http.WithResponse(npmURL, 404, nil)

	_, err := c.Lookup(context.Background(), npmPkg)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestLookupUnexpectedStatusErrors(t *testing.T) {
	c, _, _, http := newNPMTest()
	http.WithResponse(npmURL, 500, []byte("oops"))

	_, err := c.Lookup(context.Background(), npmPkg)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNotFound)
}

func TestLookupTransportErrorIsSurfaced(t *testing.T) {
	c, _, _, http := newNPMTest()
	sentinel := errors.New("dial tcp: connection refused")
	http.WithError(npmURL, sentinel)

	_, err := c.Lookup(context.Background(), npmPkg)
	require.ErrorIs(t, err, sentinel)
}

func TestLookupReadCacheErrorIsSurfaced(t *testing.T) {
	c, fsys, _, http := newNPMTest()
	sentinel := errors.New("permission denied")
	fsys.Errs[npmPath] = sentinel // a genuine read error, not ErrNotExist

	_, err := c.Lookup(context.Background(), npmPkg)
	require.ErrorIs(t, err, sentinel)
	require.Empty(t, http.Calls, "a real read error must not fall through to a fetch")
}

func TestLookupAtomicWriteLeavesNoPartialFileOnRenameFailure(t *testing.T) {
	c, fsys, _, http := newNPMTest()
	http.WithResponse(npmURL, 200, npmBody)
	fsys.RenameErrs[npmPath+".tmp"] = errors.New("rename failed")

	_, err := c.Lookup(context.Background(), npmPkg)
	require.Error(t, err)
	require.NotContains(t, fsys.Files, npmPath, "no committed cache file")
	require.NotContains(t, fsys.Files, npmPath+".tmp", "temp file cleaned up")
}

func TestLookupRejectsPathTraversal(t *testing.T) {
	c, _, _, http := newNPMTest()
	for _, pkg := range []string{"../evil", "", "/abs", "a/../../b"} {
		_, err := c.Lookup(context.Background(), pkg)
		require.Error(t, err, "pkg %q", pkg)
	}
	require.Empty(t, http.Calls, "invalid names never reach the network")
}

func TestInvalidateRemovesPresentEntry(t *testing.T) {
	c, fsys, _, _ := newNPMTest()
	seedCache(t, fsys, npmPath, baseTime, Metadata{Homepage: "https://cached/"})

	existed, err := c.Invalidate(npmPkg)
	require.NoError(t, err)
	require.True(t, existed, "a seeded entry must report existed")
	require.NotContains(t, fsys.Files, npmPath, "cache entry removed")
}

func TestInvalidateAbsentEntryIsNoOp(t *testing.T) {
	c, _, _, _ := newNPMTest()

	existed, err := c.Invalidate(npmPkg)
	require.NoError(t, err)
	require.False(t, existed, "an absent entry reports not-existed, not an error")
}

func TestInvalidateRejectsPathTraversal(t *testing.T) {
	c, _, _, _ := newNPMTest()
	for _, pkg := range []string{"../evil", "", "/abs", "a/../../b"} {
		_, err := c.Invalidate(pkg)
		require.Error(t, err, "pkg %q", pkg)
	}
}

func TestPackagistClientUsesPackagistCachePath(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	clk := sysdeptest.NewFakeClock(baseTime)
	http := sysdeptest.NewFakeHTTPGetter()
	const url = "https://repo.packagist.org/p2/monolog/monolog.json"
	http.WithResponse(url, 200, []byte(`{"packages":{"monolog/monolog":[{"homepage":"https://h/","source":{"url":"https://r.git"}}]}}`))

	c := NewPackagist(fsys, clk, http, testRoot)
	md, err := c.Lookup(context.Background(), "monolog/monolog")
	require.NoError(t, err)
	require.Equal(t, "https://h/", md.Homepage)
	require.Equal(t, "https://r.git", md.Repository)

	// Cache lands under the packagist/<vendor>/<pkg>.json segment.
	require.Contains(t, fsys.Files, testRoot+"/packagist/monolog/monolog.json")
}
