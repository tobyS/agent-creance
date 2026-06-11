package compile

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/progress"
)

// recordingReporter logs progress events in call order — the same ordered-log
// idiom as fakeRunner.log — so tests can assert when the compiler emits what.
type recordingReporter struct {
	events []string
}

var _ progress.Reporter = (*recordingReporter)(nil)

func (r *recordingReporter) BuildStart(ms []progress.ManifestRef) {
	paths := make([]string, len(ms))
	for i, m := range ms {
		paths[i] = m.Path
	}
	r.events = append(r.events, "build-start:"+strings.Join(paths, ","))
}

func (r *recordingReporter) ManifestStart(m progress.ManifestRef) {
	r.events = append(r.events, "manifest-start:"+m.Path)
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

func TestCompile_ProgressEventsAndCacheHitSilence(t *testing.T) {
	files := representativeFiles()
	// A second manifest below the root exercises the monorepo fan-out ordering.
	files[projDir+"/.agent-creance.yaml"] = "" +
		"network:\n  egress:\n    generators:\n" +
		"      - package_json\n" +
		"      - type: package_json\n        path: apps/web/package.json\n"
	files[projDir+"/apps/web/package.json"] = `{"dependencies":{"vue":"^3"}}`

	rep := &recordingReporter{}
	c, _ := fixture(t, files, representativeRunner())
	c.rep = rep

	res, err := c.Compile(context.Background(), projDir)
	require.NoError(t, err)
	require.False(t, res.Skipped)
	require.Equal(t, []string{
		"build-start:package.json,apps/web/package.json",
		"manifest-start:package.json",
		"manifest-done",
		"manifest-start:apps/web/package.json",
		"manifest-done",
	}, rep.events, "miss: expectation message, then one start/done pair per manifest in config order")

	// The input-hash cache hit must stay completely silent: no generator work,
	// nothing to report.
	rep.events = nil
	res2, err := c.Compile(context.Background(), projDir)
	require.NoError(t, err)
	require.True(t, res2.Skipped)
	require.Empty(t, rep.events, "cache hit must emit no progress events")
}

func TestCompile_NoManifestsEmitsNoBuildStart(t *testing.T) {
	// A configured generator whose manifest is absent contributes no manifest
	// input — there is no lookup work ahead, so no expectation message either.
	files := map[string]string{
		projDir + "/.agent-creance.yaml": "network:\n  egress:\n    generators:\n      - package_json\n",
	}

	rep := &recordingReporter{}
	c, _ := fixture(t, files, representativeRunner())
	c.rep = rep

	res, err := c.Compile(context.Background(), projDir)
	require.NoError(t, err)
	require.False(t, res.Skipped)
	require.Empty(t, rep.events)
}
