package sysdep_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tobyS/agent-creance/internal/sysdep"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// These exercise OSSecretResolver's scheme dispatch and per-backend error
// mapping against the sysdep fakes (black-box: importing sysdeptest from package
// sysdep would cycle). The real op/security backends are covered by the
// integration test; the env:// real backend is hermetic so it is tested here too.

func TestOSSecretResolverOp(t *testing.T) {
	const ref = "op://Private/GitHub/token"

	t.Run("success forwards the reference verbatim and returns stdout", func(t *testing.T) {
		cmd := sysdeptest.NewFakeCommander().WithTool("op", "/usr/local/bin/op", "ghp_resolved")
		r := sysdep.OSSecretResolver{Commander: cmd, Keychain: sysdeptest.NewFakeKeychain(), Paths: sysdeptest.NewFakePathResolver()}

		secret, err := r.Resolve(context.Background(), ref)
		if err != nil {
			t.Fatalf("Resolve(%q) unexpected err: %v", ref, err)
		}
		if string(secret) != "ghp_resolved" {
			t.Errorf("secret = %q, want %q", secret, "ghp_resolved")
		}
		wantArgv := []string{"op", "read", "--no-newline", ref}
		if len(cmd.Calls) != 1 || !slices.Equal(cmd.Calls[0], wantArgv) {
			t.Errorf("op argv = %v, want exactly [%v]", cmd.Calls, wantArgv)
		}
	})

	t.Run("missing op tool maps to ErrSecretToolMissing", func(t *testing.T) {
		r := sysdep.OSSecretResolver{Commander: sysdeptest.NewFakeCommander(), Keychain: sysdeptest.NewFakeKeychain(), Paths: sysdeptest.NewFakePathResolver()}
		_, err := r.Resolve(context.Background(), ref)
		if !errors.Is(err, sysdep.ErrSecretToolMissing) {
			t.Fatalf("Resolve err = %v, want ErrSecretToolMissing", err)
		}
	})

	t.Run("op read failure maps to ErrSecretNotFound", func(t *testing.T) {
		cmd := sysdeptest.NewFakeCommander()
		cmd.Paths["op"] = "/usr/local/bin/op" // installed
		cmd.Errs["op"] = errors.New("sysdep: run \"op\": exit status 1: [ERROR] item not found")
		r := sysdep.OSSecretResolver{Commander: cmd, Keychain: sysdeptest.NewFakeKeychain(), Paths: sysdeptest.NewFakePathResolver()}

		_, err := r.Resolve(context.Background(), ref)
		if !errors.Is(err, sysdep.ErrSecretNotFound) {
			t.Fatalf("Resolve err = %v, want ErrSecretNotFound", err)
		}
	})
}

