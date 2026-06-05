package sysdeptest

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestFakeProcessGroupStartRecordsAndReturnsHandle(t *testing.T) {
	pg := NewFakeProcessGroup()
	proc, err := pg.Start(context.Background(), "agent-safehouse", "run", "claude")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if proc == nil {
		t.Fatal("Start returned nil Process")
	}
	if len(pg.Started) != 1 {
		t.Fatalf("Started = %+v, want one entry", pg.Started)
	}
	got := pg.Started[0]
	if got.Name != "agent-safehouse" || len(got.Args) != 2 || got.Args[0] != "run" || got.Args[1] != "claude" {
		t.Errorf("Started[0] = %+v, want {agent-safehouse [run claude]}", got)
	}
}

func TestFakeProcessGroupStartErr(t *testing.T) {
	pg := NewFakeProcessGroup()
	sentinel := errors.New("fork failed")
	pg.StartErr = sentinel
	proc, err := pg.Start(context.Background(), "x")
	if !errors.Is(err, sentinel) {
		t.Errorf("Start error = %v, want %v", err, sentinel)
	}
	if proc != nil {
		t.Error("Start Process != nil, want nil on error")
	}
}

func TestFakeProcessGroupNotifyRecords(t *testing.T) {
	pg := NewFakeProcessGroup()
	ch := make(chan os.Signal, 1)
	pg.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	if len(pg.Notified) != 1 || len(pg.Notified[0]) != 2 {
		t.Fatalf("Notified = %+v, want one entry of two signals", pg.Notified)
	}
	if pg.Notified[0][0] != syscall.SIGINT || pg.Notified[0][1] != syscall.SIGTERM {
		t.Errorf("Notified[0] = %v, want [SIGINT SIGTERM]", pg.Notified[0])
	}
}

func TestFakeProcessRecordsSignalsAndScriptsWait(t *testing.T) {
	pg := NewFakeProcessGroup()
	pg.Proc = &FakeProcess{PgidVal: 4242, WaitErr: errors.New("exit 130")}
	proc, err := pg.Start(context.Background(), "x")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	if proc.Pgid() != 4242 {
		t.Errorf("Pgid() = %d, want 4242", proc.Pgid())
	}
	if err := proc.Wait(); err == nil || err.Error() != "exit 130" {
		t.Errorf("Wait() = %v, want exit 130", err)
	}
	if len(pg.Proc.Signals) != 1 || pg.Proc.Signals[0] != syscall.SIGTERM {
		t.Errorf("recorded Signals = %v, want [SIGTERM]", pg.Proc.Signals)
	}
}
