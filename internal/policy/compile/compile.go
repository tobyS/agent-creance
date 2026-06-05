// Package compile is the policy compiler (AC-0013): it unions a project's effective
// egress configuration — explicit project rules, the implicit global baseline,
// generator output, and the session-overlay's `allow --once` rules — into one
// source-annotated, versioned policy.json written to the project's out-of-tree state
// directory, gated by an input-hash cache so an unchanged config skips regeneration.
//
// It lives beside (not inside) the pure matcher package so internal/policy stays free
// of filesystem/clock/network I/O: this package does the side effects and orchestration,
// importing the matcher only for the artifact schema (policy.Compiled / policy.Rule).
//
// Provenance is recovered by loading each source layer separately through
// config.Loader.ResolveLayer — the fused config.Loader.Load flattens which file a rule
// came from, which the `source` annotation needs. The cache key is a SHA-256 over a
// canonical serialization of the resolved layers plus the referenced manifest bytes,
// stored in the artifact's input_hash field; the check runs *before* generators do, so a
// cache hit performs zero registry work and leaves policy.json (and its mtime) untouched.
package compile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/generator"
	"github.com/tobyS/agent-creance/internal/policy"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// Rule-source annotations the compiler stamps onto each rule. Generated rules carry the
// generator's own "generated:<gen>:<pkg>" source verbatim.
const (
	sourceExplicit = "explicit" // a project YAML rule (the project file or its includes)
	sourceGlobal   = "global"   // the implicit ~/.config/agent-creance.yaml baseline
	sourceOnce     = "once"     // a session-overlay `allow --once` rule
)

const (
	dirPerm   = 0o755
	filePerm  = 0o644
	tmpSuffix = ".tmp"
)

// projectConfigName is the per-project config file at the project root.
const projectConfigName = ".agent-creance.yaml"

// manifestFiles maps a generator name to the in-tree manifest it reads. The generator
// itself takes bytes, not a path, so the compiler owns this mapping (and the read).
var manifestFiles = map[string]string{
	generator.GeneratorPackageJSON:  "package.json",
	generator.GeneratorComposerJSON: "composer.json",
}

// generatorRunner runs one named generator over manifest bytes. It is the hermetic seam
// for the compiler's tests: production wires realGenerators (real registry fetches);
// tests inject a fake returning canned rules, so the compiler's own logic is exercised
// without HTTP (the generators are already covered by their own package's tests).
type generatorRunner interface {
	Run(ctx context.Context, name string, manifest []byte) ([]generator.Rule, error)
	// Invalidate clears the named generator's cached state for manifest (its output
	// cache and each dependency's registry entry), returning what it cleared. It is the
	// invalidation half `policy refresh` drives before forcing a rebuild.
	Invalidate(name string, manifest []byte) (generator.InvalidationStats, error)
}

// realGenerators is the production generatorRunner: it constructs the named generator
// over the shared registry/output cache roots and runs it.
type realGenerators struct {
	fs             sysdep.FileSystem
	clock          sysdep.Clock
	getter         sysdep.HTTPGetter
	registriesRoot string
	generatorsRoot string
}

func (r realGenerators) Run(ctx context.Context, name string, manifest []byte) ([]generator.Rule, error) {
	g, err := generator.New(name, r.fs, r.clock, r.getter, r.registriesRoot, r.generatorsRoot)
	if err != nil {
		return nil, err
	}
	return g.Generate(ctx, manifest)
}

func (r realGenerators) Invalidate(name string, manifest []byte) (generator.InvalidationStats, error) {
	g, err := generator.New(name, r.fs, r.clock, r.getter, r.registriesRoot, r.generatorsRoot)
	if err != nil {
		return generator.InvalidationStats{}, err
	}
	return g.Invalidate(manifest)
}

// Compiler compiles a project's effective config into its out-of-tree policy.json.
type Compiler struct {
	fs     sysdep.FileSystem
	loader *config.Loader
	state  *state.Resolver
	runner generatorRunner
}

// New wires a Compiler with the OS seams and the production generator runner. The
// registry/generator cache roots are resolved up front (project-independent).
func New(fsys sysdep.FileSystem, paths sysdep.PathResolver, clock sysdep.Clock, getter sysdep.HTTPGetter) (*Compiler, error) {
	st := state.New(paths)
	registriesRoot, err := st.RegistriesRoot()
	if err != nil {
		return nil, fmt.Errorf("compile: registries root: %w", err)
	}
	generatorsRoot, err := st.GeneratorsRoot()
	if err != nil {
		return nil, fmt.Errorf("compile: generators root: %w", err)
	}
	return &Compiler{
		fs:     fsys,
		loader: config.NewLoader(fsys, paths),
		state:  st,
		runner: realGenerators{
			fs:             fsys,
			clock:          clock,
			getter:         getter,
			registriesRoot: registriesRoot,
			generatorsRoot: generatorsRoot,
		},
	}, nil
}

