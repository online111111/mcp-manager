package router

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/catalog"
)

const (
	CategoryUnavailable     = "unavailable"
	CategoryBusy            = "busy"
	CategoryTimeout         = "timeout"
	CategoryDownstreamError = "downstream_error"
	CategoryInternalError   = "internal_error"
)

var (
	ErrServerUnavailable = errors.New("server unavailable")
	ErrServerBusy        = errors.New("server busy, concurrency limit reached")
	ErrServerNotFound    = errors.New("server not found")
)

type Session interface {
	CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error)
}

type Lease interface {
	Release()
	Session() Session
	ServerID() string
	GenerationID() uint64
	CallTimeout() time.Duration
	GenerationDone() <-chan struct{}
}

type LeaseProvider interface {
	AcquireLease(serverID string) (Lease, error)
}

type RouteLookup interface {
	RLock()
	RUnlock()
	LookupRoute(publicName string) (catalog.RouteEntry, bool)
}

type CallRecord struct {
	RequestID     string
	Time          time.Time
	Duration      time.Duration
	Tool          string
	ServerID      string
	Outcome       string
	ErrorCategory string
}

type CallRecorder func(CallRecord)

type Router struct {
	routes RouteLookup
	leases LeaseProvider

	recordMu sync.RWMutex
	record   CallRecorder
}

func NewRouter(routes RouteLookup, leases LeaseProvider) *Router {
	return &Router{routes: routes, leases: leases}
}

func (r *Router) SetCallRecorder(record CallRecorder) {
	r.recordMu.Lock()
	defer r.recordMu.Unlock()
	r.record = record
}

func (r *Router) callRecorder() CallRecorder {
	r.recordMu.RLock()
	defer r.recordMu.RUnlock()
	return r.record
}

func (r *Router) recordCall(call CallRecord) {
	if record := r.callRecorder(); record != nil {
		record(call)
	}
}

func GenerateRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("req-%x", b)
}

func (r *Router) RouteTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if req == nil || req.Params == nil || req.Params.Name == "" {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid params: tool name is required"}
	}

	publicName := req.Params.Name
	// Public catalog names are deliberately restricted to short printable ASCII.
	// Reject anything outside that contract before diagnostics or error messages
	// retain/echo attacker-controlled text.
	if !catalog.IsValidPublicToolName(publicName) {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid params: tool name is not a valid public tool name"}
	}

	reqID := GenerateRequestID()
	startedAt := time.Now()
	outcome := "error"
	errorCategory := CategoryInternalError
	serverID := ""
	defer func() {
		if ctx.Err() != nil && errorCategory == CategoryInternalError {
			errorCategory = "cancelled"
		}
		r.recordCall(CallRecord{
			RequestID:     reqID,
			Time:          startedAt,
			Duration:      time.Since(startedAt),
			Tool:          publicName,
			ServerID:      serverID,
			Outcome:       outcome,
			ErrorCategory: errorCategory,
		})
	}()

	if r.routes == nil {
		errorCategory = CategoryInternalError
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "router: route lookup not configured"}
	}

	var route catalog.RouteEntry
	var validatedSnapshot *catalog.Snapshot
	if options, validate := ctx.Value(validationKey{}).(validationOptions); validate {
		var validationErr error
		route, validatedSnapshot, validationErr = r.validateCall(ctx, req, options)
		if validationErr != nil {
			errorCategory = "invalid_params"
			return nil, validationErr
		}
	} else {
		r.routes.RLock()
		var ok bool
		route, ok = r.routes.LookupRoute(publicName)
		r.routes.RUnlock()
		if !ok {
			errorCategory = CategoryUnavailable
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("unknown tool %q", publicName)}
		}
	}
	serverID = route.ServerID

	if r.leases == nil {
		errorCategory = CategoryInternalError
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("[%s] lease provider not configured (request_id: %s)", CategoryInternalError, reqID)}},
		}, nil
	}

	var lease Lease
	var err error
	if validatedSnapshot != nil {
		if provider, ok := r.leases.(interface {
			AcquireValidatedLease(string, *catalog.Snapshot) (Lease, error)
		}); ok {
			lease, err = provider.AcquireValidatedLease(route.ServerID, validatedSnapshot)
		} else {
			lease, err = r.leases.AcquireLease(route.ServerID)
		}
	} else {
		lease, err = r.leases.AcquireLease(route.ServerID)
	}
	if err != nil {
		if errors.Is(err, ErrServerBusy) {
			errorCategory = CategoryBusy
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("[%s] server %q concurrency limit reached (request_id: %s)", CategoryBusy, route.ServerID, reqID)}},
			}, nil
		}
		errorCategory = CategoryUnavailable
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("[%s] server %q is unavailable (request_id: %s)", CategoryUnavailable, route.ServerID, reqID)}},
		}, nil
	}
	defer lease.Release()
	if validatedSnapshot != nil {
		// AcquireLease takes manager/coordinator locks. Never hold the publisher
		// read lock across it: config apply takes those locks in the reverse order.
		// Publication is revision-monotonic, so an optimistic recheck after the
		// lease is acquired rejects old-schema/new-generation admission without
		// blocking hot reload or holding a lock over downstream execution.
		r.routes.RLock()
		current := r.routes.(snapshotProvider).Snapshot()
		unchanged := current != nil && current.Revision == validatedSnapshot.Revision
		r.routes.RUnlock()
		if !unchanged {
			errorCategory = "invalid_params"
			return nil, staleValidation()
		}
	}

	generationCtx, generationCancel := context.WithCancel(ctx)
	defer generationCancel()
	if generationDone := lease.GenerationDone(); generationDone != nil {
		go func() {
			select {
			case <-generationDone:
				generationCancel()
			case <-generationCtx.Done():
			}
		}()
	}

	callCtx := context.Context(generationCtx)
	callTimeout := lease.CallTimeout()
	if callTimeout > 0 {
		var timeoutCancel context.CancelFunc
		callCtx, timeoutCancel = context.WithTimeout(generationCtx, callTimeout)
		defer timeoutCancel()
	}

	downstreamSession := lease.Session()
	if downstreamSession == nil {
		errorCategory = CategoryUnavailable
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("[%s] server %q session is nil (request_id: %s)", CategoryUnavailable, route.ServerID, reqID)}},
		}, nil
	}

	res, err := downstreamSession.CallTool(callCtx, route.OriginalName, req.Params.Arguments)
	if ctx.Err() != nil {
		errorCategory = "cancelled"
		return nil, ctx.Err()
	}
	if callCtx.Err() != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			errorCategory = CategoryTimeout
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("[%s] tool call timed out after %v (request_id: %s)", CategoryTimeout, callTimeout, reqID)}},
			}, nil
		}
		errorCategory = CategoryUnavailable
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("[%s] server %q generation stopped (request_id: %s)", CategoryUnavailable, route.ServerID, reqID)}},
		}, nil
	}
	if err != nil {
		errorCategory = CategoryDownstreamError
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("[%s] downstream tool execution failed (request_id: %s)", CategoryDownstreamError, reqID)}},
		}, nil
	}
	if res == nil {
		outcome = "success"
		errorCategory = ""
		return &mcp.CallToolResult{}, nil
	}
	if res.IsError {
		outcome = "tool_error"
		errorCategory = CategoryDownstreamError
	} else {
		outcome = "success"
		errorCategory = ""
	}
	return res, nil
}
