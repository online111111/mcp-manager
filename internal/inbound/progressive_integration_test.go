package inbound_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/bridge"
	"github.com/online111111/mcp-manager/internal/config"
	"github.com/online111111/mcp-manager/internal/inbound"
	"github.com/online111111/mcp-manager/internal/manager"
	hubruntime "github.com/online111111/mcp-manager/internal/runtime"
	"github.com/online111111/mcp-manager/internal/testserver"
)

func TestProgressiveRealRouterHTTPAndStdio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	downstream := testserver.New()
	downstream.Paged.SetInterceptEnabled(false)
	url, closeDownstream, err := downstream.StartHTTP()
	if err != nil {
		t.Fatal(err)
	}
	defer closeDownstream()
	pub, err := inbound.NewPublisher(inbound.NewHubServer("integration", "test"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr := manager.NewManager(pub, manager.WithDrainTimeout(100*time.Millisecond))
	pub.SetRouterCallback(mgr.RouteTool)
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop(context.Background())
	cfg := &config.ResolvedConfig{Servers: map[string]config.ResolvedServer{"fixture": {ID: "fixture", Enabled: true, Type: config.ServerTypeStreamableHTTP, URL: url, StartupTimeout: 5 * time.Second, CallTimeout: 3 * time.Second, MaxConcurrency: 4}}}
	if err := mgr.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !mgr.IsReady() || pub.Snapshot().TotalPublished() < 6 {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("manager did not become ready")
		}
	}
	ln, err := inbound.BindLoopbackListener("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := inbound.NewHTTPServer(ln, pub, hubruntime.NewController(mgr, "", ln.Addr().String(), nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve()
	defer server.Close()
	for _, transport := range []string{"http", "stdio"} {
		t.Run(transport, func(t *testing.T) {
			client := mcp.NewClient(&mcp.Implementation{Name: "progressive-integration", Version: "1"}, nil)
			var session *mcp.ClientSession
			if transport == "http" {
				session, err = client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL() + inbound.ProgressivePath}, nil)
			} else {
				c2sR, c2sW := io.Pipe()
				s2cR, s2cW := io.Pipe()
				defer c2sR.Close()
				defer c2sW.Close()
				defer s2cR.Close()
				defer s2cW.Close()
				bridgeCtx, bridgeCancel := context.WithCancel(ctx)
				defer bridgeCancel()
				done := make(chan error, 1)
				go func() {
					done <- bridge.RunWithOptions(bridgeCtx, bridge.Options{Endpoint: server.URL() + inbound.ProgressivePath, Stdin: c2sR, Stdout: s2cW, Stderr: io.Discard, StartupTimeout: 5 * time.Second})
				}()
				defer func() {
					bridgeCancel()
					c2sW.Close()
					s2cR.Close()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("bridge cleanup timed out")
					}
				}()
				session, err = client.Connect(ctx, &mcp.IOTransport{Reader: s2cR, Writer: c2sW}, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			list, err := session.ListTools(ctx, nil)
			if err != nil || len(list.Tools) != 3 {
				t.Fatalf("list: %+v %v", list, err)
			}
			call, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_call_tool", Arguments: json.RawMessage(`{"name":"fixture__echo_raw","arguments":{"n":9007199254740993}}`)})
			if err != nil || call.IsError {
				t.Fatalf("echo call: %+v %v", call, err)
			}
			if !bytes.Contains(downstream.EchoRaw.LastRawArgs(), []byte("9007199254740993")) {
				t.Fatalf("raw args lost precision: %s", downstream.EchoRaw.LastRawArgs())
			}
			image, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_call_tool", Arguments: map[string]any{"name": "fixture__image", "arguments": map[string]any{}}})
			if err != nil || image.IsError {
				t.Fatalf("image call: %+v %v", image, err)
			}
			hasImage := false
			for _, content := range image.Content {
				if c, ok := content.(*mcp.ImageContent); ok {
					hasImage = bytes.Equal(c.Data, testserver.DefaultPNG)
				}
			}
			if !hasImage {
				t.Fatal("real image result not preserved")
			}
			fail, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_call_tool", Arguments: map[string]any{"name": "fixture__fail_tool", "arguments": map[string]any{}}})
			if err != nil || !fail.IsError {
				t.Fatalf("business error lost: %+v %v", fail, err)
			}
			before := downstream.EchoRaw.CallCount()
			if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_call_tool", Arguments: json.RawMessage(`{"name":"fixture__echo_raw","arguments":[]}`)}); err == nil {
				t.Fatal("invalid argument object executed")
			}
			if downstream.EchoRaw.CallCount() != before {
				t.Fatal("invalid arguments reached downstream")
			}
			if transport == "http" {
				desc, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_describe_tool", Arguments: map[string]any{"name": "fixture__echo_raw"}})
				if err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(desc.StructuredContent)
				var def struct {
					Revision int64 `json:"revision"`
				}
				json.Unmarshal(data, &def)
				srvCfg := cfg.Servers["fixture"]
				srvCfg.DisabledTools = map[string]struct{}{"echo_raw": {}}
				cfg.Servers["fixture"] = srvCfg
				if err := mgr.Apply(ctx, cfg); err != nil {
					t.Fatal(err)
				}
				if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_call_tool", Arguments: map[string]any{"name": "fixture__echo_raw", "arguments": map[string]any{}, "revision": def.Revision}}); err == nil {
					t.Fatal("disabled/stale tool executed")
				}
				srvCfg.DisabledTools = nil
				cfg.Servers["fixture"] = srvCfg
				if err := mgr.Apply(ctx, cfg); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	calls := mgr.GetRecentCalls()
	seen := false
	for _, call := range calls {
		if call.Tool == "fixture__echo_raw" {
			seen = true
		}
		if strings.HasPrefix(call.Tool, "hub_") {
			t.Fatal("audit recorded generic wrapper rather than target")
		}
	}
	if !seen {
		t.Fatal("underlying tool audit missing")
	}
}