// Result reports what Compile did. Skipped is true on a cache hit (no generators run, no
// file rewritten); the counts reflect the artifact's rule lists either way.
type Result struct {
	PolicyPath string
	InputHash  string
	Skipped    bool
	AllowCount int
	DenyCount  int
}

// GeneratorRefresh reports what Refresh cleared for one generator: the packages it
// considered, how many had a cached registry entry actually removed, and whether its
// output-cache entry existed and was removed.
type GeneratorRefresh struct {
	Name                string
	Packages            int
	CacheEntriesCleared int
	OutputCacheCleared  bool
}

// RefreshResult reports what Refresh did: the per-generator invalidation detail and the
// rule counts of the freshly recompiled policy.json.
type RefreshResult struct {
	PolicyPath string
	Generators []GeneratorRefresh
	AllowCount int
	DenyCount  int
}

// compileInputs is everything resolve() derives from a project directory before the
// cache gate: the resolved layout, the three config layers, the validated generator
// list, the referenced manifest bytes, and the input hash. Compile and Refresh share it.
type compileInputs struct {
	layout    state.Layout
	global    *config.Config
	project   *config.Config
	overlay   *config.Config
	gens      []string
	manifests map[string][]byte
	hash      string
}

// resolve loads projectDir's effective config layers, validates and merges the generator
// list, reads the referenced manifests, and computes the input hash — the shared prelude
// to Compile (which then checks the cache gate) and Refresh (which invalidates then
// rebuilds).
func (c *Compiler) resolve(projectDir string) (compileInputs, error) {
	layout, err := c.state.Resolve(projectDir)
	if err != nil {
		return compileInputs{}, err
	}

	globalPath, err := c.loader.GlobalPath()
	if err != nil {
		return compileInputs{}, err
	}
	global, err := c.loader.ResolveLayer(globalPath, true /*optional*/)
	if err != nil {
		return compileInputs{}, fmt.Errorf("compile: load global: %w", err)
	}
	project, err := c.loader.ResolveLayer(filepath.Join(layout.Canonical, projectConfigName), false /*required*/)
	if err != nil {
		return compileInputs{}, fmt.Errorf("compile: load project: %w", err)
	}
	overlay, err := c.loadOverlay(layout.SessionOverlay())
	if err != nil {
		return compileInputs{}, err
	}

	gens := mergeGenerators(global.Network.Egress.Generators, project.Network.Egress.Generators)
	for _, name := range gens {
		if !generator.Known(name) {
			return compileInputs{}, fmt.Errorf("compile: unknown generator %q", name)
		}
	}

	manifests, err := c.readManifests(layout.Canonical, gens)
	if err != nil {
		return compileInputs{}, err
	}

	hash, err := inputHash(global, project, overlay, manifests)
	if err != nil {
		return compileInputs{}, err
	}

	return compileInputs{
		layout:    layout,
		global:    global,
		project:   project,
		overlay:   overlay,
		gens:      gens,
		manifests: manifests,
		hash:      hash,
	}, nil
}

// Compile resolves projectDir's effective config and writes its policy.json, skipping
// regeneration when the input hash matches the cached artifact. Nothing is ever written
// inside the project tree — the artifact lives under the out-of-tree state directory.
func (c *Compiler) Compile(ctx context.Context, projectDir string) (Result, error) {
	in, err := c.resolve(projectDir)
	if err != nil {
		return Result{}, err
	}

	// Cache check precedes the generator run: a hit makes zero registry calls and leaves
	// the existing policy.json (and its mtime) in place so the proxy does not hot-reload.
	if existing, ok := c.readCompiled(in.layout.PolicyJSON()); ok &&
		existing.Version == policy.CompiledVersion && existing.InputHash == in.hash {
		return Result{
			PolicyPath: in.layout.PolicyJSON(),
			InputHash:  in.hash,
			Skipped:    true,
			AllowCount: len(existing.Allow),
			DenyCount:  len(existing.DenyAlways),
		}, nil
	}

	return c.build(ctx, in)
}

