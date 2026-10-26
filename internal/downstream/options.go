package downstream

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/netpolicy"
)

var (
	ErrUnsupportedTransport = errors.New("unsupported downstream transport type")
	ErrInvalidOptions       = errors.New("invalid downstream options")
	ErrForbiddenHeader      = errors.New("forbidden header in downstream configuration")
	ErrRedirectNotAllowed   = errors.New("downstream HTTP redirects are not allowed")
	ErrDuplicateCursor      = errors.New("duplicate cursor detected during pagination")
	ErrDuplicateToolName    = errors.New("duplicate tool name detected during tool discovery")
	ErrSessionClosed        = errors.New("downstream session is closed")
)

type TransportType string

const (
	TransportTypeStreamableHTTP TransportType = "streamable_http"
	TransportTypeIO             TransportType = "io"
)

// DefaultHTTPKeepAlive refreshes stateful remote sessions without keeping stdio workers busy.
const DefaultHTTPKeepAlive = 5 * time.Minute

type HTTPOptions struct {
	Endpoint             string
	Headers              map[string]string
	BaseRoundTripper     http.RoundTripper
	DisableStandaloneSSE bool
	MaxRetries           int
	// KeepAlive is the HTTP protocol ping interval. Zero uses the default; negative disables it.
	KeepAlive         time.Duration
	StartupTimeout    time.Duration
	ClientInfo        *mcp.Implementation
	OnToolListChanged func()
}

func (o *HTTPOptions) Validate() error {
	if o.Endpoint == "" {
		return fmt.Errorf("%w: endpoint URL must not be empty", ErrInvalidOptions)
	}
	if _, err := netpolicy.ValidateMCPHTTPURL(o.Endpoint); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidOptions, err)
	}
	if err := ValidateHeaders(o.Headers); err != nil {
		return err
	}
	return nil
}

type IOOptions struct {
	Reader            io.ReadCloser
	Writer            io.WriteCloser
	StartupTimeout    time.Duration
	ClientInfo        *mcp.Implementation
	OnToolListChanged func()
}

func (o *IOOptions) Validate() error {
	if o.Reader == nil {
		return fmt.Errorf("%w: reader must not be nil", ErrInvalidOptions)
	}
	if o.Writer == nil {
		return fmt.Errorf("%w: writer must not be nil", ErrInvalidOptions)
	}
	return nil
}

type Options struct {
	Type TransportType

	Endpoint             string
	Headers              map[string]string
	BaseRoundTripper     http.RoundTripper
	DisableStandaloneSSE bool
	MaxRetries           int
	// KeepAlive is the HTTP protocol ping interval. Zero uses the default; negative disables it.
	KeepAlive time.Duration

	Reader io.ReadCloser
	Writer io.WriteCloser

	StartupTimeout    time.Duration
	ClientInfo        *mcp.Implementation
	OnToolListChanged func()
}

func (o *Options) Validate() error {
	switch o.Type {
	case TransportTypeStreamableHTTP:
		httpOpts := HTTPOptions{
			Endpoint:             o.Endpoint,
			Headers:              o.Headers,
			BaseRoundTripper:     o.BaseRoundTripper,
			DisableStandaloneSSE: o.DisableStandaloneSSE,
			MaxRetries:           o.MaxRetries,
		}
		return httpOpts.Validate()
	case TransportTypeIO:
		ioOpts := IOOptions{Reader: o.Reader, Writer: o.Writer}
		return ioOpts.Validate()
	case "":
		return fmt.Errorf("%w: transport type must not be empty", ErrUnsupportedTransport)
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedTransport, o.Type)
	}
}
