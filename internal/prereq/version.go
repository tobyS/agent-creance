package prereq

import "regexp"

// Skew classifies how an installed tool version relates to the version
// agent-creance was tested against. The design (design.md, "Prerequisites and
// version handling") drives behavior off this classification: Patch skew is
// silent on run/setup but shown by doctor; Minor/Major are warned loudly
// everywhere; Unparseable is given the benefit of the doubt.
type Skew int

const (
	// SkewExact means installed == tested.
	SkewExact Skew = iota
	// SkewPatch means same major.minor, different patch.
	SkewPatch
	// SkewMinor means same major, different minor.
	SkewMinor
	// SkewMajor means different major.
	SkewMajor
	// SkewUnparseable means we could not extract a version number; treated as
	// compatible (no warning) on purpose — a false warning on every upstream
	// format change would be worse than a missed one.
	SkewUnparseable
)

func (s Skew) String() string {
	switch s {
	case SkewExact:
		return "exact"
	case SkewPatch:
		return "patch skew"
	case SkewMinor:
		return "minor skew"
	case SkewMajor:
		return "major skew"
	default:
		return "unparseable"
	}
}

// Loud reports whether this skew warrants a loud warning on run/setup (not just
// doctor). Only minor and major skews are loud.
func (s Skew) Loud() bool {
	return s == SkewMinor || s == SkewMajor
}

// semverRE extracts the first "N.N" or "N.N.N" sequence from arbitrary tool
// output like "Mitmproxy: 12.0.1" or "agent-safehouse version 1.4.2 (darwin)".
// We roll our own tiny extractor rather than pull in a semver library: the
// classification we need (exact/patch/minor/major) is custom anyway, and the
// inputs are messy human-facing banners, not clean semver strings.
var semverRE = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// parsedVersion holds the three numeric components; ok is false when no version
// could be extracted.
type parsedVersion struct {
	major, minor, patch int
	ok                  bool
}

func parseVersion(s string) parsedVersion {
	m := semverRE.FindStringSubmatch(s)
	if m == nil {
		return parsedVersion{}
	}
	// Submatches are guaranteed numeric by the regexp, so atoiSafe never fails;
	// a missing patch group (m[3] == "") parses to 0, which is what we want.
	return parsedVersion{
		major: atoiSafe(m[1]),
		minor: atoiSafe(m[2]),
		patch: atoiSafe(m[3]),
		ok:    true,
	}
}

// classify compares an installed version banner against a tested version string
// and returns the skew category.
func classify(installed, tested string) Skew {
	in := parseVersion(installed)
	te := parseVersion(tested)
	if !in.ok || !te.ok {
		return SkewUnparseable
	}
	switch {
	case in.major != te.major:
		return SkewMajor
	case in.minor != te.minor:
		return SkewMinor
	case in.patch != te.patch:
		return SkewPatch
	default:
		return SkewExact
	}
}

// atoiSafe converts a decimal string to int, returning 0 on empty/invalid. We
// avoid strconv.Atoi's error return here because the regexp already guarantees
// the shape, and 0 is the correct default for an absent patch component.
func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
