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

func newTestGenerator(eco ecosystem, lookup lookuper) *Generator {
	return newGenerator(eco, lookup, sysdeptest.NewFakeFileSystem(), "/gen")
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
		},
		notFound: map[string]bool{"gitlablib": false},
	}
	// gitlablib: a GitLab repo with no homepage.
	lookup.meta["gitlablib"] = registry.Metadata{Repository: "https://gitlab.com/group/gitlablib"}

	goldenRun(t, packageJSON{}, "package.json", "package_json.golden", lookup)
}

func TestGenerate_ComposerJSONGolden(t *testing.T) {
	lookup := &fakeLookuper{
		meta: map[string]registry.Metadata{
			"monolog/monolog":   {Repository: "https://github.com/Seldaek/monolog.git"},
			"laravel/framework": {Homepage: "https://laravel.com", Repository: "https://github.com/laravel/framework"},
			"phpunit/phpunit":   {Homepage: "https://phpunit.de/", Repository: "https://github.com/sebastianbergmann/phpunit.git"},
		},
	}
	goldenRun(t, composerJSON{}, "composer.json", "composer_json.golden", lookup)
}

func TestGenerate_NotFoundEmitsNothingAndContinues(t *testing.T) {
	lookup := &fakeLookuper{
		meta:     map[string]registry.Metadata{"react": {Homepage: "https://react.dev/"}},
		notFound: map[string]bool{"barehome": true, "pageddocs": true, "norepo": true, "gitlablib": true, "@scope/tool": true},
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
		"react": {}, "barehome": {}, "pageddocs": {}, "norepo": {}, "gitlablib": {}, "@scope/tool": {},
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

func TestKnownAndNew(t *testing.T) {
	require.True(t, Known("package_json"))
	require.True(t, Known("composer_json"))
	require.False(t, Known("pyproject_toml"))

	fs := sysdeptest.NewFakeFileSystem()
	clock := sysdeptest.NewFakeClock(time.Unix(0, 0))
	http := sysdeptest.NewFakeHTTPGetter()

	for _, name := range []string{"package_json", "composer_json"} {
		g, err := New(name, fs, clock, http, "/registries", "/generators")
		require.NoError(t, err)
		require.NotNil(t, g)
	}
	_, err := New("unknown", fs, clock, http, "/registries", "/generators")
	require.Error(t, err)
}

func TestDeps_ManifestParsing(t *testing.T) {
	pkg, err := packageJSON{}.deps([]byte(`{"dependencies":{"b":"1","a":"1"},"devDependencies":{"a":"1","c":"1"}}`))
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
