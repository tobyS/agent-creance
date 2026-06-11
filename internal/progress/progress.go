// Package progress carries live progress events from the policy compiler and
// its generators to a renderer, so the run command can show what a compile is
// doing while it works (AC-0041). The Reporter interface is the event seam the
// logic layers emit into; Printer is the terminal renderer the CLI wires up;
// Nop keeps every other compiler caller (policy refresh/show, tests) silent.
package progress

// ManifestRef identifies one generator manifest in the compile inputs: the
// generator type (e.g. "composer_json") and the project-relative manifest path
// (e.g. "backend/composer.json").
type ManifestRef struct {
	Type string
	Path string
}

// Reporter receives live progress events from the policy compiler and its
// generators, in the order the work happens: BuildStart once per compile that
// misses the input-hash gate, then per manifest a ManifestStart, either
// ManifestCached (output-cache hit, zero lookups) or LookupsStart followed by
// one LookupDone per dependency, and finally ManifestDone. A compile served
// entirely from the input-hash cache emits no events at all.
//
// Calls arrive sequentially from a single goroutine; implementations need no
// locking. Constructors that accept a Reporter normalize nil via OrNop.
type Reporter interface {
	// BuildStart fires when the input-hash gate misses and rules will be
	// (re)generated for the given manifests. It is not fired for a build with
	// no configured manifests (there is no lookup work to explain).
	BuildStart(manifests []ManifestRef)
	// ManifestStart fires before the generator for m runs.
	ManifestStart(m ManifestRef)
	// LookupsStart fires when the generator misses its output cache and will
	// perform n registry lookups for the current manifest.
	LookupsStart(n int)
	// LookupDone fires after each registry lookup; i is 1-based and counts
	// not-found packages too (their lookup completed).
	LookupDone(i, n int)
	// ManifestCached fires instead of LookupsStart when the generator's output
	// cache already holds the current manifest's rules.
	ManifestCached()
	// ManifestDone fires after the generator for the current manifest returned.
	ManifestDone()
}

// Nop is the Reporter that does nothing, for callers that want a silent
// compile (policy refresh/show, the mutate commands, tests).
type Nop struct{}

var _ Reporter = Nop{}

func (Nop) BuildStart([]ManifestRef) {}

func (Nop) ManifestStart(ManifestRef) {}

func (Nop) LookupsStart(int) {}

func (Nop) LookupDone(int, int) {}

func (Nop) ManifestCached() {}

func (Nop) ManifestDone() {}

// OrNop normalizes a possibly-nil Reporter to Nop so consumers can emit events
// unconditionally instead of nil-checking at every call site.
func OrNop(r Reporter) Reporter {
	if r == nil {
		return Nop{}
	}
	return r
}
