// Package registry fetches a package's homepage + repository metadata from a
// package registry (npm or Packagist) and caches it on disk so repeated runs do
// not re-hit the network.
//
// The cache lives at <cache>/agent-creance/registries/<registry>/<package>.json
// (see internal/state.RegistriesRoot) and is refreshed lazily: an entry younger
// than refreshInterval is reused without any network call; an older — or absent,
// or unparseable — entry triggers a single fetch that overwrites it. Cache age is
// read from a fetched_at field *inside* the file (via the injected sysdep.Clock),
// not from the file's mtime, so the behaviour is deterministic under test.
//
// All side effects go through sysdep seams (FileSystem, Clock, HTTPGetter)
// injected at construction, so unit tests are fully hermetic; real registry
// lookups exist only under the integration build tag. Turning the returned
// Metadata into allow rules is a separate concern (AC-0012); this package only
// fetches and caches.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/tobyS/agent-creance/internal/buildinfo"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// refreshInterval is the lazy-refresh window: a cache entry younger than this is
// reused without a network call (design default: 30 days).
const refreshInterval = 30 * 24 * time.Hour

// HTTP status codes the client branches on, named locally so the logic does not
// import net/http (only the sysdep HTTP seam touches the network).
const (
	statusOKMin     = 200
	statusOKMax     = 299
	statusNotFound  = 404
	cacheDirPerm    = 0o755
	cacheFilePerm   = 0o644
	cacheFileSuffix = ".json"
	cacheTempSuffix = ".tmp"
)

// userAgent identifies the client to registries. Packagist asks callers to send
// contact info so they can reach a misbehaving client; npm just wants a
// descriptive UA.
var userAgent = "agent-creance/" + buildinfo.Version + " (+https://github.com/tobyS/agent-creance; mailto:tobias@schlitt.info)"

// ErrNotFound reports that the registry has no such package (HTTP 404). Callers
// (AC-0012) treat this as "emit no rules" rather than a hard failure.
var ErrNotFound = errors.New("registry: package not found")

// Metadata is the subset of a package's registry record this project consumes:
// the homepage URL and the repository (VCS) URL, each stored verbatim as the
// registry reports it (the design trusts these without validation).
type Metadata struct {
	Homepage   string `json:"homepage"`
	Repository string `json:"repository"`
}

// source is the per-registry strategy: where to fetch a package and how to parse
// that registry's JSON shape into the common Metadata.
type source interface {
	// name is the cache directory segment for this registry ("npm"/"packagist").
	name() string
	// validate rejects a package name that does not conform to this registry's
	// real name charset, so a hostile manifest key cannot reshape the outbound
	// URL. Called before url(); url() additionally PathEscapes each segment as a
	// defence in depth.
	validate(pkg string) error
	// url is the metadata endpoint for pkg.
	url(pkg string) string
	// parse extracts Metadata from a 2xx response body.
	parse(body []byte) (Metadata, error)
}

// Client fetches and caches metadata for one registry.
type Client struct {
	src            source
	fs             sysdep.FileSystem
	clock          sysdep.Clock
	http           sysdep.HTTPGetter
	registriesRoot string
}

// NewNPM returns a Client for the npm registry. registriesRoot is the resolved
// <cache>/agent-creance/registries directory (see state.RegistriesRoot).
func NewNPM(filesystem sysdep.FileSystem, clock sysdep.Clock, getter sysdep.HTTPGetter, registriesRoot string) *Client {
	return newClient(npmSource{}, filesystem, clock, getter, registriesRoot)
}

// NewPackagist returns a Client for Packagist.
func NewPackagist(filesystem sysdep.FileSystem, clock sysdep.Clock, getter sysdep.HTTPGetter, registriesRoot string) *Client {
	return newClient(packagistSource{}, filesystem, clock, getter, registriesRoot)
}

func newClient(src source, filesystem sysdep.FileSystem, clock sysdep.Clock, getter sysdep.HTTPGetter, registriesRoot string) *Client {
	return &Client{
		src:            src,
		fs:             filesystem,
		clock:          clock,
		http:           getter,
		registriesRoot: registriesRoot,
	}
}

// cacheEntry is the on-disk cache record: the normalized Metadata plus the time
// it was fetched, so age can be checked without relying on file mtime. Metadata
// is embedded so the file is a flat {fetched_at, homepage, repository}.
type cacheEntry struct {
	FetchedAt time.Time `json:"fetched_at"`
	Metadata
}

// Lookup returns the metadata for pkg, serving a fresh cache entry without any
// network call and otherwise fetching once and caching the result. A registry
// 404 returns ErrNotFound.
func (c *Client) Lookup(ctx context.Context, pkg string) (Metadata, error) {
	path, err := c.cachePath(pkg)
	if err != nil {
		return Metadata{}, err
	}

	if md, fresh, err := c.readFreshCache(path); err != nil {
		return Metadata{}, err
	} else if fresh {
		return md, nil
	}

	md, err := c.fetch(ctx, pkg)
	if err != nil {
		return Metadata{}, err
	}
	if err := c.writeCache(path, md); err != nil {
		return Metadata{}, err
	}
	return md, nil
}

