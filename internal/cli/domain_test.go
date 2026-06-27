package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
)

func TestDomainAddPathsAndMethodsUppercased(t *testing.T) {
	f := newMutateFixture(t)

	opts := domainAddOpts{paths: []string{"/repos/"}, methods: []string{"get", "Post"}}
	require.NoError(t, runDomainAdd(context.Background(), f.app, mutProjDir, "api.github.com", opts))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	r := findHost(t, cfg.Network.Egress.Allow, "api.github.com")
	require.Equal(t, []string{"/repos/"}, *r.Paths)
	require.Equal(t, []string{"GET", "POST"}, *r.Methods, "methods are uppercased for the case-sensitive enforcer")
}

func TestDomainAddAllPathsConflict(t *testing.T) {
	f := newMutateFixture(t)
	err := runDomainAdd(context.Background(), f.app, mutProjDir, "example.com",
		domainAddOpts{allPaths: true, paths: []string{"/x/"}})
	require.ErrorContains(t, err, "cannot combine --all-paths with --path")
}

func TestDomainAddPassthroughWithPathsRejected(t *testing.T) {
	f := newMutateFixture(t)
	err := runDomainAdd(context.Background(), f.app, mutProjDir, "example.com",
		domainAddOpts{mode: config.ModePassthrough, paths: []string{"/x/"}})
	require.ErrorContains(t, err, "passthrough cannot carry paths")
}

func TestDomainAddDenyRejectsMethodAndMode(t *testing.T) {
	f := newMutateFixture(t)
	err := runDomainAdd(context.Background(), f.app, mutProjDir, "example.com",
		domainAddOpts{deny: true, methods: []string{"GET"}})
	require.ErrorContains(t, err, "not valid with --deny")

	err = runDomainAdd(context.Background(), f.app, mutProjDir, "example.com",
		domainAddOpts{deny: true, mode: config.ModeIntercept})
	require.ErrorContains(t, err, "not valid with --deny")
}

func TestDomainAddUnknownMode(t *testing.T) {
	f := newMutateFixture(t)
	err := runDomainAdd(context.Background(), f.app, mutProjDir, "example.com",
		domainAddOpts{allPaths: true, mode: "bogus"})
	require.ErrorContains(t, err, "unknown --mode")
}

func TestDomainAddDenyWritesDenyList(t *testing.T) {
	f := newMutateFixture(t)
	require.NoError(t, runDomainAdd(context.Background(), f.app, mutProjDir, "w3schools.com",
		domainAddOpts{deny: true, reason: "low quality"}))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	r := findHost(t, cfg.Network.Egress.DenyAlways, "w3schools.com")
	require.Equal(t, "low quality", r.Reason)
	require.Nil(t, r.Paths, "an unscoped deny is host-wide")
}

func TestDomainAddPassthroughHostWide(t *testing.T) {
	f := newMutateFixture(t)
	require.NoError(t, runDomainAdd(context.Background(), f.app, mutProjDir, "pinned.example",
		domainAddOpts{mode: config.ModePassthrough}))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	r := findHost(t, cfg.Network.Egress.Allow, "pinned.example")
	require.Equal(t, config.ModePassthrough, r.Mode)
	require.Nil(t, r.Paths)
}

func TestDomainRemoveWholeRule(t *testing.T) {
	f := newMutateFixture(t)
	require.NoError(t, runDomainAdd(context.Background(), f.app, mutProjDir, "react.dev", domainAddOpts{allPaths: true}))

	require.NoError(t, runDomainRemove(context.Background(), f.app, mutProjDir, "react.dev", "", false))
	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	for _, r := range cfg.Network.Egress.Allow {
		require.NotEqual(t, "react.dev", r.Host, "the rule was removed")
	}
}

func TestDomainRemoveSinglePath(t *testing.T) {
	f := newMutateFixture(t)
	require.NoError(t, runDomainAdd(context.Background(), f.app, mutProjDir, "api.github.com",
		domainAddOpts{paths: []string{"/repos/", "/user/"}}))

	require.NoError(t, runDomainRemove(context.Background(), f.app, mutProjDir, "api.github.com", "/repos/", false))
	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	r := findHost(t, cfg.Network.Egress.Allow, "api.github.com")
	require.Equal(t, []string{"/user/"}, *r.Paths)
}

func TestDomainRemoveNotFound(t *testing.T) {
	f := newMutateFixture(t)
	err := runDomainRemove(context.Background(), f.app, mutProjDir, "notthere.example", "", false)
	require.ErrorContains(t, err, "nothing to remove")
}
