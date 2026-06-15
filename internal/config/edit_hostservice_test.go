package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppendHostServiceExistingList(t *testing.T) {
	src := `network:
  host_services:
    - web:3000  # keep this comment
  egress:
    allow:
      - host: example.com
`
	got, changed, err := AppendHostService([]byte(src), HostService{Label: "api", Port: 8080})
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(got), "- web:3000  # keep this comment", "existing comment must survive")
	require.Contains(t, string(got), "    - api:8080")

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []HostService{{Label: "web", Port: 3000}, {Label: "api", Port: 8080}}, cfg.Network.HostServices)
	// The egress allow list must be untouched.
	require.Len(t, cfg.Network.Egress.Allow, 1)
}

func TestAppendHostServiceSynthesizesStructure(t *testing.T) {
	src := `agent:
  command: [claude]
`
	got, changed, err := AppendHostService([]byte(src), HostService{Label: "web", Port: 5173})
	require.NoError(t, err)
	require.True(t, changed)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []HostService{{Label: "web", Port: 5173}}, cfg.Network.HostServices)
	require.Contains(t, string(got), "agent:", "original content preserved")
}

func TestAppendHostServiceNetworkWithoutHostServices(t *testing.T) {
	src := `network:
  egress:
    allow:
      - host: example.com
`
	got, changed, err := AppendHostService([]byte(src), HostService{Label: "web", Port: 3000})
	require.NoError(t, err)
	require.True(t, changed)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []HostService{{Label: "web", Port: 3000}}, cfg.Network.HostServices)
	require.Len(t, cfg.Network.Egress.Allow, 1)
}

func TestAppendHostServiceDuplicatePortNoOp(t *testing.T) {
	src := `network:
  host_services:
    - web:3000
`
	got, changed, err := AppendHostService([]byte(src), HostService{Label: "other", Port: 3000})
	require.NoError(t, err)
	require.False(t, changed, "same port is a no-op regardless of label")
	require.Equal(t, src, string(got))
}

func TestAppendHostServiceEmptyFile(t *testing.T) {
	got, changed, err := AppendHostService([]byte(""), HostService{Label: "web", Port: 8000})
	require.NoError(t, err)
	require.True(t, changed)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []HostService{{Label: "web", Port: 8000}}, cfg.Network.HostServices)
}

func TestAppendHostServiceUnparseable(t *testing.T) {
	_, _, err := AppendHostService([]byte("network: [unterminated"), HostService{Label: "web", Port: 3000})
	require.Error(t, err)
}

func TestRenderHelpers(t *testing.T) {
	require.Equal(t, []string{"  - web:3000"}, RenderHostService(HostService{Label: "web", Port: 3000}, 2))

	methods := []string{"GET"}
	got := RenderRule(Rule{Host: "example.com", Methods: &methods, Reason: "x"}, 6)
	require.Equal(t, []string{
		"      - host: example.com",
		"        methods: [GET]",
		`        reason: "x"`,
	}, got)
	require.Equal(t, strings.Repeat(" ", 6)+"- host: example.com", got[0])
}
