package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// appendIncludeGoldenCases pin the splice output for each structural shape
// AppendInclude must handle: an existing populated include list (with a comment
// outside the insertion point), a config with no include: key yet (build from
// scratch at end of file), an empty flow list (`include: []`, rewritten to a block
// list), and a home-relative entry (quoted). Regenerate with `make golden` or
// `go test ./internal/config -run TestAppendIncludeGolden -update`.
var appendIncludeGoldenCases = []struct {
	name string
	inc  string
}{
	{"include_existing", "./extra.yaml"},
	{"include_from_scratch", "~/baseline.yaml"},
	{"include_empty_flow", "./frag.yaml"},
	{"include_home", "~/baseline.yaml"},
}

func TestAppendIncludeGolden(t *testing.T) {
	for _, tc := range appendIncludeGoldenCases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.name + ".in.yaml"
			// include_home reuses the existing-list fixture; only the entry differs.
			if tc.name == "include_home" {
				in = "include_existing.in.yaml"
			}
			src, err := os.ReadFile(filepath.Join("testdata", "edit", in))
			require.NoError(t, err)

			got, changed, err := AppendInclude(src, tc.inc)
			require.NoError(t, err)
			require.True(t, changed)

			// The result must parse and the appended entry must be present.
			cfg, err := Parse(got)
			require.NoError(t, err)
			require.True(t, containsInclude(cfg.Include, tc.inc),
				"appended include not found in parsed result")

			golden := filepath.Join("testdata", "edit", tc.name+".golden.yaml")
			if *update {
				require.NoError(t, os.WriteFile(golden, got, 0o644))
				return
			}
			want, err := os.ReadFile(golden)
			require.NoError(t, err, "missing golden file; run with -update to create it")
			require.Equal(t, string(want), string(got))
		})
	}
}

// TestAppendIncludeDuplicate: an entry already present verbatim is a no-op —
// changed is false and the bytes are returned unchanged.
func TestAppendIncludeDuplicate(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "edit", "include_existing.in.yaml"))
	require.NoError(t, err)

	got, changed, err := AppendInclude(src, "./base.yaml")
	require.NoError(t, err)
	require.False(t, changed, "expected a no-op for a duplicate include")
	require.Equal(t, string(src), string(got))
}

// TestAppendIncludeRejectsBrokenInput: AppendInclude refuses to edit a config that
// does not already parse, rather than appending to a broken file.
func TestAppendIncludeRejectsBrokenInput(t *testing.T) {
	_, _, err := AppendInclude([]byte("network:\n  egress:\n    allow: not-a-list\n"), "./x.yaml")
	require.Error(t, err)
}
