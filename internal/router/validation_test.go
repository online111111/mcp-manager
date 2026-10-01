package router

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/catalog"
)

type snapshotRoutes struct {
	fakeRouteLookup
	snapshot *catalog.Snapshot
}

func (s *snapshotRoutes) Snapshot() *catalog.Snapshot { return s.snapshot }

func validationFixture(schema string) (*Router, *snapshotRoutes, *fakeSession, *fakeLease, *fakeLeaseProvider) {
	route := catalog.RouteEntry{PublicName: "srv__tool", ServerID: "srv", OriginalName: "tool"}
	routes := &snapshotRoutes{
		fakeRouteLookup: fakeRouteLookup{routes: map[string]catalog.RouteEntry{"srv__tool": route}},
		snapshot:        &catalog.Snapshot{Revision: 7, Routes: map[string]catalog.RouteEntry{"srv__tool": route}, Tools: []*mcp.Tool{{Name: "srv__tool", InputSchema: json.RawMessage(schema)}}},
	}
	session := &fakeSession{}
	lease := &fakeLease{session: session, serverID: "srv", genID: 1}
	leases := &fakeLeaseProvider{leaseFn: func(string) (Lease, error) { return lease, nil }}
	return NewRouter(routes, leases), routes, session, lease, leases
}

func assertInvalidParams(t *testing.T, err error) {
	t.Helper()
	var rpc *jsonrpc.Error
	if !errors.As(err, &rpc) || rpc.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("expected InvalidParams, got %v", err)
	}
}

func TestValidatedRouterRejectsInvalidArgumentsBeforeLease(t *testing.T) {
	r, _, session, _, leases := validationFixture(`{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}`)
	acquired := false
	leases.leaseFn = func(string) (Lease, error) { acquired = true; return nil, ErrServerUnavailable }
	_, err := r.RouteTool(WithValidation(context.Background(), nil), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "srv__tool", Arguments: json.RawMessage(`{"count":"bad"}`)}})
	assertInvalidParams(t, err)
	if acquired || session.callCount.Load() != 0 {
		t.Fatal("invalid arguments reached downstream admission")
	}
	// Full interface keeps its legacy raw-handler contract.
	_, err = r.RouteTool(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "srv__tool", Arguments: json.RawMessage(`{"count":"bad"}`)}})
	if err != nil || !acquired {
		t.Fatalf("legacy unvalidated path changed: %v, acquired=%v", err, acquired)
	}
}

func TestValidatedRouterRejectsPublicationDuringLeaseAcquisition(t *testing.T) {
	r, routes, session, lease, leases := validationFixture(`{"type":"object","properties":{"x":{"type":"integer"}}}`)
	leases.leaseFn = func(string) (Lease, error) {
		routes.mu.Lock()
		changed := *routes.snapshot
		changed.Revision++
		changed.Tools = []*mcp.Tool{{Name: "srv__tool", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)}}
		routes.snapshot = &changed
		routes.mu.Unlock()
		return lease, nil
	}
	_, err := r.RouteTool(WithValidation(context.Background(), nil), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "srv__tool", Arguments: json.RawMessage(`{"x":1}`)}})
	assertInvalidParams(t, err)
	if session.callCount.Load() != 0 {
		t.Fatal("old-schema validated arguments executed on a changed publication")
	}
	if lease.releaseCount.Load() != 1 {
		t.Fatal("rejected admission leaked generation lease")
	}
}

func TestValidatedRouterStaleRevisionRequiresDescribe(t *testing.T) {
	r, _, session, _, _ := validationFixture(`{"type":"object"}`)
	revision := int64(6)
	_, err := r.RouteTool(WithValidation(context.Background(), &revision), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "srv__tool", Arguments: json.RawMessage(`{}`)}})
	assertInvalidParams(t, err)
	if !strings.Contains(err.Error(), "describe") {
		t.Fatalf("missing recovery guidance: %v", err)
	}
	if session.callCount.Load() != 0 {
		t.Fatal("stale request executed")
	}
}
