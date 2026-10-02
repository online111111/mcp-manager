package downstream

import (
	"bufio"
	"bytes"

	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/catalog"
)

// HTTP must retain the SDK's concrete transport connection: its unexported
// sessionUpdated hook starts legacy SSE subscriptions and sets protocol state.
// Intercept only response bytes instead of decorating that connection.
type preciseListRoundTripper struct {
	base  http.RoundTripper
	owner *preciseToolTransport
}

func (rt *preciseListRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	capture, tracked := req.Context().Value(preciseListKey{}).(*preciseListCapture)
	if tracked && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(body, 1<<20))
		body.Close()
		if err != nil {
			return nil, err
		}
		message, err := jsonrpc.DecodeMessage(data)
		if err != nil {
			return nil, err
		}
		if request, ok := message.(*jsonrpc.Request); ok {
			rt.owner.mu.Lock()
			capture.id = request.ID
			rt.owner.mu.Unlock()
		}
	}
	response, err := rt.base.RoundTrip(req)
	if err != nil {
		return response, err
	}
	if !tracked || response.Body == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, nil
	}
	media, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	response.Body = &preciseListBody{ReadCloser: response.Body, capture: capture, owner: rt.owner, sse: media == "text/event-stream"}
	return response, nil
}

type preciseListBody struct {
	io.ReadCloser
	capture *preciseListCapture
	owner   *preciseToolTransport
	sse     bool
	buffer  []byte
	done    bool
}

func (body *preciseListBody) Read(p []byte) (int, error) {
	n, err := body.ReadCloser.Read(p)
	if body.done {
		return n, err
	}
	if len(body.buffer)+n > catalog.MaxCatalogJSONBytes {
		body.setError(errors.New("tool list response exceeds limit"))
		return n, err
	}
	body.buffer = append(body.buffer, p[:n]...)
	if body.sse {
		for {
			index := bytes.Index(body.buffer, []byte("\n\n"))
			sep := 2
			if index < 0 {
				index = bytes.Index(body.buffer, []byte("\r\n\r\n"))
				sep = 4
			}
			if index < 0 {
				break
			}
			event := body.buffer[:index]
			body.buffer = body.buffer[index+sep:]
			var data strings.Builder
			scanner := bufio.NewScanner(bytes.NewReader(event))
			scanner.Buffer(make([]byte, 4096), catalog.MaxCatalogJSONBytes)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data:") {
					if data.Len() > 0 {
						data.WriteByte('\n')
					}
					data.WriteString(strings.TrimSpace(line[5:]))
				}
			}
			if scanner.Err() != nil {
				body.setError(errors.New("invalid tool list event"))
				break
			}
			body.captureResponse([]byte(data.String()))
			if body.done {
				break
			}
		}
	} else if json.Valid(body.buffer) {
		body.captureResponse(body.buffer)
	}
	return n, err
}

func (body *preciseListBody) setError(err error) {
	body.owner.mu.Lock()
	body.capture.err = err
	body.owner.mu.Unlock()
	body.done = true
}

func (body *preciseListBody) captureResponse(data []byte) {
	if len(data) == 0 {
		return
	}
	message, err := jsonrpc.DecodeMessage(data)
	response, ok := message.(*jsonrpc.Response)
	if err != nil || !ok || response.Error != nil || len(response.Result) == 0 {
		return
	}
	body.owner.mu.Lock()
	matching := response.ID == body.capture.id
	body.owner.mu.Unlock()
	if !matching {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(response.Result))
	decoder.UseNumber()
	var result mcp.ListToolsResult
	err = decoder.Decode(&result)
	body.owner.mu.Lock()
	body.capture.err = err
	if err == nil {
		body.capture.result = &result
	}
	body.owner.mu.Unlock()
	body.done = true
}

func preciseHTTPTransport(transport *mcp.StreamableClientTransport, owner *preciseToolTransport) {
	base := transport.HTTPClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport.HTTPClient.Transport = &preciseListRoundTripper{base: base, owner: owner}
}
