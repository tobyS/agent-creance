package state

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// All tests here use the in-memory FakePathResolver — never the real filesystem —
// so the package stays hermetic and never imports the os package (enforced by the
// ticket's grep guard). The real OSPathResolver is smoke-tested in internal/sysdep.

func TestResolveSymlinkAliasCollapsesToOneIdentity(t *testing.T) {
	fake := sysdeptest.NewFakePathResolver()
	fake.HomeDir = "/home/u"
	// /work/alias is a symlink to the physical /real/proj.
	fake.Symlinks = map[string]string{
		"/work/alias": "/real/proj",
		"/real/proj":  "/real/proj",
	}
	r := New(fake)

	viaAlias, err := r.Resolve("/work/alias")
	if err != nil {
		t.Fatalf("Resolve(alias): %v", err)
	}
	viaTarget, err := r.Resolve("/real/proj")
	if err != nil {
		t.Fatalf("Resolve(target): %v", err)
	}

	if viaAlias.Canonical != viaTarget.Canonical {
		t.Errorf("canonical differs: alias=%q target=%q", viaAlias.Canonical, viaTarget.Canonical)
	}
	if viaAlias.Hash != viaTarget.Hash {
		t.Errorf("hash differs: alias=%q target=%q", viaAlias.Hash, viaTarget.Hash)
	}
	if viaAlias.Root != viaTarget.Root {
		t.Errorf("root differs: alias=%q target=%q", viaAlias.Root, viaTarget.Root)
	}
}

func TestResolveDistinctDirsGiveDistinctHashes(t *testing.T) {
	fake := sysdeptest.NewFakePathResolver()
	fake.HomeDir = "/home/u"
	r := New(fake)

	a, err := r.Resolve("/projects/alpha")
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Resolve("/projects/beta")
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash == b.Hash {
		t.Errorf("distinct dirs produced same hash %q", a.Hash)
	}
}

func TestResolveRelativePathIsMadeAbsoluteThenCanonicalised(t *testing.T) {
	fake := sysdeptest.NewFakePathResolver()
	fake.Cwd = "/work"
	fake.HomeDir = "/home/u"
	// Abs("proj") -> "/work/proj"; that path is then a symlink to the real dir.
	fake.Symlinks = map[string]string{"/work/proj": "/real/canonical"}
	r := New(fake)

	got, err := r.Resolve("proj")
	if err != nil {
		t.Fatalf("Resolve(relative): %v", err)
	}
	if got.Canonical != "/real/canonical" {
		t.Errorf("Canonical = %q, want %q", got.Canonical, "/real/canonical")
	}
	// And it must match resolving the canonical path directly.
	want := hashPath("/real/canonical")
	if got.Hash != want {
		t.Errorf("Hash = %q, want %q", got.Hash, want)
	}
}

var hashRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

func TestHashShapeAndDeterminism(t *testing.T) {
	fake := sysdeptest.NewFakePathResolver()
	fake.HomeDir = "/home/u"
	r := New(fake)

	first, err := r.Resolve("/some/proj")
	if err != nil {
		t.Fatal(err)
	}
	if !hashRE.MatchString(first.Hash) {
		t.Errorf("Hash = %q, want 16 lowercase hex chars", first.Hash)
	}
	second, err := r.Resolve("/some/proj")
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash != second.Hash {
		t.Errorf("hash not deterministic: %q != %q", first.Hash, second.Hash)
	}
}

func TestCacheRootHonoursXDGThenFallsBackToHome(t *testing.T) {
	cases := []struct {
		name     string
		xdg      string
		home     string
		wantBase string // expected prefix before /agent-creance/projects/<hash>
	}{
		{"xdg set", "/xdg/cache", "/home/u", "/xdg/cache"},
		{"xdg empty -> home/.cache", "", "/home/u", "/home/u/.cache"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := sysdeptest.NewFakePathResolver()
			fake.HomeDir = tc.home
			if tc.xdg != "" {
				fake.Env["XDG_CACHE_HOME"] = tc.xdg
			}
			r := New(fake)

			got, err := r.Resolve("/proj")
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(tc.wantBase, "agent-creance", "projects", got.Hash)
			if got.Root != want {
				t.Errorf("Root = %q, want %q", got.Root, want)
			}
		})
	}
}

