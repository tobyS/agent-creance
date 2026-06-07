//go:build integration

// Real-host test for OSListenerScanner: it shells out to lsof, so it can only run
// under `make test-integration`. We open a real loopback listener and assert the
// scanner finds it; we also bind 0.0.0.0 and assert IsExposed flags it. Both binds
// are on ephemeral ports we own, so the test needs no privileges.
package sysdep_test

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestOSListenerScannerFindsLoopbackListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	listeners, err := sysdep.OSListenerScanner{}.Listeners(ctx)
	require.NoError(t, err)

	var found bool
	for _, l := range listeners {
		if strings.HasSuffix(l.Address, ":"+port) {
			found = true
			require.False(t, sysdep.IsExposed(l.Address), "a 127.0.0.1 listener is not exposed: %s", l.Address)
		}
	}
	require.True(t, found, "scanner did not find the loopback listener on port %s", port)
}

func TestOSListenerScannerFlagsWildcardBind(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	require.Positive(t, port)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	listeners, err := sysdep.OSListenerScanner{}.Listeners(ctx)
	require.NoError(t, err)

	var exposed bool
	for _, l := range listeners {
		if strings.HasSuffix(l.Address, ":"+portStr) && sysdep.IsExposed(l.Address) {
			exposed = true
		}
	}
	require.True(t, exposed, "scanner did not flag the 0.0.0.0 listener on port %d as exposed", port)
}
