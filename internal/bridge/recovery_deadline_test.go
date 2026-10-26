package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sync/atomic"
	"testing"
	"time"
)

func TestBridgeRecoveryStartupDeadlineBoundsSSEHeaders(t *testing.T) {
	_, server, ids, _ := recoveryHub(t, 0)
	var hang atomic.Bool
	started := make(chan struct{}, 1)
	rt := bridgeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if hang.Load() && r.Method == http.MethodGet {
			select {
			case started <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	_, done, _ := startRecoveryBridge(t, Options{Endpoint: server.URL, BaseRoundTripper: rt,
		StartupTimeout: 100 * time.Millisecond, ReconnectAttempts: 1})
	old := nextRecoverySID(t, ids)
	hang.Store(true)
	deleteRecoverySession(t, server.URL, old)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("fixture did not reach stalled SSE headers")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled handshake exhaustion was hidden")
		}
	case <-time.After(time.Second):
		t.Fatal("startup deadline did not bound detached initial SSE GET")
	}
}

func TestBridgeCanceledDirectoryRefreshDoesNotReconnectHealthySession(t *testing.T) {
	// Exercise an actual in-flight remote list cancellation, not a pre-canceled
	// client request that never reaches the bridge's refresh path.
	hub, server, ids, count := recoveryHub(t, 0)
	callStarted := make(chan struct{})
	releaseCall := make(chan struct{})
	hub.AddTool(&mcp.Tool{Name: "slow", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			close(callStarted)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-releaseCall:
			}
			return &mcp.CallToolResult{}, nil
		})
	var blockList atomic.Bool
	listStarted := make(chan struct{})
	rt := bridgeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if blockList.Load() && r.Method == http.MethodPost && r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var message struct{ Method string }
			_ = json.Unmarshal(raw, &message)
			if message.Method == "tools/list" {
				close(listStarted)
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	bridge, err := New(Options{Endpoint: server.URL, BaseRoundTripper: rt})
	if err != nil {
		t.Fatal(err)
	}
	generation, _, err := bridge.connectHub(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer generation.close()
	bridge.current = generation
	generation.ready = true
	nextRecoverySID(t, ids)
	callDone := make(chan error, 1)
	go func() {
		_, err := generation.session.CallToolWithParams(ctx, &mcp.CallToolParams{Name: "slow"})
		callDone <- err
	}()
	select {
	case <-callStarted:
	case <-time.After(time.Second):
		t.Fatal("parallel tool did not start")
	}
	blockList.Store(true)
	refreshCtx, cancelRefresh := context.WithCancel(ctx)
	refreshDone := make(chan error, 1)
	bridge.syncNeeded.Store(true)
	go func() { refreshDone <- bridge.syncGeneration(refreshCtx, generation) }()
	select {
	case <-listStarted:
	case <-time.After(time.Second):
		t.Fatal("remote refresh did not start")
	}
	cancelRefresh()
	select {
	case err := <-refreshDone:
		if err == nil {
			t.Fatal("canceled refresh unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("refresh cancellation hung")
	}
	bridge.sessionMu.Lock()
	ready := generation.ready
	bridge.sessionMu.Unlock()
	if !ready || len(bridge.recoverCh) != 0 || !bridge.syncNeeded.Load() {
		t.Error("caller cancellation scheduled recovery or erased pending refresh")
	}
	if count.Load() != 1 {
		t.Error("canceled refresh replaced original Hub session")
	}
	close(releaseCall)
	select {
	case err := <-callDone:
		if err != nil {
			t.Fatalf("canceling directory refresh broke parallel tool: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("parallel tool did not finish")
	}
}
