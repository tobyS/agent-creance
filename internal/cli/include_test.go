package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
)

// goodFragment is a valid config fragment an include can point at.
const goodFragment = "network:\n  egress:\n    allow:\n      - host: frag.example\n"

func TestIncludeAppendsToProjectFile(t *testing.T) {
	f := newMutateFixture(t)
	f.fs.Files[mutProjDir+"/baseline.yaml"] = []byte(goodFragment)

	require.NoError(t, runInclude(context.Background(), f.app, mutProjDir, "./baseline.yaml"))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	require.Contains(t, cfg.Include, "./baseline.yaml")

	// The mutation recompiled and the include's rule is merged into policy.json.
	require.Contains(t, string(f.policyJSON(t)), "frag.example")
	require.Contains(t, f.out.String(), "policy recompiled")
}

func TestIncludeDuplicateIsNoOp(t *testing.T) {
	f := newMutateFixture(t)
	f.fs.Files[mutProjDir+"/baseline.yaml"] = []byte(goodFragment)

	require.NoError(t, runInclude(context.Background(), f.app, mutProjDir, "./baseline.yaml"))
	after := string(f.projectConfig(t))
	f.out.Reset()

	require.NoError(t, runInclude(context.Background(), f.app, mutProjDir, "./baseline.yaml"))
	require.Equal(t, after, string(f.projectConfig(t)))
	require.Contains(t, f.out.String(), "already included")
}

func TestIncludeMissingTargetErrors(t *testing.T) {
	f := newMutateFixture(t)

	err := runInclude(context.Background(), f.app, mutProjDir, "./missing.yaml")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing.yaml")
	// Config untouched: the pre-check failed before any write.
	require.Equal(t, seededAllow, string(f.projectConfig(t)))
}

func TestIncludeUnparseableTargetErrors(t *testing.T) {
	f := newMutateFixture(t)
	f.fs.Files[mutProjDir+"/bad.yaml"] = []byte("network:\n  egress:\n    allow: not-a-list\n")

	err := runInclude(context.Background(), f.app, mutProjDir, "./bad.yaml")
	require.Error(t, err)
	require.Contains(t, err.Error(), "bad.yaml")
	require.Equal(t, seededAllow, string(f.projectConfig(t)))
}
