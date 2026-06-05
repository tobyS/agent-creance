package generator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// Output-cache file/dir permissions, mirroring the registry cache.
const (
	cacheDirPerm    = 0o755
	cacheFilePerm   = 0o644
	cacheTempSuffix = ".tmp"
)

// cacheRecord is the on-disk output-cache envelope: the emitted rules under a named
// key, so the file is self-describing and can grow new fields without breaking older
// readers.
type cacheRecord struct {
	Rules []Rule `json:"rules"`
}

// cacheKey is the hex SHA-256 of the manifest bytes. The cache is content-addressed:
// an unchanged manifest hashes the same and hits the cache; any edit misses. There is
// no TTL — per-package metadata freshness is the registry client's concern (its 30-day
// cache); the manifest-hash cache only short-circuits re-walking an unchanged manifest.
func cacheKey(manifest []byte) string {
	sum := sha256.Sum256(manifest)
	return hex.EncodeToString(sum[:])
}

// cachePath is <generatorsRoot>/<generator>/<manifest-hash>.json.
func (g *Generator) cachePath(manifest []byte) string {
	return filepath.Join(g.generatorsRoot, g.eco.name(), cacheKey(manifest)+".json")
}

// readCache returns the cached rules for path. An absent file (miss) or an unparseable
// one (treated as a miss so a corrupt cache self-heals on the next run) returns
// (nil, false, nil); a genuine read error is surfaced.
func (g *Generator) readCache(path string) ([]Rule, bool, error) {
	data, err := g.fs.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("generator: read cache %q: %w", path, err)
	}
	var rec cacheRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, false, nil
	}
	return rec.Rules, true, nil
}

// writeCache atomically writes rules to path: create the parent dir, write a temp file,
// then rename it into place, so a crash mid-write never leaves a torn cache entry
// (the same idiom as registry.writeCache).
func (g *Generator) writeCache(path string, rules []Rule) error {
	if err := g.fs.MkdirAll(filepath.Dir(path), cacheDirPerm); err != nil {
		return fmt.Errorf("generator: create cache dir for %q: %w", path, err)
	}
	data, err := json.MarshalIndent(cacheRecord{Rules: rules}, "", "  ")
	if err != nil {
		return fmt.Errorf("generator: encode cache %q: %w", path, err)
	}
	data = append(data, '\n')

	tmp := path + cacheTempSuffix
	if err := g.fs.WriteFile(tmp, data, cacheFilePerm); err != nil {
		return fmt.Errorf("generator: write cache %q: %w", tmp, err)
	}
	if err := g.fs.Rename(tmp, path); err != nil {
		_ = g.fs.Remove(tmp) // best-effort cleanup; the temp file is otherwise orphaned
		return fmt.Errorf("generator: commit cache %q: %w", path, err)
	}
	return nil
}
