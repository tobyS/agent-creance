package render_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/policy"
	"github.com/tobyS/agent-creance/internal/policy/compile"
	"github.com/tobyS/agent-creance/internal/policy/render"
	"github.com/tobyS/agent-creance/internal/style"
)

var update = flag.Bool("update", false, "regenerate golden files")

// fixture mirrors internal/policy/compile/testdata/policy.golden: it exercises a
// passthrough (global) allow, an explicit host+path+method allow that injects a
// credential, an in-cage allow, two generated react rules, a lower-trust generated
// rule, a session (once) rule, a host-wide global deny, an **/.env explicit deny with
// a reason, and a top-level credentials: block (reference only, AC-0068b).
func fixture() policy.Compiled {
	return policy.Compiled{
		Version:   1,
		InputHash: "02c3f8990f8181bf49c2d3aa767927779a03598387edb3cb6097a91840ffecd8",
		Credentials: map[string]policy.Credential{
			"github-token": {Source: "op://Private/GitHub PAT/token", Header: "Authorization", Template: "Bearer {token}"},
		},
		RuleSet: policy.RuleSet{
			Allow: []policy.Rule{
				{Host: "api.anthropic.com", Mode: "passthrough", Source: "global"},
				{Host: "api.github.com", Paths: []string{"/repos/tobyS/x/"}, Methods: []string{"GET", "POST"}, Mode: "intercept", Inject: "github-token", Source: "explicit"},
				{Host: "s3.eu-central-1.amazonaws.com", Mode: "intercept", InCage: true, Source: "explicit"},
				{Host: "react.dev", Mode: "intercept", Source: "generated:package_json:react"},
				{Host: "github.com", Paths: []string{"/facebook/react/"}, Mode: "intercept", Source: "generated:package_json:react"},
				{Host: "objects.githubusercontent.com", Mode: "intercept", Source: "generated:package_json:react", LowerTrust: true},
				{Host: "docs.somelib.io", Paths: []string{"/v2/"}, Mode: "intercept", Source: "once"},
			},
			DenyAlways: []policy.Rule{
				{Host: "w3schools.com", Mode: "intercept", Reason: "low quality", Source: "global"},
				{Host: "*", Paths: []string{"**/.env"}, Mode: "intercept", Reason: "secrets", Source: "explicit"},
			},
		},
	}
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	golden := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err, "missing golden %s; run with -update to create it", name)
	require.Equal(t, string(want), got)
}

// assertGoldenModes pins both the plain and colored rendering. Plain keeps the
// original golden name (proving byte-identical plain parity); color adds a
// _color sibling. r is invoked once per mode with the matching styler.
func assertGoldenModes(t *testing.T, baseName string, r func(*style.Styler) string) {
	t.Helper()
	base := strings.TrimSuffix(baseName, ".golden")
	assertGolden(t, base+".golden", r(style.Plain()))
	assertGolden(t, base+"_color.golden", r(style.New(true)))
}

func TestShow(t *testing.T) {
	assertGoldenModes(t, "show.golden", func(s *style.Styler) string { return render.Show(fixture(), s) })
}

func TestShowJSON(t *testing.T) {
	got, err := render.ShowJSON(fixture())
	require.NoError(t, err)
	assertGolden(t, "show.json.golden", got)
}

func TestShowEmpty(t *testing.T) {
	assertGoldenModes(t, "show_empty.golden", func(s *style.Styler) string {
		return render.Show(policy.Compiled{Version: 1, InputHash: "deadbeef"}, s)
	})
}

func TestExplain(t *testing.T) {
	cases := []struct {
		name string
		req  policy.Request
	}{
		{"allow", policy.Request{Host: "github.com", Path: "/facebook/react/pull/1", Method: "GET"}},
		{"allow_passthrough", policy.Request{Host: "api.anthropic.com", Path: "/v1/messages", Method: "POST"}},
		{"hard_deny", policy.Request{Host: "w3schools.com", Path: "/html", Method: "GET"}},
		{"soft_deny", policy.Request{Host: "evil.test", Path: "/", Method: "GET"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertGoldenModes(t, "explain_"+tc.name+".golden", func(s *style.Styler) string {
				return render.Explain(fixture(), tc.req, s)
			})
		})
	}
}

// refreshFixture is a two-generator refresh result: one with all entries cleared, one
// with a singular package whose entry was already absent (exercises plural/singular and
// the zero-cleared path).
func refreshFixture() compile.RefreshResult {
	return compile.RefreshResult{
		Generators: []compile.GeneratorRefresh{
			{Name: "package_json", Packages: 3, CacheEntriesCleared: 3, OutputCacheCleared: true},
			{Name: "composer_json", Packages: 1, CacheEntriesCleared: 0, OutputCacheCleared: false},
		},
		AllowCount: 7,
		DenyCount:  2,
	}
}

func TestRefresh(t *testing.T) {
	assertGoldenModes(t, "refresh.golden", func(s *style.Styler) string { return render.Refresh(refreshFixture(), s) })
}

func TestRefreshJSON(t *testing.T) {
	got, err := render.RefreshJSON(refreshFixture())
	require.NoError(t, err)
	assertGolden(t, "refresh.json.golden", got)
}

func TestRefreshEmpty(t *testing.T) {
	assertGoldenModes(t, "refresh_empty.golden", func(s *style.Styler) string {
		return render.Refresh(compile.RefreshResult{AllowCount: 2, DenyCount: 1}, s)
	})
}

func TestRefreshEmptyJSON(t *testing.T) {
	got, err := render.RefreshJSON(compile.RefreshResult{AllowCount: 2, DenyCount: 1})
	require.NoError(t, err)
	assertGolden(t, "refresh_empty.json.golden", got)
}

func TestExplainJSON(t *testing.T) {
	cases := []struct {
		name string
		req  policy.Request
	}{
		{"allow", policy.Request{Host: "github.com", Path: "/facebook/react/pull/1", Method: "GET"}},
		{"soft_deny", policy.Request{Host: "evil.test", Path: "/", Method: "GET"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := render.ExplainJSON(fixture(), tc.req)
			require.NoError(t, err)
			assertGolden(t, "explain_"+tc.name+".json.golden", got)
		})
	}
}
