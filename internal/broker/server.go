package broker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// Server answers credential lookups from the mitmproxy enforcer over a unix
// socket. It is the only reader of the Store that leaves the process.
//
// The socket is the whole access control (AC-0069b): 0600, in a 0700 directory
// under ~/.cache/agent-creance/, which is never mounted into the cage and which
// the Seatbelt profile denies outright. A peer-credential check would add nothing
// — the caged agent runs as the same uid as mitmproxy, so LOCAL_PEERCRED reports
// the same uid for the attacker and the legitimate client.
type Server struct {
	store *Store
	clock sysdep.Clock
}

// NewServer returns a Server serving store, using clock to judge expiry.
func NewServer(store *Store, clock sysdep.Clock) *Server {
	return &Server{store: store, clock: clock}
}

// Serve accepts connections on ln until ctx is cancelled, answering one request
// per connection. It closes ln on return.
//
// A connection-level failure (a client that hangs up mid-request, a malformed
// line) is logged and dropped, never fatal: a broken enforcer connection must not
// take down the credential channel for every other request.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	// Unblock the Accept below when the context is cancelled; closing the
	// listener is the only way to interrupt it.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil // shutting down
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handle(conn)
		}()
	}
}

// handle answers a single request on conn and closes it.
func (s *Server) handle(conn net.Conn) {
	defer conn.Close()

	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		// A connection that closes without sending anything is the readiness
		// probe (UnixSocket.Probe dials and hangs up), not a client error.
		if errors.Is(err, io.EOF) {
			return
		}
		// Log the failure kind only — a malformed request could otherwise echo
		// attacker-chosen bytes into the log next to real credential names.
		log.Printf("broker: malformed request: %v", err)
		return
	}

	if err := json.NewEncoder(conn).Encode(s.answer(req)); err != nil {
		log.Printf("broker: write response for %q: %v", req.Credential, err)
	}
}

// answer resolves req against the store. It never returns a token and an error
// together, and it never logs a token value.
func (s *Server) answer(req Request) Response {
	token, expiresAt, ok := s.store.Get(req.Credential)
	if !ok {
		return Response{Error: ErrUnknownCredential}
	}
	if !expiresAt.IsZero() && !s.clock.Now().Before(expiresAt) {
		// Serving a dead token would send the caged agent upstream to collect a
		// 401; a 472 tells the human what to actually do about it.
		return Response{Error: ErrExpired}
	}
	resp := Response{Token: string(token)}
	if !expiresAt.IsZero() {
		resp.ExpiresAt = expiresAt.Format(time.RFC3339)
	}
	return resp
}
