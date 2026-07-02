package config

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// maxIncludeDepth bounds include: nesting. Cycle detection already stops infinite
// loops; this is the secondary guard against a pathological-but-acyclic deep chain.
// It is a fixed constant, not a config field: a security tool's safety limit should
// not be adjustable from the very config it guards.
const maxIncludeDepth = 10

// Loader resolves a project's effective configuration: the implicit global baseline
// (~/.config/agent-creance.yaml, skipped when absent) plus the project document and
// its recursively-included files, merged into one Config.
//
// Merge precedence runs low → high: the global's includes, then the global's own
// values, then the project's includes (in listed order), then the project's own
// values. An including file's own values are applied last, so it overrides what it
// includes — the same way a project config overrides the global. Scalars override and
// list fields union-with-dedupe per merge() / docs/design.md:151.
//
// The Loader is the filesystem-aware counterpart to the pure Parse: it reads through
// the injected sysdep seams (FileSystem for contents, PathResolver for the home dir
// and canonical paths), so its behaviour — including cycle and depth handling — is
// unit-tested hermetically against the fakes in sysdeptest.
type Loader struct {
	fs    sysdep.FileSystem
	paths sysdep.PathResolver
}

// NewLoader wires a Loader with the filesystem and path-resolution seams.
func NewLoader(fsys sysdep.FileSystem, paths sysdep.PathResolver) *Loader {
	return &Loader{fs: fsys, paths: paths}
}

// Load resolves the implicit global plus the project config at projectPath into one
// effective Config. include: directives are resolved recursively with cycle detection
// and a depth limit; a missing global is a no-op while a missing project file or a
// missing declared include is an error. The returned Config has Include cleared (it is
// fully resolved).
func (l *Loader) Load(projectPath string) (*Config, error) {
	eff := Config{}

	home, err := l.paths.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("config: locate home directory: %w", err)
	}
	globalPath := filepath.Join(home, ".config", "agent-creance.yaml")

	global, err := l.resolve(globalPath, home, true /*optional*/, nil, 0)
	if err != nil {
		return nil, err
	}
	eff = merge(eff, *global)

	project, err := l.resolve(projectPath, home, false /*optional*/, nil, 0)
	if err != nil {
		return nil, err
	}
	eff = merge(eff, *project)

	// Cross-layer checks run only on the merged view: a project rule may inject a
	// credential defined in the global baseline, so inject → credential resolution
	// must wait until both layers are fused. Hard errors fail the load; warnings ride
	// along on the effective config for the caller to surface.
	warnings, err := eff.ValidateEffective()
	if err != nil {
		return nil, err
	}
	eff.Warnings = warnings

	eff.Include = nil
	return &eff, nil
}

// GlobalPath reports the implicit global config path (~/.config/agent-creance.yaml) —
// the baseline Load merges under every project. It is exported so the policy compiler
// can load that layer on its own (Load fuses it into the project and discards which
// rules came from where).
func (l *Loader) GlobalPath() (string, error) {
	home, err := l.paths.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locate home directory: %w", err)
	}
	return filepath.Join(home, ".config", "agent-creance.yaml"), nil
}

// ResolveLayer resolves a single config file and its include: chain into one Config,
// *without* merging the implicit global. It is what the policy compiler uses to keep the
// global / project / session-overlay layers separate so each rule can be annotated with
// its source — the fused Load() flattens that provenance away. optional=true makes a
// not-exist file yield an empty Config (used for the global baseline and the overlay,
// both of which may be absent); optional=false makes a missing file an error. The
// returned Config has Include cleared (it is fully resolved).
func (l *Loader) ResolveLayer(path string, optional bool) (*Config, error) {
	home, err := l.paths.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("config: locate home directory: %w", err)
	}
	cfg, err := l.resolve(path, home, optional, nil, 0)
	if err != nil {
		return nil, err
	}
	cfg.Include = nil
	return cfg, nil
}

