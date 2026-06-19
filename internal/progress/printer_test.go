package progress

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/style"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

func newTestPrinter(interactive bool) (*Printer, *bytes.Buffer, *sysdeptest.FakeClock) {
	var buf bytes.Buffer
	clock := sysdeptest.NewFakeClock(time.Unix(0, 0))
	return NewPrinter(&buf, clock, interactive, style.Plain()), &buf, clock
}

// pad returns the spaces a rewrite of prev with next emits to cover residue.
// It measures visible width (ignoring SGR escapes), mirroring the printer.
func pad(prev, next string) string {
	n := style.VisibleWidth(prev) - style.VisibleWidth(next)
	if n < 0 {
		n = 0
	}
	return strings.Repeat(" ", n)
}

func TestStepInPlaceCompletion(t *testing.T) {
	p, buf, clock := newTestPrinter(true)

	p.StepStart("Compiling egress policy")
	clock.Advance(400 * time.Millisecond)
	p.StepDone("Egress policy up to date (cached)")

	open := "Compiling egress policy…"
	done := "✓ Egress policy up to date (cached) (400ms)"
	require.Equal(t, open+"\r"+done+pad(open, done)+"\n", buf.String())
}

func TestStepDirtyCompletionAppends(t *testing.T) {
	p, buf, _ := newTestPrinter(true)

	p.StepStart("Compiling egress policy")
	p.Line("something intervened")
	p.StepDone("Egress policy compiled: 4 allow, 1 deny")

	require.Equal(t,
		"Compiling egress policy…\n"+
			"something intervened\n"+
			"✓ Egress policy compiled: 4 allow, 1 deny (0s)\n",
		buf.String())
}

func TestStepNonInteractive(t *testing.T) {
	p, buf, clock := newTestPrinter(false)

	p.StepStart("Starting egress proxy")
	clock.Advance(2 * time.Second)
	p.StepDone("Egress proxy ready on port 8081")

	require.Equal(t,
		"Starting egress proxy…\n"+
			"✓ Egress proxy ready on port 8081 (2s)\n",
		buf.String())
}

func TestInteractiveCounterSequence(t *testing.T) {
	p, buf, clock := newTestPrinter(true)

	p.StepStart("Compiling egress policy")
	p.BuildStart([]ManifestRef{
		{Type: "composer_json", Path: "backend/composer.json"},
		{Type: "package_json", Path: "frontend/package.json"},
	})
	p.ManifestStart(ManifestRef{Type: "composer_json", Path: "backend/composer.json"})
	p.LookupsStart(3)
	p.LookupDone(1, 3)
	p.LookupDone(2, 3)
	p.LookupDone(3, 3)
	clock.Advance(41200 * time.Millisecond)
	p.ManifestDone()
	clock.Advance(time.Minute)
	p.StepDone("Egress policy compiled: 12 allow, 0 deny")

	counter := func(i int) string {
		return fmt.Sprintf("  backend/composer.json: looking up %d/3 packages…", i)
	}
	final := "  ✓ backend/composer.json: 3 packages (41.2s)"
	require.Equal(t,
		"Compiling egress policy…\n"+
			"  inputs changed (first run or updated config/manifest) — fetching package metadata from packagist/npm; results are cached for future runs\n"+
			counter(0)+
			"\r"+counter(1)+
			"\r"+counter(2)+
			"\r"+counter(3)+
			"\r"+final+pad(counter(3), final)+"\n"+
			"✓ Egress policy compiled: 12 allow, 0 deny (1m41s)\n",
		buf.String())
}

func TestNonInteractiveMilestones(t *testing.T) {
	p, buf, _ := newTestPrinter(false)

	p.ManifestStart(ManifestRef{Type: "package_json", Path: "frontend/package.json"})
	p.LookupsStart(8)
	for i := 1; i <= 8; i++ {
		p.LookupDone(i, 8)
	}
	p.ManifestDone()

	require.Equal(t,
		"  frontend/package.json: looking up 8 packages…\n"+
			"  frontend/package.json: 2/8…\n"+
			"  frontend/package.json: 4/8…\n"+
			"  frontend/package.json: 6/8…\n"+
			"  ✓ frontend/package.json: 8 packages (0s)\n",
		buf.String())
}

func TestNonInteractiveSmallWalkHasNoMilestones(t *testing.T) {
	p, buf, _ := newTestPrinter(false)

	p.ManifestStart(ManifestRef{Type: "composer_json", Path: "composer.json"})
	p.LookupsStart(3)
	for i := 1; i <= 3; i++ {
		p.LookupDone(i, 3)
	}
	p.ManifestDone()

	require.Equal(t,
		"  composer.json: looking up 3 packages…\n"+
			"  ✓ composer.json: 3 packages (0s)\n",
		buf.String())
}

