package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRenderJSON golden-tests the machine-readable report against the same
// fixtures TestRender uses, so the JSON shape stays pinned. Regenerate with
// `make golden` or `go test ./internal/doctor -run TestRenderJSON -update`.
func TestRenderJSON(t *testing.T) {
	for name, rep := range goldenCases() {
		t.Run(name, func(t *testing.T) {
			got, err := RenderJSON(rep)
			require.NoError(t, err)
			require.True(t, json.Valid([]byte(got)), "RenderJSON must emit valid JSON")

			golden := filepath.Join("testdata", "render_"+name+".json.golden")
			if *update {
				require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
				return
			}
			want, err := os.ReadFile(golden)
			require.NoError(t, err, "missing golden file; run with -update to create it")
			require.Equal(t, string(want), got)
		})
	}
}
