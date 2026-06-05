package sysdep_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// These tests exercise OSHTTPGetter against httptest's loopback server only —
// no external network, so they stay hermetic and run in the unit suite.

func TestOSHTTPGetterForwardsHeadersAndReturnsBody(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(201)
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	g := sysdep.OSHTTPGetter{Client: srv.Client()}
	status, body, err := g.Get(context.Background(), srv.URL, map[string]string{"User-Agent": "agent-creance/test"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != 201 {
		t.Errorf("status = %d, want 201", status)
	}
	if string(body) != "hello" {
		t.Errorf("body = %q, want hello", body)
	}
	if gotUA != "agent-creance/test" {
		t.Errorf("server saw User-Agent %q, want agent-creance/test", gotUA)
	}
}

func TestOSHTTPGetterReturnsErrorStatusWithoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	g := sysdep.OSHTTPGetter{Client: srv.Client()}
	status, _, err := g.Get(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != 404 {
		t.Errorf("status = %d, want 404", status)
	}
}

func TestOSHTTPGetterContextCancellationAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	g := sysdep.OSHTTPGetter{Client: srv.Client()}
	if _, _, err := g.Get(ctx, srv.URL, nil); err == nil {
		t.Error("want error when context times out, got nil")
	}
}
