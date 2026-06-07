//go:build integration

// Real-host test for OSFilesystemTyper: it calls statfs(2), so it runs under
// `make test-integration`. A normal temp dir is on a local filesystem (apfs/hfs),
// so FSType returns a non-empty name and Local==true; a missing path surfaces as
// fs.ErrNotExist so callers can ascend to an existing ancestor.
package sysdep_test

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestOSFilesystemTyperLocalTempDir(t *testing.T) {
	info, err := sysdep.OSFilesystemTyper{}.FSType(t.TempDir())
	require.NoError(t, err)
	require.NotEmpty(t, info.Name, "expected a filesystem type name (e.g. apfs)")
	require.True(t, info.Local, "a temp dir should be on a local (MNT_LOCAL) filesystem, got %q", info.Name)
}

func TestOSFilesystemTyperMissingPathIsNotExist(t *testing.T) {
	_, err := sysdep.OSFilesystemTyper{}.FSType(filepath.Join(t.TempDir(), "does-not-exist"))
	require.Error(t, err)
	require.True(t, errors.Is(err, fs.ErrNotExist), "statfs on a missing path should be fs.ErrNotExist, got %v", err)
}
