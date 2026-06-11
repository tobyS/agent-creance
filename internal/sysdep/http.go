package sysdep

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxBodyBytes caps how large a response body OSHTTPGetter accepts. Registry
// metadata documents are small (kilobytes); the cap stops a misbehaving or
// hostile endpoint from streaming an unbounded body into memory. A body over
// the cap is a hard error, never a silent truncation — truncated bytes once
// surfaced downstream as a baffling "unexpected end of JSON input" (AC-0040).
const maxBodyBytes = 16 << 20 // 16 MiB

// defaultHTTPTimeout bounds a single GET when OSHTTPGetter.Client is nil, so a
// hung registry cannot wedge the caller.
const defaultHTTPTimeout = 30 * time.Second

// HTTPGetter abstracts an HTTP GET: the first network touchpoint in the codebase.
// Registry lookups (npm, Packagist) go through this seam so unit tests stay
// hermetic against a fake transport; production wires OSHTTPGetter, integration
// tests wire the real one.
//
// Why route HTTP through the seam (for someone coming from PHP/TS): a logic
// package that called net/http directly would hit the network in unit tests.
// Packages take an HTTPGetter and call *that*.
type HTTPGetter interface {
	// Get performs a GET for url with the given request headers and returns the
	// response status code and fully-read body. A non-nil error means the request
	// could not be completed at all (DNS, connection, timeout, body read); an HTTP
	// error *status* (404, 5xx) is reported via status with a nil error, so callers
	// can branch on it.
	Get(ctx context.Context, url string, headers map[string]string) (status int, body []byte, err error)
}

// OSHTTPGetter is the production HTTPGetter backed by net/http.
//
// Client may be nil, in which case a default client with a 30s timeout is used —
// a bounded timeout so a hung registry cannot wedge the caller.
type OSHTTPGetter struct {
	Client *http.Client
}

var _ HTTPGetter = (*OSHTTPGetter)(nil)

func (g OSHTTPGetter) Get(ctx context.Context, url string, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("sysdep: build request for %q: %w", url, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("sysdep: GET %q: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read one byte past the cap so "exactly at the cap" and "over the cap"
	// are distinguishable.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("sysdep: read body of %q: %w", url, err)
	}
	if len(body) > maxBodyBytes {
		return resp.StatusCode, nil, fmt.Errorf("sysdep: response body of %q exceeds %d MiB", url, maxBodyBytes>>20)
	}
	return resp.StatusCode, body, nil
}
