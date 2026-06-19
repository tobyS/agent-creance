package style_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/style"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		noColor bool
		isTTY   bool
		want    bool
		wantErr bool
	}{
		{"always beats NO_COLOR", "always", true, false, true, false},
		{"always on a pipe", "always", false, false, true, false},
		{"never on a tty", "never", false, true, false, false},
		{"never with NO_COLOR", "never", true, true, false, false},
		{"auto on a tty", "auto", false, true, true, false},
		{"auto on a pipe", "auto", false, false, false, false},
		{"auto honors NO_COLOR on a tty", "auto", true, true, false, false},
		{"invalid value", "sometimes", false, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := style.Resolve(tc.mode, tc.noColor, tc.isTTY)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestDisabledIsIdentity(t *testing.T) {
	for _, s := range []*style.Styler{style.Plain(), style.New(false), nil} {
		require.False(t, s.Enabled())
		require.Equal(t, "ok", s.OK("ok"))
		require.Equal(t, "warn", s.Warn("warn"))
		require.Equal(t, "bad", s.Bad("bad"))
		require.Equal(t, "head", s.Header("head"))
		require.Equal(t, "dim", s.Dim("dim"))
	}
}

func TestEnabledWraps(t *testing.T) {
	s := style.New(true)
	require.True(t, s.Enabled())
	got := s.OK("✓")
	require.True(t, strings.HasPrefix(got, "\x1b["), "expected a leading SGR escape, got %q", got)
	require.True(t, strings.HasSuffix(got, "\x1b[0m"), "expected a trailing reset, got %q", got)
	require.Contains(t, got, "✓")
}

func TestVisibleWidth(t *testing.T) {
	s := style.New(true)
	plain := "✓ ok"
	colored := s.OK("✓") + " ok"
	// The colored string has more bytes/runes but the same visible width.
	require.Equal(t, 4, style.VisibleWidth(plain))
	require.Equal(t, 4, style.VisibleWidth(colored))
	require.Equal(t, style.VisibleWidth(plain), style.VisibleWidth(colored))
}
