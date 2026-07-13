package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

var (
	testNow    = time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	testExpiry = time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
)

func TestServerAnswer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		credential string
		token      string
		expiresAt  time.Time
		advance    time.Duration
		want       Response
	}{
		{
			name:       "static token, no expiry on the wire",
			credential: "gh",
			token:      "ghs_static",
			want:       Response{Token: "ghs_static"},
		},
		{
			name:       "minted token carries expires_at",
			credential: "gh",
			token:      "ghs_minted",
			expiresAt:  testExpiry,
			want:       Response{Token: "ghs_minted", ExpiresAt: "2026-07-13T11:00:00Z"},
		},
		{
			name:       "unknown credential",
			credential: "deploy",
			token:      "ghs_static", // held under "gh", not "deploy"
			want:       Response{Error: ErrUnknownCredential},
		},
		{
			name:       "expired token is refused, not served",
			credential: "gh",
			token:      "ghs_minted",
			expiresAt:  testExpiry,
			advance:    time.Hour,
			want:       Response{Error: ErrExpired},
		},
		{
			name:       "a token expiring this instant is already dead",
			credential: "gh",
			token:      "ghs_minted",
			expiresAt:  testNow,
			want:       Response{Error: ErrExpired},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			clock := sysdeptest.NewFakeClock(testNow)
			store := NewStore(&sysdeptest.FakeMemory{})
			store.Set("gh", []byte(tc.token), tc.expiresAt)
			clock.Advance(tc.advance)

			got := NewServer(store, clock).answer(Request{Credential: tc.credential})
			assert.Equal(t, tc.want, got)
		})
	}
}

// End-to-end over a real unix socket in the test's TempDir: hermetic (no external
// tool, no shared state), and it exercises the framing the enforcer will speak.
func TestServerServeOverSocket(t *testing.T) {
	t.Parallel()

	store := NewStore(&sysdeptest.FakeMemory{})
	store.Set("gh", []byte("ghs_token"), time.Time{})

	sock := serve(t, store, sysdeptest.NewFakeClock(testNow))

	assert.Equal(t, Response{Token: "ghs_token"}, roundTrip(t, sock, Request{Credential: "gh"}))
	assert.Equal(t, Response{Error: ErrUnknownCredential}, roundTrip(t, sock, Request{Credential: "nope"}))

	// A second connection proves the server did not stop after the first — one
	// request per connection, many connections per broker.
	assert.Equal(t, Response{Token: "ghs_token"}, roundTrip(t, sock, Request{Credential: "gh"}))
}

// A rotated token is served to the next request without restarting anything —
// the property the fd channel could not offer, and the reason this ticket exists.
func TestServerServesRotatedToken(t *testing.T) {
	t.Parallel()

	store := NewStore(&sysdeptest.FakeMemory{})
	store.Set("gh", []byte("ghs_old"), time.Time{})

	sock := serve(t, store, sysdeptest.NewFakeClock(testNow))
	require.Equal(t, Response{Token: "ghs_old"}, roundTrip(t, sock, Request{Credential: "gh"}))

	store.Set("gh", []byte("ghs_new"), testExpiry)

	assert.Equal(t, Response{Token: "ghs_new", ExpiresAt: "2026-07-13T11:00:00Z"},
		roundTrip(t, sock, Request{Credential: "gh"}))
}

// A garbage line must not kill the server: the next request still gets an answer.
func TestServerSurvivesMalformedRequest(t *testing.T) {
	t.Parallel()

	store := NewStore(&sysdeptest.FakeMemory{})
	store.Set("gh", []byte("ghs_token"), time.Time{})

	sock := serve(t, store, sysdeptest.NewFakeClock(testNow))

	conn, err := net.Dial("unix", sock)
	require.NoError(t, err)
	_, err = conn.Write([]byte("this is not json\n"))
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	assert.Equal(t, Response{Token: "ghs_token"}, roundTrip(t, sock, Request{Credential: "gh"}))
}

func TestServerSocketPermissions(t *testing.T) {
	t.Parallel()

	sock := serve(t, NewStore(&sysdeptest.FakeMemory{}), sysdeptest.NewFakeClock(testNow))

	fi, err := os.Stat(sock)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(),
		"the socket's mode is the whole access control — see the package doc")
}

func TestListenRejectsOverlongPath(t *testing.T) {
	t.Parallel()

	long := filepath.Join(shortTempDir(t), strings.Repeat("x", sysdep.MaxSocketPathLen)+".sock")
	_, err := sysdep.OSUnixSocket{}.Listen(long, 0o600)
	assert.ErrorIs(t, err, sysdep.ErrSocketPathTooLong)
}

// shortTempDir returns a temp dir short enough that a socket inside it fits in
// sun_path. t.TempDir() embeds the test's name, which on macOS (where TMPDIR is
// already a long /var/folders/… path) routinely overshoots the 104-byte limit.
func shortTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "ac")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// serve starts a Server on a socket in a temp dir and returns its path. The
// server is stopped when the test ends.
func serve(t *testing.T, store *Store, clock sysdep.Clock) string {
	t.Helper()

	sock := filepath.Join(shortTempDir(t), "broker.sock")
	ln, err := sysdep.OSUnixSocket{}.Listen(sock, 0o600)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- NewServer(store, clock).Serve(ctx, ln) }()

	t.Cleanup(func() {
		cancel()
		assert.NoError(t, <-done, "Serve must exit cleanly on context cancellation")
	})

	require.Eventually(t, func() bool { return sysdep.OSUnixSocket{}.Probe(sock) },
		time.Second, 10*time.Millisecond, "server never came up")
	return sock
}

// roundTrip speaks the wire protocol once, the way the enforcer's broker.py will.
func roundTrip(t *testing.T, sock string, req Request) Response {
	t.Helper()

	conn, err := net.Dial("unix", sock)
	require.NoError(t, err)
	defer conn.Close()

	require.NoError(t, json.NewEncoder(conn).Encode(req))

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	require.NoError(t, err)

	var resp Response
	require.NoError(t, json.Unmarshal(line, &resp))
	return resp
}
