package broker

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

func TestStoreSetGet(t *testing.T) {
	t.Parallel()

	expiry := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		set         map[string]entry
		lookup      string
		wantToken   string
		wantExpires time.Time
		wantOK      bool
	}{
		{
			name:      "static credential has no expiry",
			set:       map[string]entry{"gh": {token: []byte("ghs_static")}},
			lookup:    "gh",
			wantToken: "ghs_static",
			wantOK:    true,
		},
		{
			name:        "minted credential carries its expiry",
			set:         map[string]entry{"gh": {token: []byte("ghs_minted"), expiresAt: expiry}},
			lookup:      "gh",
			wantToken:   "ghs_minted",
			wantExpires: expiry,
			wantOK:      true,
		},
		{
			name:   "unknown credential",
			set:    map[string]entry{"gh": {token: []byte("ghs_static")}},
			lookup: "deploy",
			wantOK: false,
		},
		{
			name:   "empty store",
			lookup: "gh",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := NewStore(&sysdeptest.FakeMemory{})
			for name, e := range tc.set {
				s.Set(name, e.token, e.expiresAt)
			}

			token, expiresAt, ok := s.Get(tc.lookup)
			require.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				return
			}
			assert.Equal(t, tc.wantToken, string(token))
			assert.True(t, tc.wantExpires.Equal(expiresAt), "expiry: want %v, got %v", tc.wantExpires, expiresAt)
		})
	}
}

// Rotation: replacing a credential must leave no readable copy of the token it
// replaced. This is the property AC-0069a's refresh loop depends on.
func TestStoreSetWipesReplacedToken(t *testing.T) {
	t.Parallel()

	mem := &sysdeptest.FakeMemory{}
	s := NewStore(mem)

	old := []byte("ghs_old")
	s.Set("gh", old, time.Time{})
	s.Set("gh", []byte("ghs_new"), time.Time{})

	assert.Equal(t, make([]byte, len("ghs_old")), old, "the replaced token must be zeroed in place")

	token, _, ok := s.Get("gh")
	require.True(t, ok)
	assert.Equal(t, "ghs_new", string(token))

	assert.Equal(t, []int{len("ghs_old"), len("ghs_new")}, mem.Locked, "each custodied token is locked")
	assert.Equal(t, []int{len("ghs_old")}, mem.Unlocked, "the replaced token is unlocked")
}

func TestStoreWipe(t *testing.T) {
	t.Parallel()

	mem := &sysdeptest.FakeMemory{}
	s := NewStore(mem)

	gh := []byte("ghs_token")
	deploy := []byte("deploy_token")
	s.Set("gh", gh, time.Time{})
	s.Set("deploy", deploy, time.Time{})

	s.Wipe()

	assert.Equal(t, make([]byte, len("ghs_token")), gh, "wipe zeroes the buffer in place")
	assert.Equal(t, make([]byte, len("deploy_token")), deploy)

	_, _, ok := s.Get("gh")
	assert.False(t, ok, "a wiped store holds nothing")
	assert.Len(t, mem.Unlocked, 2, "every locked token is unlocked")
}

// An mlock failure (RLIMIT_MEMLOCK, a hostile sysctl) must not stop the broker
// from serving: custody is best-effort, refusing to inject is not the fallback.
func TestStoreToleratesLockFailure(t *testing.T) {
	t.Parallel()

	mem := &sysdeptest.FakeMemory{LockErr: errors.New("mlock: cannot allocate memory")}
	s := NewStore(mem)

	s.Set("gh", []byte("ghs_token"), time.Time{})

	token, _, ok := s.Get("gh")
	require.True(t, ok)
	assert.Equal(t, "ghs_token", string(token))
}
