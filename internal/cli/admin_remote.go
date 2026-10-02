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
	"net/http/cookiejar"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/online111111/mcp-manager/internal/config"
	"github.com/online111111/mcp-manager/internal/netpolicy"
)

const adminTokenEnv = managerAdminTokenEnv

type remoteAdminClient struct {
	baseURL string
	client  *http.Client
	csrf    string
}

type remoteAdminConfig struct {
	Version    int                            `json:"version"`
	MCPServers map[string]config.ServerConfig `json:"mcpServers"`
}

func runAdmin(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printAdminUsage(stderr)
		return ExitInvalidParams
	}

	switch args[0] {
	case "list":
		return runAdminList(args[1:], stdout, stderr)
	case "get":
		return runAdminGet(args[1:], stdout, stderr)
	case "add":
		return runAdminPut(args[1:], stdout, stderr, false)
	case "edit":
		return runAdminPut(args[1:], stdout, stderr, true)
	case "delete", "rm":
		return runAdminDelete(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printAdminUsage(stdout)
		return ExitSuccess
	default:
		fmt.Fprintf(stderr, "unknown admin command %q\n\n", args[0])
		printAdminUsage(stderr)
		return ExitInvalidParams
	}
}

func printAdminUsage(w io.Writer) {
	fmt.Fprint(w, `Remote Admin - manage downstream MCP services on MCP Manager

Usage:
  mcp-manager admin list --endpoint <url> [--token <admin-token>] [--json]
  mcp-manager admin get <id> --endpoint <url> [--token <admin-token>]
  mcp-manager admin add <id> --file <server.json|-> --endpoint <url> [--token <admin-token>]
  mcp-manager admin edit <id> --file <server.json|-> --endpoint <url> [--token <admin-token>]
  mcp-manager admin delete <id> --endpoint <url> [--token <admin-token>] --yes

Admin token defaults to MCP_MANAGER_ADMIN_TOKEN. The legacy MCP_HUB_ADMIN_TOKEN is accepted during the v0.4 compatibility window. Remote endpoints require HTTPS;
plain HTTP is allowed only for loopback/local development.
`)
}

type adminCommonFlags struct {
	endpoint string
	token    string
}

func addAdminCommonFlags(fs *flag.FlagSet) *adminCommonFlags {
	common := &adminCommonFlags{}
	fs.StringVar(&common.endpoint, "endpoint", "http://127.0.0.1:8080", "Hub base URL")
	fs.StringVar(&common.token, "token", "", "Admin token (or MCP_MANAGER_ADMIN_TOKEN; legacy MCP_HUB_ADMIN_TOKEN is accepted)")
	return common
}

func (c *adminCommonFlags) resolvedToken() string {
	if strings.TrimSpace(c.token) != "" {
		return c.token
	}
	return adminTokenFromEnv()
}

func runAdminList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	common := addAdminCommonFlags(fs)
	asJSON := fs.Bool("json", false, "Output the redacted server map as JSON")
	if err := fs.Parse(args); err != nil {
		return ExitInvalidParams
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "error: admin list does not accept positional arguments")
		return ExitInvalidParams
	}

	client, code := newRemoteAdminClient(common.endpoint, common.resolvedToken(), stderr)
	if code != ExitSuccess {
		return code
	}
	defer client.close()
	cfg, _, err := client.getConfig()
	if err != nil {
		return printAdminError(stderr, err)
	}
	if *asJSON {
		data, err := json.MarshalIndent(cfg.MCPServers, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "error: encode server list: %v\n", err)
			return ExitInternalError
		}
		fmt.Fprintln(stdout, string(data))
		return ExitSuccess
	}

	ids := make([]string, 0, len(cfg.MCPServers))
	for id := range cfg.MCPServers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		fmt.Fprintln(stdout, "No downstream MCP services configured.")
		return ExitSuccess
	}
	for _, id := range ids {
		srv := cfg.MCPServers[id]
		target := srv.Command
		if srv.Type == "streamable_http" {
			target = srv.URL
		}
		fmt.Fprintf(stdout, "%s\t%s\t%t\t%s\n", id, srv.Type, srv.IsEnabled(), target)
	}
	return ExitSuccess
}

func runAdminGet(args []string, stdout, stderr io.Writer) int {
	id, rest, ok := adminServerID(args, stderr, "get")
	if !ok {
		return ExitInvalidParams
	}
	fs := flag.NewFlagSet("admin get", flag.ContinueOnError)
	fs.SetOutput(stderr)
	common := addAdminCommonFlags(fs)
	if err := fs.Parse(rest); err != nil {
		return ExitInvalidParams
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "error: unexpected positional arguments")
		return ExitInvalidParams
	}
	client, code := newRemoteAdminClient(common.endpoint, common.resolvedToken(), stderr)
	if code != ExitSuccess {
		return code
	}
	defer client.close()
	cfg, _, err := client.getConfig()
	if err != nil {
		return printAdminError(stderr, err)
	}
	srv, exists := cfg.MCPServers[id]
	if !exists {
		fmt.Fprintf(stderr, "error: server %q does not exist\n", id)
		return ExitInvalidParams
	}
	data, err := json.MarshalIndent(srv, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "error: encode server: %v\n", err)
		return ExitInternalError
	}
	fmt.Fprintln(stdout, string(data))
	return ExitSuccess
}

