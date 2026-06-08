// Package state answers one question every later component asks: "what project is
// this, and where does its out-of-tree state live?" It maps a project directory to
// a stable identity hash — derived from the directory's canonical (realpath-
// resolved) absolute path — and to the fully-resolved layout of the project's state
// directory under ~/.cache/agent-creance/projects/<hash>/.
//
// Two paths that point at the same physical directory (e.g. via a symlink) collapse
// to one identity; a renamed or moved directory is intentionally a different
// identity (any proxy under the old path is irrelevant). This is the same identity
// scheme the proxy lock file uses — see docs/design.md, "Multi-agent lifecycle".
//
// The package performs no artifact I/O: it only computes paths and the hash. The
// compiler, proxy, and audit log own creating and writing the files whose locations
// this package reports. All OS access (canonicalising the path, locating the cache
// root) goes through the sysdep.PathResolver seam, so the logic stays hermetically
// testable.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// Artifact file names within a project's state directory. These names are the
// contract downstream packages (policy compiler, proxy lifecycle, audit log, cage
// config redirect, session-overlay mutation) build against, so they are defined
// once here.
const (
	appCacheSubdir     = "agent-creance"
	projectsSubdir     = "projects"
	registriesSubdir   = "registries"
	generatorsSubdir   = "generators"
	enforcerSubdir     = "enforcer"
	policyJSONName     = "policy.json"
	networkSBName      = "network.sb"
	proxyProfileSBName = "proxy.sb"
	caProfileSBName    = "ca.sb"
	proxyLockName      = "proxy.lock"
	egressJSONLName    = "egress.jsonl"
	// egressJSONLRotatedName is the single rotated backup the enforcer keeps
	// (egress.jsonl.1). The reader (internal/audit) reads it then the current file
	// as one logical stream, so the ".1" suffix is part of that contract and is
	// named once here, mirroring the writer's ROTATED_SUFFIX.
	egressJSONLRotatedName = egressJSONLName + ".1"
	claudeDirName          = "claude"
	sessionOverlayName     = "session-overlay.yaml"

	// hashHexLen is the number of hex characters in a project hash: the first 8
	// bytes (64 bits) of the SHA-256 of the canonical path. Short enough for a
	// readable directory name, wide enough that collisions across any realistic
	// number of projects are negligible.
	hashHexLen = 16
)

// Resolver turns a project directory into its identity and state-dir layout using
// the injected path/environment seam.
type Resolver struct {
	paths sysdep.PathResolver
}

// New returns a Resolver backed by the given seam.
func New(paths sysdep.PathResolver) *Resolver {
	return &Resolver{paths: paths}
}

// Layout is the fully-resolved set of paths for one project's state directory.
type Layout struct {
	// Canonical is the realpath-resolved absolute project directory.
	Canonical string
	// Hash is the deterministic identity derived from Canonical.
	Hash string
	// Root is the project's state directory:
	// <cache>/agent-creance/projects/<hash>.
	Root string
}

// Resolve canonicalises dir (absolute, then symlink-resolved), derives the identity
// hash, and returns the layout. It returns an error if dir cannot be resolved (for
// example, it does not exist — symlink resolution requires the directory to be
// present) or the cache root cannot be determined.
func (r *Resolver) Resolve(dir string) (Layout, error) {
	abs, err := r.paths.Abs(dir)
	if err != nil {
		return Layout{}, fmt.Errorf("state: absolute path for %q: %w", dir, err)
	}
	canonical, err := r.paths.EvalSymlinks(abs)
	if err != nil {
		return Layout{}, fmt.Errorf("state: resolve %q: %w", dir, err)
	}
	hash := hashPath(canonical)
	root, err := r.projectRoot(hash)
	if err != nil {
		return Layout{}, err
	}
	return Layout{Canonical: canonical, Hash: hash, Root: root}, nil
}

// LayoutForRoot builds a Layout from an existing project state-dir path, taking
// the hash from the directory's base name. It is for enumeration paths (e.g.
// `status` iterating projects/<hash>) where only Root-derived paths (ProxyLock,
// SessionOverlay) are needed and the canonical project directory is not known —
// the hash is one-way, so Canonical is left empty. Do not use it where Canonical
// is required; use Resolve for that.
func LayoutForRoot(root string) Layout {
	return Layout{Hash: filepath.Base(root), Root: root}
}

// hashPath derives the project identity from a canonical path: the first 8 bytes of
// its SHA-256, rendered as 16 lowercase hex characters.
func hashPath(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:hashHexLen/2])
}

// projectRoot returns <cache>/agent-creance/projects/<hash>.
func (r *Resolver) projectRoot(hash string) (string, error) {
	cache, err := r.cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, appCacheSubdir, projectsSubdir, hash), nil
}

// ProjectsRoot returns <cache>/agent-creance/projects — the parent of every
// project's per-hash state dir. `status` (AC-0032) lists this directory to find
// all projects with proxy state; it is a path-only accessor (no I/O), so the
// caller enumerates it through a sysdep.FileSystem seam.
func (r *Resolver) ProjectsRoot() (string, error) {
	cache, err := r.cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, appCacheSubdir, projectsSubdir), nil
}

