package audit_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "regenerate golden files")

// assertGolden compares got against testdata/<name>, regenerating it under -update.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	golden := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err, "missing golden file %s; run with -update to create it", golden)
	require.Equal(t, string(want), got)
}
