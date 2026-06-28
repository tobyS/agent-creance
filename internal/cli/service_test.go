package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

func TestServiceAddAppends(t *testing.T) {
	f := newMutateFixture(t)
	require.NoError(t, runServiceAdd(f.app, mutProjDir, "mysql:3306"))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	require.Equal(t, []config.HostService{{Label: "mysql", Port: 3306}}, cfg.Network.HostServices)
	require.Contains(t, f.out.String(), "added mysql:3306")
	require.NotContains(t, f.errOut.String(), "next agent-creance run", "no cage running → no warning")
}

func TestServiceAddPromptsForLabel(t *testing.T) {
	f := newMutateFixture(t)
	f.app.Terminal = &sysdeptest.FakeTerminal{Interactive: true}
	f.app.Stdin = strings.NewReader("redis\n")

	require.NoError(t, runServiceAdd(f.app, mutProjDir, "6379"))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	require.Equal(t, []config.HostService{{Label: "redis", Port: 6379}}, cfg.Network.HostServices)
}

func TestServiceAddPortOnlyNonInteractiveHint(t *testing.T) {
	f := newMutateFixture(t)
	f.app.Terminal = &sysdeptest.FakeTerminal{Interactive: false}
	f.app.Stdin = strings.NewReader("")

	err := runServiceAdd(f.app, mutProjDir, "6379")
	require.ErrorContains(t, err, "LABEL:PORT")
}

func TestServiceRemoveByPort(t *testing.T) {
	f := newMutateFixture(t)
	require.NoError(t, runServiceAdd(f.app, mutProjDir, "mysql:3306"))

	require.NoError(t, runServiceRemove(f.app, mutProjDir, "3306"))
	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	require.Empty(t, cfg.Network.HostServices)
}

func TestServiceRemoveNotFound(t *testing.T) {
	f := newMutateFixture(t)
	err := runServiceRemove(f.app, mutProjDir, "9999")
	require.ErrorContains(t, err, "nothing to remove")
}

func TestServiceAddWarnsWhenCageRunning(t *testing.T) {
	f := newMutateFixture(t)
	// Seed a live proxy.lock: a lock naming an alive PID on a probed port makes
	// proxy.Manager.Inspect report ProxyUp, which drives the "next run" warning.
	f.flock.Contents[f.layout.ProxyLock()] =
		[]byte(`{"proxy_pid":4242,"port":8080,"policy_hash":"","agents":[],"canonical_path":""}`)
	f.procMgr.AlivePIDs[4242] = true
	f.portAlloc.Listening[8080] = true

	require.NoError(t, runServiceAdd(f.app, mutProjDir, "mysql:3306"))
	require.Contains(t, f.errOut.String(), "takes effect on the next agent-creance run")
	require.Contains(t, f.errOut.String(), "running cage is unchanged")
}
