package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/broker"
	"github.com/tobyS/agent-creance/internal/mint"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// revokeTimeout bounds the best-effort teardown revocation of minted GitHub tokens,
// so a slow or dead GitHub cannot wedge broker shutdown.
const revokeTimeout = 5 * time.Second

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
// them on the socket until signalled, and wipes them on the way out. Static
// credentials are Set once; minted credentials (AC-0069a) get a per-credential
// refresh goroutine that mints the first token and re-mints before expiry, and are
// best-effort-revoked on shutdown.
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
	payload, err := loadPayload(sysdep.SecretFD)
	if err != nil {
		fmt.Fprintf(app.Stderr, "warning: read injection payload: %v\n", err)
	}

	// Custody static tokens immediately; collect the minters so the refreshers can be
	// started after the socket is listening.
	minters := map[string]mint.Minter{}
	for name, spec := range payload {
		if spec.Kind == broker.KindStatic {
			store.Set(name, []byte(spec.Token), time.Time{})
			continue
		}
		if m, ok := broker.MinterFor(spec, app.HTTPClient, app.Clock); ok {
			minters[name] = m
		}
	}

	ln, err := app.UnixSocket.Listen(sock, brokerSockPerm)
	if err != nil {
		return fmt.Errorf("broker: listen: %w", err)
	}
	// Clean up after ourselves even when nobody reaps us: the proxy lifecycle also
	// removes the socket on teardown, but a broker killed directly should not leave a
	// dead socket file that the next Listen has to clear.
	defer func() { _ = app.FS.Remove(sock) }()

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Start one refresh goroutine per minted credential. The refresher does not block
	// startup on the first mint — a minted credential answers 472 (unknown_credential)
	// for the brief window until its first mint lands.
	var wg sync.WaitGroup
	refresher := broker.NewRefresher(store, app.Clock, app.Sleeper, func(msg string) {
		fmt.Fprintf(app.Stderr, "warning: %s\n", msg)
	})
	for name, m := range minters {
		wg.Add(1)
		go func(name string, m mint.Minter) {
			defer wg.Done()
			refresher.Run(ctx, name, m, marginsFor(name, payload))
		}(name, m)
	}

	// Serve returns when the context is cancelled. The refreshers stop on the same
	// cancellation; join them before revoking and wiping.
	serveErr := broker.NewServer(store, app.Clock).Serve(ctx, ln)
	stop()
	wg.Wait()

	// Best-effort teardown revocation of minted GitHub tokens, before the deferred
	// Wipe zeroes the store. A revoke failure is swallowed — the token expires on its
	// own within the hour regardless.
	revokeMinted(app, minters, store, payload)

	if serveErr != nil {
		return fmt.Errorf("broker: serve: %w", serveErr)
	}
	return nil
}

// marginsFor picks the refresh margins for a credential by its kind.
func marginsFor(name string, payload broker.Payload) broker.Margins {
	if payload[name].Kind == broker.KindOAuth2 {
		return broker.OAuth2Margins
	}
	return broker.GitHubMargins
}

// revokeMinted asks each minter to revoke its currently-served token (GitHub tokens
// are revocable; OAuth2 is a nil-op). It reads the current token from the store, so a
// credential that never minted successfully is skipped. Bounded by revokeTimeout.
func revokeMinted(app *App, minters map[string]mint.Minter, store *broker.Store, payload broker.Payload) {
	for name, m := range minters {
		if payload[name].Kind != broker.KindGitHubApp {
			continue // OAuth2 revoke is a nil-op; skip the store read and timeout
		}
		token, _, ok := store.Get(name)
		if !ok {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), revokeTimeout)
		if err := m.Revoke(ctx, string(token)); err != nil {
			fmt.Fprintf(app.Stderr, "warning: revoke minted credential %q on shutdown: %v\n", name, err)
		}
		cancel()
	}
}

// loadPayload reads the JSON broker.Payload the proxy lifecycle wrote to the inherited
// descriptor.
//
// The descriptor is the same one-shot, write-then-EOF channel Phase 1 used to hand
// secrets to the Python addon (AC-0068c) — it just terminates in Go now. It stays the
// delivery mechanism for the same reason it was chosen then: unlike argv (which `ps`
// shows) or the environment (which any same-uid process can read via KERN_PROCARGS2),
// a pipe leaks nothing.
func loadPayload(fd int) (broker.Payload, error) {
	f := os.NewFile(uintptr(fd), "creance-secrets")
	if f == nil {
		return nil, fmt.Errorf("no descriptor %d", fd)
	}
	defer f.Close()

	var payload broker.Payload
	if err := json.NewDecoder(f).Decode(&payload); err != nil {
		// Never interpolate the payload into the error — only the failure kind. An
		// error string ends up in the parent's stderr, which is not where a token
		// should be.
		return nil, fmt.Errorf("decode: %w", err)
	}
	return payload, nil
}

// A caveat worth stating rather than hiding: decoding the payload leaves the tokens
// (and minted-credential key material) in Go strings, which cannot be wiped, so those
// copies live until the GC reclaims them. The custodied []byte is the one that gets
// mlocked and zeroed.
// That is the honest bound of this design — see sysdep.Memory for why chasing every
// derived copy is not achievable in Go anyway, and why TTL and scope, not memory
// hygiene, are what actually limit a leaked token.
