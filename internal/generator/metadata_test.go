package generator

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAllMetadata(t *testing.T) {
	all := All()

	byType := map[string]Metadata{}
	for _, m := range all {
		byType[m.Type] = m
	}

	require.Len(t, all, 2)
	require.Equal(t, Metadata{
		Type:           GeneratorPackageJSON,
		ManifestFile:   "package.json",
		DependencyDirs: []string{"node_modules"},
	}, byType[GeneratorPackageJSON])
	require.Equal(t, Metadata{
		Type:           GeneratorComposerJSON,
		ManifestFile:   "composer.json",
		DependencyDirs: []string{"vendor"},
	}, byType[GeneratorComposerJSON])
}

func TestLookup(t *testing.T) {
	m, ok := Lookup(GeneratorPackageJSON)
	require.True(t, ok)
	require.Equal(t, "package.json", m.ManifestFile)

	_, ok = Lookup("pyproject_toml")
	require.False(t, ok)
}

func TestKnownDerivesFromRegistry(t *testing.T) {
	require.True(t, Known(GeneratorPackageJSON))
	require.True(t, Known(GeneratorComposerJSON))
	require.False(t, Known("cargo_toml"))
	require.False(t, Known(""))
}
