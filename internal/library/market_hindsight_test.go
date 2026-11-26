package library

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeHindsight is Hindsight's MCP endpoint as hindsight-api's
// MCPMiddleware serves it (vectorize-io/hindsight 47831d5c,
// hindsight_api/api/mcp.py): under /mcp, a GET without a session answered
// 200 and empty, the key, when Hindsight asks for one, taken from
// Authorization with or without "Bearer " and a 401 {"error": …} without
// it, and the bank from the path, else X-Bank-Id, else "default". FastMCP's
// stateful Streamable HTTP answers on an event stream, in a session from
// initialize. Each call answers with the tool and the bank it ran in.
type fakeHindsight struct {
	key   string
	mu    sync.Mutex
	banks []string // the bank each tools/call ran in
}

func (f *fakeHindsight) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mcp" && !strings.HasPrefix(r.URL.Path, "/mcp/") {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet && r.Header.Get("Mcp-Session-Id") == "" {
		w.Header().Set("Content-Type", "application/json")
		return
	}
	if f.key != "" {
		a := r.Header.Get("Authorization")
		if a == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			io.WriteString(w, `{"error": "Authorization header required"}`)
			return
		}
		if strings.TrimSpace(strings.TrimPrefix(a, "Bearer ")) != f.key {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			io.WriteString(w, `{"error": "Invalid authentication token"}`)
			return
		}
	}
	bank := strings.Trim(strings.TrimPrefix(r.URL.Path, "/mcp"), "/")
	if bank == "" {
		bank = r.Header.Get("X-Bank-Id")
	}
	if bank == "" {
		bank = "default"
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(200)
		return
	}
	var m struct {
		ID     *int   `json:"id"`
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	json.NewDecoder(r.Body).Decode(&m)
	reply := ""
	switch m.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "hs-1")
		reply = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"hindsight-mcp-server","version":"1.0"}}}`, *m.ID)
	case "notifications/initialized":
		w.WriteHeader(202)
		return
	case "tools/list":
		reply = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"retain","inputSchema":{"type":"object"}},{"name":"recall","inputSchema":{"type":"object"}},{"name":"reflect","inputSchema":{"type":"object"}},{"name":"list_banks","inputSchema":{"type":"object"}}]}}`, *m.ID)
	case "tools/call":
		f.mu.Lock()
		f.banks = append(f.banks, bank)
		f.mu.Unlock()
		reply = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"%s in %s"}]}}`, *m.ID, m.Params.Name, bank)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", reply)
}

// Hindsight is offered as a featured memory server, added in one step at
// its default address with the bank as X-Bank-Id and its key, when it asks
// for one, as a bearer token; the router's calls reach the bank picked
// (zysicyj on X).
func TestHindsightServer(t *testing.T) {
	hubSandbox(t)
	m, found := marketServer("hindsight")
	if !found || !m.Featured || m.Transport != "http" || m.server.URL != "http://localhost:8888/mcp" || m.Homepage == "" {
		t.Fatalf("hindsight in the market: %v %+v", found, m)
	}
	for _, in := range m.Inputs {
		if in.Required {
			t.Errorf("%s is required: Hindsight runs without a key and with its default bank", in.Key)
		}
	}
	if !strings.Contains(m.Description, "HINDSIGHT_API_LLM_BASE_URL") {
		t.Errorf("the description doesn't say its LLM can be magpie's gateway: %q", m.Description)
	}

	// nothing given: the default bank, no key
	ok(t)(InstallServer("hindsight", nil, []string{"claude"}))
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if s := l.server("hindsight"); s == nil || s.URL != "http://localhost:8888/mcp" || len(s.Headers) != 0 {
		t.Fatalf("added with nothing given as %+v", s)
	}
	ok(t)(RemoveServer("hindsight"))

	ok(t)(InstallServer("hindsight", map[string]string{"X-Bank-Id": " team ", "Authorization": "hs-secret"}, []string{"claude"}))
	if l, err = load(); err != nil {
		t.Fatal(err)
	}
	s := l.server("hindsight")
	if s == nil || s.Transport != "http" || s.URL != "http://localhost:8888/mcp" ||
		len(s.Headers) != 2 || s.Headers["X-Bank-Id"] != "team" || s.Headers["Authorization"] != "Bearer hs-secret" {
		t.Fatalf("added as %+v", s)
	}

	// the server as added, at a Hindsight that asks for that key
	f := &fakeHindsight{key: "hs-secret"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	at := *s
	at.URL = srv.URL + "/mcp"
	if h := CheckServer(context.Background(), &at); h.State != "ok" || h.Tools != 4 {
		t.Fatalf("check: %+v", h)
	}
	ok(t)(SaveServer("hindsight", at))
	ok(t)(SetMCPHub(true))
	res, err := theHub{}.Call(context.Background(), "claude", "hindsight", "recall", json.RawMessage(`{"query":"stack"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := hubText(t, res); got != "recall in team" {
		t.Fatalf("the router's call answered %q, want recall in team", got)
	}

	// without the key it was added with, Hindsight turns it away
	bare := at
	bare.Headers = map[string]string{"X-Bank-Id": "team"}
	if h := CheckServer(context.Background(), &bare); h.State != "auth" || h.Code != 401 {
		t.Fatalf("check without the key: %+v", h)
	}
}