func TestSingleLookupUsesSingular(t *testing.T) {
	p, buf, _ := newTestPrinter(false)

	p.ManifestStart(ManifestRef{Type: "composer_json", Path: "composer.json"})
	p.LookupsStart(1)
	p.LookupDone(1, 1)
	p.ManifestDone()

	require.Equal(t,
		"  composer.json: looking up 1 package…\n"+
			"  ✓ composer.json: 1 package (0s)\n",
		buf.String())
}

func TestCachedManifest(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		p, buf, _ := newTestPrinter(interactive)

		p.ManifestStart(ManifestRef{Type: "composer_json", Path: "backoffice/composer.json"})
		p.ManifestCached()
		p.ManifestDone()

		require.Equal(t,
			"  ✓ backoffice/composer.json: rules cached (0s)\n",
			buf.String(), "interactive=%v", interactive)
	}
}

func TestBuildStartSingleRegistry(t *testing.T) {
	p, buf, _ := newTestPrinter(false)

	p.BuildStart([]ManifestRef{
		{Type: "composer_json", Path: "backend/composer.json"},
		{Type: "composer_json", Path: "backoffice/composer.json"},
	})

	require.Equal(t,
		"  inputs changed (first run or updated config/manifest) — fetching package metadata from packagist; results are cached for future runs\n",
		buf.String())
}

func TestBuildStartUnknownTypeFallsBack(t *testing.T) {
	p, buf, _ := newTestPrinter(false)

	p.BuildStart([]ManifestRef{{Type: "cargo_toml", Path: "Cargo.toml"}})

	require.Contains(t, buf.String(), "from cargo_toml;")
}

func TestCloseTerminatesOpenLineOnce(t *testing.T) {
	p, buf, _ := newTestPrinter(true)

	p.ManifestStart(ManifestRef{Type: "composer_json", Path: "composer.json"})
	p.LookupsStart(5)
	p.Close()
	p.Close()

	require.Equal(t, "  composer.json: looking up 0/5 packages…\n", buf.String())
}

func TestMilestoneIndices(t *testing.T) {
	cases := []struct {
		n    int
		want []int
	}{
		{n: 0, want: nil},
		{n: 7, want: nil},
		{n: 8, want: []int{2, 4, 6}},
		{n: 87, want: []int{21, 43, 65}},
		{n: 132, want: []int{33, 66, 99}},
	}
	for _, c := range cases {
		require.Equal(t, c.want, milestoneIndices(c.n), "n=%d", c.n)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{d: 0, want: "0s"},
		{d: 12 * time.Millisecond, want: "0s"},
		{d: 400 * time.Millisecond, want: "400ms"},
		{d: 41200 * time.Millisecond, want: "41.2s"},
		{d: 2*time.Minute + 14*time.Second + 300*time.Millisecond, want: "2m14s"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, formatDuration(c.d), "d=%v", c.d)
	}
}

// newColorPrinter builds a printer with color forced on (tests are never a tty).
func newColorPrinter(interactive bool) (*Printer, *bytes.Buffer, *sysdeptest.FakeClock) {
	var buf bytes.Buffer
	clock := sysdeptest.NewFakeClock(time.Unix(0, 0))
	return NewPrinter(&buf, clock, interactive, style.New(true)), &buf, clock
}

func TestColorStepDoneColorsGlyphAndDimsDuration(t *testing.T) {
	sty := style.New(true)
	p, buf, clock := newColorPrinter(false)

	p.StepStart("Starting egress proxy")
	clock.Advance(2 * time.Second)
	p.StepDone("Egress proxy ready on port 8081")

	want := "Starting egress proxy…\n" +
		sty.OK("✓") + " Egress proxy ready on port 8081 " + sty.Dim("(2s)") + "\n"
	require.Equal(t, want, buf.String())
	// The glyph carries a real escape; stripping it recovers the plain line.
	require.Contains(t, buf.String(), "\x1b[")
}

func TestColorInteractivePadIgnoresEscapes(t *testing.T) {
	sty := style.New(true)
	p, buf, clock := newColorPrinter(true)

	p.StepStart("Compiling egress policy")
	clock.Advance(400 * time.Millisecond)
	p.StepDone("Egress policy up to date (cached)")

	open := "Compiling egress policy…"
	done := sty.OK("✓") + " Egress policy up to date (cached) " + sty.Dim("(400ms)")
	// pad is computed on the VISIBLE width, so the escape bytes don't inflate it:
	// the colored done line is visibly shorter than the open line, so it pads.
	require.Equal(t, open+"\r"+done+pad(open, done)+"\n", buf.String())
}

func TestOrNop(t *testing.T) {
	require.Equal(t, Nop{}, OrNop(nil))

	p, _, _ := newTestPrinter(false)
	require.Equal(t, Reporter(p), OrNop(p))

	// Nop tolerates the full event sequence without output or panic.
	var rep Reporter = Nop{}
	rep.BuildStart([]ManifestRef{{Type: "composer_json", Path: "composer.json"}})
	rep.ManifestStart(ManifestRef{})
	rep.LookupsStart(1)
	rep.LookupDone(1, 1)
	rep.ManifestCached()
	rep.ManifestDone()
}
