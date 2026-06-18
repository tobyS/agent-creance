// Package pluginmkt detects locally-sourced Claude Code plugin marketplaces so
// agent-creance can grant their source directories into the cage read-only.
//
// Claude Code records every registered marketplace in
// ~/.claude/plugins/known_marketplaces.json and, on each startup, loads each
// marketplace's catalog from <source.path>/.claude-plugin/marketplace.json. For a
// marketplace whose source is a local "directory" (or "file"), source.path is an
// arbitrary on-disk location; when it lies outside the cage's mounts the Seatbelt
// sandbox EPERM-denies the read and the marketplace — and its plugins — fail to
// load. Git/remote marketplaces (and installed-plugin caches) live under
// ~/.claude, which is already mounted, so only local-source roots need granting.
//
// Like internal/claudeimport, this reads third-party JSON leniently through a
// sysdep.FileSystem and never touches the OS directly, so it is unit-testable with
// the in-memory fake.
package pluginmkt

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// marketplaceEntry is the lenient on-disk shape of one value in
// known_marketplaces.json (keyed by marketplace name). Only the fields needed to
// find a local source dir are modelled; unknown keys are ignored.
type marketplaceEntry struct {
	Source struct {
		Source string `json:"source"` // "directory","file","github","url","git-subdir"
		Path   string `json:"path"`   // local source dir for "directory"/"file"
	} `json:"source"`
	InstallLocation string `json:"installLocation"`
}

// Detect reads ~/.claude/plugins/known_marketplaces.json and returns the absolute,
// canonicalized source directories of locally-sourced ("directory"/"file")
// marketplaces, for read-only mounting into the cage. Git/remote sources are
// skipped — they live under ~/.claude, already mounted.
//
// A missing registry file is normal (no marketplaces, or none local): no dirs, no
// warning. An unresolvable home dir, an unreadable or malformed registry, or a
// local source dir that does not exist on disk each yield one warning and are
// skipped — never fatal. Returned dirs are deduplicated and sorted.
func Detect(fsys sysdep.FileSystem, paths sysdep.PathResolver) (dirs []string, warns []string) {
	home, err := paths.UserHomeDir()
	if err != nil {
		return nil, []string{fmt.Sprintf("resolve home dir: %v", err)}
	}
	path := filepath.Join(home, ".claude", "plugins", "known_marketplaces.json")

	data, err := fsys.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			warns = append(warns, fmt.Sprintf("read %s: %v", path, err))
		}
		return nil, warns
	}
	var entries map[string]marketplaceEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, append(warns, fmt.Sprintf("parse %s: %v", path, err))
	}

	seen := map[string]bool{}
	for _, name := range sortedKeys(entries) {
		src := entries[name].Source
		if src.Source != "directory" && src.Source != "file" {
			continue // git/remote source: lives under ~/.claude, already mounted
		}
		raw := src.Path
		if raw == "" {
			raw = entries[name].InstallLocation
		}
		if raw == "" {
			continue
		}
		dir := canonical(paths, raw)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if info, err := fsys.Stat(dir); err != nil || !info.IsDir() {
			warns = append(warns, fmt.Sprintf("marketplace %q: source dir %s is missing or not a directory; skipping", name, dir))
			continue
		}
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs, warns
}

// canonical resolves raw to an absolute path and follows symlinks best-effort, so
// dedup here and the cage's own already-mounted comparison both work on one stable
// identity (mirrors internal/state's Abs+EvalSymlinks).
func canonical(paths sysdep.PathResolver, raw string) string {
	abs, err := paths.Abs(raw)
	if err != nil {
		abs = raw
	}
	if resolved, err := paths.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func sortedKeys(m map[string]marketplaceEntry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
