package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"

	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/admin"
	"github.com/online111111/mcp-manager/internal/bridge"
	"github.com/online111111/mcp-manager/internal/buildinfo"
	"github.com/online111111/mcp-manager/internal/inbound"
	"github.com/online111111/mcp-manager/internal/manager"
	"github.com/online111111/mcp-manager/internal/netpolicy"
	hubruntime "github.com/online111111/mcp-manager/internal/runtime"
)

// Exit codes per contract:
// 0: success
// 1: internal error
// 2: parameter / config invalid / conflict
// 3: network / runtime unavailable
const (
	ExitSuccess            = 0
	ExitInternalError      = 1
	ExitInvalidParams      = 2
	ExitRuntimeUnavailable = 3
)

// Run parses command-line arguments and executes the requested subcommand.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return ExitInvalidParams
	}

	switch args[0] {
	case "serve":
		return runServe(args[1:], stdout, stderr, nil)
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "import":
		return runImport(args[1:], stdout, stderr)
	case "export":
		return runExport(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "admin":
		return runAdmin(args[1:], stdout, stderr)
	case "stdio":
		return runStdio(args[1:], stdout, stderr)
	case "version", "--version":
		fmt.Fprintf(stdout, "%s %s\n", buildinfo.Name, buildinfo.Version)
		return ExitSuccess
	case "help", "-h", "--help":
		printUsage(stdout)
		return ExitSuccess
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printUsage(stderr)
		return ExitInvalidParams
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `MCP Manager - self-hosted MCP gateway & management console

Usage:
  mcp-manager version
  mcp-manager serve --config <path>
  mcp-manager validate --config <path>
  mcp-manager import --from <path> --config <path> [--dry-run | --yes] [--remote-type <type>]
  mcp-manager export [--client <cursor|claude-desktop>] [--transport <stdio|http>] [--discovery <progressive|full>] [--endpoint <url>] [--token-env]
  mcp-manager status [--endpoint <url>] [--token <bearer-token>] [--json]
  mcp-manager doctor [--endpoint <url>] [--token <bearer-token>]
  mcp-manager admin <list|get|add|edit|delete> ...
  mcp-manager stdio --connect <url> [--token <bearer-token>]
`)
}

// runServe executes the serve subcommand.
func runServe(args []string, stdout, stderr io.Writer, stopCh <-chan struct{}) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "Path to configuration file")

	if err := fs.Parse(args); err != nil {
		return ExitInvalidParams
	}

	if *configPath == "" {
		fmt.Fprintln(stderr, "error: --config flag is required")
		return ExitInvalidParams
	}

	cfg, resolved, err := ValidateConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "configuration validation error: %v\n", err)
		return ExitInvalidParams
	}

	listener, err := inbound.BindListener(resolved.Listen, resolved.PublicMode)
	if err != nil {
		fmt.Fprintf(stderr, "failed to bind listen address %q: %v\n", resolved.Listen, err)
		return ExitInvalidParams
	}
	defer listener.Close()

	hubServer := inbound.NewHubServer(buildinfo.Name, buildinfo.Version)
	publisher, err := inbound.NewPublisher(hubServer, nil, nil)
	if err != nil {
		fmt.Fprintf(stderr, "failed to initialize publisher: %v\n", err)
		return ExitInternalError
	}

	mgrCtx, cancelMgr := context.WithCancel(context.Background())
	defer cancelMgr()

	mgr := manager.NewManager(publisher)
	publisher.SetRouterCallback(mgr.RouteTool)

	if err := mgr.Start(mgrCtx); err != nil {
		fmt.Fprintf(stderr, "failed to start downstream manager: %v\n", err)
		return ExitInternalError
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.Stop(ctx)
	}()
	if err := mgr.Apply(mgrCtx, resolved); err != nil {
		fmt.Fprintf(stderr, "failed to apply initial configuration: %v\n", err)
		return ExitInternalError
	}

	controller := hubruntime.NewController(mgr, *configPath, resolved.Listen, nil, resolved)
	controller.Start(mgrCtx, time.Second)

	startedAt := time.Now()
	var adminHandler http.Handler
	if resolved.AdminEnabled {
		adminUI, adminErr := admin.New(admin.Options{
			ConfigPath:     *configPath,
			AdminToken:     resolved.AdminToken,
			PublicURL:      resolved.PublicURL,
			PublicMode:     resolved.PublicMode,
			TrustedProxies: resolved.TrustedProxies,
			SessionTimeout: resolved.AdminSessionTimeout,
			Status: func() any {
				return inbound.StatusDTO{
					Version:          buildinfo.Version,
					UptimeSeconds:    int64(time.Since(startedAt).Seconds()),
					CatalogRevision:  publisher.Revision(),
					RestartRequired:  controller.RestartRequired(),
					LastReloadStatus: controller.LastReloadStatus(),
					Servers:          controller.GetServerStatuses(),
					RecentCalls:      controller.GetRecentCalls(),
				}
			},
			Reload:            controller.ReloadNow,
			Preflight:         mgr.Preflight,
			ConfigTransaction: controller.WithConfigTransaction,
		})
		if adminErr != nil {
			fmt.Fprintf(stderr, "failed to initialize admin UI: %v\n", adminErr)
			return ExitInternalError
		}
		adminHandler = adminUI
	}
	httpSrv, err := inbound.NewHTTPServer(listener, publisher, controller, &inbound.HTTPServerOptions{
		Version:        buildinfo.Version,
		PublicMode:     resolved.PublicMode,
		PublicURL:      resolved.PublicURL,
		AllowedHosts:   resolved.AllowedHosts,
		TrustedProxies: resolved.TrustedProxies,
		BearerToken:    resolved.BearerToken,
		AdminHandler:   adminHandler,
	})
	if err != nil {
		fmt.Fprintf(stderr, "failed to create HTTP server: %v\n", err)
		return ExitInternalError
	}

	defer httpSrv.Close()
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	shutdownDone := make(chan struct{})

	go func() {
		select {
		case <-sigChan:
		case <-stopCh:
		case <-mgrCtx.Done():
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		_ = mgr.Stop(shutdownCtx)
		cancelMgr()
		close(shutdownDone)
	}()

	fmt.Fprintf(stderr, "MCP Manager serving on %s (listening on %s, %d servers configured)\n",
		httpSrv.URL(), resolved.Listen, len(cfg.MCPServers))

	if err := httpSrv.Serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "server encountered an error: %v\n", err)
		cancelMgr()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = mgr.Stop(shutdownCtx)
		cancel()
		return ExitInternalError
	}

	<-shutdownDone
	return ExitSuccess
}

func runValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "Path to configuration file")

	if err := fs.Parse(args); err != nil {
		return ExitInvalidParams
	}

	if *configPath == "" {
		fmt.Fprintln(stderr, "error: --config flag is required")
		return ExitInvalidParams
	}

	cfg, _, err := ValidateConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "validation failed for %q: %v\n", *configPath, err)
		return ExitInvalidParams
	}

	fmt.Fprintf(stdout, "Configuration is valid: %s (%d servers configured)\n", *configPath, len(cfg.MCPServers))
	return ExitSuccess
}

func runImport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fromPath := fs.String("from", "", "Source configuration file")
	configPath := fs.String("config", "", "Target configuration file")
	dryRun := fs.Bool("dry-run", false, "Preview import without modifying configuration")
	yes := fs.Bool("yes", false, "Execute import and save to configuration")
	remoteType := fs.String("remote-type", "", "Remote server type: streamableHttp or http")

	if err := fs.Parse(args); err != nil {
		return ExitInvalidParams
	}

	if *fromPath == "" || *configPath == "" {
		fmt.Fprintln(stderr, "error: both --from and --config are required")
		return ExitInvalidParams
	}

	if (*dryRun && *yes) || (!*dryRun && !*yes) {
		fmt.Fprintln(stderr, "error: exactly one of --dry-run or --yes must be specified")
		return ExitInvalidParams
	}

	preview, err := RunImport(*fromPath, *configPath, *remoteType, *dryRun, *yes)
	if err != nil {
		fmt.Fprintf(stderr, "import failed: %v\n", err)
		return ExitInvalidParams
	}

	if *dryRun {
		fmt.Fprint(stdout, FormatPreview(preview))
	} else {
		fmt.Fprintf(stdout, "Successfully imported %d new servers into %s (total: %d)\n",
			preview.NewCount, preview.TargetConfigPath, preview.ExistingCount+preview.NewCount)
	}

	return ExitSuccess
}

type exportServerEntry struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
}

type exportConfig struct {
	MCPServers map[string]exportServerEntry `json:"mcpServers"`
}

func runExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	client := fs.String("client", "cursor", "Target client: cursor or claude-desktop")
	transport := fs.String("transport", "stdio", "Client transport: stdio or http")
	endpoint := fs.String("endpoint", "http://127.0.0.1:8080/mcp/progressive", "MCP endpoint URL")
	discovery := fs.String("discovery", "progressive", "Tool discovery mode: progressive or full")
	withTokenEnv := fs.Bool("token-env", false, "Add MCP_MANAGER_TOKEN environment placeholder for public MCP Manager stdio export")

	if err := fs.Parse(args); err != nil {
		return ExitInvalidParams
	}

	clientLower := strings.ToLower(strings.TrimSpace(*client))
	if clientLower != "cursor" && clientLower != "claude-desktop" {
		fmt.Fprintf(stderr, "error: unsupported client %q (supported: cursor, claude-desktop)\n", *client)
		return ExitInvalidParams
	}

	transportLower := strings.ToLower(strings.TrimSpace(*transport))
	if transportLower != "stdio" && transportLower != "http" {
		fmt.Fprintf(stderr, "error: unsupported transport %q (supported: stdio, http)\n", *transport)
		return ExitInvalidParams
	}
	if clientLower == "claude-desktop" && transportLower != "stdio" {
		fmt.Fprintln(stderr, "error: claude-desktop export supports only stdio transport")
		return ExitInvalidParams
	}

	discoveryLower := strings.ToLower(strings.TrimSpace(*discovery))
	if discoveryLower != "progressive" && discoveryLower != "full" {
		fmt.Fprintf(stderr, "error: unsupported discovery %q (supported: progressive, full)\n", *discovery)
		return ExitInvalidParams
	}
	exportEndpoint, err := normalizeExportEndpoint(*endpoint, discoveryLower)
	if err != nil {
		fmt.Fprintf(stderr, "error: invalid MCP endpoint: %v\n", err)
		return ExitInvalidParams
	}

	exp := exportConfig{MCPServers: make(map[string]exportServerEntry)}

	if transportLower == "stdio" {
		exePath, err := os.Executable()
		if err != nil {
			exePath = "mcp-manager"
		} else if abs, err := filepath.Abs(exePath); err == nil {
			exePath = abs
		}
		entry := exportServerEntry{Command: exePath, Args: []string{"stdio", "--connect", exportEndpoint}}
		if *withTokenEnv {
			entry.Env = map[string]string{managerTokenEnv: "<YOUR_MCP_MANAGER_TOKEN>"}
		}
		exp.MCPServers["mcp-manager"] = entry
	} else {
		exp.MCPServers["mcp-manager"] = exportServerEntry{URL: exportEndpoint}
	}

	data, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "failed to marshal export configuration: %v\n", err)
		return ExitInternalError
	}

	fmt.Fprintf(stderr, "Note: Client compatibility status for %s is NOT_RUN until verified with an installed client.\n", clientLower)
	fmt.Fprintln(stdout, string(data))
	return ExitSuccess
}

// normalizeExportEndpoint rewrites only the standard Manager paths. Custom
// proxy paths (including encoded paths) remain under the client's control.
func normalizeExportEndpoint(raw, discovery string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := netpolicy.ValidateHubEndpoint(raw)
	if err != nil {
		return "", errors.New("use an absolute HTTPS URL (HTTP only on loopback), without userinfo, query, or fragment")
	}
	switch strings.TrimSuffix(u.EscapedPath(), "/") {
	case "", "/mcp", "/mcp/progressive":
		u.Path = "/mcp"
		if discovery == "progressive" {
			u.Path += "/progressive"
		}
		u.RawPath = ""
		return u.String(), nil
	default:
		return raw, nil
	}
}

