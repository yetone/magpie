package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
	"github.com/yetone/magpie/internal/provider"
)

// keyHeldToMCP is a gateway key (and its secret) held to the MCP servers
// and tools in mcp.
func keyHeldToMCP(t *testing.T, name string, mcp ...string) string {
	t.Helper()
	keys, secrets := newCaller(t, name)
	if _, err := access.Update("mcp-key", access.Change{Key: keys[0].ID, MCP: mcp}); err != nil {
		t.Fatal(err)
	}
	return secrets[0]
}

// A key held to some MCP servers and tools (wakaka on Discord) is held on
// magpie's own servers: a server it isn't given answers 403, a server it
// is given some tools of lists those alone and refuses a call of another
// before it runs, and OwnMCPPath leaves the others out. A key held to
// nothing, and a request with no key, see every tool as before.
func TestKeyMCPOwnServer(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	apis := newSearchAPIs(t)
	if err := provider.SetSearchAPI(apis.api("tavily", "tvly-k")); err != nil {
		t.Fatal(err)
	}
	s := New()
	h := lanGuard(s.Handler())
	fetchOnly := keyHeldToMCP(t, "Fetch", "magpie-web-search/web_fetch")
	other := keyHeldToMCP(t, "Other", "github")
	free := keyHeldToMCP(t, "Free")
	auth := func(secret string) []string { return []string{"Authorization", "Bearer " + secret} }

	if got := listedNames(t, readMCP(t, mcpPost(h, here, toolsList, auth(fetchOnly)...))); !slices.Equal(got, []string{fetchMCPTool}) {
		t.Fatalf("a key given web_fetch alone lists %v", got)
	}
	m := readMCP(t, mcpPost(h, here, mcpSearchCall, auth(fetchOnly)...))
	if m.Error == nil || m.Error.Code != -32602 || !strings.Contains(m.Error.Message, `"Fetch" may not use the tool web_search`) {
		t.Fatalf("a call of web_search: %+v", m)
	}
	// in a batch, the refused call is refused alone
	w := mcpPost(h, here, `[`+toolsList+`,`+mcpSearchCall+`]`, auth(fetchOnly)...)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"web_fetch"`) || strings.Contains(w.Body.String(), `"name":"web_search"`) || !strings.Contains(w.Body.String(), "may not use the tool web_search") {
		t.Fatalf("a batch: %d %s", w.Code, w.Body)
	}
	if w := mcpPost(h, here, mcpSearchCall, auth(other)...); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "may not use the MCP server magpie-web-search") {
		t.Fatalf("a key not given the server: %d %s", w.Code, w.Body)
	}
	if len(apis.asked) != 0 {
		t.Fatalf("searched for a refused call: %q", apis.asked)
	}
	for _, hdr := range [][]string{auth(free), nil} {
		if got := listedNames(t, readMCP(t, mcpPost(h, here, toolsList, hdr...))); !slices.Equal(got, []string{searchMCPTool, fetchMCPTool}) {
			t.Fatalf("%v lists %v", hdr, got)
		}
	}
	if m := readMCP(t, mcpPost(h, here, mcpSearchCall, auth(free)...)); m.Error != nil || m.Result.IsError {
		t.Fatalf("a key held to nothing: %+v", m)
	}

	own := func(hdr ...string) []string {
		r := httptest.NewRequest("GET", OwnMCPPath, nil)
		r.RemoteAddr = here
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var got struct{ Servers []OwnMCP }
		json.Unmarshal(w.Body.Bytes(), &got)
		var names []string
		for _, o := range got.Servers {
			names = append(names, o.Name)
		}
		return names
	}
	if got := own(auth(other)...); slices.Contains(got, "magpie-web-search") {
		t.Errorf("a key not given web search is told of it: %v", got)
	}
	if got := own(auth(fetchOnly)...); !slices.Contains(got, "magpie-web-search") {
		t.Errorf("a key given web_fetch isn't told of its server: %v", got)
	}
	if got := own(); !slices.Contains(got, "magpie-web-search") {
		t.Errorf("no key: %v", got)
	}

	r := httptest.NewRequest("GET", "/v1/magpie/limit", nil)
	r.RemoteAddr = here
	r.Header.Set("Authorization", "Bearer "+fetchOnly)
	lw := httptest.NewRecorder()
	h.ServeHTTP(lw, r)
	if !strings.Contains(lw.Body.String(), `"mcp":["magpie-web-search/web_fetch"]`) {
		t.Errorf("the key isn't told its MCP list: %s", lw.Body)
	}
}

// Through the hub, a key is given the tools of the Library's servers it
// may use alone: listed, named in the instructions, found and run on
// demand, and by a name the hub asks the server for (hubToolNamed's
// fallback); any other is unknown and never runs.
func TestKeyMCPHub(t *testing.T) {
	fresh(t)
	tools := []HubTool{
		{Server: "github", Tool: "create_issue", Spec: hubSpec("create_issue")},
		{Server: "github", Tool: "delete_repo", Spec: hubSpec("delete_repo")},
		{Server: "fs", Tool: "read", Spec: hubSpec("read")},
		{Server: "linear", Tool: "list_issues", Spec: hubSpec("list_issues")},
	}
	servers := []HubServer{
		{Name: "github", Known: true, Tools: []HubToolNote{{Name: "create_issue", About: "Opens an issue"}, {Name: "delete_repo", About: "Deletes a repository"}}},
		{Name: "fs", Known: true, Tools: []HubToolNote{{Name: "read", About: "Reads a file"}}},
		{Name: "linear", Known: true, About: "Linear's issues", Tools: []HubToolNote{{Name: "list_issues", About: "Lists issues"}}},
	}
	f := &fakeHub{on: true, tools: map[string][]HubTool{"claude": tools}, servers: map[string][]HubServer{"claude": servers}}
	withHub(t, f)
	h := lanGuard(New().Handler())
	held := keyHeldToMCP(t, "Held", "github/create_issue", "fs")
	key := []string{"Authorization", "Bearer " + held}

	if got := listedNames(t, readMCP(t, hubPost(h, "claude", here, toolsList, key...))); !slices.Equal(got, []string{"github__create_issue", "fs__read"}) {
		t.Fatalf("listed %v", got)
	}
	say := initInstructions(t, hubPost(h, "claude", here, hubInit, key...).Body.String())
	if !strings.Contains(say, "create_issue") || strings.Contains(say, "delete_repo") || strings.Contains(say, "linear") {
		t.Fatalf("instructions: %s", say)
	}
	for _, name := range []string{"github__delete_repo", "linear__list_issues", "linear__other", "github__other"} {
		m := readMCP(t, hubPost(h, "claude", here, toolsCall(name, `{}`), key...))
		if m.Error == nil || !strings.Contains(m.Error.Message, "unknown tool "+name) {
			t.Errorf("%s: %+v", name, m)
		}
	}
	// a server given whole: a tool it no longer lists is still asked for
	if m := readMCP(t, hubPost(h, "claude", here, toolsCall("fs__write", `{}`), key...)); m.Error != nil || m.Result.Content[0].Text != "ran fs write" {
		t.Fatalf("fs__write: %+v", m)
	}
	if m := readMCP(t, hubPost(h, "claude", here, toolsCall("github__create_issue", `{}`), key...)); m.Error != nil || m.Result.IsError {
		t.Fatalf("github__create_issue: %+v", m)
	}

	// on demand: find_tools and call_tool are held the same way
	srv := New()
	r := httptest.NewRequest("POST", HubMCPPath+"claude", nil)
	who, _ := access.Authenticate(held)
	r = r.WithContext(access.WithIdentity(t.Context(), who))
	ts := hubTools(r.Context(), "claude")
	if res, _ := json.Marshal(srv.hubCallByName(r, "claude", ts, json.RawMessage(`{"name":"delete_repo"}`), hubCallMeta{})); !strings.Contains(string(res), `"isError":true`) {
		t.Fatalf("call_tool delete_repo: %s", res)
	}
	if found := hubMatches(ts, "issue"); slices.ContainsFunc(found, func(t hubTool) bool { return t.Server == "linear" || t.Tool == "delete_repo" }) {
		t.Fatalf("find_tools found %v", found)
	}
	// every hub call is held where it runs, however it was found
	res, _ := json.Marshal(srv.hubMCPRun(r, "claude", hubTool{HubTool: HubTool{Server: "github", Tool: "delete_repo"}, name: "github__delete_repo"}, nil))
	if !strings.Contains(string(res), "may not use the tool delete_repo") {
		t.Fatalf("hubMCPRun: %s", res)
	}
	want := []string{"claude fs write {}", "claude github create_issue {}"}
	if got := f.ran(); !slices.Equal(got, want) {
		t.Fatalf("ran %q, want %q", got, want)
	}
	// a request with no key sees every tool
	if got := listedNames(t, readMCP(t, hubPost(h, "claude", here, toolsList))); len(got) != 4 {
		t.Fatalf("no key: %v", got)
	}
}

// /mcp/<server>, a server magpie signed in to: a key not given it is
// refused before the server is asked, a key given some of its tools has
// a call of another answered with an error and never sent, and a batch
// holding one refused whole.
func TestKeyMCPProxy(t *testing.T) {
	fresh(t)
	f := mcpauthtest.New(t)
	f.SignIn(t, "neon")
	h := lanGuard(New().Handler())
	call := func(secret, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/mcp/neon", strings.NewReader(body))
		r.RemoteAddr, r.Host = here, "127.0.0.1:3425"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	other := keyHeldToMCP(t, "Other", "github")
	some := keyHeldToMCP(t, "Some", "neon/run_sql")
	if w := call(other, toolsList); w.Code != http.StatusForbidden || len(f.Calls) != 0 {
		t.Fatalf("a key not given neon: %d %s, calls %v", w.Code, w.Body, f.Calls)
	}
	m := readMCP(t, call(some, toolsCall("delete_branch", `{}`)))
	if m.Error == nil || m.Error.Code != -32602 || !strings.Contains(m.Error.Message, "may not use the tool delete_branch") || len(f.Calls) != 0 {
		t.Fatalf("a call of another tool: %+v, calls %v", m, f.Calls)
	}
	if w := call(some, `[`+toolsCall("run_sql", `{}`)+`,`+toolsCall("delete_branch", `{}`)+`]`); w.Code != http.StatusForbidden || len(f.Calls) != 0 {
		t.Fatalf("a batch: %d %s, calls %v", w.Code, w.Body, f.Calls)
	}
	if w := call(some, `{"jsonrpc":`); w.Code != http.StatusBadRequest || len(f.Calls) != 0 {
		t.Fatalf("a body that isn't one: %d %s, calls %v", w.Code, w.Body, f.Calls)
	}
	// its own tool goes through, its event stream with it
	w := call(some, toolsCall("run_sql", `{}`))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "notifications/progress") || !strings.Contains(w.Body.String(), `"id":3,"result"`) {
		t.Fatalf("run_sql: %d %s", w.Code, w.Body)
	}
	if !slices.Equal(f.Calls, []string{"POST tools/call "}) {
		t.Fatalf("calls %q", f.Calls)
	}
}

// /mcp/run/<server>, a command magpie runs: a key given some of its tools
// is listed those alone, in a JSON answer or an event stream however it
// is cut into writes, and a call of another never reaches the server. A
// key given the server whole, and one held to nothing, get its answers as
// they are.
func TestKeyMCPRun(t *testing.T) {
	fresh(t)
	const list = `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"read","inputSchema":{"type":"object"}},{"name":"write","inputSchema":{"type":"object"}}]}}`
	var got []string
	was := RunMCP
	RunMCP = func(w http.ResponseWriter, r *http.Request, name string) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, name+" "+string(b))
		if r.Header.Get("X-Stream") == "" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "999")
			io.WriteString(w, list[:20])
			io.WriteString(w, list[20:])
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		ev := "event: message\nid: 7\ndata: " + list + "\n\n"
		note := "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"
		for _, part := range []string{note[:9], note[9:] + ev[:30], ev[30:70], ev[70:]} {
			io.WriteString(w, part)
			w.(http.Flusher).Flush()
		}
	}
	t.Cleanup(func() { RunMCP = was })
	h := lanGuard(New().Handler())
	call := func(secret, body string, stream bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/mcp/run/fs", strings.NewReader(body))
		r.RemoteAddr, r.Host = here, "127.0.0.1:3425"
		r.Header.Set("Content-Type", "application/json")
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		if stream {
			r.Header.Set("X-Stream", "1")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	some := keyHeldToMCP(t, "Some", "fs/read")
	if names := listedNames(t, readMCP(t, call(some, toolsList, false))); !slices.Equal(names, []string{"read"}) {
		t.Fatalf("JSON lists %v", names)
	}
	w := call(some, toolsList, true)
	body := w.Body.String()
	if !strings.Contains(body, `"name":"read"`) || strings.Contains(body, `"name":"write"`) || !strings.Contains(body, "notifications/progress") || !strings.Contains(body, "id: 7\n") || !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("the stream: %q", body)
	}
	m := readMCP(t, call(some, toolsCall("write", `{"path":"/etc/hosts"}`), false))
	if m.Error == nil || !strings.Contains(m.Error.Message, "may not use the tool write") {
		t.Fatalf("a call of write: %+v", m)
	}
	// the body is passed on as magpie read it (strictMCPBody)
	if len(got) != 2 || got[0] != `fs {"id":2,"jsonrpc":"2.0","method":"tools/list"}` {
		t.Fatalf("the server got %q", got)
	}
	if w := call(keyHeldToMCP(t, "Other", "github"), toolsList, false); w.Code != http.StatusForbidden || len(got) != 2 {
		t.Fatalf("a key not given fs: %d %s", w.Code, w.Body)
	}
	for _, secret := range []string{keyHeldToMCP(t, "Whole", "fs"), keyHeldToMCP(t, "Free"), ""} {
		if names := listedNames(t, readMCP(t, call(secret, toolsList, false))); len(names) != 2 {
			t.Fatalf("%q lists %v", secret, names)
		}
		if w := call(secret, toolsList, true); !strings.Contains(w.Body.String(), `"name":"write"`) {
			t.Fatalf("%q's stream: %s", secret, w.Body)
		}
	}
}

// An answer magpie can't read (an encoding it didn't ask for) isn't passed
// to a key held to some tools: the tools it lists couldn't be cut.
func TestKeyMCPFilterRefusesUnreadable(t *testing.T) {
	rec := httptest.NewRecorder()
	f := &mcpToolsFilter{w: rec, keep: func(string) bool { return false }}
	f.Header().Set("Content-Type", "application/json")
	f.Header().Set("Content-Encoding", "zstd")
	f.WriteHeader(200)
	f.Write([]byte(`{"result":{"tools":[{"name":"x"}]}}`))
	f.finish()
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), `"x"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// A relayed server reads the body magpie passes on, keys as written: a
// call of a tool the key may not use doesn't get past mcpKeyGuard by a key
// Go's decoder reads in another case (NAME, METHOD, Params, paramſ), and a
// key given twice reaches the server once, as magpie checked it.
func TestKeyMCPRelayReadsOneWay(t *testing.T) {
	fresh(t)
	var got []string
	was := RunMCP
	RunMCP = func(w http.ResponseWriter, r *http.Request, name string) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, string(b))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"jsonrpc":"2.0","id":3,"result":{}}`)
	}
	t.Cleanup(func() { RunMCP = was })
	h := lanGuard(New().Handler())
	some := keyHeldToMCP(t, "Some", "fs/read")
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/mcp/run/fs", strings.NewReader(body))
		r.RemoteAddr, r.Host = here, "127.0.0.1:3425"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+some)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"write","NAME":"read"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"write","nAme":"read","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","METHOD":"ping","params":{"name":"write"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"write"},"Params":{"name":"read"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"write"},"paramſ":{"name":"read"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"write","Arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":["write"]}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":7}}`,
		`[{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"write","NAME":"read"}}]`,
		`[]`,
		`null`,
	} {
		if w := call(body); w.Code == 200 && !strings.Contains(w.Body.String(), "may not use the tool") || len(got) != 0 {
			t.Fatalf("%s: %d %s, the server got %q", body, w.Code, w.Body, got)
		}
	}
	// a key given twice: the server is sent the one magpie read
	for _, c := range []struct{ body, want string }{
		{`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"write","name":"read"}}`, `{"id":3,"jsonrpc":"2.0","method":"tools/call","params":{"name":"read"}}`},
		{`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"write"},"method":"tools/list"}`, `{"id":3,"jsonrpc":"2.0","method":"tools/list","params":{"name":"write"}}`},
		{`[{"jsonrpc":"2.0","id":"a","method":"tools/call","params":{"name":"write","name":"read","arguments":{"p":1}}}]`, `[{"id":"a","jsonrpc":"2.0","method":"tools/call","params":{"arguments":{"p":1},"name":"read"}}]`},
	} {
		got = nil
		if w := call(c.body); w.Code != 200 || len(got) != 1 || got[0] != c.want {
			t.Fatalf("%s: %d %s, the server got %q", c.body, w.Code, w.Body, got)
		}
	}
}
