package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const validProjectConfig = "/proj/.agent-creance.yaml"

// TestValidateInclude covers the pre-write check `agent-creance include` runs: an
// entry resolving to a readable, parseable config passes; a missing or unparseable
// target fails with an error naming the resolved path. Path forms (relative to the
// declaring file's dir, absolute, ~/-relative) resolve the same way the loader does.
func TestValidateInclude(t *testing.T) {
	const goodFragment = "network:\n  egress:\n    allow:\n      - host: frag.example\n"

	t.Run("relative resolves against the declaring file's dir", func(t *testing.T) {
		l, _ := newLoader(map[string]string{"/proj/base.yaml": goodFragment})
		require.NoError(t, l.ValidateInclude(validProjectConfig, "./base.yaml"))
	})

	t.Run("absolute path inside the project subtree", func(t *testing.T) {
		l, _ := newLoader(map[string]string{"/proj/shared/frag.yaml": goodFragment})
		require.NoError(t, l.ValidateInclude(validProjectConfig, "/proj/shared/frag.yaml"))
	})

	t.Run("absolute path outside scope rejected", func(t *testing.T) {
		// AC-0059 (F8): an absolute include outside the project subtree and ~/.config
		// is rejected before it is read, so `include` never commits a leak surface.
		l, _ := newLoader(map[string]string{"/shared/frag.yaml": goodFragment})
		err := l.ValidateInclude(validProjectConfig, "/shared/frag.yaml")
		require.ErrorIs(t, err, ErrIncludeOutOfScope)
	})

	t.Run("home-relative path outside scope rejected", func(t *testing.T) {
		l, _ := newLoader(map[string]string{testHome + "/baseline.yaml": goodFragment})
		err := l.ValidateInclude(validProjectConfig, "~/baseline.yaml")
		require.ErrorIs(t, err, ErrIncludeOutOfScope)
	})

	t.Run("path under the global config dir allowed", func(t *testing.T) {
		l, _ := newLoader(map[string]string{testHome + "/.config/baseline.yaml": goodFragment})
		require.NoError(t, l.ValidateInclude(validProjectConfig, "~/.config/baseline.yaml"))
	})

	t.Run("missing target errors naming the resolved path", func(t *testing.T) {
		l, _ := newLoader(map[string]string{})
		err := l.ValidateInclude(validProjectConfig, "./missing.yaml")
		require.Error(t, err)
		require.Contains(t, err.Error(), "/proj/missing.yaml")
		require.Contains(t, err.Error(), "not found")
	})

	t.Run("unparseable target errors naming the resolved path", func(t *testing.T) {
		l, _ := newLoader(map[string]string{"/proj/bad.yaml": "network:\n  egress:\n    allow: not-a-list\n"})
		err := l.ValidateInclude(validProjectConfig, "./bad.yaml")
		require.Error(t, err)
		require.Contains(t, err.Error(), "/proj/bad.yaml")
	})
}
