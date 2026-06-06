package audit_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/audit"
)

// safeBuf is a mutex-guarded sink so the Follow goroutine and the test can write/read
// it concurrently under -race.
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(line + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// reqEntry builds an intercepted allow line whose URL carries a unique token.
func reqEntry(token string) string {
	return `{"ts":"t","method":"GET","url":"https://h/` + token + `","decision":"allow","rule":null,"status":200}`
}

// startFollow runs Follow in a goroutine and returns the sink and a stop func that
// cancels and asserts a clean return.
func startFollow(t *testing.T, dir, cur string) (*safeBuf, func()) {
	t.Helper()
	sink := &safeBuf{}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- audit.Follow(ctx, sink, dir, cur) }()
	stop := func() {
		cancel()
		select {
		case err := <-errCh:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Fatal("Follow did not return after context cancel")
		}
	}
	return sink, stop
}

func eventuallyContains(t *testing.T, sink *safeBuf, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return strings.Contains(sink.String(), want)
	}, 5*time.Second, 20*time.Millisecond, "never saw %q; got:\n%s", want, sink.String())
}

// waitReady appends sentinel entries until one is echoed back, proving the follower
// has finished opening/seeking and is live at the current end of the file. This is a
// deterministic handshake that closes the open/seek startup window without a fixed
// sleep; entries appended after it returns are reliably streamed.
func waitReady(t *testing.T, sink *safeBuf, cur string) {
	t.Helper()
	require.Eventually(t, func() bool {
		appendLine(t, cur, reqEntry("ready"))
		return strings.Contains(sink.String(), "https://h/ready")
	}, 5*time.Second, 50*time.Millisecond, "follower never became ready")
}

func TestFollowStreamsAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "egress.jsonl")
	// Start from an empty file so the seek-to-end lands at offset 0: every appended
	// entry is then guaranteed to be after our start position, removing the
	// open/seek startup race without a fixed sleep.
	require.NoError(t, os.WriteFile(cur, nil, 0o600))

	sink, stop := startFollow(t, dir, cur)
	defer stop()
	waitReady(t, sink, cur) // follower is now live at end-of-file

	// Pre-rotation entries.
	appendLine(t, cur, reqEntry("1"))
	appendLine(t, cur, reqEntry("2"))
	eventuallyContains(t, sink, "https://h/2")

	// Rotate mid-stream: current -> .1, then a fresh current is created by the next append.
	require.NoError(t, os.Rename(cur, cur+".1"))
	appendLine(t, cur, reqEntry("3"))
	appendLine(t, cur, reqEntry("4"))
	eventuallyContains(t, sink, "https://h/4")

	// Every entry written after follow began crossed the flip — including the two in
	// the renamed file and the two in the fresh file.
	got := sink.String()
	for _, tok := range []string{"https://h/1", "https://h/2", "https://h/3", "https://h/4"} {
		require.Contains(t, got, tok)
	}
	// Order preserved (the follower did not get stuck on the renamed file).
	require.Less(t, strings.Index(got, "https://h/2"), strings.Index(got, "https://h/3"))
}

func TestFollowStartsAtEndSkippingHistory(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "egress.jsonl")
	appendLine(t, cur, reqEntry("old")) // historical, must not be re-emitted

	sink, stop := startFollow(t, dir, cur)
	defer stop()

	// Keep appending a marker until one lands after the follower has seeked to end;
	// this both proves liveness and sidesteps the startup race without a fixed sleep.
	require.Eventually(t, func() bool {
		appendLine(t, cur, reqEntry("new"))
		return strings.Contains(sink.String(), "https://h/new")
	}, 5*time.Second, 50*time.Millisecond)

	require.NotContains(t, sink.String(), "https://h/old")
}

func TestFollowWaitsForFileToAppear(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "egress.jsonl") // does not exist yet

	sink, stop := startFollow(t, dir, cur)
	defer stop()

	// The file is born after Follow starts; append until the follower picks it up
	// (this both proves first-appearance handling and avoids the startup race).
	require.Eventually(t, func() bool {
		appendLine(t, cur, reqEntry("born"))
		return strings.Contains(sink.String(), "https://h/born")
	}, 5*time.Second, 50*time.Millisecond)
}

func TestFollowErrorsWhenDirMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nope")
	cur := filepath.Join(dir, "egress.jsonl")
	err := audit.Follow(context.Background(), &safeBuf{}, dir, cur)
	require.Error(t, err)
	require.Contains(t, err.Error(), "has the cage run?")
}