func diagnosticEndpoints(raw string) (baseURL, mcpURL string) {
	baseURL = strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.HasSuffix(baseURL, "/mcp/progressive") {
		return strings.TrimSuffix(baseURL, "/mcp/progressive"), baseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/mcp")
	return baseURL, baseURL + "/mcp"
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	endpoint := fs.String("endpoint", "http://127.0.0.1:8080", "MCP Manager base URL")
	token := fs.String("token", "", "Bearer token (or MCP_MANAGER_TOKEN; legacy MCP_HUB_TOKEN is supported)")
	asJSON := fs.Bool("json", false, "Output status as JSON")

	if err := fs.Parse(args); err != nil {
		return ExitInvalidParams
	}

	baseURL, _ := diagnosticEndpoints(*endpoint)
	statusURL := baseURL + "/api/v1/status"

	client := newHubHTTPClient(hubToken(*token), 5*time.Second)
	resp, err := client.Get(statusURL)
	if err != nil {
		fmt.Fprintf(stderr, "error: failed to connect to MCP Manager at %s: %v\n", statusURL, err)
		return ExitRuntimeUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "error: MCP Manager returned HTTP status %d (%s)\n", resp.StatusCode, resp.Status)
		return ExitRuntimeUnavailable
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(stderr, "error: failed to read status response: %v\n", err)
		return ExitInternalError
	}

	if *asJSON {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body, "", "  "); err == nil {
			fmt.Fprintln(stdout, pretty.String())
		} else {
			fmt.Fprintln(stdout, string(body))
		}
		return ExitSuccess
	}

	var status inbound.StatusDTO
	if err := json.Unmarshal(body, &status); err != nil {
		fmt.Fprintf(stderr, "error: failed to parse status JSON: %v\n", err)
		return ExitInternalError
	}

	fmt.Fprintf(stdout, `MCP Manager Status:
  Version:          %s
  Uptime:           %d seconds
  Catalog Revision: %d
  Restart Required: %v
  Last Reload:      %s

Managed Servers (%d):
`, status.Version, status.UptimeSeconds, status.CatalogRevision, status.RestartRequired, status.LastReloadStatus, len(status.Servers))

	for _, s := range status.Servers {
		fmt.Fprintf(stdout, "  - [%s] State: %s | Published Tools: %d | Unpublished Tools: %d | Active Calls: %d\n",
			s.ID, s.State, s.PublishedToolCount, s.UnpublishedToolCount, s.ActiveCalls)
	}

	if len(status.RecentCalls) > 0 {
		fmt.Fprintf(stdout, "\nRecent Calls (%d):\n", len(status.RecentCalls))
		for _, c := range status.RecentCalls {
			fmt.Fprintf(stdout, "  - [%s] Tool: %s (Server: %s) -> Outcome: %s (%d ms)\n",
				c.Time, c.Tool, c.ServerID, c.Outcome, c.DurationMs)
		}
	}

	return ExitSuccess
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	endpoint := fs.String("endpoint", "http://127.0.0.1:8080", "MCP Manager base URL")
	token := fs.String("token", "", "Bearer token (or MCP_MANAGER_TOKEN; legacy MCP_HUB_TOKEN is supported)")

	if err := fs.Parse(args); err != nil {
		return ExitInvalidParams
	}

	baseURL, mcpURL := diagnosticEndpoints(*endpoint)
	healthURL := baseURL + "/healthz"
	readyURL := baseURL + "/readyz"

	httpClient := newHubHTTPClient(hubToken(*token), 5*time.Second)
	hResp, err := httpClient.Get(healthURL)
	if err != nil {
		fmt.Fprintf(stderr, "MCP Manager is not running at %s. Start it with: mcp-manager serve --config <config-file>\n", baseURL)
		return ExitRuntimeUnavailable
	}
	defer hResp.Body.Close()

	if hResp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "MCP Manager health check returned non-OK status: %d\n", hResp.StatusCode)
		return ExitRuntimeUnavailable
	}

	rResp, err := httpClient.Get(readyURL)
	if err != nil {
		fmt.Fprintf(stderr, "MCP Manager readiness check failed at %s: %v\n", readyURL, err)
		return ExitRuntimeUnavailable
	}
	defer rResp.Body.Close()
	if rResp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "MCP Manager is not ready: readiness check returned HTTP %d\n", rResp.StatusCode)
		return ExitRuntimeUnavailable
	}
	readyStatus := "Ready (200)"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "mcp-manager-doctor", Version: buildinfo.Version}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:             mcpURL,
		HTTPClient:           newHubHTTPClient(hubToken(*token), 10*time.Second),
		DisableStandaloneSSE: true,
	}

	session, err := mcpClient.Connect(ctx, transport, nil)
	if err != nil {
		fmt.Fprintf(stderr, "MCP Manager protocol initialize failed at %s: %v\n", mcpURL, err)
		return ExitRuntimeUnavailable
	}
	defer session.Close()

	toolsResult, err := session.ListTools(ctx, nil)
	if err != nil {
		fmt.Fprintf(stderr, "MCP Manager tools/list failed: %v\n", err)
		return ExitRuntimeUnavailable
	}

	fmt.Fprintf(stdout, `MCP Manager Doctor Diagnostic Report
====================================
Endpoint:          %s
Health Check:      OK (200)
Readiness:         %s
MCP Initialize:    OK
MCP Tools Listed:  %d tools available
Result:            All checks passed
`, baseURL, readyStatus, len(toolsResult.Tools))
	return ExitSuccess
}

func runStdio(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stdio", flag.ContinueOnError)
	fs.SetOutput(stderr)
	endpoint := fs.String("connect", "", "Running MCP Manager MCP endpoint URL")
	token := fs.String("token", "", "Bearer token (or MCP_MANAGER_TOKEN; legacy MCP_HUB_TOKEN is supported)")
	if err := fs.Parse(args); err != nil {
		return ExitInvalidParams
	}
	if strings.TrimSpace(*endpoint) == "" {
		fmt.Fprintln(stderr, "error: --connect flag is required")
		return ExitInvalidParams
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := bridge.RunAuthenticated(ctx, *endpoint, hubToken(*token), os.Stdin, stdout, stderr); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
			return ExitSuccess
		}
		fmt.Fprintf(stderr, "stdio bridge failed: %v\n", err)
		return ExitRuntimeUnavailable
	}
	return ExitSuccess
}
