package library

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// routerSays is what an agent connecting to this magpie's router at
// agent's address is told in initialize: its instructions and title.
func routerSays(t *testing.T, agent string) (instructions, title string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, gateway.HubMCPPath+agent, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"2.1.0"}}}`))
	r.RemoteAddr, r.Host = "127.0.0.1:50000", "127.0.0.1:3425"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()
	gateway.New().Handler().ServeHTTP(w, r)
	var m struct {
		Result struct {
			Instructions string
			ServerInfo   struct{ Title string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("initialize: %v: %s", err, w.Body)
	}
	return m.Result.Instructions, m.Result.ServerInfo.Title
}

// otherRouter is another magpie's router as this one's Library reaches it
// (the reporter's magpies that only share MCP servers): its initialize
// result as a magpie router gives it, title and instructions, and two
// tools named and described as that router names them.
func otherRouter(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(204)
			return
		}
		var m struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		w.Header().Set("Content-Type", "application/json")
		switch m.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "la-1")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"magpie","title":"Magpie MCP hub · Los Angeles","version":"0.1.2300"},"instructions":"Tools of fetch (Fetch a web page and hand it over as Markdown), time.\nThis router: Los Angeles. Each of its tools' descriptions starts with [Los Angeles]; when another MCP server gives the same tools, choose between them by this.\n- fetch: Fetch a web page and hand it over as Markdown.\nThe tools of every MCP server in magpie's Library, each named <server>__<tool>. Add servers in magpie's Library → MCP."}}`, *m.ID)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"fetch__fetch","description":"[Los Angeles] Fetches a URL from the internet and optionally extracts its contents as markdown.","inputSchema":{"type":"object"}},{"name":"time__get_current_time","description":"[Los Angeles] Get current time in a specific timezones","inputSchema":{"type":"object"}}]}}`, *m.ID)
		default:
			w.WriteHeader(202)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// notesTools is a command server's tools/list reply: three tools, one
// turned off in the Library, descriptions of several sentences.
func notesTools(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "tools.json")
	write(t, f, `{"jsonrpc":"2.0","id":0,"result":{"tools":[
		{"name":"add_note","description":"Add a note to the notebook. The note is saved at once; pass tags to file it.","inputSchema":{"type":"object"}},
		{"name":"search_notes","description":"Search the notebook by text, newest first.\n\nReturns at most 20 notes.","inputSchema":{"type":"object"}},
		{"name":"drop_all","description":"Delete every note.","inputSchema":{"type":"object"}}]}}`)
	return f
}

// An agent given several routers (packinggoodsllc_07668 on Discord: magpies
// that only share MCP servers) knew each by its name alone. A router's
// initialize now says what each server behind it is and does: its own
// title and words when the market doesn't know it (another magpie's
// router says where it is), and its tools, each with the first clause of
// its description, but those turned off.
func TestHubInstructionsDescribeServers(t *testing.T) {
	hubSandbox(t)
	la := otherRouter(t)
	pids := filepath.Join(t.TempDir(), "pids")
	notes := fakeStdio(t, "ok", map[string]string{"MAGPIE_FAKE_MCP_TOOLS": notesTools(t), "MAGPIE_FAKE_MCP_LOG": pids})
	notes.Name, notes.Agents, notes.DisabledTools = "notes", []string{"claude"}, []string{"drop_all"}
	ok(t)(SaveServer("", *notes))
	ok(t)(SaveServer("", Server{Name: "la", Transport: "http", URL: la.URL, Agents: []string{"claude"}}))
	ok(t)(SetMCPHub(true))

	say, _ := routerSays(t, "claude")
	for _, want := range []string{
		"- la (Magpie MCP hub · Los Angeles): Tools of fetch (Fetch a web page and hand it over as Markdown), time. Tools: fetch__fetch (Fetches a URL from the internet and optionally extracts its contents as markdown), time__get_current_time (Get current time in a specific timezones).\n",
		"- notes: Tools: add_note (Add a note to the notebook), search_notes (Search the notebook by text).\n",
	} {
		if !strings.Contains(say, want) {
			t.Errorf("the instructions don't say\n%s\nthey are:\n%s", want, say)
		}
	}
	if strings.Contains(say, "drop_all") {
		t.Errorf("a tool turned off is named:\n%s", say)
	}
	if n := len([]rune(say)); n > 2000 {
		t.Errorf("the instructions are %d characters", n)
	}
}

// What the router learnt of its servers is kept: started again, it
// describes them before it has run any (no server is started to say what
// it is); a server edited since is described once it is listed again.
func TestHubDescriptionsOutliveARestart(t *testing.T) {
	hubSandbox(t)
	la := otherRouter(t)
	pids := filepath.Join(t.TempDir(), "pids")
	notes := fakeStdio(t, "ok", map[string]string{"MAGPIE_FAKE_MCP_TOOLS": notesTools(t), "MAGPIE_FAKE_MCP_LOG": pids})
	notes.Name, notes.Agents, notes.DisabledTools = "notes", []string{"claude"}, []string{"drop_all"}
	ok(t)(SaveServer("", *notes))
	ok(t)(SaveServer("", Server{Name: "la", Transport: "http", URL: la.URL, Agents: []string{"claude"}}))
	ok(t)(SetMCPHub(true))
	say, _ := routerSays(t, "claude")
	if !strings.Contains(say, "- notes: Tools: add_note") {
		t.Fatalf("notes isn't described:\n%s", say)
	}
	b, _ := os.ReadFile(pids)
	started := strings.Count(string(b), "\n")

	// a router started again describes them before it runs any server
	hubStopAll()
	cards.Lock()
	cards.m = nil
	cards.Unlock()
	if again, _ := routerSays(t, "claude"); again != say {
		t.Errorf("started again, the instructions are\n%s\nwant\n%s", again, say)
	}
	if b, _ := os.ReadFile(pids); strings.Count(string(b), "\n") != started {
		t.Errorf("describing the servers again started notes: %s", b)
	}

	// a server edited is another: its tools aren't said until listed
	// again, which initialize waits for
	notes.Env["MAGPIE_FAKE_MCP_EXTRA"] = "1"
	ok(t)(SaveServer("notes", *notes))
	if s := hubServerOf(hubServers("claude")[1]); s.Name != "notes" || s.Known {
		t.Fatalf("edited, notes is still known: %+v", s)
	}
	if again, _ := routerSays(t, "claude"); !strings.Contains(again, "- notes: Tools: add_note") {
		t.Errorf("edited, notes isn't described:\n%s", again)
	}
}
