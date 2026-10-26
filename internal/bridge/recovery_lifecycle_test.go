package bridge

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBridgeRecoveryDefaultsAreBounded(t *testing.T) {
	bridge, err := New(Options{Endpoint: "http://127.0.0.1:8080/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if bridge.opts.ReconnectAttempts != 5 || bridge.opts.ReconnectDelay != 500*time.Millisecond {
		t.Fatalf("unexpected bounded recovery policy: %d %v", bridge.opts.ReconnectAttempts, bridge.opts.ReconnectDelay)
	}
}

func TestBridgeConcurrentLossPublishesOneFreshDirectory(t *testing.T) {
	hub, server, ids, count := recoveryHub(t, 0)
	local, _, _ := startRecoveryBridge(t, Options{Endpoint: server.URL})
	old := nextRecoverySID(t, ids)
	hub.RemoveTools("echo")
	hub.AddTool(&mcp.Tool{Name: "replacement", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "new"}}}, nil
		})
	deleteRecoverySession(t, server.URL, old)
	fresh := nextRecoverySID(t, ids)
	if old == fresh {
		t.Fatal("stale ID reused")
	}
	var wait sync.WaitGroup
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			list, err := local.ListTools(ctx, nil)
			if err != nil {
				failures <- err
				return
			}
			if len(list.Tools) != 1 || list.Tools[0].Name != "replacement" {
				failures <- errors.New("stale directory observed after recovery")
			}
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if got := count.Load(); got != 2 {
		t.Fatalf("concurrent requests caused multiple reconnects: %d", got)
	}
	result, err := local.CallTool(context.Background(), &mcp.CallToolParams{Name: "replacement"})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("replacement tool failed: %v %v", result, err)
	}
}

func TestBridgeRecoveryCancelAndEOFDuringBlockedHandshake(t *testing.T) {
	for _, eof := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "EOF"}[eof], func(t *testing.T) {
			_, server, ids, _ := recoveryHub(t, 0)
			var blocked atomic.Bool
			started := make(chan struct{})
			var once sync.Once
			rt := bridgeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if blocked.Load() {
					once.Do(func() { close(started) })
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return http.DefaultTransport.RoundTrip(r)
			})
			local, done, cancel := startRecoveryBridge(t, Options{Endpoint: server.URL, BaseRoundTripper: rt, StartupTimeout: 4 * time.Second})
			old := nextRecoverySID(t, ids)
			blocked.Store(true)
			deleteRecoverySession(t, server.URL, old)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("reconnect did not start")
			}
			start := time.Now()
			if eof {
				_ = local.Close()
			} else {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("blocked reconnect prevented shutdown")
			}
			if time.Since(start) > time.Second {
				t.Fatal("shutdown did not cancel detached SDK request")
			}
		})
	}
}

func TestBridgeRecoveryExhaustionIsBounded(t *testing.T) {
	_, server, ids, _ := recoveryHub(t, 0)
	var unavailable atomic.Bool
	var inits atomic.Int32
	rt := bridgeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if unavailable.Load() {
			if r.Body != nil {
				raw, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(strings.NewReader(string(raw)))
				var message struct{ Method string }
				_ = json.Unmarshal(raw, &message)
				if message.Method == "initialize" {
					inits.Add(1)
				}
			}
			return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("offline")), Request: r}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	_, done, _ := startRecoveryBridge(t, Options{Endpoint: server.URL, BaseRoundTripper: rt, ReconnectAttempts: 2, ReconnectDelay: 10 * time.Millisecond})
	old := nextRecoverySID(t, ids)
	unavailable.Store(true)
	deleteRecoverySession(t, server.URL, old)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("recovery exhaustion hidden")
		}
	case <-time.After(time.Second):
		t.Fatal("recovery attempts were not bounded")
	}
	if n := inits.Load(); n > 2 {
		t.Fatalf("too many fresh initialization attempts: %d", n)
	}
}

func TestBridgeRecoveryDoesNotRetryTLSVerificationFailure(t *testing.T) {
	_, server, ids, _ := recoveryHub(t, 0)
	var bad atomic.Bool
	var failures atomic.Int32
	rt := bridgeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if bad.Load() {
			failures.Add(1)
			return nil, &tls.CertificateVerificationError{Err: errors.New("untrusted certificate")}
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	local, done, _ := startRecoveryBridge(t, Options{Endpoint: server.URL, BaseRoundTripper: rt})
	nextRecoverySID(t, ids)
	bad.Store(true)
	_, _ = local.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo"})
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("TLS verification failure hidden")
		}
	case <-time.After(time.Second):
		t.Fatal("TLS error triggered retries")
	}
	if n := failures.Load(); n > 2 {
		t.Fatalf("terminal TLS error retried %d times", n)
	}
}