// CacheDir returns <cache>/agent-creance — the base of all agent-creance state
// (the parent of projects/, registries/, generators/, enforcer/). doctor (AC-0031)
// probes its filesystem type to warn when the out-of-tree state — and therefore the
// proxy.lock advisory locks — land on iCloud/SMB where flock is unreliable.
func (r *Resolver) CacheDir() (string, error) {
	cache, err := r.cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, appCacheSubdir), nil
}

// RegistriesRoot returns <cache>/agent-creance/registries — the cross-project home
// of per-package registry metadata caches (npm, Packagist). Unlike the project
// state dir, this is a sibling of projects/<hash>/ and is intentionally
// project-independent: a fetched package's homepage/repository is the same for
// every project, so the cache survives across them (see docs/design.md,
// "Allowlist generators" → Caching). The registry client owns the per-registry
// and per-package path segments beneath this root.
func (r *Resolver) RegistriesRoot() (string, error) {
	cache, err := r.cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, appCacheSubdir, registriesSubdir), nil
}

// GeneratorsRoot returns <cache>/agent-creance/generators — the cross-project home
// of generator output caches, content-addressed by manifest hash. Like
// RegistriesRoot it is a sibling of projects/<hash>/ and project-independent: two
// projects with an identical manifest share the same generated rule set, so the
// cache survives across them (see docs/design.md, "Allowlist generators" →
// Caching). The generator owns the per-generator and per-hash path segments beneath
// this root.
func (r *Resolver) GeneratorsRoot() (string, error) {
	cache, err := r.cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, appCacheSubdir, generatorsSubdir), nil
}

// EnforcerRoot returns <cache>/agent-creance/enforcer — the constant,
// cross-project home of the extracted mitmproxy enforcer addon. Like
// RegistriesRoot/GeneratorsRoot it is a sibling of projects/<hash>/ and
// project-independent: the addon is a constant shipped in the binary, identical
// for every project (see docs/design.md, "Tech stack"), so users never install
// or version it. The proxy extractor (AC-0019) owns writing the module files
// beneath this root.
func (r *Resolver) EnforcerRoot() (string, error) {
	cache, err := r.cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, appCacheSubdir, enforcerSubdir), nil
}

// cacheRoot returns the base cache directory, honouring XDG_CACHE_HOME when set and
// falling back to $HOME/.cache. The design uses the XDG-style ~/.cache on macOS too
// (not ~/Library/Caches), so os.UserCacheDir is deliberately not used.
func (r *Resolver) cacheRoot() (string, error) {
	if xdg := r.paths.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return xdg, nil
	}
	home, err := r.paths.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("state: locate cache root: %w", err)
	}
	return filepath.Join(home, ".cache"), nil
}

// PolicyJSON is the compiled egress policy consumed by the proxy enforcer.
func (l Layout) PolicyJSON() string { return filepath.Join(l.Root, policyJSONName) }

// NetworkSB is the Seatbelt profile passed to Safehouse via --append-profile.
func (l Layout) NetworkSB() string { return filepath.Join(l.Root, networkSBName) }

// ProxyProfileSB is the launch-time Seatbelt fragment for the live proxy port,
// passed to Safehouse via a second --append-profile after NetworkSB (the ordering
// contract enforced by profile.RenderProxyFragment). Rewritten every launch
// because the port is ephemeral.
func (l Layout) ProxyProfileSB() string { return filepath.Join(l.Root, proxyProfileSBName) }

// CAProfileSB is the launch-time Seatbelt fragment that grants in-cage read of the
// single mitmproxy CA PEM (AC-0034), passed to Safehouse via a third --append-profile.
// Rewritten every launch because the resolved CA path depends on the host.
func (l Layout) CAProfileSB() string { return filepath.Join(l.Root, caProfileSBName) }

// ProxyLock is the mitmproxy lifecycle lock file (PID, port, policy hash, agents).
func (l Layout) ProxyLock() string { return filepath.Join(l.Root, proxyLockName) }

// EgressJSONL is the JSONL audit log of proxied requests.
func (l Layout) EgressJSONL() string { return filepath.Join(l.Root, egressJSONLName) }

// EgressJSONLRotated is the single rotated backup of the audit log
// (egress.jsonl.1). The reader (internal/audit) reads it then EgressJSONL as one
// logical stream.
func (l Layout) EgressJSONLRotated() string {
	return filepath.Join(l.Root, egressJSONLRotatedName)
}

// ClaudeConfigDir is the ephemeral CLAUDE_CONFIG_DIR the caged agent is pointed at.
func (l Layout) ClaudeConfigDir() string { return filepath.Join(l.Root, claudeDirName) }

// SessionOverlay is the session-scoped allow-overlay file written by `allow --once`
// and purged on last-agent-exit teardown.
func (l Layout) SessionOverlay() string { return filepath.Join(l.Root, sessionOverlayName) }
