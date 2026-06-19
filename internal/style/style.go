// Package style is the CLI's semantic color / visual-hierarchy layer. It wraps
// github.com/fatih/color in a small Styler that renderers and commands consult
// while building output, so the color decision is made once (per stream) and the
// renderers stay decoupled from the color library.
//
// The cardinal rule is byte-for-byte plain parity: a disabled Styler (or a nil
// one) is the identity function, so output with color off is exactly what the
// CLI produced before any styling existed. Color is always additive — every
// status stays identifiable by its glyph or word with the escapes stripped.
package style

import (
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/fatih/color"
)

// Styler renders semantic emphasis. The zero value is not usable; construct with
// New or Plain. A nil *Styler is safe and behaves like a disabled one (every
// method returns its input unchanged), so call sites that have not been wired
// yet keep producing plain output.
type Styler struct {
	ok      *color.Color // green: success / healthy
	warn    *color.Color // yellow: warning / skew
	bad     *color.Color // red: problem / failure
	header  *color.Color // bold: section headers
	dim     *color.Color // faint: secondary detail (pids, ports, durations)
	enabled bool
}

// New returns a Styler. When enabled is false every method is the identity
// function and emits no escape bytes (byte-identical plain output). The decision
// is forced per color object via EnableColor/DisableColor so it does not depend
// on fatih/color's process-global NoColor state — keeping tests deterministic
// regardless of the test process's TTY or environment.
func New(enabled bool) *Styler {
	s := &Styler{
		ok:      color.New(color.FgGreen),
		warn:    color.New(color.FgYellow),
		bad:     color.New(color.FgRed),
		header:  color.New(color.Bold),
		dim:     color.New(color.Faint),
		enabled: enabled,
	}
	for _, c := range []*color.Color{s.ok, s.warn, s.bad, s.header, s.dim} {
		if enabled {
			c.EnableColor()
		} else {
			c.DisableColor()
		}
	}
	return s
}

// Plain returns a disabled Styler — the identity function. Handy as a default
// and in tests that want guaranteed plain output.
func Plain() *Styler { return New(false) }

// Enabled reports whether this Styler emits color. A nil Styler is disabled.
func (s *Styler) Enabled() bool { return s != nil && s.enabled }

// OK colors text green (success / healthy).
func (s *Styler) OK(text string) string {
	if !s.Enabled() {
		return text
	}
	return s.ok.Sprint(text)
}

// Warn colors text yellow (warning / skew).
func (s *Styler) Warn(text string) string {
	if !s.Enabled() {
		return text
	}
	return s.warn.Sprint(text)
}

// Bad colors text red (problem / failure).
func (s *Styler) Bad(text string) string {
	if !s.Enabled() {
		return text
	}
	return s.bad.Sprint(text)
}

// Header makes text bold (section headers).
func (s *Styler) Header(text string) string {
	if !s.Enabled() {
		return text
	}
	return s.header.Sprint(text)
}

// Dim makes text faint (secondary detail that should recede).
func (s *Styler) Dim(text string) string {
	if !s.Enabled() {
		return text
	}
	return s.dim.Sprint(text)
}

// Resolve turns the --color flag value into an enabled decision for one stream.
// mode is "auto", "always", or "never"; noColor reports whether NO_COLOR is set;
// isTTY reports whether the target stream is a terminal. Per the no-color.org
// spec an explicit "always" overrides NO_COLOR. An unrecognized mode is an error.
func Resolve(mode string, noColor, isTTY bool) (bool, error) {
	switch mode {
	case "always":
		return true, nil
	case "never":
		return false, nil
	case "auto":
		return !noColor && isTTY, nil
	default:
		return false, fmt.Errorf("invalid --color value %q (want auto, always, or never)", mode)
	}
}

var ansiSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// VisibleWidth returns the on-screen width of s in runes, ignoring SGR escape
// sequences (which occupy zero columns). For plain strings it equals
// utf8.RuneCountInString, so callers that swap RuneCount for VisibleWidth keep
// identical behavior when no color is present — which is what lets the progress
// printer's \r padding math stay correct with color active.
func VisibleWidth(s string) int {
	return utf8.RuneCountInString(ansiSGR.ReplaceAllString(s, ""))
}