func TestOSSecretResolverKeychain(t *testing.T) {
	r := func(kc *sysdeptest.FakeKeychain) sysdep.OSSecretResolver {
		return sysdep.OSSecretResolver{Commander: sysdeptest.NewFakeCommander(), Keychain: kc, Paths: sysdeptest.NewFakePathResolver()}
	}

	t.Run("success via the Keychain seam", func(t *testing.T) {
		kc := sysdeptest.NewFakeKeychain().WithItem("GitHub", "octocat", "ghp_kc")
		secret, err := r(kc).Resolve(context.Background(), "keychain://GitHub/octocat")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if string(secret) != "ghp_kc" {
			t.Errorf("secret = %q, want %q", secret, "ghp_kc")
		}
		if len(kc.Lookups) != 1 || kc.Lookups[0] != (sysdeptest.KeychainQuery{Service: "GitHub", Account: "octocat"}) {
			t.Errorf("keychain lookups = %v, want one {GitHub, octocat}", kc.Lookups)
		}
	})

	t.Run("service-only reference", func(t *testing.T) {
		kc := sysdeptest.NewFakeKeychain().WithItem("GitHub", "", "ghp_kc")
		secret, err := r(kc).Resolve(context.Background(), "keychain://GitHub")
		if err != nil || string(secret) != "ghp_kc" {
			t.Fatalf("Resolve = (%q, %v), want (ghp_kc, nil)", secret, err)
		}
	})

	t.Run("absent item maps to ErrSecretNotFound", func(t *testing.T) {
		_, err := r(sysdeptest.NewFakeKeychain()).Resolve(context.Background(), "keychain://GitHub/octocat")
		if !errors.Is(err, sysdep.ErrSecretNotFound) {
			t.Fatalf("err = %v, want ErrSecretNotFound", err)
		}
	})

	t.Run("locked keychain propagates ErrKeychainLocked", func(t *testing.T) {
		kc := sysdeptest.NewFakeKeychain()
		kc.Locked = true
		_, err := r(kc).Resolve(context.Background(), "keychain://GitHub/octocat")
		if !errors.Is(err, sysdep.ErrKeychainLocked) {
			t.Fatalf("err = %v, want ErrKeychainLocked", err)
		}
	})

	t.Run("empty service is ErrUnknownSecretScheme", func(t *testing.T) {
		_, err := r(sysdeptest.NewFakeKeychain()).Resolve(context.Background(), "keychain://")
		if !errors.Is(err, sysdep.ErrUnknownSecretScheme) {
			t.Fatalf("err = %v, want ErrUnknownSecretScheme", err)
		}
	})
}

func TestOSSecretResolverEnv(t *testing.T) {
	t.Run("success via the PathResolver seam", func(t *testing.T) {
		paths := sysdeptest.NewFakePathResolver()
		paths.Env["GH_TOKEN"] = "ghp_env"
		r := sysdep.OSSecretResolver{Commander: sysdeptest.NewFakeCommander(), Keychain: sysdeptest.NewFakeKeychain(), Paths: paths}
		secret, err := r.Resolve(context.Background(), "env://GH_TOKEN")
		if err != nil || string(secret) != "ghp_env" {
			t.Fatalf("Resolve = (%q, %v), want (ghp_env, nil)", secret, err)
		}
	})

	t.Run("unset variable maps to ErrSecretNotFound", func(t *testing.T) {
		r := sysdep.OSSecretResolver{Commander: sysdeptest.NewFakeCommander(), Keychain: sysdeptest.NewFakeKeychain(), Paths: sysdeptest.NewFakePathResolver()}
		_, err := r.Resolve(context.Background(), "env://NOPE")
		if !errors.Is(err, sysdep.ErrSecretNotFound) {
			t.Fatalf("err = %v, want ErrSecretNotFound", err)
		}
	})

	t.Run("real backend via OSPathResolver and t.Setenv", func(t *testing.T) {
		t.Setenv("AC_TEST_SECRET", "real_env_value")
		r := sysdep.OSSecretResolver{Commander: sysdeptest.NewFakeCommander(), Keychain: sysdeptest.NewFakeKeychain(), Paths: sysdep.OSPathResolver{}}
		secret, err := r.Resolve(context.Background(), "env://AC_TEST_SECRET")
		if err != nil || string(secret) != "real_env_value" {
			t.Fatalf("Resolve = (%q, %v), want (real_env_value, nil)", secret, err)
		}
	})
}

func TestOSSecretResolverUnknownScheme(t *testing.T) {
	r := sysdep.OSSecretResolver{Commander: sysdeptest.NewFakeCommander(), Keychain: sysdeptest.NewFakeKeychain(), Paths: sysdeptest.NewFakePathResolver()}
	for _, ref := range []string{"https://example.com", "vault/item/field", "", "OP://x"} {
		if _, err := r.Resolve(context.Background(), ref); !errors.Is(err, sysdep.ErrUnknownSecretScheme) {
			t.Errorf("Resolve(%q) err = %v, want ErrUnknownSecretScheme", ref, err)
		}
	}
}
