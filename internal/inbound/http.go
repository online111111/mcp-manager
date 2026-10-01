package inbound

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/buildinfo"
)

const (
	// DefaultSessionTimeout is the maximum inactivity duration for upstream HTTP sessions.
	DefaultSessionTimeout = 30 * time.Minute

	// MaxHTTPSessions is the hard capacity ceiling on concurrent upstream HTTP sessions.
	MaxHTTPSessions = 32

	// MaxBodySize is the maximum size in bytes of an incoming HTTP POST request body (8 MiB).
	MaxBodySize = 8 * 1024 * 1024

	// ReadHeaderTimeout is the maximum time allowed to read incoming HTTP request headers.
	ReadHeaderTimeout = 5 * time.Second

	// IdleTimeout is the maximum amount of time to wait for the next request when keep-alives are enabled.
	IdleTimeout = 120 * time.Second

	// InitReadDeadline is the bounded timeout for reading the initial unauthenticated session POST body.
	InitReadDeadline = 5 * time.Second

	// SessionPOSTReadDeadline prevents an authenticated existing session from
	// pinning a handler indefinitely by slow-dripping a POST body. SSE GET
	// streams are intentionally unaffected.
	SessionPOSTReadDeadline = 15 * time.Second

	// latestStatefulProtocol is the newest MCP revision supported by this
	// stateful Streamable HTTP endpoint. MCP 2026-07-28 requires stateless HTTP;
	// rejecting the modern discovery probe makes v1.7+ clients negotiate down
	// to the latest legacy revision without creating a throwaway stateful session.
	latestStatefulProtocol = "2025-11-25"
)

// ServerStatusDTO represents the sanitized, non-secret diagnostic status of a managed downstream server.
// Per contract: URL, env, headers, command, args, paths, and session tokens MUST NEVER be exposed.
type ServerStatusDTO struct {
	ID                   string `json:"id"`
	State                string `json:"state"`
	PublishedToolCount   int    `json:"publishedToolCount"`
	UnpublishedToolCount int    `json:"unpublishedToolCount"`
	ActiveCalls          int    `json:"activeCalls"`
	DesiredRevision      int64  `json:"desiredRevision"`
	ActiveRevision       int64  `json:"activeRevision"`
	ErrorCategory        string `json:"errorCategory,omitempty"`
}

// RecentCallDTO represents the sanitized summary of a recent tool call invocation.
// Per contract: parameters, results, raw stacks, and secret values MUST NEVER be exposed.
type RecentCallDTO struct {
	RequestID     string `json:"requestId"`
	Time          string `json:"time"`
	DurationMs    int64  `json:"durationMs"`
	Tool          string `json:"tool"`
	ServerID      string `json:"serverId"`
	Outcome       string `json:"outcome"`
	ErrorCategory string `json:"errorCategory,omitempty"`
}

// StatusDTO represents the complete read-only health and diagnostic status returned by GET /api/v1/status.
type StatusDTO struct {
	Version          string            `json:"version"`
	UptimeSeconds    int64             `json:"uptimeSeconds"`
	CatalogRevision  int64             `json:"catalogRevision"`
	RestartRequired  bool              `json:"restartRequired"`
	LastReloadStatus string            `json:"lastReloadStatus"`
	Servers          []ServerStatusDTO `json:"servers"`
	RecentCalls      []RecentCallDTO   `json:"recentCalls"`
}

// ManagerCallback defines the read-only inspection methods required from the downstream manager.
type ManagerCallback interface {
	// IsReady reports whether the manager is ready per contract:
	// returns true if no servers are enabled, or at least one enabled server is Ready with published tools.
	IsReady() bool

	// GetServerStatuses returns the safe scalar status for all managed servers.
	GetServerStatuses() []ServerStatusDTO

	// GetRecentCalls returns the safe scalar summaries of recent tool calls.
	GetRecentCalls() []RecentCallDTO

	// RestartRequired reports whether a configuration reload requires a process restart.
	RestartRequired() bool

	// LastReloadStatus returns the outcome description of the last config reload.
	LastReloadStatus() string
}

