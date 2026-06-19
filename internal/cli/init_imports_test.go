package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
)

// seedClaude writes a project .claude/settings.json with one WebFetch domain.
func (f *initFixture) seedClaudeWebDomain(host string) {
	f.fs.Files[filepath.Join(initDir, ".claude", "settings.json")] =
		[]byte(`{"permissions":{"allow":["WebFetch(domain:` + host + `)"]}}`)
}

func TestInitImportsWebDomainInteractive(t *testing.T) {
	f := newInitFixture()
	f.term.Interactive = true
	f.seedClaudeWebDomain("docs.example.com")
	// web gate=y, review=y, agent-prompt=n
	f.withStdin("y\ny\nn\n")

	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false, false))

	cfg, err := config.Parse(f.configAt(t))
	require.NoError(t, err)
	require.Len(t, cfg.Network.Egress.Allow, 1)
	r := cfg.Network.Egress.Allow[0]
	require.Equal(t, "docs.example.com", r.Host)
	require.Equal(t, config.ModeIntercept, r.Mode)
	require.NotNil(t, r.Methods)
	require.Equal(t, []string{"GET"}, *r.Methods)
}

func TestInitImportsDeclineReviewWritesNothing(t *testing.T) {
	f := newInitFixture()
	f.term.Interactive = true
	f.seedClaudeWebDomain("docs.example.com")
	// web gate=y, review=n
	f.withStdin("y\nn\n")

	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false, false))
	_, ok := f.fs.Files[filepath.Join(initDir, configFile)]
	require.False(t, ok, "declined review must write no config")
	require.Contains(t, f.out.String(), "not written")
}

func TestInitImportsDeclineGateScaffoldsPlain(t *testing.T) {
	f := newInitFixture()
	f.term.Interactive = true
	f.seedClaudeWebDomain("docs.example.com")
	// web gate=n → nothing imported → no review prompt → plain scaffold written.
	// remaining line: agent-prompt=n
	f.withStdin("n\nn\n")

	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false, false))
	cfg, err := config.Parse(f.configAt(t))
	require.NoError(t, err)
	require.Empty(t, cfg.Network.Egress.Allow, "declined gate imports nothing")
}

func TestInitImportsDetectedPorts(t *testing.T) {
	f := newInitFixture()
	f.term.Interactive = true
	f.fs.Files[filepath.Join(initDir, "docker-compose.yml")] =
		[]byte("services:\n  web:\n    ports:\n      - \"8080:80\"\n")
	// port gate=y, review=y, agent-prompt=n
	f.withStdin("y\ny\nn\n")

	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false, false))
	cfg, err := config.Parse(f.configAt(t))
	require.NoError(t, err)
	require.Equal(t, []config.HostService{{Label: "web", Port: 8080}}, cfg.Network.HostServices)
}

func TestInitImportsAgentPromptPrinted(t *testing.T) {
	f := newInitFixture()
	f.term.Interactive = true
	// no imports available → no gates/review; only the agent-prompt offer (=y).
	f.withStdin("y\n")

	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false, false))
	require.Contains(t, f.out.String(), "agent-creance.suggested.yaml")
	require.Contains(t, f.out.String(), "host_services:")
}

func TestInitNonInteractiveIgnoresClaudeSettings(t *testing.T) {
	f := newInitFixture() // terminal non-interactive by default
	f.seedClaudeWebDomain("docs.example.com")

	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false, false))
	cfg, err := config.Parse(f.configAt(t))
	require.NoError(t, err)
	require.Empty(t, cfg.Network.Egress.Allow, "non-interactive must not import")
	require.NotContains(t, f.out.String(), "Import")
}
