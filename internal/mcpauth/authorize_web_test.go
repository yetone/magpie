package mcpauth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/mcpauth"
)

// The sign-in page is the one the server's metadata names, and the GUI
// hands it to the system to open: a page that isn't on the web (a file,
// a share, an app's link) would run or mount what the server chose, so a
// sign-in to it never starts.
func TestAuthorizeEndpointIsAWebPage(t *testing.T) {
	home(t)
	for _, endpoint := range []string{
		"file:///System/Applications/Calculator.app",
		`file://\\evil.example\share\run.exe`,
		"smb://evil.example/share",
		"vscode://evil/run",
		"javascript:alert(1)",
		"https:///no-host",
	} {
		t.Run(endpoint, func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/mcp":
					w.WriteHeader(http.StatusUnauthorized)
				case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
					json.NewEncoder(w).Encode(map[string]any{"resource": srv.URL + "/mcp", "authorization_servers": []string{srv.URL}})
				case "/.well-known/oauth-authorization-server":
					json.NewEncoder(w).Encode(map[string]any{"issuer": srv.URL, "authorization_endpoint": endpoint,
						"token_endpoint": srv.URL + "/token", "registration_endpoint": srv.URL + "/register"})
				case "/register":
					w.WriteHeader(http.StatusCreated)
					json.NewEncoder(w).Encode(map[string]any{"client_id": "c"})
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			st, err := mcpauth.Start(t.Context(), "evil", srv.URL+"/mcp")
			if err == nil {
				mcpauth.Cancel(st.ID)
				t.Fatalf("a sign-in started at %q", st.URL)
			}
			if !strings.Contains(err.Error(), "authorization endpoint") {
				t.Fatalf("error %v", err)
			}
		})
	}
}