// HTTPServerOptions holds configuration settings for the inbound HTTP server.
type HTTPServerOptions struct {
	Version              string
	SessionTimeout       time.Duration
	MaxSessions          int
	MaxBodySize          int64
	PublicMode           bool
	PublicURL            string
	AllowedHosts         []string
	TrustedProxies       []string
	BearerToken          string
	BearerTokens         []string
	BearerTokensProvider func() []string
	AdminHandler         http.Handler
}

// HTTPServer manages the public-facing HTTP endpoints for MCP Manager.
// It enforces safe loopback listener binding, strict Host/Origin checking,
// body and session capacity limits, and route isolation.
type HTTPServer struct {
	listener             net.Listener
	httpServer           *http.Server
	publisher            *Publisher
	manager              ManagerCallback
	sdkHandler           *mcp.StreamableHTTPHandler
	progressiveHandler   *mcp.StreamableHTTPHandler
	progressiveServer    *mcp.Server
	boundHost            string
	boundPort            string
	startTime            time.Time
	version              string
	maxSessions          int
	maxBodySize          int64
	initMu               sync.Mutex
	closeOnce            sync.Once
	closeErr             error
	rootHandler          http.Handler
	publicMode           bool
	publicURL            string
	allowedHosts         map[string]struct{}
	trustedProxies       []*net.IPNet
	bearerToken          string
	bearerTokens         []string
	bearerTokensProvider func() []string
	adminHandler         http.Handler
}

// NewHubServer creates an SDK Server instance configured with Hub server options (Tools.ListChanged=true).
func NewHubServer(name, version string) *mcp.Server {
	return mcp.NewServer(
		&mcp.Implementation{
			Name:    name,
			Version: version,
		},
		&mcp.ServerOptions{
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{
					ListChanged: true,
				},
			},
		},
	)
}

// BindLoopbackListener binds a loopback-only TCP listener.
func BindLoopbackListener(listenAddr string) (net.Listener, error) {
	return BindListener(listenAddr, false)
}

// BindListener binds a TCP listener. Non-loopback addresses require explicit
// public mode, which is separately gated by configuration authentication.
func BindListener(listenAddr string, allowPublic bool) (net.Listener, error) {
	if strings.TrimSpace(listenAddr) == "" {
		listenAddr = "127.0.0.1:8080"
	}

	host, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid listen address format %q (must be host:port): %w", listenAddr, err)
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port %q in listen address", portStr)
	}

	if !isLoopbackHost(host) && !allowPublic {
		return nil, fmt.Errorf("listen host %q is not loopback; public binding was not enabled", host)
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to bind loopback listener on %s: %w", listenAddr, err)
	}

	return ln, nil
}

