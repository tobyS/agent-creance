package sysdeptest

import (
	"context"
	"fmt"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// HTTPRequest is one recorded call to FakeHTTPClient.Do — enough for a test to assert
// the method, target, headers, and request body a minter produced.
type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// FakeHTTPClient is a scripted HTTPClient. Responses (and transport errors) are keyed
// by "METHOD URL" so the same URL can script different replies per verb (e.g. a POST
// mint vs a DELETE revoke). Every call is recorded in Requests so a test can assert
// the exact request a minter built.
type FakeHTTPClient struct {
	// Responses maps "METHOD URL" to the status+body Do should return.
	Responses map[string]FakeHTTPResponse
	// Errs optionally maps "METHOD URL" to a transport error Do returns instead.
	Errs map[string]error
	// Requests records every call, in order.
	Requests []HTTPRequest
}

var _ sysdep.HTTPClient = (*FakeHTTPClient)(nil)

// NewFakeHTTPClient returns an empty, ready-to-populate fake.
func NewFakeHTTPClient() *FakeHTTPClient {
	return &FakeHTTPClient{
		Responses: map[string]FakeHTTPResponse{},
		Errs:      map[string]error{},
	}
}

func httpKey(method, url string) string { return method + " " + url }

// WithResponse registers a scripted status+body for method+url. Returns the receiver
// for chaining.
func (f *FakeHTTPClient) WithResponse(method, url string, status int, body []byte) *FakeHTTPClient {
	f.Responses[httpKey(method, url)] = FakeHTTPResponse{Status: status, Body: body}
	return f
}

// WithError registers a transport error for method+url. Returns the receiver for
// chaining.
func (f *FakeHTTPClient) WithError(method, url string, err error) *FakeHTTPClient {
	f.Errs[httpKey(method, url)] = err
	return f
}

func (f *FakeHTTPClient) Do(_ context.Context, method, url string, headers map[string]string, body []byte) (int, []byte, error) {
	// Copy the header map so a later mutation by the caller cannot rewrite history.
	hdr := make(map[string]string, len(headers))
	for k, v := range headers {
		hdr[k] = v
	}
	f.Requests = append(f.Requests, HTTPRequest{Method: method, URL: url, Headers: hdr, Body: append([]byte(nil), body...)})

	key := httpKey(method, url)
	if err, ok := f.Errs[key]; ok {
		return 0, nil, err
	}
	if resp, ok := f.Responses[key]; ok {
		return resp.Status, resp.Body, nil
	}
	return 0, nil, fmt.Errorf("%s: no scripted response", key)
}

// LastRequest returns the most recent recorded request, or a zero value if none.
func (f *FakeHTTPClient) LastRequest() HTTPRequest {
	if len(f.Requests) == 0 {
		return HTTPRequest{}
	}
	return f.Requests[len(f.Requests)-1]
}
