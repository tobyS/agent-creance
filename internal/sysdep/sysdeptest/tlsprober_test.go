package sysdeptest

import (
	"context"
	"errors"
	"testing"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestFakeTLSProberReportsOutcomeAndRecordsCall(t *testing.T) {
	p := NewFakeTLSProber()
	p.Outcome = sysdep.ProbeUntrusted
	got, err := p.ProbeViaProxy(context.Background(), "http://127.0.0.1:9", "https://example.com")
	if err != nil {
		t.Fatalf("ProbeViaProxy: %v", err)
	}
	if got != sysdep.ProbeUntrusted {
		t.Errorf("outcome = %v, want ProbeUntrusted", got)
	}
	if len(p.Calls) != 1 || p.Calls[0] != (TLSProbe{ProxyURL: "http://127.0.0.1:9", TargetURL: "https://example.com"}) {
		t.Errorf("Calls = %+v, want one matching probe", p.Calls)
	}
}

func TestFakeTLSProberDefaultsToTrusted(t *testing.T) {
	got, err := NewFakeTLSProber().ProbeViaProxy(context.Background(), "p", "t")
	if err != nil || got != sysdep.ProbeTrusted {
		t.Errorf("default probe = (%v, %v), want (ProbeTrusted, nil)", got, err)
	}
}

func TestFakeTLSProberConsumesOutcomesInOrderThenFallsBack(t *testing.T) {
	p := NewFakeTLSProber()
	p.Outcome = sysdep.ProbeTrusted // fallback once Outcomes is exhausted
	p.Outcomes = []sysdep.ProbeOutcome{sysdep.ProbeUntrusted, sysdep.ProbeTrusted}

	want := []sysdep.ProbeOutcome{
		sysdep.ProbeUntrusted, // 1st: scripted
		sysdep.ProbeTrusted,   // 2nd: scripted
		sysdep.ProbeTrusted,   // 3rd: fallback to Outcome
	}
	for i, w := range want {
		got, err := p.ProbeViaProxy(context.Background(), "p", "t")
		if err != nil {
			t.Fatalf("call %d: ProbeViaProxy: %v", i, err)
		}
		if got != w {
			t.Errorf("call %d: outcome = %v, want %v", i, got, w)
		}
	}
	if len(p.Calls) != 3 {
		t.Errorf("Calls = %d, want 3", len(p.Calls))
	}
}

func TestFakeTLSProberReturnsErr(t *testing.T) {
	p := NewFakeTLSProber()
	sentinel := errors.New("curl missing")
	p.Err = sentinel
	got, err := p.ProbeViaProxy(context.Background(), "p", "t")
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want %v", err, sentinel)
	}
	if got != sysdep.ProbeError {
		t.Errorf("outcome = %v, want ProbeError on probe failure", got)
	}
}