func isLoopbackHost(host string) bool {
	clean := strings.Trim(host, "[]")
	if strings.EqualFold(clean, "localhost") {
		return true
	}
	ip := net.ParseIP(clean)
	if ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

// NewHTTPServer constructs an inbound HTTPServer on an already-bound listener.
// It initializes routes (/mcp, /healthz, /readyz, /api/v1/status), applies capacity
// and security middleware, and prepares the official Streamable HTTP handler.
func NewHTTPServer(listener net.Listener, publisher *Publisher, manager ManagerCallback, opts *HTTPServerOptions) (*HTTPServer, error) {
	if listener == nil {
		return nil, errors.New("listener cannot be nil")
	}
	if publisher == nil {
		return nil, errors.New("publisher cannot be nil")
	}

	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return nil, fmt.Errorf("failed to determine listener address: %w", err)
	}

	version := buildinfo.Version
	sessionTimeout := DefaultSessionTimeout
	maxSessions := MaxHTTPSessions
	maxBodySize := int64(MaxBodySize)

	if opts != nil {
		if opts.Version != "" {
			version = opts.Version
		}
		if opts.SessionTimeout > 0 {
			sessionTimeout = opts.SessionTimeout
		}
		if opts.MaxSessions > 0 {
			maxSessions = opts.MaxSessions
		}
		if opts.MaxBodySize > 0 {
			maxBodySize = opts.MaxBodySize
		}
	}
	if maxSessions > MaxHTTPSessions {
		return nil, fmt.Errorf("max sessions %d exceeds hard limit %d", maxSessions, MaxHTTPSessions)
	}
	if maxBodySize > MaxBodySize {
		return nil, fmt.Errorf("max body size %d exceeds hard limit %d", maxBodySize, MaxBodySize)
	}

	s := &HTTPServer{
		listener:     listener,
		publisher:    publisher,
		manager:      manager,
		boundHost:    host,
		boundPort:    port,
		startTime:    time.Now(),
		version:      version,
		maxSessions:  maxSessions,
		maxBodySize:  maxBodySize,
		allowedHosts: make(map[string]struct{}),
	}
	if opts != nil {
		s.publicMode = opts.PublicMode
		s.publicURL = strings.TrimRight(opts.PublicURL, "/")
		s.bearerToken = opts.BearerToken
		s.bearerTokens = append([]string(nil), opts.BearerTokens...)
		if len(s.bearerTokens) == 0 && s.bearerToken != "" {
			s.bearerTokens = []string{s.bearerToken}
		}
		s.bearerTokensProvider = opts.BearerTokensProvider
		s.adminHandler = opts.AdminHandler
		for _, allowed := range opts.AllowedHosts {
			s.allowedHosts[strings.ToLower(strings.TrimSpace(allowed))] = struct{}{}
		}
		for _, raw := range opts.TrustedProxies {
			_, network, parseErr := net.ParseCIDR(raw)
			if parseErr != nil {
				return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", raw, parseErr)
			}
			s.trustedProxies = append(s.trustedProxies, network)
		}
	}

	// Create SDK Streamable HTTP handler configured statefully with session timeout.
	s.sdkHandler = mcp.NewStreamableHTTPHandler(
		func(req *http.Request) *mcp.Server {
			return publisher.Server()
		},
		&mcp.StreamableHTTPOptions{
			SessionTimeout:             sessionTimeout,
			MaxRequestBodyBytes:        maxBodySize,
			DisableLocalhostProtection: true,
		},
	)

	s.progressiveServer = newProgressiveServer(publisher, version)
	s.progressiveHandler = mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s.progressiveServer },
		&mcp.StreamableHTTPOptions{
			SessionTimeout:             sessionTimeout,
			MaxRequestBodyBytes:        maxBodySize,
			DisableLocalhostProtection: true,
		},
	)

	mux := http.NewServeMux()

	// MCP streamable endpoint
	mcpHandler := http.HandlerFunc(s.handleMCP)
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/mcp/", mcpHandler)
	mux.Handle(ProgressivePath, mcpHandler)
	mux.Handle(ProgressivePath+"/", mcpHandler)

	// Read-only diagnostic endpoints
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/api/v1/status", s.handleStatus)
	if s.adminHandler != nil {
		mux.Handle("/admin", s.adminHandler)
		mux.Handle("/admin/", s.adminHandler)
		mux.Handle("/api/admin/v1/", s.adminHandler)
	}

	// Wrap root handler with security and capacity middleware
	s.rootHandler = s.wrapSecurityMiddleware(mux)

	s.httpServer = &http.Server{
		Handler:           s.rootHandler,
		ReadHeaderTimeout: ReadHeaderTimeout,
		IdleTimeout:       IdleTimeout,
	}

	return s, nil
}

