package cage_test

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/cage"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// testInvocation is a minimal Invocation the Runner just forwards to Start.
func testInvocation() cage.Invocation {
	return cage.Invocation{
		Path: "safehouse",
		Args: []string{"--workdir", "/proj", "--", "claude"},
		Env:  []string{"HTTPS_PROXY=http://127.0.0.1:8080"},
	}
}

// startedGroup runs the Runner in a goroutine and waits until it has subscribed to
// signals (so NotifyChans[0] is available to inject into). It returns the fake, the
// captured signal channel, and a channel carrying Run's eventual return.
func startedGroup(t *testing.T, fake *sysdeptest.FakeProcessGroup, opts ...cage.Option) (chan<- os.Signal, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cage.NewRunner(fake, opts...).Run(context.Background(), testInvocation()) }()
	require.Eventually(t, func() bool { return len(fake.NotifyChans()) > 0 }, time.Second, time.Millisecond,
		"Run did not subscribe to signals")
	return fake.NotifyChans()[0], done
}

func TestRunStartsInvocation(t *testing.T) {
	fake := sysdeptest.NewFakeProcessGroup()
	fake.Proc = &sysdeptest.FakeProcess{} // Wait returns immediately, clean exit

	err := cage.NewRunner(fake).Run(context.Background(), testInvocation())
	require.NoError(t, err)

	require.Len(t, fake.Started(), 1)
	got := fake.Started()[0]
	require.Equal(t, "safehouse", got.Name)
	require.Equal(t, []string{"--workdir", "/proj", "--", "claude"}, got.Args)
	require.Equal(t, []string{"HTTPS_PROXY=http://127.0.0.1:8080"}, got.Env)
	require.Equal(t, [][]os.Signal{{syscall.SIGINT, syscall.SIGTERM}}, fake.Notified())
}

func TestRunForwardsSignalToGroup(t *testing.T) {
	fake := sysdeptest.NewFakeProcessGroup()
	fake.Proc = &sysdeptest.FakeProcess{PgidVal: 4242, WaitGate: make(chan struct{})}

	sigCh, done := startedGroup(t, fake)
	sigCh <- syscall.SIGINT

	require.Eventually(t, func() bool {
		sigs := fake.Proc.SignalsSnapshot()
		return len(sigs) == 1 && sigs[0] == syscall.SIGINT
	}, time.Second, time.Millisecond, "SIGINT was not forwarded to the group")

	// The group has not exited yet, so Run must still be blocked.
	select {
	case <-done:
		t.Fatal("Run returned before the group exited")
	case <-time.After(20 * time.Millisecond):
	}

	close(fake.Proc.WaitGate) // group exits
	require.NoError(t, <-done)
}

func TestRunWaitsBeforeReturning(t *testing.T) {
	// Demonstrates the wait-before-decrement ordering: Run does not return (so a
	// caller's lock decrement cannot run) until the group's Wait completes.
	fake := sysdeptest.NewFakeProcessGroup()
	gate := make(chan struct{})
	fake.Proc = &sysdeptest.FakeProcess{WaitGate: gate, WaitErr: errors.New("exit 130")}

	done := make(chan error, 1)
	go func() { done <- cage.NewRunner(fake).Run(context.Background(), testInvocation()) }()

	select {
	case <-done:
		t.Fatal("Run returned before Wait completed")
	case <-time.After(30 * time.Millisecond):
	}

	close(gate)
	require.EqualError(t, <-done, "exit 130", "Run should surface the child's exit error")
}

func TestRunStartError(t *testing.T) {
	fake := sysdeptest.NewFakeProcessGroup()
	fake.StartErr = errors.New("fork failed")

	err := cage.NewRunner(fake).Run(context.Background(), testInvocation())
	require.Error(t, err)
	require.ErrorContains(t, err, "fork failed")
	require.Empty(t, fake.Notified(), "must not subscribe to signals when Start fails")
}

func TestRunEscalatesToSIGKILL(t *testing.T) {
	fake := sysdeptest.NewFakeProcessGroup()
	fake.Proc = &sysdeptest.FakeProcess{WaitGate: make(chan struct{})}

	sigCh, done := startedGroup(t, fake, cage.WithGrace(10*time.Millisecond))
	sigCh <- syscall.SIGTERM // child ignores it; grace elapses

	require.Eventually(t, func() bool {
		for _, s := range fake.Proc.SignalsSnapshot() {
			if s == syscall.SIGKILL {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond, "grace elapsed but SIGKILL was not sent")

	close(fake.Proc.WaitGate)
	require.NoError(t, <-done)
}
