package downstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/catalog"
)

type preciseListKey struct{}
type preciseListCapture struct {
	id     jsonrpc.ID
	result *mcp.ListToolsResult
	err    error
}

// The SDK currently decodes interface-valued schemas through float64. Capture
// only the matching tools/list wire result before that decoding, and replace
// the decoded result with UseNumber. Requests, negotiation, cancellation and
// all other responses stay on the official SDK path.
type preciseToolTransport struct {
	base    mcp.Transport
	mu      sync.Mutex
	pending map[jsonrpc.ID]*preciseListCapture
}

type preciseToolConnection struct {
	mcp.Connection
	owner *preciseToolTransport
}

func (t *preciseToolTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := t.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &preciseToolConnection{Connection: connection, owner: t}, nil
}

func (c *preciseToolConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	if request, ok := message.(*jsonrpc.Request); ok && request.Method == "tools/list" && request.IsCall() {
		if capture, ok := ctx.Value(preciseListKey{}).(*preciseListCapture); ok {
			c.owner.mu.Lock()
			capture.id = request.ID
			c.owner.pending[request.ID] = capture
			c.owner.mu.Unlock()
		}
	}
	return c.Connection.Write(ctx, message)
}

func (c *preciseToolConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	message, err := c.Connection.Read(ctx)
	if err != nil {
		return message, err
	}
	if response, ok := message.(*jsonrpc.Response); ok {
		c.owner.mu.Lock()
		if capture, exists := c.owner.pending[response.ID]; exists {
			delete(c.owner.pending, response.ID)
			if response.Error == nil {
				if len(response.Result) > catalog.MaxCatalogJSONBytes {
					capture.err = errors.New("tool list response exceeds limit")
				} else {
					decoder := json.NewDecoder(bytes.NewReader(response.Result))
					decoder.UseNumber()
					var result mcp.ListToolsResult
					capture.err = decoder.Decode(&result)
					if capture.err == nil {
						capture.result = &result
					}
				}
			}
		}
		c.owner.mu.Unlock()
	}
	return message, nil
}

func (t *preciseToolTransport) middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		if method != "tools/list" {
			return next(ctx, method, request)
		}
		capture := &preciseListCapture{}
		result, err := next(context.WithValue(ctx, preciseListKey{}, capture), method, request)
		t.mu.Lock()
		delete(t.pending, capture.id)
		raw, decodeErr := capture.result, capture.err
		t.mu.Unlock()
		if err != nil {
			return result, err
		}
		if decodeErr != nil || raw == nil {
			return nil, errors.New("tool list response cannot be decoded precisely")
		}
		return raw, nil
	}
}