// wrapSecurityMiddleware enforces:
// 1. Origin header rejection (all Origin headers forbidden in P0).
// 2. Host header loopback matching (rejects non-loopback or mismatched port; ignores proxy headers).
// 3. POST request body size capping (8 MiB).
func (s *HTTPServer) wrapSecurityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		isAdmin := strings.HasPrefix(req.URL.Path, "/admin") || strings.HasPrefix(req.URL.Path, "/api/admin/")
		// MCP and diagnostic APIs reject browser origins. The admin handler has its
		// own strict same-origin and CSRF policy.
		if s.publicMode && !s.isHTTPSRequest(req) {
			http.Error(w, "HTTPS required", http.StatusUpgradeRequired)
			return
		}
		if !isAdmin {
			if _, hasOrigin := req.Header["Origin"]; hasOrigin {
				http.Error(w, "Forbidden: Origin header is not allowed", http.StatusForbidden)
				return
			}
			if req.URL.Path != "/healthz" && !s.authorizedBearer(req) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="mcp-manager"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}

		// Host header check: must match the configured public allowlist or loopback bind.
		if !s.isAllowedHost(req.Host) {
			http.Error(w, "Forbidden: invalid Host header", http.StatusForbidden)
			return
		}

		// Bound every request body, including MCP DELETE requests.
		if req.Body != nil {
			req.Body = http.MaxBytesReader(w, req.Body, s.maxBodySize)
		}

		next.ServeHTTP(w, req)
	})
}

