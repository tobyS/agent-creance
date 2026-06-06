package sysdeptest

import (
	"context"
	"os"
	"sync"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeProcessGroup is a scripted ProcessGroup. Start records the commands it is
// asked to run and returns Proc (a FakeProcess, lazily created) unless StartErr
// is set; Notify records the signal sets it is subscribed to (and the channels, so
// a test can inject a signal) without delivering anything itself. This lets
// teardown logic be tested without spawning real processes.
type FakeProcessGroup struct {
	// StartErr, if set, makes Start fail (and not return a Process).
	StartErr error
	// Started records each Start call, in order.
	Started []StartedCommand
	// Notified records the signal set of each Notify call, in order.
	Notified [][]os.Signal
	// NotifyChans records the channel from each Notify call, in order, so a test can
	// push a signal into it (the fake never delivers on its own).
	NotifyChans []chan<- os.Signal
	// Proc is the handle Start returns when StartErr is nil. Created on first use
	// if nil; pre-set it to script Pgid/WaitErr/WaitGate.
	Proc *FakeProcess
}

// StartedCommand is one recorded Start call.
type StartedCommand struct {
	Name string
	Args []string
	Env  []string
}

// FakeProcess is a scripted Process handle. It records forwarded signals and
// returns the configured Pgid/WaitErr. Set WaitGate to make Wait block until a
// test releases it — so the test can assert forwarding before the group exits.
type FakeProcess struct {
	// PgidVal is returned by Pgid.
	PgidVal int
	// WaitErr is returned by Wait.
	WaitErr error
	// WaitGate, if non-nil, blocks Wait until it is closed (or receives a value).
	WaitGate chan struct{}

	mu sync.Mutex
	// signals records the signals forwarded via Signal, in order. Guarded by mu
	// because Signal (the forwarding loop) and the test read it concurrently.
	signals []os.Signal
}

var (
	_ sysdep.ProcessGroup = (*FakeProcessGroup)(nil)
	_ sysdep.Process      = (*FakeProcess)(nil)
)

// NewFakeProcessGroup returns an empty, ready-to-use fake.
func NewFakeProcessGroup() *FakeProcessGroup { return &FakeProcessGroup{} }

func (f *FakeProcessGroup) Start(_ context.Context, env []string, name string, args ...string) (sysdep.Process, error) {
	f.Started = append(f.Started, StartedCommand{Name: name, Args: args, Env: env})
	if f.StartErr != nil {
		return nil, f.StartErr
	}
	if f.Proc == nil {
		f.Proc = &FakeProcess{}
	}
	return f.Proc, nil
}

func (f *FakeProcessGroup) Notify(ch chan<- os.Signal, sigs ...os.Signal) {
	f.Notified = append(f.Notified, sigs)
	f.NotifyChans = append(f.NotifyChans, ch)
}

func (p *FakeProcess) Signal(sig os.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.signals = append(p.signals, sig)
	return nil
}

func (p *FakeProcess) Wait() error {
	if p.WaitGate != nil {
		<-p.WaitGate
	}
	return p.WaitErr
}

func (p *FakeProcess) Pgid() int { return p.PgidVal }

// SignalsSnapshot returns a copy of the signals forwarded via Signal so far. Safe
// to call while the forwarding loop runs.
func (p *FakeProcess) SignalsSnapshot() []os.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]os.Signal(nil), p.signals...)
}
