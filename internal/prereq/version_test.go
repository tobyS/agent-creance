package prereq

import "testing"

// This file demonstrates the single most common Go testing pattern:
// table-driven tests. Instead of one test function per case (à la PHPUnit
// methods), you declare a slice of cases and loop, calling t.Run to give each a
// named subtest. It's the rough equivalent of a @dataProvider / it.each, but
// it's just a plain for-loop — no framework.
func TestClassify(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		tested    string
		want      Skew
	}{
		{"exact", "1.4.2", "1.4.2", SkewExact},
		{"patch differs", "1.4.5", "1.4.2", SkewPatch},
		{"minor differs", "1.5.0", "1.4.2", SkewMinor},
		{"major differs", "2.0.1", "1.4.2", SkewMajor},
		{"banner with noise", "Mitmproxy: 12.0.1", "12.0.1", SkewExact},
		{"two-component installed", "1.4", "1.4.0", SkewExact},
		{"installed unparseable", "not a version", "1.4.2", SkewUnparseable},
		{"tested unparseable", "1.4.2", "n/a", SkewUnparseable},
	}

	for _, tc := range cases {
		// Capturing tc by running a subtest keeps failures individually named in
		// the output (e.g. TestClassify/minor_differs).
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.installed, tc.tested)
			if got != tc.want {
				t.Errorf("classify(%q, %q) = %v, want %v", tc.installed, tc.tested, got, tc.want)
			}
		})
	}
}

func TestSkewLoud(t *testing.T) {
	// Only minor and major skews are "loud" (warned on run/setup).
	loud := map[Skew]bool{SkewMinor: true, SkewMajor: true}
	for _, s := range []Skew{SkewExact, SkewPatch, SkewMinor, SkewMajor, SkewUnparseable} {
		if s.Loud() != loud[s] {
			t.Errorf("Skew(%v).Loud() = %v, want %v", s, s.Loud(), loud[s])
		}
	}
}
