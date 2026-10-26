package downstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPKeepAlivePreservesIdleSession(t *testing.T) {
	hub := mcp.NewServer(serverImpl("short-idle"), nil)
	var pings atomic.Int32
	hub.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "ping" {
				pings.Add(1)
			}
			return next(ctx, method, req)
		}
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return hub },
		&mcp.StreamableHTTPOptions{SessionTimeout: 150 * time.Millisecond})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Protocol-Version") > "2025-11-25" {
			http.Error(w, "stateful compatibility endpoint", http.StatusBadRequest)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	session, err := DialHTTP(ctx, HTTPOptions{Endpoint: server.URL, DisableStandaloneSSE: true,
		KeepAlive: 25 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	time.Sleep(450 * time.Millisecond)
	if pings.Load() < 3 {
		t.Fatalf("no protocol keepalive: %d pings", pings.Load())
	}
	if err := session.RawSession().Ping(ctx, nil); err != nil {
		t.Fatalf("idle session expired despite keepalive: %v", err)
	}
}

func TestHTTPKeepAliveCanBeDisabledAndIdleSessionActuallyExpires(t *testing.T) {
	hub := mcp.NewServer(serverImpl("idle-expiry-control"), nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return hub },
		&mcp.StreamableHTTPOptions{SessionTimeout: 150 * time.Millisecond})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Protocol-Version") > "2025-11-25" {
			http.Error(w, "stateful compatibility endpoint", http.StatusBadRequest)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	session, err := DialHTTP(ctx, HTTPOptions{Endpoint: server.URL, DisableStandaloneSSE: true, KeepAlive: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	time.Sleep(450 * time.Millisecond)
	if err := session.RawSession().Ping(ctx, nil); err == nil {
		t.Fatal("control fixture did not expire; keepalive coverage would be vacuous")
	}
}

func TestDefaultHTTPKeepAliveInterval(t *testing.T) {
	if DefaultHTTPKeepAlive != 5*time.Minute {
		t.Fatalf("unexpected default: %v", DefaultHTTPKeepAlive)
	}
}
