package sysdeptest

import (
	"context"
	"errors"
	"testing"
)

func TestFakeHTTPGetterScriptedResponseAndCalls(t *testing.T) {
	const url = "https://registry.npmjs.org/left-pad"
	f := NewFakeHTTPGetter().WithResponse(url, 200, []byte(`{"name":"left-pad"}`))

	status, body, err := f.Get(context.Background(), url, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if string(body) != `{"name":"left-pad"}` {
		t.Errorf("body = %q", body)
	}
	if len(f.Calls) != 1 || f.Calls[0] != url {
		t.Errorf("Calls = %v, want one entry %q", f.Calls, url)
	}
}

func TestFakeHTTPGetterScriptedError(t *testing.T) {
	const url = "https://registry.npmjs.org/boom"
	sentinel := errors.New("dial tcp: connection refused")
	f := NewFakeHTTPGetter().WithError(url, sentinel)

	_, _, err := f.Get(context.Background(), url, nil)
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want %v", err, sentinel)
	}
	if len(f.Calls) != 1 {
		t.Errorf("Calls = %v, want one entry", f.Calls)
	}
}

func TestFakeHTTPGetterUnscriptedURL(t *testing.T) {
	f := NewFakeHTTPGetter()
	if _, _, err := f.Get(context.Background(), "https://example.com", nil); err == nil {
		t.Error("want error for unscripted URL, got nil")
	}
}
