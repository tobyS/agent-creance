package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
)

func TestDenyWritesProjectFileWithReason(t *testing.T) {
	f := newMutateFixture(t)

	require.NoError(t, runDeny(context.Background(), f.app, mutProjDir, "w3schools.com", "low-quality source"))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	r := findHost(t, cfg.Network.Egress.DenyAlways, "w3schools.com")
	require.Equal(t, "low-quality source", r.Reason)

	// Recompiled, and a deny lands in policy.json's deny set.
	require.Contains(t, string(f.policyJSON(t)), "w3schools.com")
	require.Contains(t, f.out.String(), "policy recompiled")
}

func TestDenyWithoutReason(t *testing.T) {
	f := newMutateFixture(t)

	require.NoError(t, runDeny(context.Background(), f.app, mutProjDir, "spam.example", ""))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	require.Empty(t, findHost(t, cfg.Network.Egress.DenyAlways, "spam.example").Reason)
}

func TestDenyDuplicateIsNoOp(t *testing.T) {
	f := newMutateFixture(t)

	require.NoError(t, runDeny(context.Background(), f.app, mutProjDir, "dup.example", "first"))
	after := string(f.projectConfig(t))
	f.out.Reset()

	require.NoError(t, runDeny(context.Background(), f.app, mutProjDir, "dup.example", "second wording"))
	require.Equal(t, after, string(f.projectConfig(t)))
	require.Contains(t, f.out.String(), "already denied")
}
