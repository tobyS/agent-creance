package generator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Generator names, as listed in network.egress.generators.
const (
	GeneratorPackageJSON  = "package_json"
	GeneratorComposerJSON = "composer_json"
)

// ecosystem is the per-manifest strategy: the generator's name and how to extract the
// direct dependency names from that manifest. The manifest is decoded leniently — it
// is a third-party file, not one of our own strict-schema configs.
type ecosystem interface {
	name() string
	deps(manifest []byte) ([]string, error)
}

// packageJSON walks an npm package.json's direct dependencies.
type packageJSON struct{}

func (packageJSON) name() string { return GeneratorPackageJSON }

func (packageJSON) deps(manifest []byte) ([]string, error) {
	var doc struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(manifest, &doc); err != nil {
		return nil, fmt.Errorf("package_json: parse manifest: %w", err)
	}
	return sortedUnique(keys(doc.Dependencies), keys(doc.DevDependencies)), nil
}

// composerJSON walks a composer.json's direct require + require-dev, dropping platform
// and meta requirements that are not Packagist packages.
type composerJSON struct{}

func (composerJSON) name() string { return GeneratorComposerJSON }

func (composerJSON) deps(manifest []byte) ([]string, error) {
	var doc struct {
		Require    map[string]string `json:"require"`
		RequireDev map[string]string `json:"require-dev"`
	}
	if err := json.Unmarshal(manifest, &doc); err != nil {
		return nil, fmt.Errorf("composer_json: parse manifest: %w", err)
	}
	all := sortedUnique(keys(doc.Require), keys(doc.RequireDev))
	pkgs := all[:0:0]
	for _, name := range all {
		if isComposerPackage(name) {
			pkgs = append(pkgs, name)
		}
	}
	return pkgs, nil
}

// isComposerPackage reports whether a composer require key is a real Packagist
// package (vendor/name) rather than a platform/meta requirement. Packagist mandates
// the vendor/name form, so a key without a "/" is necessarily a platform constraint
// (php, ext-*, lib-*, composer-runtime-api, …) that has no registry entry — looking
// it up would only 404.
func isComposerPackage(name string) bool {
	return strings.Contains(name, "/")
}

// keys returns the keys of m (nil map → nil slice).
func keys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// sortedUnique merges the given name lists into one sorted, de-duplicated slice so
// the generator's output is deterministic regardless of map iteration order.
func sortedUnique(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range lists {
		for _, name := range list {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}
