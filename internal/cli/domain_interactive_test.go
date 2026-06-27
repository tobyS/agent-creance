package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// interactive wires the fixture's App for a prompt: an interactive terminal and a stdin
// preloaded with the scripted answers (one line consumed per prompt).
func interactive(f *mutateFixture, answers string) {
	f.app.Terminal = &sysdeptest.FakeTerminal{Interactive: true}
	f.app.Stdin = strings.NewReader(answers)
}

func TestDomainAddInteractiveAllPaths(t *testing.T) {
	f := newMutateFixture(t)
	interactive(f, "1\n") // choose "All paths"

	require.NoError(t, runDomainAdd(context.Background(), f.app, mutProjDir, "react.dev", domainAddOpts{}))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	require.Nil(t, findHost(t, cfg.Network.Egress.Allow, "react.dev").Paths, "all-paths chosen → host-wide rule")
	require.Contains(t, f.out.String(), "Allow all paths")
}

func TestDomainAddInteractiveSpecificPaths(t *testing.T) {
	f := newMutateFixture(t)
	interactive(f, "2\n/repos/ /user/\n") // "Specific paths", then two prefixes

	require.NoError(t, runDomainAdd(context.Background(), f.app, mutProjDir, "api.github.com", domainAddOpts{}))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	r := findHost(t, cfg.Network.Egress.Allow, "api.github.com")
	require.Equal(t, []string{"/repos/", "/user/"}, *r.Paths)
}

func TestDomainAddInteractiveSpecificButEmptyErrors(t *testing.T) {
	f := newMutateFixture(t)
	interactive(f, "2\n\n") // "Specific paths" then an empty line

	err := runDomainAdd(context.Background(), f.app, mutProjDir, "api.github.com", domainAddOpts{})
	require.ErrorContains(t, err, "no paths entered")
}

func TestDomainAddNonInteractiveMissingPathsHint(t *testing.T) {
	f := newMutateFixture(t)
	f.app.Terminal = &sysdeptest.FakeTerminal{Interactive: false}
	f.app.Stdin = strings.NewReader("")

	err := runDomainAdd(context.Background(), f.app, mutProjDir, "lonely.example", domainAddOpts{})
	require.ErrorContains(t, err, "no terminal for interactive input")
	require.ErrorContains(t, err, "all-paths")
	// Nothing was written.
	require.Equal(t, seededAllow, string(f.projectConfig(t)))
}
