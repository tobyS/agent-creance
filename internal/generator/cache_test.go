package generator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/generator/registry"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// fullPackageLookuper scripts metadata for every dependency in testdata/package.json
// so a full run produces rules without an unscripted-package error.
func fullPackageLookuper() *fakeLookuper {
	return &fakeLookuper{meta: map[string]registry.Metadata{
		"react":       {Homepage: "https://react.dev/", Repository: "git+https://github.com/facebook/react.git"},
		"barehome":    {Homepage: "https://barehome.example/"},
		"pageddocs":   {Homepage: "https://someuser.github.io/pageddocs/"},
		"norepo":      {},
		"@scope/tool": {Homepage: "https://scope.example", Repository: "https://github.com/scope/tool"},
		"gitlablib":   {Repository: "https://gitlab.com/group/gitlablib"},
	}}
}

func readPackageManifest(t *testing.T) []byte {
	t.Helper()
	manifest, err := os.ReadFile(filepath.Join("testdata", "package.json"))
	require.NoError(t, err)
	return manifest
}

func TestGenerate_OutputCacheHitMakesZeroLookups(t *testing.T) {
	lookup := fullPackageLookuper()
	g := newGenerator(packageJSON{}, lookup, sysdeptest.NewFakeFileSystem(), "/gen")
	manifest := readPackageManifest(t)

	first, err := g.Generate(context.Background(), manifest)
	require.NoError(t, err)
	require.NotEmpty(t, first)
	callsAfterFirst := lookup.Calls
	require.Positive(t, callsAfterFirst)

	// The cache file the first run wrote now lives in the fake fs.
	_, ok := g.fs.(*sysdeptest.FakeFileSystem).Files[g.cachePath(manifest)]
	require.True(t, ok, "expected cache file written")

	second, err := g.Generate(context.Background(), manifest)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, callsAfterFirst, lookup.Calls, "second run must make zero additional lookups")
}

func TestGenerate_ChangedManifestMissesCache(t *testing.T) {
	lookup := fullPackageLookuper()
	g := newGenerator(packageJSON{}, lookup, sysdeptest.NewFakeFileSystem(), "/gen")

	_, err := g.Generate(context.Background(), readPackageManifest(t))
	require.NoError(t, err)
	callsAfterFirst := lookup.Calls

	// One extra byte changes the hash -> different key -> re-walk.
	_, err = g.Generate(context.Background(), append(readPackageManifest(t), ' '))
	require.NoError(t, err)
	require.Greater(t, lookup.Calls, callsAfterFirst, "changed manifest must re-walk dependencies")
}

func TestGenerate_UnparseableCacheRegenerates(t *testing.T) {
	lookup := fullPackageLookuper()
	fs := sysdeptest.NewFakeFileSystem()
	g := newGenerator(packageJSON{}, lookup, fs, "/gen")
	manifest := readPackageManifest(t)

	fs.Files[g.cachePath(manifest)] = []byte("{ this is not valid json")

	rules, err := g.Generate(context.Background(), manifest)
	require.NoError(t, err)
	require.NotEmpty(t, rules)
	require.Positive(t, lookup.Calls, "a corrupt cache must trigger regeneration")
}

func TestGenerate_AtomicWriteLeavesNoPartialFileOnRenameError(t *testing.T) {
	lookup := fullPackageLookuper()
	fs := sysdeptest.NewFakeFileSystem()
	g := newGenerator(packageJSON{}, lookup, fs, "/gen")
	manifest := readPackageManifest(t)

	path := g.cachePath(manifest)
	fs.RenameErrs[path+cacheTempSuffix] = errors.New("rename boom")

	_, err := g.Generate(context.Background(), manifest)
	require.Error(t, err)
	_, ok := fs.Files[path]
	require.False(t, ok, "no final cache file should exist after a failed rename")
}

func TestGenerate_ReadCacheErrorIsSurfaced(t *testing.T) {
	lookup := fullPackageLookuper()
	fs := sysdeptest.NewFakeFileSystem()
	g := newGenerator(packageJSON{}, lookup, fs, "/gen")
	manifest := readPackageManifest(t)

	fs.Errs[g.cachePath(manifest)] = errors.New("read boom") // exists but unreadable

	_, err := g.Generate(context.Background(), manifest)
	require.Error(t, err)
}