// Refresh forces a re-fetch of generator metadata and a recompile (WP-2.7): it
// invalidates each configured generator's cached state (output cache + the registry
// entries of its dependencies), then rebuilds policy.json unconditionally — bypassing
// the input-hash gate, since the inputs are unchanged but the caches were just cleared,
// so the rebuild re-runs the generators and re-hits the registry. Only this project's
// packages are touched; the cross-project registry/generator caches for other packages
// are left intact. It does not require the cage to be running.
func (c *Compiler) Refresh(ctx context.Context, projectDir string) (RefreshResult, error) {
	in, err := c.resolve(projectDir)
	if err != nil {
		return RefreshResult{}, err
	}

	var refreshed []GeneratorRefresh
	for _, name := range in.gens {
		manifest, ok := in.manifests[name]
		if !ok {
			continue // no manifest on disk → nothing was ever cached for this generator
		}
		stats, err := c.runner.Invalidate(name, manifest)
		if err != nil {
			return RefreshResult{}, fmt.Errorf("compile: refresh generator %q: %w", name, err)
		}
		refreshed = append(refreshed, GeneratorRefresh{
			Name:                name,
			Packages:            stats.Packages,
			CacheEntriesCleared: stats.CacheEntriesCleared,
			OutputCacheCleared:  stats.OutputCacheCleared,
		})
	}

	res, err := c.build(ctx, in)
	if err != nil {
		return RefreshResult{}, err
	}
	return RefreshResult{
		PolicyPath: res.PolicyPath,
		Generators: refreshed,
		AllowCount: res.AllowCount,
		DenyCount:  res.DenyCount,
	}, nil
}

// build runs the generators, unions every source into the rule set, and writes
// policy.json — the shared tail of Compile (on a cache miss) and Refresh (always). It
// never consults the cache gate; the caller decides whether to skip it.
func (c *Compiler) build(ctx context.Context, in compileInputs) (Result, error) {
	rs, err := c.buildRuleSet(ctx, in.global, in.project, in.overlay, in.gens, in.manifests)
	if err != nil {
		return Result{}, err
	}

	compiled := policy.Compiled{
		Version:   policy.CompiledVersion,
		InputHash: in.hash,
		RuleSet:   rs,
	}
	if err := c.write(in.layout, compiled); err != nil {
		return Result{}, err
	}
	return Result{
		PolicyPath: in.layout.PolicyJSON(),
		InputHash:  in.hash,
		Skipped:    false,
		AllowCount: len(rs.Allow),
		DenyCount:  len(rs.DenyAlways),
	}, nil
}

// loadOverlay reads and parses the session-overlay file. An absent overlay is the common
// case (no `allow --once` in this session) and yields an empty Config, not an error.
func (c *Compiler) loadOverlay(path string) (*config.Config, error) {
	data, err := c.fs.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &config.Config{}, nil
		}
		return nil, fmt.Errorf("compile: read session overlay: %w", err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("compile: parse session overlay: %w", err)
	}
	return cfg, nil
}