func runAdminPut(args []string, stdout, stderr io.Writer, requireExisting bool) int {
	verb := "add"
	if requireExisting {
		verb = "edit"
	}
	id, rest, ok := adminServerID(args, stderr, verb)
	if !ok {
		return ExitInvalidParams
	}
	fs := flag.NewFlagSet("admin "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	common := addAdminCommonFlags(fs)
	file := fs.String("file", "", "Server JSON file, or - for stdin")
	if err := fs.Parse(rest); err != nil {
		return ExitInvalidParams
	}
	if fs.NArg() != 0 || strings.TrimSpace(*file) == "" {
		fmt.Fprintln(stderr, "error: --file is required and no extra positional arguments are allowed")
		return ExitInvalidParams
	}

	data, err := readAdminServerFile(*file)
	if err != nil {
		fmt.Fprintf(stderr, "error: read server configuration: %v\n", err)
		return ExitInvalidParams
	}
	var srv config.ServerConfig
	if err := config.DecodeStrict(data, &srv); err != nil {
		fmt.Fprintf(stderr, "error: invalid server configuration: %v\n", err)
		return ExitInvalidParams
	}
	canonical, err := json.Marshal(srv)
	if err != nil {
		fmt.Fprintf(stderr, "error: encode server configuration: %v\n", err)
		return ExitInternalError
	}

	client, code := newRemoteAdminClient(common.endpoint, common.resolvedToken(), stderr)
	if code != ExitSuccess {
		return code
	}
	defer client.close()
	cfg, etag, err := client.getConfig()
	if err != nil {
		return printAdminError(stderr, err)
	}
	_, exists := cfg.MCPServers[id]
	if requireExisting && !exists {
		fmt.Fprintf(stderr, "error: server %q does not exist; use admin add\n", id)
		return ExitInvalidParams
	}
	if !requireExisting && exists {
		fmt.Fprintf(stderr, "error: server %q already exists; use admin edit\n", id)
		return ExitInvalidParams
	}
	if err := client.putServer(id, canonical, etag); err != nil {
		return printAdminError(stderr, err)
	}
	fmt.Fprintf(stdout, "Server %q %s successfully.\n", id, map[bool]string{true: "updated", false: "added"}[requireExisting])
	return ExitSuccess
}

func runAdminDelete(args []string, stdout, stderr io.Writer) int {
	id, rest, ok := adminServerID(args, stderr, "delete")
	if !ok {
		return ExitInvalidParams
	}
	fs := flag.NewFlagSet("admin delete", flag.ContinueOnError)
	fs.SetOutput(stderr)
	common := addAdminCommonFlags(fs)
	yes := fs.Bool("yes", false, "Confirm deletion")
	if err := fs.Parse(rest); err != nil {
		return ExitInvalidParams
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "error: unexpected positional arguments")
		return ExitInvalidParams
	}
	if !*yes {
		fmt.Fprintln(stderr, "error: --yes is required for remote deletion")
		return ExitInvalidParams
	}
	client, code := newRemoteAdminClient(common.endpoint, common.resolvedToken(), stderr)
	if code != ExitSuccess {
		return code
	}
	defer client.close()
	cfg, etag, err := client.getConfig()
	if err != nil {
		return printAdminError(stderr, err)
	}
	if _, exists := cfg.MCPServers[id]; !exists {
		fmt.Fprintf(stderr, "error: server %q does not exist\n", id)
		return ExitInvalidParams
	}
	if err := client.deleteServer(id, etag); err != nil {
		return printAdminError(stderr, err)
	}
	fmt.Fprintf(stdout, "Server %q deleted successfully.\n", id)
	return ExitSuccess
}

func adminServerID(args []string, stderr io.Writer, verb string) (string, []string, bool) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(stderr, "error: admin %s requires a server ID before flags\n", verb)
		return "", nil, false
	}
	id := strings.TrimSpace(args[0])
	if id == "" || strings.Contains(id, "/") {
		fmt.Fprintln(stderr, "error: invalid server ID")
		return "", nil, false
	}
	return id, args[1:], true
}