// ResolveFiles returns the canonical absolute paths of every file that contributes
// to the effective project config: the implicit global baseline (if present), the
// project file, and all transitively-included fragments. It is the watch-set
// counterpart to Load — same include rules, cycle detection, and depth limit — but
// accumulates file paths instead of merging values, so the run-session config
// watcher knows which files to watch. Paths are symlink-resolved (the on-disk
// identity a file watcher observes) and deduplicated; order is unspecified. A
// missing global is a no-op; a missing project file or declared include is an error.
func (l *Loader) ResolveFiles(projectPath string) ([]string, error) {
	home, err := l.paths.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("config: locate home directory: %w", err)
	}
	globalPath := filepath.Join(home, ".config", "agent-creance.yaml")

	seen := map[string]struct{}{}
	var out []string
	// Shared seen across both trees collapses a file reachable from each into one
	// entry; the separate stacks keep cycle detection per resolution tree.
	if err := l.collectFiles(globalPath, home, true /*optional*/, seen, &out, nil, 0); err != nil {
		return nil, err
	}
	if err := l.collectFiles(projectPath, home, false /*required*/, seen, &out, nil, 0); err != nil {
		return nil, err
	}
	return out, nil
}

// collectFiles is the path-accumulating counterpart to resolve: it walks the same
// include graph (same resolveIncludePath rules, cycle detection via the canonical
// stack, and maxIncludeDepth) but records each file's canonical path into out/seen
// instead of merging its values. seen deduplicates files reachable on more than one
// branch (e.g. a diamond).
func (l *Loader) collectFiles(path, home string, optional bool, seen map[string]struct{}, out *[]string, stack []string, depth int) error {
	if depth > maxIncludeDepth {
		return fmt.Errorf("%w: %s", ErrMaxIncludeDepth, path)
	}

	abs, err := l.paths.Abs(path)
	if err != nil {
		return fmt.Errorf("config: resolve path %s: %w", path, err)
	}

	data, err := l.fs.ReadFile(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if optional {
				return nil
			}
			return fmt.Errorf("config: file not found: %s: %w", path, err)
		}
		return fmt.Errorf("config: read %s: %w", path, err)
	}

	canon, err := l.paths.EvalSymlinks(abs)
	if err != nil {
		canon = abs
	}
	for _, s := range stack {
		if s == canon {
			return fmt.Errorf("%w: %s", ErrIncludeCycle, renderCycle(stack, canon))
		}
	}
	if _, ok := seen[canon]; ok {
		return nil
	}
	seen[canon] = struct{}{}
	*out = append(*out, canon)

	cfg, err := Parse(data)
	if err != nil {
		return fmt.Errorf("config: %s: %w", path, err)
	}

	stack = append(stack, canon)
	for _, inc := range cfg.Include {
		incPath, err := l.resolveIncludePath(abs, home, inc)
		if err != nil {
			return fmt.Errorf("config: %s: %w", path, err)
		}
		if err := l.collectFiles(incPath, home, false /*required*/, seen, out, stack, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// resolve reads, parses, and recursively resolves one config file into a fully-merged
// Config. stack holds the canonical paths currently on the resolution path (for cycle
// detection); depth counts include nesting. When optional is true a not-exist file
// yields an empty Config rather than an error (used for the implicit global only).
func (l *Loader) resolve(path, home string, optional bool, stack []string, depth int) (*Config, error) {
	if depth > maxIncludeDepth {
		return nil, fmt.Errorf("%w: %s", ErrMaxIncludeDepth, path)
	}

	abs, err := l.paths.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: resolve path %s: %w", path, err)
	}

	data, err := l.fs.ReadFile(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if optional {
				return &Config{}, nil
			}
			return nil, fmt.Errorf("config: file not found: %s: %w", path, err)
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	// The file exists, so canonicalising it is safe (EvalSymlinks needs the target on
	// disk). The canonical path is the cycle-detection identity, so a symlinked alias
	// of an ancestor is caught as a cycle rather than re-read forever.
	canon, err := l.paths.EvalSymlinks(abs)
	if err != nil {
		canon = abs
	}
	for _, seen := range stack {
		if seen == canon {
			return nil, fmt.Errorf("%w: %s", ErrIncludeCycle, renderCycle(stack, canon))
		}
	}

	cfg, err := Parse(data)
	if err != nil {
		// Prefix the per-file validation error with the offending path so a problem in
		// an included file is locatable.
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}

	stack = append(stack, canon)
	acc := Config{}
	for _, inc := range cfg.Include {
		incPath, err := l.resolveIncludePath(abs, home, inc)
		if err != nil {
			return nil, fmt.Errorf("config: %s: %w", path, err)
		}
		resolved, err := l.resolve(incPath, home, false /*required*/, stack, depth+1)
		if err != nil {
			return nil, err
		}
		acc = merge(acc, *resolved)
	}

	// The file's own values are the most specific layer: applied after its includes.
	cfg.Include = nil
	acc = merge(acc, *cfg)
	return &acc, nil
}

// ValidateInclude checks that include entry inc, as declared by the config file at
// declaringPath, resolves to a readable, parseable config file. It is the pre-write
// check for `agent-creance include`: it pinpoints a wrong or missing path before the
// entry is committed, so the user is never left with a config that only fails to
// compile later. It validates the single entry (resolve + read + parse) using the
// same form rules as the loader; it does not walk the whole include graph. The
// returned error names the resolved path.
func (l *Loader) ValidateInclude(declaringPath, inc string) error {
	home, err := l.paths.UserHomeDir()
	if err != nil {
		return fmt.Errorf("config: locate home directory: %w", err)
	}
	abs, err := l.paths.Abs(declaringPath)
	if err != nil {
		return fmt.Errorf("config: resolve path %s: %w", declaringPath, err)
	}
	incPath, err := l.resolveIncludePath(abs, home, inc)
	if err != nil {
		return err
	}

	data, err := l.fs.ReadFile(incPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("config: include not found: %s", incPath)
		}
		return fmt.Errorf("config: read include %s: %w", incPath, err)
	}
	if _, err := Parse(data); err != nil {
		return fmt.Errorf("config: include %s: %w", incPath, err)
	}
	return nil
}

// resolveIncludePath turns an include: entry into a path to read. A leading ~/ expands
// against the home directory; an absolute path is used verbatim; a relative path is
// resolved against the directory of the file that declared the include.
//
// The resolved path is then confined (AC-0059, F8): it must lie within the declaring
// file's own directory subtree or within the global config dir (~/.config). An
// absolute, ~/, or ..-escaping include that lands outside both is rejected with
// ErrIncludeOutOfScope so a cloned, untrusted .agent-creance.yaml cannot read
// arbitrary user files. The implicit global config file is loaded as a root path (not
// through here), so it is unaffected; only includes it *declares* pass through this
// check, and they are allowed via the ~/.config grant. The error names the include and
// its resolved path — never the file's contents — so an out-of-scope target is rejected
// before it is ever read or parsed.
func (l *Loader) resolveIncludePath(declaringAbs, home, inc string) (string, error) {
	var resolved string
	switch {
	case strings.HasPrefix(inc, "~/"):
		resolved = filepath.Join(home, inc[len("~/"):])
	case filepath.IsAbs(inc):
		resolved = inc
	default:
		resolved = filepath.Join(filepath.Dir(declaringAbs), inc)
	}

	declaringDir := filepath.Dir(declaringAbs)
	globalDir := filepath.Join(home, ".config")
	if pathWithin(declaringDir, resolved) || pathWithin(globalDir, resolved) {
		return resolved, nil
	}
	return "", fmt.Errorf("%w: %q resolves to %q (allowed: under %s or %s)",
		ErrIncludeOutOfScope, inc, resolved, declaringDir, globalDir)
}

// pathWithin reports whether target is dir itself or lies below it. Both paths are
// expected to be absolute and cleaned (filepath.Join/Dir produce cleaned paths); the
// check is lexical, which is sufficient here because the include scope is decided
// before any read so there is no on-disk symlink to resolve yet.
func pathWithin(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// renderCycle formats the resolution stack plus the revisited node as a readable
// chain, e.g. "/a.yaml -> /b.yaml -> /a.yaml".
func renderCycle(stack []string, repeat string) string {
	return strings.Join(append(append([]string{}, stack...), repeat), " -> ")
}