// isAllowedHost validates that the Host header matches loopback and the bound port.
// Proxy headers like X-Forwarded-Host are intentionally ignored.
func (s *HTTPServer) isHTTPSRequest(req *http.Request) bool {
	if req.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	peer := net.ParseIP(strings.Trim(host, "[]"))
	trusted := false
	for _, network := range s.trustedProxies {
		if peer != nil && network.Contains(peer) {
			trusted = true
			break
		}
	}
	if !trusted {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(strings.Split(req.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

func (s *HTTPServer) authorizedBearer(req *http.Request) bool {
	tokens := s.bearerTokens
	hasProvider := s.bearerTokensProvider != nil
	if s.bearerTokensProvider != nil {
		tokens = s.bearerTokensProvider()
	} else if provider, ok := s.manager.(interface{ BearerTokens() []string }); ok {
		hasProvider = true
		// The runtime controller supplies the hot-reloaded token set. Keeping the
		// dependency structural avoids expanding ManagerCallback just for auth.
		tokens = provider.BearerTokens()
	}
	if !hasProvider && len(tokens) == 0 && s.bearerToken != "" {
		tokens = []string{s.bearerToken}
	}
	if len(tokens) == 0 {
		return !s.publicMode
	}
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	candidate := []byte(strings.TrimPrefix(auth, "Bearer "))
	matched := 0
	for _, token := range tokens {
		matched |= subtle.ConstantTimeCompare(candidate, []byte(token))
	}
	return matched == 1
}

func (s *HTTPServer) isAllowedHost(hostHeader string) bool {
	if strings.TrimSpace(hostHeader) == "" {
		return false
	}

	host, port, err := net.SplitHostPort(hostHeader)
	if err != nil {
		host = hostHeader
		port = ""
	}

	if s.publicMode {
		_, ok := s.allowedHosts[strings.ToLower(hostHeader)]
		if ok {
			return true
		}
		_, ok = s.allowedHosts[strings.ToLower(host)]
		return ok
	}

	// Local mode pins the Host port to the bound port.
	if s.boundPort != "" {
		if port != "" {
			if port != s.boundPort {
				return false
			}
		} else if s.boundPort != "80" {
			return false
		}
	}

	// Verify host is loopback
	return isLoopbackHost(host)
}

// handleMCP handles requests to /mcp with session admission control.
// For initial POST requests (without Mcp-Session-Id), it enforces a 5s read deadline
// and serializes capacity verification under initMu. Existing sessions and GET SSE
// streams do not hold initMu.
func (s *HTTPServer) handleMCP(w http.ResponseWriter, req *http.Request) {
	handler := s.sdkHandler
	if req.URL.Path == ProgressivePath || req.URL.Path == ProgressivePath+"/" {
		handler = s.progressiveHandler
	} else if req.URL.Path != "/mcp" && req.URL.Path != "/mcp/" {
		http.NotFound(w, req)
		return
	}
	// MCP 2026-07-28 is stateless over Streamable HTTP. This endpoint remains
	// deliberately stateful so legacy clients keep session-scoped SSE/listChanged
	// behavior. Reject the modern probe before the SDK allocates a temporary
	// stateful session; v1.7+ clients then fall back to 2025-11-25 as designed.
	if protocolVersion := strings.TrimSpace(req.Header.Get("Mcp-Protocol-Version")); protocolVersion > latestStatefulProtocol {
		http.Error(w, "Bad Request: this stateful endpoint supports MCP through "+latestStatefulProtocol, http.StatusBadRequest)
		return
	}

	sessionID := req.Header.Get("Mcp-Session-Id")
	isInitPOST := (req.Method == http.MethodPost && sessionID == "")

	if req.Method == http.MethodPost {
		deadline := SessionPOSTReadDeadline
		if isInitPOST {
			deadline = InitReadDeadline
		}
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(deadline))
		defer rc.SetReadDeadline(time.Time{})
	}

	if isInitPOST {
		s.initMu.Lock()
		defer s.initMu.Unlock()

		// Count active sessions on the SDK server
		activeSessions := 0
		for range s.publisher.Server().Sessions() {
			activeSessions++
		}
		for range s.progressiveServer.Sessions() {
			activeSessions++
		}
		if activeSessions >= s.maxSessions {
			http.Error(w, "server session capacity reached", http.StatusServiceUnavailable)
			return
		}

		handler.ServeHTTP(w, req)
		return
	}

	// Existing session POST, GET SSE stream, DELETE request:
	// Serve directly without holding initMu.
	handler.ServeHTTP(w, req)
}

// handleHealthz handles GET /healthz (200 OK, status=ok).
func (s *HTTPServer) handleHealthz(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleReadyz handles GET /readyz (200 OK if ready, 503 if not ready).
// Contract: ready if no enabled servers, or at least one is Ready with published tools.
func (s *HTTPServer) handleReadyz(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	ready := true
	if s.manager != nil {
		ready = s.manager.IsReady()
	}

	if ready {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not ready"}`))
	}
}

// handleStatus handles GET /api/v1/status (200 OK with sanitized StatusDTO).
// Per contract: Cache-Control: no-store, no CORS, and no secret/internal fields exposed.
func (s *HTTPServer) handleStatus(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	status := StatusDTO{
		Version:          s.version,
		UptimeSeconds:    int64(time.Since(s.startTime).Seconds()),
		CatalogRevision:  s.publisher.Revision(),
		RestartRequired:  false,
		LastReloadStatus: "ok",
		Servers:          []ServerStatusDTO{},
		RecentCalls:      []RecentCallDTO{},
	}

	if s.manager != nil {
		status.RestartRequired = s.manager.RestartRequired()
		status.LastReloadStatus = s.manager.LastReloadStatus()
		if srvs := s.manager.GetServerStatuses(); srvs != nil {
			status.Servers = srvs
		}
		if calls := s.manager.GetRecentCalls(); calls != nil {
			status.RecentCalls = calls
		}
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(status)
}

// Serve begins accepting incoming HTTP connections on the bound listener.
func (s *HTTPServer) Serve() error {
	return s.httpServer.Serve(s.listener)
}

// Shutdown gracefully shuts down the HTTP server.
func (s *HTTPServer) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Close immediately closes the HTTP server and listener. It is idempotent.
func (s *HTTPServer) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.httpServer.Close()
	})
	return s.closeErr
}

// URL returns the base HTTP URL of the bound server (e.g. "http://127.0.0.1:8080").
func (s *HTTPServer) URL() string {
	host := s.boundHost
	if strings.EqualFold(host, "localhost") {
		return "http://" + net.JoinHostPort("localhost", s.boundPort)
	}
	return "http://" + net.JoinHostPort(strings.Trim(host, "[]"), s.boundPort)
}

// Port returns the bound port string.
func (s *HTTPServer) Port() string {
	return s.boundPort
}

// Addr returns the net.Addr of the bound listener.
func (s *HTTPServer) Addr() net.Addr {
	return s.listener.Addr()
}

// Handler returns the root HTTP handler with security middleware installed.
func (s *HTTPServer) Handler() http.Handler {
	return s.rootHandler
}
