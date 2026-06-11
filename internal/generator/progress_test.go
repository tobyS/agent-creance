package generator

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/generator/registry"
	"github.com/tobyS/agent-creance/internal/progress"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// recordingReporter logs the generator-emitted progress events in call order.
type recordingReporter struct {
	events []string
}

var _ progress.Reporter = (*recordingReporter)(nil)

func (r *recordingReporter) BuildStart([]progress.ManifestRef) {
	r.events = append(r.events, "build-start")
}

func (r *recordingReporter) ManifestStart(progress.ManifestRef) {
	r.events = append(r.events, "manifest-start")
}

func (r *recordingReporter) LookupsStart(n int) {
	r.events = append(r.events, fmt.Sprintf("lookups:%d", n))
}

func (r *recordingReporter) LookupDone(i, n int) {
	r.events = append(r.events, fmt.Sprintf("lookup:%d/%d", i, n))
}

func (r *recordingReporter) ManifestCached() {
	r.events = append(r.events, "cached")
}

func (r *recordingReporter) ManifestDone() {
	r.events = append(r.events, "manifest-done")
}

func TestGenerate_ProgressEvents(t *testing.T) {
	rep := &recordingReporter{}
	lookup := &fakeLookuper{
		meta:     map[string]registry.Metadata{"react": {Homepage: "https://react.dev/"}},
		notFound: map[string]bool{"acme-internal": true},
	}
	g := newGenerator(packageJSON{}, lookup, sysdeptest.NewFakeFileSystem(), "/gen", rep)

	manifest := []byte(`{"dependencies":{"react":"*","acme-internal":"*"}}`)
	_, err := g.Generate(context.Background(), manifest)
	require.NoError(t, err)
	require.Equal(t, []string{
		"lookups:2",
		"lookup:1/2", // acme-internal: not found still completes the lookup
		"lookup:2/2",
	}, rep.events)

	// The second run is served from the output cache: a single cached event,
	// zero lookups.
	rep.events = nil
	_, err = g.Generate(context.Background(), manifest)
	require.NoError(t, err)
	require.Equal(t, []string{"cached"}, rep.events)
	require.Equal(t, 2, lookup.Calls, "no further registry lookups on output-cache hit")
}

func TestGenerate_NilReporterIsSilentAndSafe(t *testing.T) {
	lookup := &fakeLookuper{meta: map[string]registry.Metadata{"react": {Homepage: "https://react.dev/"}}}
	g := newGenerator(packageJSON{}, lookup, sysdeptest.NewFakeFileSystem(), "/gen", nil)

	_, err := g.Generate(context.Background(), []byte(`{"dependencies":{"react":"*"}}`))
	require.NoError(t, err)
}
