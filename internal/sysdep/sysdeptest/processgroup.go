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
//
// The recorded outputs are read via the Started/Notified/NotifyChans accessors,
// which are mutex-guarded: the forwarding loop (cage.Runner) calls Start/Notify
// from one goroutine while the test asserts from another.
type FakeProcessGroup struct {
	// StartErr, if set, makes Start fail (and not return a Process). Set before use.
	StartErr error
	// Proc is the handle Start returns when StartErr is nil. Created on first use
	// if nil; pre-set it to script Pgid/WaitErr/WaitGate.
	Proc *FakeProcess

	mu          sync.Mutex
	started     []StartedCommand
	notified    [][]os.Signal
	notifyChans []chan<- os.Signal
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
	f.mu.Lock()
	f.started = append(f.started, StartedCommand{Name: name, Args: args, Env: env})
	f.mu.Unlock()
	if f.StartErr != nil {
		return nil, f.StartErr
	}
	if f.Proc == nil {
		f.Proc = &FakeProcess{}
	}
	return f.Proc, nil
}

func (f *FakeProcessGroup) Notify(ch chan<- os.Signal, sigs ...os.Signal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notified = append(f.notified, sigs)
	f.notifyChans = append(f.notifyChans, ch)
}

// Started returns a snapshot of the recorded Start calls, in order.
func (f *FakeProcessGroup) Started() []StartedCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]StartedCommand(nil), f.started...)
}

// Notified returns a snapshot of the recorded Notify signal sets, in order.
func (f *FakeProcessGroup) Notified() [][]os.Signal {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]os.Signal(nil), f.notified...)
}

// NotifyChans returns a snapshot of the channels passed to Notify, in order, so a
// test can push a signal into one (the fake never delivers on its own).
func (f *FakeProcessGroup) NotifyChans() []chan<- os.Signal {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]chan<- os.Signal(nil), f.notifyChans...)
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
