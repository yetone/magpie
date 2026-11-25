package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// only=said on both export routes (usrname1833 on Discord) keeps what the
// user and the agent said and leaves thinking and tools out, under a file
// name of its own; without it the export is whole, as before.
func TestSessionExportOnlySaid(t *testing.T) {
	h := sandboxHome(t)
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	if err := os.MkdirAll(filepath.Join(h, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	sessionManageRoutes(mux, folderOnly{})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/sessions/recording", strings.NewReader(`{"on":true}`)))
	if w.Code != 200 || !settings.Load().GatewayConversations {
		t.Fatalf("consent: %d %s", w.Code, w.Body)
	}
	id := "019a2b3c-pi-said"
	usage.Append(usage.Record{Time: time.Now(), Agent: "pi", Session: id, Provider: "fake", Model: "m", Status: 200})
	if err := sessions.SaveGatewayTurn(sessions.GatewayTurn{Agent: "pi", Session: id, Time: time.Now(),
		Input: []sessions.Part{{Role: "user", Kind: "text", Text: "rename foo to bar"}},
		Output: []sessions.Part{
			{Role: "assistant", Kind: "thinking", Text: "SECRET-THOUGHT"},
			{Role: "assistant", Kind: "tool_use", Name: "bash", Text: `{"command":"TOOL-ARG"}`},
			{Role: "assistant", Kind: "text", Text: "Renamed it in a.go."},
		}}); err != nil {
		t.Fatal(err)
	}
	said := func(md string) {
		t.Helper()
		if !strings.Contains(md, "rename foo to bar") || !strings.Contains(md, "Renamed it in a.go.") {
			t.Fatalf("lost what was said:\n%s", md)
		}
		if strings.Contains(md, "SECRET-THOUGHT") || strings.Contains(md, "TOOL-ARG") {
			t.Fatalf("kept the work:\n%s", md)
		}
	}
	q := "?agent=pi&id=" + id
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/markdown"+q+"&only=said", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), `filename="magpie-session-pi-019a2b3c-pi--messages.md"`) {
		t.Fatalf("download: %d %v %s", w.Code, w.Header(), w.Body)
	}
	said(w.Body.String())

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/sessions/export"+q+"&only=said", strings.NewReader("{}")))
	var out struct{ Path string }
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || !strings.HasSuffix(out.Path, "-messages.md") {
		t.Fatalf("export: %d %s", w.Code, w.Body)
	}
	b, err := os.ReadFile(filepath.Join(h, "Downloads", filepath.Base(out.Path)))
	if err != nil {
		t.Fatal(err)
	}
	said(string(b))

	// without only, and with one it doesn't know, the export is whole
	for _, only := range []string{"", "&only=bogus"} {
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/markdown"+q+only, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "SECRET-THOUGHT") || !strings.Contains(w.Body.String(), "TOOL-ARG") ||
			strings.Contains(w.Header().Get("Content-Disposition"), "-messages") {
			t.Fatalf("whole%q: %d %v\n%s", only, w.Code, w.Header(), w.Body)
		}
	}
}