func readAdminServerFile(path string) ([]byte, error) {
	if path == "-" {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, maxAdminCLIConfigSize+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxAdminCLIConfigSize {
			return nil, errors.New("server configuration exceeds 1 MiB")
		}
		return data, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxAdminCLIConfigSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAdminCLIConfigSize {
		return nil, errors.New("server configuration exceeds 1 MiB")
	}
	return data, nil
}

const maxAdminCLIConfigSize = 1024 * 1024

func newRemoteAdminClient(endpoint, token string, stderr io.Writer) (*remoteAdminClient, int) {
	if strings.TrimSpace(token) == "" {
		fmt.Fprintf(stderr, "error: admin token is required via --token or %s\n", adminTokenEnv)
		return nil, ExitInvalidParams
	}
	baseURL, err := normalizeAdminEndpoint(endpoint)
	if err != nil {
		fmt.Fprintf(stderr, "error: invalid admin endpoint: %v\n", err)
		return nil, ExitInvalidParams
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		fmt.Fprintf(stderr, "error: initialize cookie jar: %v\n", err)
		return nil, ExitInternalError
	}
	client := &http.Client{
		Jar:       jar,
		Transport: bearerTransport{},
		Timeout:   60 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	c := &remoteAdminClient{baseURL: baseURL, client: client}
	if err := c.login(token); err != nil {
		return nil, printAdminError(stderr, err)
	}
	return c, ExitSuccess
}

func normalizeAdminEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("endpoint is empty")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("endpoint must be an absolute URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("userinfo, query, and fragment are not allowed")
	}
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	if path != "" && path != "/admin" && path != "/mcp" && path != "/mcp/progressive" {
		return "", errors.New("endpoint path must be empty, /admin, /mcp, or /mcp/progressive")
	}
	u.Path = ""
	u.RawPath = ""
	validated, err := netpolicy.ValidateHubEndpoint(u.String())
	if err != nil {
		return "", err
	}
	return strings.TrimRight(validated.String(), "/"), nil
}

func (c *remoteAdminClient) login(token string) error {
	body, _ := json.Marshal(map[string]string{"token": token})
	resp, err := c.do(http.MethodPost, "/api/admin/v1/auth/login", body, "", false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return adminHTTPError(resp, data)
	}
	var session struct {
		CSRF string `json:"csrfToken"`
	}
	if err := json.Unmarshal(data, &session); err != nil || strings.TrimSpace(session.CSRF) == "" {
		return errors.New("admin login returned an invalid session")
	}
	c.csrf = session.CSRF
	return nil
}

// close releases the per-command Admin session, including error paths. A failed
// best-effort logout must not turn a successful write into a retryable failure.
func (c *remoteAdminClient) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/admin/v1/auth/logout", strings.NewReader("{}"))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrf)
	resp, err := c.client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

func (c *remoteAdminClient) getConfig() (*remoteAdminConfig, string, error) {
	resp, err := c.do(http.MethodGet, "/api/admin/v1/config", nil, "", false)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", adminHTTPError(resp, data)
	}
	var cfg remoteAdminConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, "", fmt.Errorf("decode admin configuration: %w", err)
	}
	return &cfg, resp.Header.Get("ETag"), nil
}

func (c *remoteAdminClient) putServer(id string, body []byte, etag string) error {
	resp, err := c.do(http.MethodPut, "/api/admin/v1/servers/"+url.PathEscape(id), body, etag, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return adminHTTPError(resp, data)
	}
	return nil
}

func (c *remoteAdminClient) deleteServer(id, etag string) error {
	resp, err := c.do(http.MethodDelete, "/api/admin/v1/servers/"+url.PathEscape(id), []byte(`{}`), etag, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return adminHTTPError(resp, data)
	}
	return nil
}

func (c *remoteAdminClient) do(method, path string, body []byte, etag string, mutation bool) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if mutation {
		req.Header.Set("X-CSRF-Token", c.csrf)
		if strings.TrimSpace(etag) != "" {
			req.Header.Set("If-Match", etag)
		}
	}
	return c.client.Do(req)
}

type adminRemoteError struct {
	status int
	body   string
}

func (e adminRemoteError) Error() string {
	body := strings.TrimSpace(e.body)
	if body == "" {
		return fmt.Sprintf("admin API returned HTTP %d", e.status)
	}
	return fmt.Sprintf("admin API returned HTTP %d: %s", e.status, body)
}

func adminHTTPError(resp *http.Response, data []byte) error {
	return adminRemoteError{status: resp.StatusCode, body: string(data)}
}

func printAdminError(stderr io.Writer, err error) int {
	var remote adminRemoteError
	if errors.As(err, &remote) {
		switch remote.status {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusNotFound:
			fmt.Fprintf(stderr, "error: %v\n", err)
			return ExitInvalidParams
		default:
			fmt.Fprintf(stderr, "error: %v\n", err)
			return ExitRuntimeUnavailable
		}
	}
	fmt.Fprintf(stderr, "error: remote admin request failed: %v\n", err)
	return ExitRuntimeUnavailable
}
