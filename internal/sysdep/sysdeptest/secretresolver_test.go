package sysdeptest

import (
	"context"
	"errors"
	"testing"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestFakeSecretResolver(t *testing.T) {
	ctx := context.Background()

	t.Run("registered reference resolves and is recorded", func(t *testing.T) {
		f := NewFakeSecretResolver().WithSecret("op://Private/GitHub/token", "ghp_x")
		secret, err := f.Resolve(ctx, "op://Private/GitHub/token")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if string(secret) != "ghp_x" {
			t.Errorf("secret = %q, want %q", secret, "ghp_x")
		}
		if len(f.Resolved) != 1 || f.Resolved[0] != "op://Private/GitHub/token" {
			t.Errorf("Resolved = %v, want one op:// reference", f.Resolved)
		}
	})

	t.Run("unregistered reference yields ErrSecretNotFound", func(t *testing.T) {
		_, err := NewFakeSecretResolver().Resolve(ctx, "env://NOPE")
		if !errors.Is(err, sysdep.ErrSecretNotFound) {
			t.Fatalf("err = %v, want ErrSecretNotFound", err)
		}
	})

	t.Run("scripted error takes precedence", func(t *testing.T) {
		f := NewFakeSecretResolver()
		f.Errs["op://x/y/z"] = sysdep.ErrSecretToolMissing
		if _, err := f.Resolve(ctx, "op://x/y/z"); !errors.Is(err, sysdep.ErrSecretToolMissing) {
			t.Fatalf("err = %v, want ErrSecretToolMissing", err)
		}
	})

	t.Run("returned bytes are a defensive copy", func(t *testing.T) {
		f := NewFakeSecretResolver().WithSecret("env://T", "secret")
		got, _ := f.Resolve(ctx, "env://T")
		got[0] = 'X'
		again, _ := f.Resolve(ctx, "env://T")
		if string(again) != "secret" {
			t.Errorf("mutating a returned secret changed the fake's stored value: %q", again)
		}
	})
}
