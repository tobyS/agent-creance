package sysdep_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestOSHTTPGetterRejectsOversizedBody(t *testing.T) {
	const maxBodyBytes = 16 << 20 // mirrors the unexported cap in http.go

	cases := map[string]struct {
		size    int
		wantErr bool
	}{
		"exactly at cap succeeds": {size: maxBodyBytes, wantErr: false},
		"over cap errors":         {size: maxBodyBytes + 1, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(bytes.Repeat([]byte("x"), tc.size))
			}))
			defer srv.Close()

			g := sysdep.OSHTTPGetter{Client: srv.Client()}
			_, body, err := g.Get(context.Background(), srv.URL, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want error for oversized body, got nil")
				}
				if !strings.Contains(err.Error(), "exceeds 16 MiB") {
					t.Errorf("error = %q, want mention of the 16 MiB limit", err)
				}
				if body != nil {
					t.Errorf("body = %d bytes, want nil on error", len(body))
				}
				return
			}
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if len(body) != tc.size {
				t.Errorf("body = %d bytes, want %d", len(body), tc.size)
			}
		})
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
