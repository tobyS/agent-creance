package sysdeptest

import (
	"context"
	"fmt"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeHTTPResponse is a scripted HTTP reply (status code + body) for a URL.
type FakeHTTPResponse struct {
	Status int
	Body   []byte
}

// FakeHTTPGetter is a scripted HTTPGetter. You pre-load the response (or
// transport error) each URL should produce; every Get is recorded in Calls so a
// test can assert how many times — and whether — the transport was hit (e.g. a
// cache hit must perform zero requests).
type FakeHTTPGetter struct {
	// Responses maps a URL to the status+body Get should return.
	Responses map[string]FakeHTTPResponse
	// Errs optionally maps a URL to a transport error Get should return instead,
	// simulating a request that never completed (DNS, connection, timeout).
	Errs map[string]error
	// Calls records every requested URL, in order.
	Calls []string
}

var _ sysdep.HTTPGetter = (*FakeHTTPGetter)(nil)

// NewFakeHTTPGetter returns an empty, ready-to-populate fake.
func NewFakeHTTPGetter() *FakeHTTPGetter {
	return &FakeHTTPGetter{
		Responses: map[string]FakeHTTPResponse{},
		Errs:      map[string]error{},
	}
}

// WithResponse registers a scripted status+body for url. Returns the receiver
// for chaining.
func (f *FakeHTTPGetter) WithResponse(url string, status int, body []byte) *FakeHTTPGetter {
	f.Responses[url] = FakeHTTPResponse{Status: status, Body: body}
	return f
}

// WithError registers a transport error for url. Returns the receiver for
// chaining.
func (f *FakeHTTPGetter) WithError(url string, err error) *FakeHTTPGetter {
	f.Errs[url] = err
	return f
}

func (f *FakeHTTPGetter) Get(_ context.Context, url string, _ map[string]string) (int, []byte, error) {
	f.Calls = append(f.Calls, url)
	if err, ok := f.Errs[url]; ok {
		return 0, nil, err
	}
	if resp, ok := f.Responses[url]; ok {
		return resp.Status, resp.Body, nil
	}
	return 0, nil, fmt.Errorf("%s: no scripted response", url)
}
