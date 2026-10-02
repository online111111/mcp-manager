package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func exportedEntry(t *testing.T, args ...string) exportServerEntry {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Run(append([]string{"export"}, args...), &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("export code=%d stderr=%s", code, stderr.String())
	}
	var cfg exportConfig
	if err := json.Unmarshal(stdout.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.MCPServers["mcp-manager"]
}

func TestCLIAdminAcceptsProgressiveEndpoint(t *testing.T) {
	ts := newRemoteAdminCLITestServer(t)
	for _, suffix := range []string{"/mcp/progressive", "/mcp/progressive/"} {
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"admin", "list", "--endpoint", ts.URL+suffix, "--token", "admin-secret"}, &stdout, &stderr); code != ExitSuccess || !strings.Contains(stdout.String(), "https://example.com/mcp") {
			t.Fatalf("admin code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	}
	for _, endpoint := range []string{"https://manager.example/custom/mcp/progressive", "http://manager.example/mcp/progressive", "https://manager.example/mcp/progressive?token=secret"} {
		if _, err := normalizeAdminEndpoint(endpoint); err == nil {
			t.Fatalf("unsafe admin endpoint accepted: %s", endpoint)
		}
	}
}

func TestCLIStatusAcceptsProgressiveEndpoint(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"test"}`))
	}))
	defer ts.Close()
	for _, suffix := range []string{"/mcp/progressive", "/mcp/progressive/"} {
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"status", "--endpoint", ts.URL+suffix, "--json"}, &stdout, &stderr); code != ExitSuccess || !strings.Contains(stdout.String(), `"version": "test"`) {
			t.Fatalf("status code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	}
}

func TestCLIDoctorChecksSelectedProgressiveEndpoint(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/mcp/progressive", mcpHandler)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	for _, suffix := range []string{"/mcp/progressive", "/mcp/progressive/"} {
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"doctor", "--endpoint", ts.URL+suffix}, &stdout, &stderr); code != ExitSuccess || !strings.Contains(stdout.String(), "All checks passed") {
			t.Fatalf("doctor code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	}
}

func TestCLIExportRejectsInvalidEndpoints(t *testing.T) {
	for _, endpoint := range []string{"", "/mcp", "://bad", "ftp://manager.example/mcp", "http://manager.example/mcp", "https://user:do-not-print@manager.example/mcp", "https://manager.example/mcp?token=do-not-print", "https://manager.example/mcp#fragment", "https://manager.example/%do-not-print"} {
		for _, transport := range []string{"http", "stdio"} {
			t.Run(transport+endpoint, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				code := Run([]string{"export", "--transport", transport, "--endpoint", endpoint}, &stdout, &stderr)
				if code != ExitInvalidParams || stdout.Len() != 0 || !strings.Contains(stderr.String(), "invalid MCP endpoint") {
					t.Fatalf("invalid endpoint: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
				}
				if strings.Contains(stderr.String(), "do-not-print") {
					t.Fatal("endpoint credentials leaked to stderr")
				}
			})
		}
	}
}

func TestCLIExportNormalizesKnownEndpoints(t *testing.T) {
	for _, mode := range []string{"progressive", "full"} {
		want := "https://manager.example/mcp"
		if mode == "progressive" {
			want += "/progressive"
		}
		for _, path := range []string{"", "/", "/mcp", "/mcp/", "/mcp/progressive", "/mcp/progressive/"} {
			for _, transport := range []string{"http", "stdio"} {
				t.Run(mode+path+"/"+transport, func(t *testing.T) {
					entry := exportedEntry(t, "--endpoint", "https://manager.example"+path, "--discovery", mode, "--transport", transport)
					got := entry.URL
					if transport == "stdio" {
						got = entry.Args[2]
					}
					if got != want {
						t.Fatalf("endpoint=%s; want %s", got, want)
					}
				})
			}
		}
	}
	for _, endpoint := range []string{"https://manager.example/custom/mcp", "https://manager.example/custom/mcp/", "https://manager.example/proxy/mcp/progressive", "https://manager.example/%6dcp"} {
		for _, mode := range []string{"progressive", "full"} {
			t.Run("custom/"+mode+endpoint, func(t *testing.T) {
				if entry := exportedEntry(t, "--endpoint", endpoint, "--discovery", mode, "--transport", "http"); entry.URL != endpoint {
					t.Fatalf("custom endpoint changed: %s; want %s", entry.URL, endpoint)
				}
			})
		}
	}
}

func TestCLIExportDiscoveryModes(t *testing.T) {
	for _, mode := range []string{"progressive", "full"} {
		want := "http://127.0.0.1:8080/mcp"
		if mode == "progressive" {
			want += "/progressive"
		}
		for _, transport := range []string{"http", "stdio"} {
			t.Run(mode+"/"+transport, func(t *testing.T) {
				entry := exportedEntry(t, "--transport", transport, "--discovery", mode)
				got := entry.URL
				if transport == "stdio" {
					got = entry.Args[2]
				}
				if got != want {
					t.Fatalf("endpoint=%s; want %s", got, want)
				}
			})
		}
	}
	t.Run("invalid", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"export", "--discovery", "automatic"}, &stdout, &stderr); code != ExitInvalidParams || !strings.Contains(stderr.String(), "unsupported discovery") || stdout.Len() != 0 {
			t.Fatalf("invalid mode: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	})
}

func TestCLIExportDefaultsToProgressive(t *testing.T) {
	const endpoint = "http://127.0.0.1:8080/mcp/progressive"
	t.Run("http", func(t *testing.T) {
		entry := exportedEntry(t, "--transport", "http")
		if entry.URL != endpoint || entry.Command != "" {
			t.Fatalf("HTTP export=%+v; want URL %s", entry, endpoint)
		}
	})
	t.Run("stdio", func(t *testing.T) {
		entry := exportedEntry(t, "--client", "claude-desktop", "--token-env")
		if entry.Command == "" || len(entry.Args) != 3 || entry.Args[2] != endpoint {
			t.Fatalf("stdio export=%+v; want progressive --connect", entry)
		}
		if entry.Env[managerTokenEnv] != "<YOUR_MCP_MANAGER_TOKEN>" {
			t.Fatalf("expected token placeholder, got %v", entry.Env)
		}
	})
}
