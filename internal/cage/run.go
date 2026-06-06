package cage

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// defaultGrace is how long Run waits after forwarding the first SIGINT/SIGTERM
// before escalating to SIGKILL, so a child that ignores the term cannot wedge
// teardown indefinitely.
const defaultGrace = 5 * time.Second

// Runner executes a prepared Invocation in its own process group and forwards
// SIGINT/SIGTERM to the whole group, escalating to SIGKILL after a grace period.
// Run returns only once the group has exited, so a caller (the run command,
// AC-0025) can perform the lock-file decrement strictly after teardown — the
// deterministic ordering the design requires. This package builds the invocation
// (Build/Prepare) and now drives it; it still does not touch the proxy lock or
// refcount (that is the caller's job, AC-0020/AC-0025).
type Runner struct {
	pg    sysdep.ProcessGroup
	grace time.Duration
}

// Option configures a Runner.
type Option func(*Runner)

// WithGrace overrides the post-signal grace period before SIGKILL escalation.
func WithGrace(d time.Duration) Option {
	return func(r *Runner) { r.grace = d }
}

// NewRunner wires a Runner to a ProcessGroup seam (production OSProcessGroup, or
// the fake in tests).
func NewRunner(pg sysdep.ProcessGroup, opts ...Option) *Runner {
	r := &Runner{pg: pg, grace: defaultGrace}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Run starts inv in a new process group, forwards SIGINT/SIGTERM received by this
// wrapper to the group, and returns the child's exit error once the whole group
// has been reaped. A child that does not exit within the grace period after the
// first forwarded signal is escalated with SIGKILL.
func (r *Runner) Run(ctx context.Context, inv Invocation) error {
	proc, err := r.pg.Start(ctx, inv.Env, inv.Path, inv.Args...)
	if err != nil {
		return fmt.Errorf("cage: start %s: %w", inv.Path, err)
	}

	// Subscribe via the seam so the loop is testable against the fake. A buffered
	// channel is required: os/signal never blocks delivering to it.
	sigCh := make(chan os.Signal, 1)
	r.pg.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh) // tidy up the real registration; a no-op for the fake

	waitCh := make(chan error, 1)
	go func() { waitCh <- proc.Wait() }()

	// killTimer is armed on the first forwarded signal; once it fires we escalate
	// to SIGKILL and keep waiting for the group to actually exit.
	var killTimer <-chan time.Time
	for {
		select {
		case sig := <-sigCh:
			_ = proc.Signal(sig) // best effort; an already-gone group (ESRCH) is fine
			if killTimer == nil {
				killTimer = time.After(r.grace)
			}
		case <-killTimer:
			_ = proc.Signal(syscall.SIGKILL)
			killTimer = nil // fire once
		case werr := <-waitCh:
			return werr
		}
	}
}
