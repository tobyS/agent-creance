package sysdeptest

import (
	"context"
	"os"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeProcessGroup is a scripted ProcessGroup. Start records the commands it is
// asked to run and returns Proc (a FakeProcess, lazily created) unless StartErr
// is set; Notify records the signal sets it is subscribed to without delivering
// anything. This lets teardown logic be tested without spawning real processes.
type FakeProcessGroup struct {
	// StartErr, if set, makes Start fail (and not return a Process).
	StartErr error
	// Started records each Start call, in order.
	Started []StartedCommand
	// Notified records the signal set of each Notify call, in order.
	Notified [][]os.Signal
	// Proc is the handle Start returns when StartErr is nil. Created on first use
	// if nil; pre-set it to script Pgid/WaitErr.
	Proc *FakeProcess
}

// StartedCommand is one recorded Start call.
type StartedCommand struct {
	Name string
	Args []string
}

// FakeProcess is a scripted Process handle. It records forwarded signals and
// returns the configured Pgid/WaitErr.
type FakeProcess struct {
	// PgidVal is returned by Pgid.
	PgidVal int
	// WaitErr is returned by Wait.
	WaitErr error
	// Signals records the signals forwarded via Signal, in order.
	Signals []os.Signal
}

var (
	_ sysdep.ProcessGroup = (*FakeProcessGroup)(nil)
	_ sysdep.Process      = (*FakeProcess)(nil)
)

// NewFakeProcessGroup returns an empty, ready-to-use fake.
func NewFakeProcessGroup() *FakeProcessGroup { return &FakeProcessGroup{} }

func (f *FakeProcessGroup) Start(_ context.Context, name string, args ...string) (sysdep.Process, error) {
	f.Started = append(f.Started, StartedCommand{Name: name, Args: args})
	if f.StartErr != nil {
		return nil, f.StartErr
	}
	if f.Proc == nil {
		f.Proc = &FakeProcess{}
	}
	return f.Proc, nil
}

func (f *FakeProcessGroup) Notify(_ chan<- os.Signal, sigs ...os.Signal) {
	f.Notified = append(f.Notified, sigs)
}

func (p *FakeProcess) Signal(sig os.Signal) error {
	p.Signals = append(p.Signals, sig)
	return nil
}

func (p *FakeProcess) Wait() error { return p.WaitErr }

func (p *FakeProcess) Pgid() int { return p.PgidVal }
