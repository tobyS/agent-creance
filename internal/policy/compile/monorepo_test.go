package compile

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/generator"
	"github.com/tobyS/agent-creance/internal/policy"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// contentRunner is a hermetic generatorRunner keyed on the manifest bytes (not the
// generator name), so two manifests of the same type can return different rules. It
// records the manifest bodies it ran, in order, for fan-out assertions.
type contentRunner struct {
	byBody map[string][]generator.Rule
	runs   []string
}

func (r *contentRunner) Run(_ context.Context, _ string, manifest []byte) ([]generator.Rule, error) {
	r.runs = append(r.runs, string(manifest))
	return r.byBody[string(manifest)], nil
}

func (r *contentRunner) Invalidate(string, []byte) (generator.InvalidationStats, error) {
	return generator.InvalidationStats{}, nil
}

func allowByHost(t *testing.T, fsys *sysdeptest.FakeFileSystem, policyPath string) map[string]policy.Rule {
	t.Helper()
	var compiled policy.Compiled
	require.NoError(t, json.Unmarshal(fsys.Files[policyPath], &compiled))
	out := map[string]policy.Rule{}
	for _, r := range compiled.Allow {
		out[r.Host] = r
	}
	return out
}

// TestCompile_MultiManifestMonorepo: two package_json generators (root + a sub-package)
// each read their own manifest and both contribute rules. The root manifest keeps the
// bare source label; the sub-package manifest's rule is path-qualified so policy show
// can attribute it.
func TestCompile_MultiManifestMonorepo(t *testing.T) {
	files := map[string]string{
		projDir + "/.agent-creance.yaml": "network:\n  egress:\n    generators:\n" +
			"      - type: package_json\n        path: package.json\n" +
			"      - type: package_json\n        path: apps/web/package.json\n",
		projDir + "/package.json":          `{"root":true}`,
		projDir + "/apps/web/package.json": `{"web":true}`,
	}
	runner := &contentRunner{byBody: map[string][]generator.Rule{
		`{"root":true}`: {{Rule: policy.Rule{Host: "react.dev"}, Source: "generated:package_json:react"}},
		`{"web":true}`:  {{Rule: policy.Rule{Host: "vuejs.org"}, Source: "generated:package_json:vue"}},
	}}

	c, fsys := fixture(t, files, runner)
	res, err := c.Compile(context.Background(), projDir)
	require.NoError(t, err)

	require.Len(t, runner.runs, 2, "both manifests run")

	allow := allowByHost(t, fsys, res.PolicyPath)
	require.Contains(t, allow, "react.dev")
	require.Contains(t, allow, "vuejs.org")
	// Root manifest → bare label (single-repo output unchanged).
	require.Equal(t, "generated:package_json:react", allow["react.dev"].Source)
	// Sub-package manifest → path woven in for disambiguation.
	require.Equal(t, "generated:package_json:apps/web/package.json:vue", allow["vuejs.org"].Source)
}

// TestCompile_BareFormResolvesToRoot: the bare-string form still reads the root manifest
// and emits the bare (path-free) source label.
func TestCompile_BareFormResolvesToRoot(t *testing.T) {
	files := map[string]string{
		projDir + "/.agent-creance.yaml": "network:\n  egress:\n    generators:\n      - package_json\n",
		projDir + "/package.json":        `{"root":true}`,
	}
	runner := &contentRunner{byBody: map[string][]generator.Rule{
		`{"root":true}`: {{Rule: policy.Rule{Host: "react.dev"}, Source: "generated:package_json:react"}},
	}}

	c, fsys := fixture(t, files, runner)
	res, err := c.Compile(context.Background(), projDir)
	require.NoError(t, err)

	require.Equal(t, []string{`{"root":true}`}, runner.runs)
	allow := allowByHost(t, fsys, res.PolicyPath)
	require.Equal(t, "generated:package_json:react", allow["react.dev"].Source)
}

// TestCompile_ResolvedPathDedupe: a bare entry and an explicit entry that resolve to the
// same manifest run the generator only once.
func TestCompile_ResolvedPathDedupe(t *testing.T) {
	files := map[string]string{
		projDir + "/.agent-creance.yaml": "network:\n  egress:\n    generators:\n" +
			"      - package_json\n" +
			"      - type: package_json\n        path: package.json\n",
		projDir + "/package.json": `{"root":true}`,
	}
	runner := &contentRunner{byBody: map[string][]generator.Rule{
		`{"root":true}`: {{Rule: policy.Rule{Host: "react.dev"}, Source: "generated:package_json:react"}},
	}}

	c, _ := fixture(t, files, runner)
	_, err := c.Compile(context.Background(), projDir)
	require.NoError(t, err)
	require.Len(t, runner.runs, 1, "bare + explicit-root resolve to one manifest, run once")
}

// TestCompile_InputHashWatchesSubManifest: editing a sub-package manifest changes the
// input hash (so a recompile is not skipped), proving each referenced manifest is
// watched even when two manifests of the same type share identical bytes.
func TestCompile_InputHashWatchesSubManifest(t *testing.T) {
	files := map[string]string{
		projDir + "/.agent-creance.yaml": "network:\n  egress:\n    generators:\n" +
			"      - type: package_json\n        path: package.json\n" +
			"      - type: package_json\n        path: apps/web/package.json\n",
		projDir + "/package.json":          `{}`,
		projDir + "/apps/web/package.json": `{}`, // identical bytes to root
	}
	runner := &contentRunner{byBody: map[string][]generator.Rule{}}
	c, fsys := fixture(t, files, runner)

	res1, err := c.Compile(context.Background(), projDir)
	require.NoError(t, err)
	require.False(t, res1.Skipped)

	fsys.Files[projDir+"/apps/web/package.json"] = []byte(`{"web":1}`)

	res2, err := c.Compile(context.Background(), projDir)
	require.NoError(t, err)
	require.False(t, res2.Skipped, "editing the sub-package manifest must force a recompile")
	require.NotEqual(t, res1.InputHash, res2.InputHash)
}
