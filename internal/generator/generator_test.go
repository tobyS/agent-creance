package generator

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/generator/registry"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// update regenerates the golden rule-set files. Run with -update after an intentional
// change, then review the git diff.
var update = flag.Bool("update", false, "regenerate golden files")

// fakeLookuper is a scripted, call-counting lookuper. A package in notFound returns
// (wrapped) registry.ErrNotFound; a package in meta returns its metadata; anything
// else is an unscripted-package error (a test bug).
type fakeLookuper struct {
	meta     map[string]registry.Metadata
	notFound map[string]bool
	Calls    int

	// Invalidate scripting: present marks packages whose cache entry "exists"
	// (so Invalidate reports existed==true); invalidated records the calls in order.
	present     map[string]bool
	invalidated []string
}

func (f *fakeLookuper) Lookup(_ context.Context, pkg string) (registry.Metadata, error) {
	f.Calls++
	if f.notFound[pkg] {
		return registry.Metadata{}, fmt.Errorf("registry: %q: %w", pkg, registry.ErrNotFound)
	}
	md, ok := f.meta[pkg]
	if !ok {
		return registry.Metadata{}, fmt.Errorf("unscripted package %q", pkg)
	}
	return md, nil
}

func (f *fakeLookuper) Invalidate(pkg string) (bool, error) {
	f.invalidated = append(f.invalidated, pkg)
	return f.present[pkg], nil
}

func newTestGenerator(eco ecosystem, lookup lookuper) *Generator {
	return newGenerator(eco, lookup, sysdeptest.NewFakeFileSystem(), "/gen", nil)
}

