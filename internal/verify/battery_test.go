package verify

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProbeOutput(t *testing.T) {
	out := strings.Join([]string{
		"noise before",
		"CREANCE::net-raw-tcp::blocked",
		"  CREANCE::proxy-soft-deny::403:soft-deny  ", // surrounding space trimmed
		"CREANCE::allow-200::skip",
		"CREANCE::malformed-without-second-delim",     // ignored (no ::)
		"CREANCE::::orphan",                           // empty id, ignored
		"CREANCE::doc-post::post-sent::extra::detail", // observed keeps the tail
		"CREANCE::net-raw-tcp::LEAK",                  // last write wins
		"trailing noise",
	}, "\n")

	got := ParseProbeOutput(out)

	assert.Equal(t, "LEAK", got["net-raw-tcp"], "last line for an id wins")
	assert.Equal(t, "403:soft-deny", got["proxy-soft-deny"])
	assert.Equal(t, "skip", got["allow-200"])
	assert.Equal(t, "post-sent::extra::detail", got["doc-post"])
	assert.NotContains(t, got, "malformed-without-second-delim")
	assert.NotContains(t, got, "")
}

// greenObservations returns the Expected outcome for every vector — a perfect run.
func greenObservations() map[string]string {
	m := make(map[string]string, len(Vectors))
	for _, v := range Vectors {
		m[v.ID] = v.Expected
	}
	return m
}

func TestEvaluate_AllGreen(t *testing.T) {
	v := Evaluate(greenObservations())
	require.False(t, v.Failed, v.Summary())
	require.False(t, v.Escaped, v.Summary())
	for _, r := range v.Results {
		assert.True(t, r.OK, "%s should pass", r.ID)
		assert.False(t, r.Escape)
	}
}

func TestEvaluate_BlockedLeakIsEscape(t *testing.T) {
	obs := greenObservations()
	obs["net-raw-tcp"] = leakToken // the deny-baseline let raw egress through

	v := Evaluate(obs)

	require.True(t, v.Escaped, "a leaked BLOCKED vector must flag an escape")
	require.True(t, v.Failed)
	assert.Contains(t, v.Summary(), "ESCAPE DETECTED")

	var found bool
	for _, r := range v.Results {
		if r.ID == "net-raw-tcp" {
			found = true
			assert.True(t, r.Escape)
		}
	}
	require.True(t, found)
}

func TestEvaluate_Proxy200IsEscape(t *testing.T) {
	obs := greenObservations()
	obs["proxy-soft-deny"] = "200" // a non-allowlisted host was forwarded
	v := Evaluate(obs)
	require.True(t, v.Escaped, "a soft-deny host returning 200 is an escape")
}

func TestEvaluate_AllowedBlockedIsFailureNotEscape(t *testing.T) {
	obs := greenObservations()
	obs["svc-allowed"] = "blocked" // over-blocking: a host_service got refused
	v := Evaluate(obs)
	require.True(t, v.Failed, "an over-blocked ALLOWED vector is a failure")
	require.False(t, v.Escaped, "over-blocking is not a security escape")
}

func TestEvaluate_SkipIsNeutral(t *testing.T) {
	obs := greenObservations()
	obs["allow-200"] = skipToken
	obs["passthrough"] = skipToken
	obs["doc-post"] = skipToken
	v := Evaluate(obs)
	require.False(t, v.Failed, v.Summary())
	require.False(t, v.Escaped)
	for _, r := range v.Results {
		if r.ID == "allow-200" {
			assert.True(t, r.Skipped)
			assert.True(t, r.OK)
		}
	}
}

func TestEvaluate_MissingLineIsFailure(t *testing.T) {
	obs := greenObservations()
	delete(obs, "doc-rm")
	v := Evaluate(obs)
	require.True(t, v.Failed, "a missing probe line is a failure")
	require.False(t, v.Escaped)
	assert.Contains(t, v.Summary(), "MISSING")
}

func TestVectorsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, vec := range Vectors {
		assert.NotEmpty(t, vec.ID)
		assert.NotEmpty(t, vec.Expected, "%s needs an Expected token", vec.ID)
		assert.NotEmpty(t, vec.Keyword, "%s needs a design Keyword", vec.ID)
		assert.False(t, seen[vec.ID], "duplicate vector id %s", vec.ID)
		seen[vec.ID] = true
		assert.Contains(t, []Label{LabelBlocked, LabelAllowed, LabelDocumented}, vec.Label)
	}
}
