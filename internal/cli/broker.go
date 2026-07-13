package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/broker"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// brokerSockPerm is the socket's mode, and the whole access control: 0600 in a
// 0700 state dir the cage never mounts. A peer-credential check would add nothing
// — a prompt-injected agent in the cage runs as the same uid as mitmproxy, so
// LOCAL_PEERCRED cannot tell them apart. See docs/design.md, "Credential injection".
const brokerSockPerm = 0o600

// newBrokerCmd builds the hidden `broker` command: the credential-broker daemon
// (AC-0069b), which the proxy lifecycle spawns as a detached sibling of mitmdump
// and never a user directly.
//
// Why a daemon of its own, rather than a goroutine in `run`: the proxy is shared
// per project and outlives the invocation that started it whenever a second agent
// is still attached. A broker owned by one run session would vanish under a live
// proxy, and every injected request from the surviving agent would 472 forever.
// The broker therefore shares the proxy's exact lifetime — spawned on the same
// branch, killed by the same last-out Detach.
//
// Why re-exec this binary instead of shipping a second one: the broker and the
// enforcer speak a protocol that must version together with the CLI that spawns
// them.
func newBrokerCmd(app *App) *cobra.Command {
	var sock string

	cmd := &cobra.Command{
		Use:    "broker",
		Short:  "Run the credential broker daemon (internal)",
		Hidden: true, // spawned by `run`, never invoked by hand
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBroker(cmd.Context(), app, sock)
		},
	}
	cmd.Flags().StringVar(&sock, "socket", "", "path of the unix socket to serve on (required)")
	_ = cmd.MarkFlagRequired("socket")
	return cmd
}

// runBroker reads the resolved credentials from the inherited descriptor, serves
// them on the socket until signalled, and wipes them on the way out.
func runBroker(ctx context.Context, app *App, sock string) error {
	// Before anything touches a secret: a core dump would spill every token this
	// process holds straight onto disk.
	if err := app.Memory.DisableCoreDumps(); err != nil {
		fmt.Fprintf(app.Stderr, "warning: %v\n", err)
	}

	store := broker.NewStore(app.Memory)
	defer store.Wipe()

	// A payload that cannot be read is not fatal. The broker serves an empty store,
	// every lookup misses, and the enforcer answers 472 per request — the same
	// fail-closed-per-request rule that governs an unresolvable credential. Refusing
	// to start would instead take down the whole cage.
	if err := loadSecrets(store, sysdep.SecretFD); err != nil {
		fmt.Fprintf(app.Stderr, "warning: read injection payload: %v\n", err)
	}

	ln, err := app.UnixSocket.Listen(sock, brokerSockPerm)
	if err != nil {
		return fmt.Errorf("broker: listen: %w", err)
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Serve returns when the context is cancelled; the deferred Wipe then zeroes and
	// unlocks every custodied token before the process image goes away.
	if err := broker.NewServer(store, app.Clock).Serve(ctx, ln); err != nil {
		return fmt.Errorf("broker: serve: %w", err)
	}
	return nil
}

// loadSecrets reads the flat {credential-name: raw-token} JSON the proxy lifecycle
// wrote to the inherited descriptor and custodies each entry.
//
// The descriptor is the same one-shot, write-then-EOF channel Phase 1 used to hand
// secrets to the Python addon (AC-0068c) — it just terminates in Go now. It stays
// the delivery mechanism for the same reason it was chosen then: unlike argv (which
// `ps` shows) or the environment (which any same-uid process can read via
// KERN_PROCARGS2), a pipe leaks nothing.
//
// Statically resolved references (op://, keychain://, env://) do not expire, so
// they are custodied with a zero expiry. Minted tokens (AC-0069a) will arrive with
// a real one.
func loadSecrets(store *broker.Store, fd int) error {
	f := os.NewFile(uintptr(fd), "creance-secrets")
	if f == nil {
		return fmt.Errorf("no descriptor %d", fd)
	}
	defer f.Close()

	var payload map[string]string
	if err := json.NewDecoder(f).Decode(&payload); err != nil {
		// Never interpolate the payload into the error — only the failure kind. An
		// error string ends up in the parent's stderr, which is not where a token
		// should be.
		return fmt.Errorf("decode: %w", err)
	}

	for name, token := range payload {
		store.Set(name, []byte(token), time.Time{})
	}
	return nil
}

// A caveat worth stating rather than hiding: decoding into map[string]string leaves
// the tokens in Go strings, which cannot be wiped, so those copies live until the
// GC reclaims them. The custodied []byte is the one that gets mlocked and zeroed.
// That is the honest bound of this design — see sysdep.Memory for why chasing every
// derived copy is not achievable in Go anyway, and why TTL and scope, not memory
// hygiene, are what actually limit a leaked token.
