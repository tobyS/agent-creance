package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPackagistSourceURL(t *testing.T) {
	require.Equal(t,
		"https://repo.packagist.org/p2/monolog/monolog.json",
		packagistSource{}.url("monolog/monolog"))
}

func TestPackagistValidate(t *testing.T) {
	valid := []string{"monolog/monolog", "symfony/console", "phpunit/php-code-coverage", "a/b", "Foo/Bar"}
	for _, pkg := range valid {
		require.NoError(t, packagistSource{}.validate(pkg), "valid %q", pkg)
	}
	invalid := []string{
		"", "monolog", "a/b/c", "vendor/pkg?x", "vendor/pkg@1", "vendor/pkg#f",
		"vendor/.pkg", "/abs", "vendor/", "/", "ven dor/pkg",
	}
	for _, pkg := range invalid {
		require.Error(t, packagistSource{}.validate(pkg), "invalid %q", pkg)
	}
}

func TestPackagistParse(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "packagist-monolog.json"))
	require.NoError(t, err)

	md, err := packagistSource{}.parse(body)
	require.NoError(t, err)
	require.Equal(t, "https://github.com/Seldaek/monolog", md.Homepage)
	require.Equal(t, "https://github.com/Seldaek/monolog.git", md.Repository)
}

func TestPackagistParseEmptyVersions(t *testing.T) {
	_, err := packagistSource{}.parse([]byte(`{"packages":{"vendor/pkg":[]}}`))
	require.Error(t, err)
}

func TestPackagistParseInvalidJSON(t *testing.T) {
	_, err := packagistSource{}.parse([]byte("not json"))
	require.Error(t, err)
}