func TestRegistriesRootHonoursXDGThenFallsBackToHome(t *testing.T) {
	cases := []struct {
		name string
		xdg  string
		home string
		want string
	}{
		{"xdg set", "/xdg/cache", "/home/u", "/xdg/cache/agent-creance/registries"},
		{"xdg empty -> home/.cache", "", "/home/u", "/home/u/.cache/agent-creance/registries"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := sysdeptest.NewFakePathResolver()
			fake.HomeDir = tc.home
			if tc.xdg != "" {
				fake.Env["XDG_CACHE_HOME"] = tc.xdg
			}

			got, err := New(fake).RegistriesRoot()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("RegistriesRoot = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRegistriesRootErrorsWhenCacheRootUnknown(t *testing.T) {
	fake := sysdeptest.NewFakePathResolver()
	fake.HomeErr = errors.New("boom") // and XDG_CACHE_HOME unset
	if _, err := New(fake).RegistriesRoot(); err == nil {
		t.Error("want error when cache root cannot be determined, got nil")
	}
}

func TestGeneratorsRootHonoursXDGThenFallsBackToHome(t *testing.T) {
	cases := []struct {
		name string
		xdg  string
		home string
		want string
	}{
		{"xdg set", "/xdg/cache", "/home/u", "/xdg/cache/agent-creance/generators"},
		{"xdg empty -> home/.cache", "", "/home/u", "/home/u/.cache/agent-creance/generators"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := sysdeptest.NewFakePathResolver()
			fake.HomeDir = tc.home
			if tc.xdg != "" {
				fake.Env["XDG_CACHE_HOME"] = tc.xdg
			}

			got, err := New(fake).GeneratorsRoot()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("GeneratorsRoot = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGeneratorsRootErrorsWhenCacheRootUnknown(t *testing.T) {
	fake := sysdeptest.NewFakePathResolver()
	fake.HomeErr = errors.New("boom") // and XDG_CACHE_HOME unset
	if _, err := New(fake).GeneratorsRoot(); err == nil {
		t.Error("want error when cache root cannot be determined, got nil")
	}
}

func TestEnforcerRootHonoursXDGThenFallsBackToHome(t *testing.T) {
	cases := []struct {
		name string
		xdg  string
		home string
		want string
	}{
		{"xdg set", "/xdg/cache", "/home/u", "/xdg/cache/agent-creance/enforcer"},
		{"xdg empty -> home/.cache", "", "/home/u", "/home/u/.cache/agent-creance/enforcer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := sysdeptest.NewFakePathResolver()
			fake.HomeDir = tc.home
			if tc.xdg != "" {
				fake.Env["XDG_CACHE_HOME"] = tc.xdg
			}

			got, err := New(fake).EnforcerRoot()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("EnforcerRoot = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEnforcerRootErrorsWhenCacheRootUnknown(t *testing.T) {
	fake := sysdeptest.NewFakePathResolver()
	fake.HomeErr = errors.New("boom") // and XDG_CACHE_HOME unset
	if _, err := New(fake).EnforcerRoot(); err == nil {
		t.Error("want error when cache root cannot be determined, got nil")
	}
}

func TestAccessorsAreRootedAtProjectsHash(t *testing.T) {
	fake := sysdeptest.NewFakePathResolver()
	fake.HomeDir = "/home/u"
	r := New(fake)

	l, err := r.Resolve("/proj")
	if err != nil {
		t.Fatal(err)
	}

	rooted := filepath.Join("projects", l.Hash) // ".../projects/<hash>/..."
	accessors := map[string]struct {
		path string
		name string
	}{
		"PolicyJSON":         {l.PolicyJSON(), "policy.json"},
		"NetworkSB":          {l.NetworkSB(), "network.sb"},
		"ProxyLock":          {l.ProxyLock(), "proxy.lock"},
		"EgressJSONL":        {l.EgressJSONL(), "egress.jsonl"},
		"EgressJSONLRotated": {l.EgressJSONLRotated(), "egress.jsonl.1"},
		"ClaudeConfigDir":    {l.ClaudeConfigDir(), "claude"},
		"SessionOverlay":     {l.SessionOverlay(), "session-overlay.yaml"},
	}
	for accessor, want := range accessors {
		if !strings.Contains(want.path, rooted) {
			t.Errorf("%s = %q, not under %q", accessor, want.path, rooted)
		}
		if filepath.Base(want.path) != want.name {
			t.Errorf("%s base = %q, want %q", accessor, filepath.Base(want.path), want.name)
		}
		if filepath.Dir(want.path) != l.Root {
			t.Errorf("%s dir = %q, want Root %q", accessor, filepath.Dir(want.path), l.Root)
		}
	}
}

func TestResolveErrors(t *testing.T) {
	sentinel := errors.New("boom")

	t.Run("eval symlinks fails", func(t *testing.T) {
		fake := sysdeptest.NewFakePathResolver()
		fake.HomeDir = "/home/u"
		fake.EvalErr = sentinel
		if _, err := New(fake).Resolve("/missing"); err == nil {
			t.Error("want error when EvalSymlinks fails, got nil")
		}
	})

	t.Run("abs fails", func(t *testing.T) {
		fake := sysdeptest.NewFakePathResolver()
		fake.AbsErr = sentinel
		if _, err := New(fake).Resolve("rel"); err == nil {
			t.Error("want error when Abs fails, got nil")
		}
	})

	t.Run("no xdg and home fails", func(t *testing.T) {
		fake := sysdeptest.NewFakePathResolver()
		fake.HomeErr = sentinel // and XDG_CACHE_HOME unset
		if _, err := New(fake).Resolve("/proj"); err == nil {
			t.Error("want error when cache root cannot be determined, got nil")
		}
	})
}
