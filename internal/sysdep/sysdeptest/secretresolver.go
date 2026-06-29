package sysdeptest

import (
	"context"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeSecretResolver is a scripted SecretResolver. You pre-load Secrets
// (reference → value) and optionally Errs (per-reference error); it records every
// resolution in Resolved so callers can assert the seam was queried with the
// expected reference. A reference that is neither in Secrets nor Errs resolves to
// sysdep.ErrSecretNotFound, so the common "unresolvable reference" case needs no
// setup. This lets downstream injection logic be tested without invoking
// `op`/`security` or touching the environment.
type FakeSecretResolver struct {
	// Secrets maps a reference to the value Resolve should return.
	Secrets map[string][]byte
	// Errs optionally maps a reference to an error to return instead (e.g.
	// sysdep.ErrSecretToolMissing, sysdep.ErrKeychainLocked).
	Errs map[string]error
	// Resolved records each reference passed to Resolve, in order.
	Resolved []string
}

var _ sysdep.SecretResolver = (*FakeSecretResolver)(nil)

// NewFakeSecretResolver returns an empty, ready-to-populate fake.
func NewFakeSecretResolver() *FakeSecretResolver {
	return &FakeSecretResolver{
		Secrets: map[string][]byte{},
		Errs:    map[string]error{},
	}
}

// WithSecret is a builder helper: register a resolvable reference. Returns the
// receiver for chaining.
func (f *FakeSecretResolver) WithSecret(ref, value string) *FakeSecretResolver {
	f.Secrets[ref] = []byte(value)
	return f
}

func (f *FakeSecretResolver) Resolve(_ context.Context, ref string) ([]byte, error) {
	f.Resolved = append(f.Resolved, ref)
	if err, ok := f.Errs[ref]; ok {
		return nil, err
	}
	if v, ok := f.Secrets[ref]; ok {
		return append([]byte(nil), v...), nil
	}
	return nil, sysdep.ErrSecretNotFound
}
