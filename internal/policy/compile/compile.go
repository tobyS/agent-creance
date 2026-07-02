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
	"strings"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/generator"
	"github.com/tobyS/agent-creance/internal/policy"
	"github.com/tobyS/agent-creance/internal/progress"
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

// resolvedGenerator is a generator entry with a concrete manifest path: a bare config
// entry's empty Path has been filled from the generator's default manifest filename
// (generator.Lookup), so two entries of the same type for different packages are
// distinct here. The path is relative to the project root.
type resolvedGenerator struct {
	Type string
	Path string
}

// manifestInput pairs a resolved generator with its manifest bytes (only generators
// whose manifest exists on disk get one).
type manifestInput struct {
	gen  resolvedGenerator
	data []byte
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
// over the shared registry/output cache roots and runs it, forwarding the compiler's
// progress reporter so the per-dependency lookup events reach the run command's printer.
type realGenerators struct {
	fs             sysdep.FileSystem
	clock          sysdep.Clock
	getter         sysdep.HTTPGetter
	registriesRoot string
	generatorsRoot string
	rep            progress.Reporter
}

func (r realGenerators) Run(ctx context.Context, name string, manifest []byte) ([]generator.Rule, error) {
	g, err := generator.New(name, r.fs, r.clock, r.getter, r.registriesRoot, r.generatorsRoot, r.rep)
	if err != nil {
		return nil, err
	}
	return g.Generate(ctx, manifest)
}

func (r realGenerators) Invalidate(name string, manifest []byte) (generator.InvalidationStats, error) {
	g, err := generator.New(name, r.fs, r.clock, r.getter, r.registriesRoot, r.generatorsRoot, nil)
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
	rep    progress.Reporter
}

// New wires a Compiler with the OS seams and the production generator runner. The
// registry/generator cache roots are resolved up front (project-independent). rep
// receives live compile-progress events (run wires its printer); nil means silent.
func New(fsys sysdep.FileSystem, paths sysdep.PathResolver, clock sysdep.Clock, getter sysdep.HTTPGetter, rep progress.Reporter) (*Compiler, error) {
	st := state.New(paths)
	registriesRoot, err := st.RegistriesRoot()
	if err != nil {
		return nil, fmt.Errorf("compile: registries root: %w", err)
	}
	generatorsRoot, err := st.GeneratorsRoot()
	if err != nil {
		return nil, fmt.Errorf("compile: generators root: %w", err)
	}
	rep = progress.OrNop(rep)
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
			rep:            rep,
		},
		rep: rep,
	}, nil
}

// reporter normalizes the progress sink at the emission sites, so a Compiler built by
// struct literal (the tests do this, bypassing New) stays valid without setting rep.
func (c *Compiler) reporter() progress.Reporter {
	return progress.OrNop(c.rep)
}

