package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// searchingRelay is a fake relay of OpenAI's API that answers Codex's
// web.run at /v1/alpha/search and a turn at /v1/responses, and records
// what reached it.
type searchingRelay struct {
	*httptest.Server
	mu     sync.Mutex
	paths  []string
	bodies []map[string]any
	heads  []http.Header
	status int
}

func newSearchingRelay(t *testing.T) *searchingRelay {
	t.Helper()
	f := &searchingRelay{status: 200}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var q map[string]any
		json.Unmarshal(b, &q)
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.bodies = append(f.bodies, q)
		f.heads = append(f.heads, r.Header.Clone())
		status := f.status
		f.mu.Unlock()
		switch r.URL.Path {
		case "/v1/alpha/search":
			if status != 200 {
				http.Error(w, `{"error":"no search here"}`, status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"encrypted_output":"relay-cipher","output":"OpenAI news from the relay","results":[{"type":"text_result","ref_id":"turn0search0","url":"https://example.com/r"}]}`)
		case "/v1/responses":
			if q["stream"] == true {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-5.5\",\"output\":[]}}\n\n"+
					"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Go 1.27.1 is out.\",\"annotations\":[]}]}}\n\n"+
					"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Go 1.27.1 is out.\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":5,\"total_tokens\":10}}}\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"go"}},{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Go 1.27.1 is out.","annotations":[]}]}],"usage":{"input_tokens":5,"output_tokens":5,"total_tokens":10}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *searchingRelay) reached() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *searchingRelay) reset(status int) {
	f.mu.Lock()
	f.paths, f.bodies, f.heads, f.status = nil, nil, nil, status
	f.mu.Unlock()
}

// A relay of OpenAI's API that runs Codex's web.run itself, set to search
// the web by itself, is sent web.run as Codex sent it: magpie's searcher
// was asked instead, and the relay's own search never used (YIXIE233 on
// X). Not set so, it is searched by magpie as before; set so and failing,
// magpie searches in its place and says why.
func TestCodexWebRunGoesToRelayThatSearches(t *testing.T) {
	fresh(t)
	relay := newSearchingRelay(t)
	apis := newSearchAPIs(t)
	if err := provider.SetSearchAPI(apis.api("tavily", "tvly-k")); err != nil {
		t.Fatal(err)
	}
	save := func(searches bool) {
		t.Helper()
		p := provider.Provider{ID: "relay", Name: "Relay", Key: "relay-k", Responses: relay.URL + "/v1", Models: []string{"gpt-5.5"}, Searches: searches}
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	h := New().Handler()

	// off: magpie's search, as before; the relay is asked nothing
	save(false)
	got := readSearchReply(t, codexSearchPost(h, "/v1/alpha/search", codexSearchBody("relay/gpt-5.5")))
	if !strings.Contains(got.Output, "go1.27.1") || len(relay.reached()) != 0 || len(apis.asked) != 1 {
		t.Fatalf("off: %+v, relay %v, apis %v", got, relay.reached(), apis.asked)
	}

	// on: the relay's own search, the request as it came, its answer as it is
	save(true)
	for _, path := range []string{"/v1/alpha/search", CodexPath + "/alpha/search"} {
		relay.reset(200)
		apis.asked = nil
		got = readSearchReply(t, codexSearchPost(h, path, codexSearchBody("relay/gpt-5.5")))
		if got.Output != "OpenAI news from the relay" || got.EncryptedOutput == nil || *got.EncryptedOutput != "relay-cipher" || len(got.Results) != 1 {
			t.Fatalf("%s: answered %+v", path, got)
		}
		if len(apis.asked) != 0 {
			t.Fatalf("%s: magpie searched too: %v", path, apis.asked)
		}
		if p := relay.reached(); len(p) != 1 || p[0] != "/v1/alpha/search" {
			t.Fatalf("%s: relay reached %v", path, p)
		}
		q, hd := relay.bodies[0], relay.heads[0]
		cmds, _ := q["commands"].(map[string]any)
		if q["model"] != "gpt-5.5" || q["id"] != "search-session" || cmds["open"] == nil || cmds["search_query"] == nil || q["settings"] == nil || q["max_output_tokens"] != float64(2500) {
			t.Fatalf("%s: body %v", path, q)
		}
		if hd.Get("Authorization") != "Bearer relay-k" || hd.Get("x-openai-actor-authorization") != "" ||
			hd.Get("x-codex-turn-metadata") != `{"turn_id":"t1"}` || hd.Get("originator") != "codex_exec" {
			t.Fatalf("%s: headers %v", path, hd)
		}
	}

	// on, and the relay can't: magpie searches in its place, never ending the turn
	relay.reset(404)
	apis.asked = nil
	got = readSearchReply(t, codexSearchPost(h, "/v1/alpha/search", codexSearchBody("relay/gpt-5.5")))
	if !strings.Contains(got.Output, "Relay's web search failed") || !strings.Contains(got.Output, "404") ||
		!strings.Contains(got.Output, "go1.27.1") || len(apis.asked) != 1 {
		t.Fatalf("relay failing: %+v, apis %v", got, apis.asked)
	}
}

// In a routing group the switch is the member's that a turn goes to: a
// group led by a relay that searches sends web.run to it, one led by a
// provider that doesn't is searched by magpie, though a member after it
// searches.
func TestCodexWebRunInGroupFollowsLeadingMember(t *testing.T) {
	fresh(t)
	relay := newSearchingRelay(t)
	other := newSearchingRelay(t)
	apis := newSearchAPIs(t)
	if err := provider.SetSearchAPI(apis.api("tavily", "tvly-k")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "relay-k", Responses: relay.URL + "/v1", Models: []string{"gpt-5.5"}, Searches: true},
		{ID: "plain", Name: "Plain", Key: "plain-k", Responses: other.URL + "/v1", Models: []string{"gpt-5.5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	h := New().Handler()
	group := func(members ...string) {
		t.Helper()
		if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: members, Routing: provider.Ordered}); err != nil {
			t.Fatal(err)
		}
	}

	group("relay/gpt-5.5", "plain/gpt-5.5")
	got := readSearchReply(t, codexSearchPost(h, "/v1/alpha/search", codexSearchBody("group/g")))
	if got.Output != "OpenAI news from the relay" || len(apis.asked) != 0 || len(other.reached()) != 0 {
		t.Fatalf("relay first: %+v, apis %v, plain %v", got, apis.asked, other.reached())
	}
	if relay.bodies[0]["model"] != "gpt-5.5" {
		t.Fatalf("relay asked for %v", relay.bodies[0]["model"])
	}

	relay.reset(200)
	group("plain/gpt-5.5", "relay/gpt-5.5")
	got = readSearchReply(t, codexSearchPost(h, "/v1/alpha/search", codexSearchBody("group/g")))
	if !strings.Contains(got.Output, "go1.27.1") || len(apis.asked) != 1 || len(relay.reached()) != 0 || len(other.reached()) != 0 {
		t.Fatalf("plain first: %+v, apis %v, relay %v, plain %v", got, apis.asked, relay.reached(), other.reached())
	}
}

// Codex's web_search tool on a turn goes to a relay set to search by
// itself as Codex sent it, and magpie's searcher isn't asked; not set so,
// the relay is sent no web_search tool, magpie's search in its place.
func TestResponsesWebSearchToolToRelayThatSearches(t *testing.T) {
	fresh(t)
	relay := newSearchingRelay(t)
	apis := newSearchAPIs(t)
	if err := provider.SetSearchAPI(apis.api("tavily", "tvly-k")); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"relay/gpt-5.5","stream":false,"input":[{"role":"user","content":[{"type":"input_text","text":"What is the latest Go release?"}]}],"tools":[{"type":"web_search","external_web_access":true}]}`
	ask := func(searches bool) map[string]any {
		t.Helper()
		if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "relay-k", Responses: relay.URL + "/v1", Models: []string{"gpt-5.5"}, Searches: searches}); err != nil {
			t.Fatal(err)
		}
		relay.reset(200)
		apis.asked = nil
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if len(relay.bodies) == 0 {
			t.Fatal("the relay was asked nothing")
		}
		return relay.bodies[0]
	}
	hasSearch := func(q map[string]any) bool {
		tools, _ := q["tools"].([]any)
		for _, x := range tools {
			if m, _ := x.(map[string]any); m["type"] == "web_search" {
				return true
			}
		}
		return false
	}
	if q := ask(true); !hasSearch(q) || len(apis.asked) != 0 {
		t.Fatalf("on: tools %v, apis %v", q["tools"], apis.asked)
	}
	if q := ask(false); hasSearch(q) {
		t.Fatalf("off: the relay was sent web_search: %v", q["tools"])
	}
}
