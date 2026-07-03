// Package proxy embeds the mitmproxy enforcer addon in the agent-creance binary
// and extracts it to the out-of-tree state dir on first run. The addon is a
// constant — users never install or version the Python themselves; they just see
// "mitmproxy is running". AC-0020 starts mitmproxy with the extracted enforcer.py.
//
// The runtime addon is five modules, not one: enforcer.py imports the siblings
// policy, audit, responses and inject, so all five must be extracted together into
// one directory (mitmproxy puts a -s script's parent dir on sys.path, which is how
// those imports resolve). The pytest suite, conftest, requirements.txt and
// golden testdata that live alongside them are dev-only and are not shipped.
package proxy

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"

	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// enforcerFS holds the embedded addon. The directive enumerates the five runtime
// modules explicitly because `enforcer/*.py` would also embed the pytest suite
// (the embed directive has no exclude/negation).
//
//go:embed enforcer/enforcer.py enforcer/policy.py enforcer/audit.py enforcer/responses.py enforcer/inject.py
var enforcerFS embed.FS

const (
	// embedDir is the path prefix of the modules within enforcerFS. embed paths
	// are always slash-separated, regardless of OS.
	embedDir = "enforcer"
	// entrypointName is the addon mitmproxy loads with -s; Extract returns its path.
	entrypointName = "enforcer.py"

	dirPerm   = 0o755
	filePerm  = 0o644
	tmpSuffix = ".tmp"
)

// enforcerModules are the embedded addon files, relative to embedDir. enforcer.py
// is the entrypoint; the rest are the siblings it imports.
var enforcerModules = []string{"enforcer.py", "policy.py", "audit.py", "responses.py", "inject.py"}

// Extractor writes the embedded enforcer addon to the constant, cross-project
// enforcer dir through the injected filesystem seam.
type Extractor struct {
	fs    sysdep.FileSystem
	state *state.Resolver
}

// NewExtractor wires an Extractor from the OS seams.
func NewExtractor(fsys sysdep.FileSystem, paths sysdep.PathResolver) *Extractor {
	return &Extractor{fs: fsys, state: state.New(paths)}
}

// Extract writes the embedded addon modules to <cache>/agent-creance/enforcer/
// and returns the path to enforcer.py (the addon entrypoint AC-0020 hands to
// mitmproxy). It is idempotent: a module already matching the embedded bytes is
// left untouched; a missing or differing one is (re)written atomically, which is
// also how a binary upgrade refreshes the extraction. The only directory it
// creates is the enforcer root under the out-of-tree cache — never anything in
// the project tree (cross-cutting C4).
func (e *Extractor) Extract() (string, error) {
	root, err := e.state.EnforcerRoot()
	if err != nil {
		return "", err
	}
	if err := e.fs.MkdirAll(root, dirPerm); err != nil {
		return "", fmt.Errorf("proxy: create enforcer dir %q: %w", root, err)
	}
	for _, name := range enforcerModules {
		want, err := enforcerFS.ReadFile(path.Join(embedDir, name))
		if err != nil {
			return "", fmt.Errorf("proxy: read embedded %q: %w", name, err)
		}
		if err := e.writeIfChanged(filepath.Join(root, name), want); err != nil {
			return "", err
		}
	}
	return filepath.Join(root, entrypointName), nil
}

// writeIfChanged writes want to dest only when the current content differs (or
// dest is absent), atomically via a temp file + rename so a crash mid-write never
// leaves a torn module (the same idiom as generator.writeCache). A genuine read
// error on an existing file is surfaced; a corrupt/short file simply mismatches
// and is rewritten, so the extraction self-heals.
func (e *Extractor) writeIfChanged(dest string, want []byte) error {
	switch got, err := e.fs.ReadFile(dest); {
	case err == nil:
		if bytes.Equal(got, want) {
			return nil // already up to date
		}
	case errors.Is(err, fs.ErrNotExist):
		// first run for this file — fall through to write
	default:
		return fmt.Errorf("proxy: read extracted %q: %w", dest, err)
	}

	tmp := dest + tmpSuffix
	if err := e.fs.WriteFile(tmp, want, filePerm); err != nil {
		return fmt.Errorf("proxy: write %q: %w", tmp, err)
	}
	if err := e.fs.Rename(tmp, dest); err != nil {
		_ = e.fs.Remove(tmp) // best-effort cleanup; the temp file is otherwise orphaned
		return fmt.Errorf("proxy: commit %q: %w", dest, err)
	}
	return nil
}