// manifestRefs projects the manifest inputs into the progress events' shape.
func manifestRefs(manifests []manifestInput) []progress.ManifestRef {
	refs := make([]progress.ManifestRef, len(manifests))
	for i, m := range manifests {
		refs[i] = progress.ManifestRef{Type: m.gen.Type, Path: m.gen.Path}
	}
	return refs
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
	manifests []manifestInput
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

	gens, err := resolveGenerators(mergeGenerators(global.Network.Egress.Generators, project.Network.Egress.Generators))
	if err != nil {
		return compileInputs{}, err
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

	// Only a gate miss with lookup work ahead warrants the expectation message;
	// a build with no manifests has nothing slow to explain.
	if len(in.manifests) > 0 {
		c.reporter().BuildStart(manifestRefs(in.manifests))
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
	for _, m := range in.manifests {
		stats, err := c.runner.Invalidate(m.gen.Type, m.data)
		if err != nil {
			return RefreshResult{}, fmt.Errorf("compile: refresh generator %q: %w", m.gen.Type, err)
		}
		refreshed = append(refreshed, GeneratorRefresh{
			Name:                refreshName(m.gen),
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
	rs, err := c.buildRuleSet(ctx, in.global, in.project, in.overlay, in.manifests)
	if err != nil {
		return Result{}, err
	}

	// Credentials come only from config layers (global → project → overlay); the
	// compiled artifact carries references, never resolved values.
	creds := policy.CredentialsFromConfig(mergeCredentials(in.global.Credentials, in.project.Credentials, in.overlay.Credentials))

	// Fail closed on an inject that names no defined credential. The per-layer Parse
	// only validates within a document, and the compiler bypasses Loader.Load's
	// merged-view check, so this is where the cross-reference is enforced before the
	// proxy ever reads the policy.
	if err := validateInjectRefs(rs, creds); err != nil {
		return Result{}, err
	}

	compiled := policy.Compiled{
		Version:     policy.CompiledVersion,
		InputHash:   in.hash,
		Credentials: creds,
		RuleSet:     rs,
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

// mergeCredentials unions the credentials: maps from each config layer, later layers
// winning on a name collision (global → project → overlay). It returns nil when empty
// so the compiled artifact omits the block.
func mergeCredentials(layers ...map[string]config.Credential) map[string]config.Credential {
	out := map[string]config.Credential{}
	for _, layer := range layers {
		for name, cred := range layer {
			out[name] = cred
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateInjectRefs fails the compile closed if any rule injects a credential that no
// layer defines — the merged-view counterpart to config.ValidateEffective, enforced
// here because the compiler reads layers separately and bypasses Loader.Load.
func validateInjectRefs(rs policy.RuleSet, creds map[string]policy.Credential) error {
	check := func(rules []policy.Rule) error {
		for _, r := range rules {
			if r.Inject == "" {
				continue
			}
			if _, ok := creds[r.Inject]; !ok {
				return fmt.Errorf("compile: rule for host %q injects credential %q, which is not defined in the credentials block", r.Host, r.Inject)
			}
		}
		return nil
	}
	if err := check(rs.Allow); err != nil {
		return err
	}
	return check(rs.DenyAlways)
}

// mergeGenerators unions the global and project generator lists (global first), deduping
// by (Type, Path) while preserving first-seen order.
func mergeGenerators(global, project []config.Generator) []config.Generator {
	seen := map[config.Generator]bool{}
	var out []config.Generator
	for _, list := range [][]config.Generator{global, project} {
		for _, g := range list {
			if !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	return out
}

// resolveGenerators validates each generator name and fills in a bare entry's manifest
// path from the generator's default filename, then dedupes by resolved (Type, Path) so
// two entries that point at the same manifest (e.g. a bare `package_json` and an
// explicit `package_json: package.json`) run only once. Order is preserved.
func resolveGenerators(gens []config.Generator) ([]resolvedGenerator, error) {
	seen := map[resolvedGenerator]bool{}
	var out []resolvedGenerator
	for _, g := range gens {
		meta, ok := generator.Lookup(g.Type)
		if !ok {
			return nil, fmt.Errorf("compile: unknown generator %q", g.Type)
		}
		path := g.Path
		if path == "" {
			path = meta.ManifestFile
		}
		rg := resolvedGenerator{Type: g.Type, Path: filepath.Clean(path)}
		if seen[rg] {
			continue
		}
		seen[rg] = true
		out = append(out, rg)
	}
	return out, nil
}

// refreshName labels a generator in the `policy refresh` report, disambiguating
// sub-package manifests by path (a root manifest keeps the bare type name).
func refreshName(rg resolvedGenerator) string {
	if filepath.Dir(rg.Path) == "." {
		return rg.Type
	}
	return rg.Type + " (" + rg.Path + ")"
}

// readManifests reads each resolved generator's manifest. A generator whose manifest is
// absent contributes nothing (no rules) rather than failing the compile.
func (c *Compiler) readManifests(projectDir string, gens []resolvedGenerator) ([]manifestInput, error) {
	var manifests []manifestInput
	for _, rg := range gens {
		data, err := c.fs.ReadFile(filepath.Join(projectDir, rg.Path))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("compile: read %s: %w", rg.Path, err)
		}
		manifests = append(manifests, manifestInput{gen: rg, data: data})
	}
	return manifests, nil
}

// inputHash is the cache key: SHA-256 over a canonical serialization of the resolved
// config layers plus the referenced manifest bytes. json.Marshal sorts map keys and the
// config structs marshal in fixed field order, so it is deterministic and
// environment-independent (a resolved Config carries no absolute paths).
func inputHash(global, project, overlay *config.Config, manifests []manifestInput) (string, error) {
	// Key by "type:path" so two manifests of the same type are distinct in the payload
	// (a type-only key would let the second overwrite the first). Editing any referenced
	// manifest changes its bytes here and so changes the hash.
	man := make(map[string]string, len(manifests))
	for _, m := range manifests {
		man[m.gen.Type+":"+m.gen.Path] = string(m.data)
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
func (c *Compiler) buildRuleSet(ctx context.Context, global, project, overlay *config.Config, manifests []manifestInput) (policy.RuleSet, error) {
	generated, err := c.runGenerators(ctx, manifests)
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

// runGenerators runs each resolved generator over its manifest and flattens the output
// into matcher rules carrying the generator's source annotation and lower-trust flag.
// For a manifest below the project root the manifest path is woven into the source so
// `policy show` can disambiguate which package produced a rule; a root manifest keeps
// the bare `generated:<type>:<pkg>` label, leaving single-repo output unchanged.
func (c *Compiler) runGenerators(ctx context.Context, manifests []manifestInput) ([]policy.Rule, error) {
	rep := c.reporter()
	var out []policy.Rule
	for _, m := range manifests {
		rep.ManifestStart(progress.ManifestRef{Type: m.gen.Type, Path: m.gen.Path})
		rules, err := c.runner.Run(ctx, m.gen.Type, m.data)
		if err != nil {
			return nil, fmt.Errorf("compile: run generator %q: %w", m.gen.Type, err)
		}
		rep.ManifestDone()
		for _, gr := range rules {
			r := gr.Rule
			r.Source = sourceWithPath(gr.Source, m.gen)
			r.LowerTrust = gr.LowerTrust
			// Generators emit rules without a mode; default to intercept so every rule
			// in the artifact carries an explicit mode (the config layer already defaults
			// its rules, so this keeps the whole policy.json uniform and self-describing).
			if r.Mode == "" {
				r.Mode = policy.ModeIntercept
			}
			// Generator-emitted rules bypass the config loader's validation, so apply the
			// same host/method checks here — a hostile or malformed generated rule (e.g.
			// from a cloned repo's manifest) is caught at compile time rather than silently
			// never-matching (AC-0058 / F18).
			if err := config.ValidateHost(r.Host); err != nil {
				return nil, fmt.Errorf("compile: generator %q produced an invalid host %q: %w", m.gen.Type, r.Host, err)
			}
			if err := config.ValidateMethods(r.Methods); err != nil {
				return nil, fmt.Errorf("compile: generator %q produced a rule that %w", m.gen.Type, err)
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// sourceWithPath weaves a sub-package manifest's path into a generated rule's source
// label, turning "generated:<type>:<pkg>" into "generated:<type>:<path>:<pkg>". A root
// manifest (path has no directory component) is left untouched so single-repo policy
// output is byte-identical to before. The path is injected here, after the
// content-addressed output cache, so two identical-byte manifests at different paths
// still share a cache entry yet get distinct labels.
func sourceWithPath(src string, rg resolvedGenerator) string {
	if filepath.Dir(rg.Path) == "." {
		return src
	}
	prefix := "generated:" + rg.Type + ":"
	if !strings.HasPrefix(src, prefix) {
		return src // not a generated source (defensive); leave as-is.
	}
	return prefix + rg.Path + ":" + strings.TrimPrefix(src, prefix)
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
	// The auth axis (inject/in_cage) is part of a rule's identity — two otherwise
	// identical rules that inject differently (or one injects, one is in-cage) are
	// behaviourally distinct and must not collapse. Source/lower-trust stay excluded
	// (pure provenance).
	k := struct {
		Host    string   `json:"h"`
		Paths   []string `json:"p"`
		Methods []string `json:"m"`
		Mode    string   `json:"o"`
		Reason  string   `json:"r"`
		Inject  string   `json:"i"`
		InCage  bool     `json:"c"`
	}{r.Host, r.Paths, r.Methods, r.Mode, r.Reason, r.Inject, r.InCage}
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
