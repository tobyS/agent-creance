package sysdeptest

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFakeSleeperRecordsAndReturnsImmediately(t *testing.T) {
	s := &FakeSleeper{}
	start := time.Now()
	if err := s.Sleep(context.Background(), time.Hour); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Error("FakeSleeper slept in real time, want immediate return")
	}
	if len(s.Sleeps) != 1 || s.Sleeps[0] != time.Hour {
		t.Errorf("Sleeps = %v, want [1h]", s.Sleeps)
	}
}

func TestFakeSleeperReturnsScriptedErr(t *testing.T) {
	sentinel := errors.New("cancelled")
	s := &FakeSleeper{SleepErr: sentinel}
	if err := s.Sleep(context.Background(), time.Second); !errors.Is(err, sentinel) {
		t.Errorf("Sleep error = %v, want %v", err, sentinel)
	}
}
