package router

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/catalog"
	"github.com/online111111/mcp-manager/internal/toolvalidation"
)

type validationKey struct{}
type validationOptions struct {
	expectedRevision int64
	hasRevision      bool
}

// WithValidation opts this call into progressive admission. A nil revision still
// requires current published membership and input-schema validation. A non-nil
// revision must match the current catalog revision. The revision value is copied
// so the caller may reuse/change the pointer after creating the context.
// Legacy calls without this context retain their raw-handler behavior.
func WithValidation(ctx context.Context, expectedRevision *int64) context.Context {
	options := validationOptions{}
	if expectedRevision != nil {
		options.expectedRevision = *expectedRevision
		options.hasRevision = true
	}
	return context.WithValue(ctx, validationKey{}, options)
}

type snapshotProvider interface{ Snapshot() *catalog.Snapshot }

func invalidValidation(message string) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid params: " + message}
}

func staleValidation() error {
	return invalidValidation("catalog revision changed; describe the tool again before calling")
}

func (r *Router) validateCall(ctx context.Context, req *mcp.CallToolRequest, options validationOptions) (catalog.RouteEntry, *catalog.Snapshot, error) {
	provider, ok := r.routes.(snapshotProvider)
	if !ok {
		return catalog.RouteEntry{}, nil, invalidValidation("current published snapshot unavailable; cannot validate tool")
	}
	r.routes.RLock()
	snapshot := provider.Snapshot()
	r.routes.RUnlock()
	if snapshot == nil {
		return catalog.RouteEntry{}, nil, invalidValidation("current published snapshot unavailable; cannot validate tool")
	}
	if options.hasRevision && options.expectedRevision != snapshot.Revision {
		return catalog.RouteEntry{}, nil, staleValidation()
	}
	route, routed := snapshot.LookupRoute(req.Params.Name)
	tool, published := snapshot.GetTool(req.Params.Name)
	if !routed || !published || tool == nil {
		return catalog.RouteEntry{}, nil, invalidValidation(fmt.Sprintf("unknown or unpublished tool %q", req.Params.Name))
	}
	if err := ctx.Err(); err != nil {
		return catalog.RouteEntry{}, nil, err
	}
	if err := toolvalidation.Validate(tool.InputSchema, req.Params.Arguments); err != nil {
		return catalog.RouteEntry{}, nil, invalidValidation(err.Error())
	}
	return route, snapshot, nil
}
