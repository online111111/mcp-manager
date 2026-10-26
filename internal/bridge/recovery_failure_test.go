package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bridgeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f bridgeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBridgeDoesNotReconnectForToolApplicationError(t *testing.T) {
	hub, server, ids, count := recoveryHub(t, 0)
	hub.AddTool(&mcp.Tool{Name: "error", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("invalid operation")
	})
	local, _, _ := startRecoveryBridge(t, Options{Endpoint: server.URL})
	nextRecoverySID(t, ids)
	_, _ = local.CallTool(context.Background(), &mcp.CallToolParams{Name: "error"})
	time.Sleep(60 * time.Millisecond)
	if got := count.Load(); got != 1 {
		t.Fatalf("application error replaced healthy Hub session: %d", got)
	}
}

func TestBridgeDroppedToolResponseNeverReplaysSideEffect(t *testing.T) {
	hub, server, ids, _ := recoveryHub(t, 0)
	var executions atomic.Int64
	hub.AddTool(&mcp.Tool{Name: "effect", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		executions.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "executed"}}}, nil
	})
	var drop atomic.Bool
	drop.Store(true)
	rt := bridgeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		method := ""
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			var message struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &message)
			method = message.Method
		}
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err == nil && method == "tools/call" && drop.CompareAndSwap(true, false) {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return nil, errors.New("tool response lost after server execution")
		}
		return resp, err
	})
	local, _, _ := startRecoveryBridge(t, Options{Endpoint: server.URL, BaseRoundTripper: rt})
	old := nextRecoverySID(t, ids)
	result, err := local.CallTool(context.Background(), &mcp.CallToolParams{Name: "effect"})
	t.Logf("uncertain call result=%+v err=%v", result, err)
	if err == nil && (result == nil || !result.IsError) {
		t.Fatal("uncertain call must fail, not return success")
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("failed in-flight call executed %d times", got)
	}
	if nextRecoverySID(t, ids) == old {
		t.Fatal("reused stale session")
	}
	result, err = local.CallTool(context.Background(), &mcp.CallToolParams{Name: "effect"})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("new call failed: %v %v", result, err)
	}
	if got := executions.Load(); got != 2 {
		t.Fatalf("uncertain tool replayed: total executions %d", got)
	}
}

func TestBridgeRecoveryStopsOnTerminalHTTPStatus(t *testing.T) {
	for _, status := range []int{401, 403, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			_, server, ids, _ := recoveryHub(t, 0)
			var denied atomic.Bool
			var requests atomic.Int64
			rt := bridgeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if denied.Load() {
					requests.Add(1)
					return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{"Location": []string{server.URL}}, Body: io.NopCloser(strings.NewReader("denied")), Request: r}, nil
				}
				return http.DefaultTransport.RoundTrip(r)
			})
			local, done, _ := startRecoveryBridge(t, Options{Endpoint: server.URL, BaseRoundTripper: rt})
			nextRecoverySID(t, ids)
			denied.Store(true)
			_, _ = local.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo"})
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("terminal authentication/redirect failure was hidden")
				}
			case <-time.After(time.Second):
				t.Fatal("terminal Hub failure did not stop bridge")
			}
			if n := requests.Load(); n > 2 {
				t.Fatalf("terminal HTTP %d retried %d times", status, n)
			}
		})
	}
}
