package generator

import (
	"context"
	"errors"
	"fmt"

	"github.com/tobyS/agent-creance/internal/generator/registry"
	"github.com/tobyS/agent-creance/internal/progress"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// lookuper is the registry seam a Generator depends on: it resolves a package name to
// its homepage + repository metadata, and (for `policy refresh`) invalidates a
// package's cached entry. *registry.Client satisfies it; unit tests substitute a
// call-counting fake so the generator is exercised without HTTP or the per-package
// cache.
type lookuper interface {
	Lookup(ctx context.Context, pkg string) (registry.Metadata, error)
	// Invalidate removes pkg's cached metadata entry, reporting whether one existed.
	Invalidate(pkg string) (bool, error)
}

// Generator turns one ecosystem's manifest into annotated allow rules, caching the
// emitted rule set by manifest hash (see cache.go).
type Generator struct {
	eco            ecosystem
	lookup         lookuper
	fs             sysdep.FileSystem
	generatorsRoot string
	rep            progress.Reporter
}

// Metadata is the scanner-facing description of a generator type: the manifest file
// it reads at a package root and the installed-dependency directories it owns. It lets
// callers (the compiler's default-path resolution, init's bounded scan) consume a
// generator's filename and skip-set without constructing a full Generator.
type Metadata struct {
	Type           string
	ManifestFile   string
	DependencyDirs []string
}

// All returns the metadata for every known generator, in registry order.
func All() []Metadata {
	out := make([]Metadata, 0, len(ecosystems))
	for _, eco := range ecosystems {
		out = append(out, metadataOf(eco))
	}
	return out
}

// Lookup returns the metadata for the named generator, or ok=false if unknown.
func Lookup(name string) (Metadata, bool) {
	for _, eco := range ecosystems {
		if eco.name() == name {
			return metadataOf(eco), true
		}
	}
	return Metadata{}, false
}

func metadataOf(eco ecosystem) Metadata {
	return Metadata{
		Type:           eco.name(),
		ManifestFile:   eco.manifestFile(),
		DependencyDirs: eco.dependencyDirs(),
	}
}

// Known reports whether name is a recognised generator (one this package can build).
func Known(name string) bool {
	_, ok := Lookup(name)
	return ok
}

// New constructs the generator for name, wiring the matching registry client. An
// unknown name is an error (the caller is responsible for validating a configured
// generators: list, e.g. with Known). registriesRoot is state.RegistriesRoot();
// generatorsRoot is state.GeneratorsRoot(). rep receives the lookup-progress
// events (LookupsStart/LookupDone/ManifestCached); nil means silent.
func New(name string, fs sysdep.FileSystem, clock sysdep.Clock, getter sysdep.HTTPGetter, registriesRoot, generatorsRoot string, rep progress.Reporter) (*Generator, error) {
	switch name {
	case GeneratorPackageJSON:
		return newGenerator(packageJSON{}, registry.NewNPM(fs, clock, getter, registriesRoot), fs, generatorsRoot, rep), nil
	case GeneratorComposerJSON:
		return newGenerator(composerJSON{}, registry.NewPackagist(fs, clock, getter, registriesRoot), fs, generatorsRoot, rep), nil
	default:
		return nil, fmt.Errorf("generator: unknown generator %q", name)
	}
}

func newGenerator(eco ecosystem, lookup lookuper, fs sysdep.FileSystem, generatorsRoot string, rep progress.Reporter) *Generator {
	return &Generator{eco: eco, lookup: lookup, fs: fs, generatorsRoot: generatorsRoot, rep: progress.OrNop(rep)}
}

// Generate returns the annotated allow rules for the manifest's direct dependencies,
// serving a cached rule set when this exact manifest has been generated before (see
// cache.go) and otherwise walking the dependencies via generate and caching the
// result. A cache hit makes zero registry lookups.
func (g *Generator) Generate(ctx context.Context, manifest []byte) ([]Rule, error) {
	path := g.cachePath(manifest)
	if rules, ok, err := g.readCache(path); err != nil {
		return nil, err
	} else if ok {
		g.rep.ManifestCached()
		return rules, nil
	}

	rules, err := g.generate(ctx, manifest)
	if err != nil {
		return nil, err
	}
	if err := g.writeCache(path, rules); err != nil {
		return nil, err
	}
	return rules, nil
}

// InvalidationStats reports what one Invalidate call cleared for a manifest: how many
// direct dependencies it considered (Packages), how many of those had a cached
// registry entry that was actually removed (CacheEntriesCleared), and whether this
// generator's own output-cache entry for the manifest existed and was removed.
type InvalidationStats struct {
	Packages            int
	CacheEntriesCleared int
	OutputCacheCleared  bool
}

// Invalidate clears the cached state this generator would consult for manifest so the
// next Generate re-fetches from the registry: its own output-cache entry (keyed by the
// manifest hash) and each direct dependency's registry metadata entry. It walks the
// same dependency set as Generate, so refresh and a following compile agree on scope.
// A dependency whose registry entry was already absent is counted in Packages but not
// CacheEntriesCleared.
func (g *Generator) Invalidate(manifest []byte) (InvalidationStats, error) {
	var stats InvalidationStats

	cleared, err := sysdep.RemoveIfPresent(g.fs, g.cachePath(manifest))
	if err != nil {
		return InvalidationStats{}, err
	}
	stats.OutputCacheCleared = cleared

	deps, err := g.eco.deps(manifest)
	if err != nil {
		return InvalidationStats{}, err
	}
	for _, pkg := range deps {
		stats.Packages++
		existed, err := g.lookup.Invalidate(pkg)
		if err != nil {
			return InvalidationStats{}, fmt.Errorf("generator: invalidate %q: %w", pkg, err)
		}
		if existed {
			stats.CacheEntriesCleared++
		}
	}
	return stats, nil
}

// generate is the uncached dependency walk. For each dependency it looks up the
// package metadata and emits a homepage rule (when present) and the repository +
// forge-companion rules (when present); a missing homepage/repository, or a package
// the registry does not know (ErrNotFound), simply contributes no rule rather than
// failing the run. The output is ordered deterministically: dependencies sorted, then
// homepage before repository, then forge hosts in table order.
func (g *Generator) generate(ctx context.Context, manifest []byte) ([]Rule, error) {
	deps, err := g.eco.deps(manifest)
	if err != nil {
		return nil, err
	}
	g.rep.LookupsStart(len(deps))
	var rules []Rule
	for i, pkg := range deps {
		md, err := g.lookup.Lookup(ctx, pkg)
		if err != nil && !errors.Is(err, registry.ErrNotFound) {
			return nil, fmt.Errorf("generator: lookup %q: %w", pkg, err)
		}
		g.rep.LookupDone(i+1, len(deps))
		if err != nil {
			// Not found: the package contributes no rules (e.g. a path repository),
			// but its lookup completed and counts toward the progress walk.
			continue
		}
		src := source(g.eco.name(), pkg)
		if md.Homepage != "" {
			if r, ok := homepageRule(md.Homepage, src); ok {
				rules = append(rules, r)
			}
		}
		rules = append(rules, repositoryRules(md.Repository, src)...)
	}
	return rules, nil
}

// source renders the provenance annotation for a rule produced by generator gen for
// package pkg: "generated:<gen>:<pkg>", matching the design's `policy show` format.
func source(gen, pkg string) string {
	return "generated:" + gen + ":" + pkg
}
