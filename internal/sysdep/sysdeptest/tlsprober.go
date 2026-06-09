package sysdeptest

import (
	"context"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeTLSProber is a scripted TLSProber. Set Outcome (and optionally Err) to the
// verdict ProbeViaProxy should return; it records each call in Calls so a test
// can assert the proxy/target URLs the caller built.
type FakeTLSProber struct {
	// Outcome is returned by ProbeViaProxy when Err is nil and Outcomes is empty.
	Outcome sysdep.ProbeOutcome
	// Outcomes, when non-empty, is consumed one entry per call (in order); once
	// exhausted, ProbeViaProxy falls back to Outcome. This models a verify-first
	// flow where the first probe is untrusted (pre-install) and the second
	// trusted (post-install).
	Outcomes []sysdep.ProbeOutcome
	// Err, if set, is returned by ProbeViaProxy (the probe could not run).
	Err error
	// Calls records each ProbeViaProxy call, in order.
	Calls []TLSProbe
}

// TLSProbe is one recorded ProbeViaProxy call.
type TLSProbe struct {
	ProxyURL  string
	TargetURL string
}

var _ sysdep.TLSProber = (*FakeTLSProber)(nil)

// NewFakeTLSProber returns a fake that reports ProbeTrusted by default.
func NewFakeTLSProber() *FakeTLSProber {
	return &FakeTLSProber{Outcome: sysdep.ProbeTrusted}
}

func (f *FakeTLSProber) ProbeViaProxy(_ context.Context, proxyURL, targetURL string) (sysdep.ProbeOutcome, error) {
	f.Calls = append(f.Calls, TLSProbe{ProxyURL: proxyURL, TargetURL: targetURL})
	if f.Err != nil {
		return sysdep.ProbeError, f.Err
	}
	if len(f.Outcomes) > 0 {
		out := f.Outcomes[0]
		f.Outcomes = f.Outcomes[1:]
		return out, nil
	}
	return f.Outcome, nil
}