// mergeGenerators unions the global and project generator lists (global first), deduping
// while preserving first-seen order.
func mergeGenerators(global, project []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{global, project} {
		for _, name := range list {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// readManifests reads each generator's in-tree manifest. A listed generator whose
// manifest is absent contributes nothing (no rules) rather than failing the compile.
func (c *Compiler) readManifests(projectDir string, gens []string) (map[string][]byte, error) {
	manifests := map[string][]byte{}
	for _, name := range gens {
		file, ok := manifestFiles[name]
		if !ok {
			continue // Known() already validated the name; defensive.
		}
		data, err := c.fs.ReadFile(filepath.Join(projectDir, file))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("compile: read %s: %w", file, err)
		}
		manifests[name] = data
	}
	return manifests, nil
}

// inputHash is the cache key: SHA-256 over a canonical serialization of the resolved
// config layers plus the referenced manifest bytes. json.Marshal sorts map keys and the
// config structs marshal in fixed field order, so it is deterministic and
// environment-independent (a resolved Config carries no absolute paths).
func inputHash(global, project, overlay *config.Config, manifests map[string][]byte) (string, error) {
	man := make(map[string]string, len(manifests))
	for name, data := range manifests {
		man[name] = string(data)
	}
	payload := struct {
		Global    *config.Config    `json:"global"`
		Project   *config.Config    `json:"project"`
		Overlay   *config.Config    `json:"overlay"`
		Manifests map[string]string `json:"manifests"`
	}{global, project, overlay, man}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("compile: hash inputs: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// readCompiled reads the existing artifact for the cache check. A missing or corrupt file
// is reported as "no usable cache" so the compiler recomputes rather than erroring.
func (c *Compiler) readCompiled(path string) (policy.Compiled, bool) {
	data, err := c.fs.ReadFile(path)
	if err != nil {
		return policy.Compiled{}, false
	}
	var compiled policy.Compiled
	if err := json.Unmarshal(data, &compiled); err != nil {
		return policy.Compiled{}, false
	}
	return compiled, true
}

// buildRuleSet unions the annotated rules from every source into the compiled rule set.
// Order is baseline → project → generated → session (then dedupe); matching itself is
// order-independent (most-specific-wins), so this only fixes a stable, readable artifact.
func (c *Compiler) buildRuleSet(ctx context.Context, global, project, overlay *config.Config, gens []string, manifests map[string][]byte) (policy.RuleSet, error) {
	generated, err := c.runGenerators(ctx, gens, manifests)
	if err != nil {
		return policy.RuleSet{}, err
	}

	var allow []policy.Rule
	allow = append(allow, annotate(global.Network.Egress.Allow, sourceGlobal)...)
	allow = append(allow, annotate(project.Network.Egress.Allow, sourceExplicit)...)
	allow = append(allow, generated...)
	allow = append(allow, annotate(overlay.Network.Egress.Allow, sourceOnce)...)

	var deny []policy.Rule
	deny = append(deny, annotate(global.Network.Egress.DenyAlways, sourceGlobal)...)
	deny = append(deny, annotate(project.Network.Egress.DenyAlways, sourceExplicit)...)
	deny = append(deny, annotate(overlay.Network.Egress.DenyAlways, sourceOnce)...)

	return policy.RuleSet{
		Allow:      dedupe(allow),
		DenyAlways: dedupe(deny),
	}, nil
}

// runGenerators runs each configured generator and flattens its output into matcher
// rules carrying the generator's source annotation and lower-trust flag.
func (c *Compiler) runGenerators(ctx context.Context, gens []string, manifests map[string][]byte) ([]policy.Rule, error) {
	var out []policy.Rule
	for _, name := range gens {
		manifest, ok := manifests[name]
		if !ok {
			continue // manifest absent → no rules
		}
		rules, err := c.runner.Run(ctx, name, manifest)
		if err != nil {
			return nil, fmt.Errorf("compile: run generator %q: %w", name, err)
		}
		for _, gr := range rules {
			r := gr.Rule
			r.Source = gr.Source
			r.LowerTrust = gr.LowerTrust
			// Generators emit rules without a mode; default to intercept so every rule
			// in the artifact carries an explicit mode (the config layer already defaults
			// its rules, so this keeps the whole policy.json uniform and self-describing).
			if r.Mode == "" {
				r.Mode = policy.ModeIntercept
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// annotate converts config rules to matcher rules and stamps them with a source.
func annotate(rules []config.Rule, source string) []policy.Rule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]policy.Rule, len(rules))
	for i, r := range rules {
		pr := policy.RuleFromConfig(r)
		pr.Source = source
		out[i] = pr
	}
	return out
}

// dedupe drops later rules that are identical to an earlier one on the matcher-relevant
// fields (source/lower-trust are ignored), keeping the first occurrence. With the
// baseline-first ordering this mirrors the loader's union-with-dedupe semantics.
func dedupe(rules []policy.Rule) []policy.Rule {
	if len(rules) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []policy.Rule
	for _, r := range rules {
		key := ruleKey(r)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

func ruleKey(r policy.Rule) string {
	k := struct {
		Host    string   `json:"h"`
		Paths   []string `json:"p"`
		Methods []string `json:"m"`
		Mode    string   `json:"o"`
		Reason  string   `json:"r"`
	}{r.Host, r.Paths, r.Methods, r.Mode, r.Reason}
	b, _ := json.Marshal(k)
	return string(b)
}

// write marshals the artifact and writes it atomically (temp file then rename) under the
// out-of-tree state directory, mirroring the generator cache's write idiom.
func (c *Compiler) write(layout state.Layout, compiled policy.Compiled) error {
	data, err := json.MarshalIndent(compiled, "", "  ")
	if err != nil {
		return fmt.Errorf("compile: marshal policy: %w", err)
	}
	data = append(data, '\n')

	if err := c.fs.MkdirAll(layout.Root, dirPerm); err != nil {
		return fmt.Errorf("compile: create state dir: %w", err)
	}
	dest := layout.PolicyJSON()
	tmp := dest + tmpSuffix
	if err := c.fs.WriteFile(tmp, data, filePerm); err != nil {
		return fmt.Errorf("compile: write policy: %w", err)
	}
	if err := c.fs.Rename(tmp, dest); err != nil {
		_ = c.fs.Remove(tmp)
		return fmt.Errorf("compile: finalize policy: %w", err)
	}
	return nil
}