// Invalidate removes pkg's cached metadata entry, if present, so the next Lookup
// re-fetches it regardless of the refresh window. It reports whether an entry
// actually existed (false when the cache held nothing for pkg). The package name is
// validated the same way Lookup validates it, so a crafted name can never delete a
// file outside the cache tree.
func (c *Client) Invalidate(pkg string) (bool, error) {
	path, err := c.cachePath(pkg)
	if err != nil {
		return false, err
	}
	return sysdep.RemoveIfPresent(c.fs, path)
}

// cachePath is <registriesRoot>/<registry>/<pkg>.json, after validating pkg can
// not escape the cache directory.
func (c *Client) cachePath(pkg string) (string, error) {
	if err := validatePackage(pkg); err != nil {
		return "", err
	}
	return filepath.Join(c.registriesRoot, c.src.name(), pkg+cacheFileSuffix), nil
}

// validatePackage rejects names that are empty, absolute, or contain a "."/".."
// or empty path segment — guarding against a crafted name writing outside the
// cache tree. A scoped npm name ("@scope/pkg") and a Packagist "vendor/pkg" are
// valid: their single "/" just nests one directory deeper.
func validatePackage(pkg string) error {
	if pkg == "" {
		return errors.New("registry: empty package name")
	}
	if strings.HasPrefix(pkg, "/") {
		return fmt.Errorf("registry: invalid package name %q", pkg)
	}
	for _, seg := range strings.Split(pkg, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("registry: invalid package name %q", pkg)
		}
	}
	return nil
}

// readFreshCache reads path and reports the cached Metadata only when the entry
// is parseable and younger than refreshInterval. An absent file (cache miss), a
// stale entry, or an unparseable file all return (_, false, nil) — i.e. "go
// fetch". A genuine read error (not "not exist") is surfaced.
func (c *Client) readFreshCache(path string) (Metadata, bool, error) {
	data, err := c.fs.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Metadata{}, false, nil
		}
		return Metadata{}, false, fmt.Errorf("registry: read cache %q: %w", path, err)
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		// A corrupt cache file is not fatal: treat it as a miss and refetch.
		return Metadata{}, false, nil
	}
	if c.clock.Since(entry.FetchedAt) >= refreshInterval {
		return Metadata{}, false, nil
	}
	return entry.Metadata, true, nil
}

// fetch performs the single network GET and parses the response.
func (c *Client) fetch(ctx context.Context, pkg string) (Metadata, error) {
	if err := c.src.validate(pkg); err != nil {
		return Metadata{}, err
	}
	headers := map[string]string{
		"User-Agent": userAgent,
		"Accept":     "application/json",
	}
	status, body, err := c.http.Get(ctx, c.src.url(pkg), headers)
	if err != nil {
		return Metadata{}, fmt.Errorf("registry: fetch %q: %w", pkg, err)
	}
	switch {
	case status == statusNotFound:
		return Metadata{}, fmt.Errorf("registry: %q: %w", pkg, ErrNotFound)
	case status < statusOKMin || status > statusOKMax:
		return Metadata{}, fmt.Errorf("registry: fetch %q: unexpected status %d", pkg, status)
	}
	md, err := c.src.parse(body)
	if err != nil {
		return Metadata{}, fmt.Errorf("registry: parse %q: %w", pkg, err)
	}
	return md, nil
}

// writeCache atomically writes the metadata to path: it creates the parent
// directory, writes a temp file, then renames it into place, so a crash mid-write
// never leaves a truncated cache entry.
func (c *Client) writeCache(path string, md Metadata) error {
	if err := c.fs.MkdirAll(filepath.Dir(path), cacheDirPerm); err != nil {
		return fmt.Errorf("registry: create cache dir for %q: %w", path, err)
	}
	data, err := json.MarshalIndent(cacheEntry{FetchedAt: c.clock.Now(), Metadata: md}, "", "  ")
	if err != nil {
		return fmt.Errorf("registry: encode cache %q: %w", path, err)
	}
	data = append(data, '\n')

	tmp := path + cacheTempSuffix
	if err := c.fs.WriteFile(tmp, data, cacheFilePerm); err != nil {
		return fmt.Errorf("registry: write cache %q: %w", tmp, err)
	}
	if err := c.fs.Rename(tmp, path); err != nil {
		_ = c.fs.Remove(tmp) // best-effort cleanup; the temp file is otherwise orphaned
		return fmt.Errorf("registry: commit cache %q: %w", path, err)
	}
	return nil
}
