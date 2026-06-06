package policy

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCorpusNotForked enforces the C1 parity contract structurally: the
// decision-vector corpus must exist in exactly ONE place in the repository, so the
// Python enforcer (AC-0017) and this Go matcher provably consume the *same* files
// rather than two copies that can silently drift. The Python test suite
// (internal/proxy/enforcer/test_vectors.py) reads internal/policy/testdata/
// decision-vectors/ directly for the same reason. This is the CI-free substitute
// for the ticket's "a CI step asserts both consume the identical files" check.
func TestCorpusNotForked(t *testing.T) {
	root := repoRoot(t)

	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Don't descend into VCS or the Python test venv.
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".venv-enforcer") {
			return fs.SkipDir
		}
		if d.IsDir() && d.Name() == "decision-vectors" {
			rel, _ := filepath.Rel(root, path)
			dirs = append(dirs, rel)
		}
		return nil
	})
	require.NoError(t, err)

	require.Equal(t, []string{
		filepath.Join("internal", "policy", "testdata", "decision-vectors"),
	}, dirs, "the decision-vector corpus must not be forked/copied; both the Go "+
		"matcher and the Python enforcer must read the single canonical directory")
}

// repoRoot walks up from the test's working directory to the module root (the
// directory containing go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "reached filesystem root without finding go.mod")
		dir = parent
	}
}
