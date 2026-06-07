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