func goldenRun(t *testing.T, eco ecosystem, fixture, golden string, lookup *fakeLookuper) {
	t.Helper()
	manifest, err := os.ReadFile(filepath.Join("testdata", fixture))
	require.NoError(t, err)

	rules, err := newTestGenerator(eco, lookup).Generate(context.Background(), manifest)
	require.NoError(t, err)

	got, err := json.MarshalIndent(rules, "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')

	path := filepath.Join("testdata", golden)
	if *update {
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file; run with -update to create it")
	require.Equal(t, string(want), string(got))
}

func TestGenerate_PackageJSONGolden(t *testing.T) {
	lookup := &fakeLookuper{
		meta: map[string]registry.Metadata{
			"react":       {Homepage: "https://react.dev/", Repository: "git+https://github.com/facebook/react.git"},
			"barehome":    {Homepage: "https://barehome.example/", Repository: ""},
			"pageddocs":   {Homepage: "https://someuser.github.io/pageddocs/", Repository: ""},
			"norepo":      {Homepage: "", Repository: ""},
			"@scope/tool": {Homepage: "https://scope.example", Repository: "https://github.com/scope/tool"},
			// Real-world-shaped scoped packages (metadata mirrors the registry):
			// a monorepo member whose homepage is its GitHub readme, and a
			// "latest"-spec dev dependency.
			"@vueuse/core": {Homepage: "https://github.com/vueuse/vueuse#readme", Repository: "git+https://github.com/vueuse/vueuse.git"},
			"@types/bun":   {Homepage: "https://bun.com", Repository: "git+https://github.com/oven-sh/bun.git"},
		},
		notFound: map[string]bool{"gitlablib": false},
	}
	// gitlablib: a GitLab repo with no homepage.
	lookup.meta["gitlablib"] = registry.Metadata{Repository: "https://gitlab.com/group/gitlablib"}

	// The fixture's peerDependencies entry ("typescript") is deliberately
	// unscripted: if the parser ever started reading that section, the lookup
	// would fail the test.
	goldenRun(t, packageJSON{}, "package.json", "package_json.golden", lookup)
}

func TestGenerate_ComposerJSONGolden(t *testing.T) {
	lookup := &fakeLookuper{
		meta: map[string]registry.Metadata{
			"monolog/monolog":   {Repository: "https://github.com/Seldaek/monolog.git"},
			"laravel/framework": {Homepage: "https://laravel.com", Repository: "https://github.com/laravel/framework"},
			"phpunit/phpunit":   {Homepage: "https://phpunit.de/", Repository: "https://github.com/sebastianbergmann/phpunit.git"},
			"laravel/pint":      {Homepage: "https://laravel.com", Repository: "https://github.com/laravel/pint.git"},
			// dev-master branch constraint — the constraint is irrelevant to the
			// lookup; only the name reaches the registry.
			"roave/security-advisories": {Repository: "https://github.com/Roave/SecurityAdvisories.git"},
		},
		// acme/core comes from the manifest's path repository and does not exist
		// on Packagist — the 404 must be skipped, not fatal.
		notFound: map[string]bool{"acme/core": true},
	}
	goldenRun(t, composerJSON{}, "composer.json", "composer_json.golden", lookup)
}

func TestGenerate_NotFoundEmitsNothingAndContinues(t *testing.T) {
	lookup := &fakeLookuper{
		meta: map[string]registry.Metadata{"react": {Homepage: "https://react.dev/"}},
		notFound: map[string]bool{
			"barehome": true, "pageddocs": true, "norepo": true, "gitlablib": true,
			"@scope/tool": true, "@vueuse/core": true, "@types/bun": true,
		},
	}
	manifest, err := os.ReadFile(filepath.Join("testdata", "package.json"))
	require.NoError(t, err)

	rules, err := newTestGenerator(packageJSON{}, lookup).Generate(context.Background(), manifest)
	require.NoError(t, err)
	require.Len(t, rules, 1) // only react's homepage
	require.Equal(t, "react.dev", rules[0].Rule.Host)
}

func TestGenerate_EmptyFieldsEmitNothing(t *testing.T) {
	lookup := &fakeLookuper{meta: map[string]registry.Metadata{
		"react": {}, "barehome": {}, "pageddocs": {}, "norepo": {}, "gitlablib": {},
		"@scope/tool": {}, "@vueuse/core": {}, "@types/bun": {},
	}}
	manifest, err := os.ReadFile(filepath.Join("testdata", "package.json"))
	require.NoError(t, err)

	rules, err := newTestGenerator(packageJSON{}, lookup).Generate(context.Background(), manifest)
	require.NoError(t, err)
	require.Empty(t, rules)
}

func TestGenerate_LookupErrorIsSurfaced(t *testing.T) {
	lookup := &fakeLookuper{meta: map[string]registry.Metadata{}} // every package is unscripted -> error
	manifest := []byte(`{"dependencies":{"react":"*"}}`)

	_, err := newTestGenerator(packageJSON{}, lookup).Generate(context.Background(), manifest)
	require.Error(t, err)
}

func TestInvalidate(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	lookup := &fakeLookuper{present: map[string]bool{"a": true, "c": true}} // "b" absent
	g := newGenerator(packageJSON{}, lookup, fsys, "/gen", nil)

	manifest := []byte(`{"dependencies":{"b":"1","a":"1"},"devDependencies":{"c":"1"}}`)

	// Seed this generator's output-cache entry so Invalidate clears it.
	outPath := g.cachePath(manifest)
	fsys.Files[outPath] = []byte(`{"rules":[]}`)

	stats, err := g.Invalidate(manifest)
	require.NoError(t, err)
	require.True(t, stats.OutputCacheCleared)
	require.NotContains(t, fsys.Files, outPath, "output cache entry removed")
	require.Equal(t, 3, stats.Packages)                           // a, b, c
	require.Equal(t, 2, stats.CacheEntriesCleared)                // a, c present; b absent
	require.Equal(t, []string{"a", "b", "c"}, lookup.invalidated) // sorted dep order
}

func TestInvalidate_SkipsComposerPlatformKeys(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	lookup := &fakeLookuper{present: map[string]bool{"vendor/a": true}}
	g := newGenerator(composerJSON{}, lookup, fsys, "/gen", nil)

	stats, err := g.Invalidate([]byte(`{"require":{"php":">=8","ext-json":"*","vendor/a":"1"}}`))
	require.NoError(t, err)
	require.Equal(t, 1, stats.Packages) // only vendor/a; php & ext-json are not Packagist packages
	require.Equal(t, 1, stats.CacheEntriesCleared)
	require.Equal(t, []string{"vendor/a"}, lookup.invalidated)
	require.False(t, stats.OutputCacheCleared) // no output cache was seeded
}

func TestInvalidate_AbsentEverythingIsAllZero(t *testing.T) {
	g := newGenerator(packageJSON{}, &fakeLookuper{}, sysdeptest.NewFakeFileSystem(), "/gen", nil)

	stats, err := g.Invalidate([]byte(`{"dependencies":{"a":"1"}}`))
	require.NoError(t, err)
	require.False(t, stats.OutputCacheCleared)
	require.Equal(t, 1, stats.Packages)
	require.Equal(t, 0, stats.CacheEntriesCleared)
}

func TestKnownAndNew(t *testing.T) {
	require.True(t, Known("package_json"))
	require.True(t, Known("composer_json"))
	require.False(t, Known("pyproject_toml"))

	fs := sysdeptest.NewFakeFileSystem()
	clock := sysdeptest.NewFakeClock(time.Unix(0, 0))
	http := sysdeptest.NewFakeHTTPGetter()

	for _, name := range []string{"package_json", "composer_json"} {
		g, err := New(name, fs, clock, http, "/registries", "/generators", nil)
		require.NoError(t, err)
		require.NotNil(t, g)
	}
	_, err := New("unknown", fs, clock, http, "/registries", "/generators", nil)
	require.Error(t, err)
}

func TestDeps_ManifestParsing(t *testing.T) {
	// peerDependencies are deliberately not read (only dependencies + devDependencies).
	pkg, err := packageJSON{}.deps([]byte(`{"dependencies":{"b":"1","a":"1"},"devDependencies":{"a":"1","c":"1"},"peerDependencies":{"p":"1"}}`))
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, pkg)

	comp, err := composerJSON{}.deps([]byte(`{"require":{"php":">=8","ext-json":"*","vendor/a":"1"},"require-dev":{"vendor/b":"1"}}`))
	require.NoError(t, err)
	require.Equal(t, []string{"vendor/a", "vendor/b"}, comp)

	_, err = packageJSON{}.deps([]byte("not json"))
	require.Error(t, err)

	empty, err := packageJSON{}.deps([]byte(`{}`))
	require.NoError(t, err)
	require.Empty(t, empty)
}
