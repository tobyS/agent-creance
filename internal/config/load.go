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

	eff.Include = nil
	return &eff, nil
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
		incPath := l.resolveIncludePath(abs, home, inc)
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

// resolveIncludePath turns an include: entry into a path to read. A leading ~/ expands
// against the home directory; an absolute path is used verbatim; a relative path is
// resolved against the directory of the file that declared the include.
func (l *Loader) resolveIncludePath(declaringAbs, home, inc string) string {
	switch {
	case strings.HasPrefix(inc, "~/"):
		return filepath.Join(home, inc[len("~/"):])
	case filepath.IsAbs(inc):
		return inc
	default:
		return filepath.Join(filepath.Dir(declaringAbs), inc)
	}
}

// renderCycle formats the resolution stack plus the revisited node as a readable
// chain, e.g. "/a.yaml -> /b.yaml -> /a.yaml".
func renderCycle(stack []string, repeat string) string {
	return strings.Join(append(append([]string{}, stack...), repeat), " -> ")
}
