package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// citationsStrict stands in for the ChatGPT backend, and a relay in front
// of it, as they check a replayed citation (#4921): an output_text
// annotation without both of the offsets OpenAI's schema wants is turned
// away naming the parameter, as Volcengine Ark's answer — its url_citations
// carry no start_index/end_index — came back when Codex replayed it:
// "Invalid response.create payload: Missing required parameter:
// 'input[n].content[m].annotations[k].start_index'."
type citationsStrict struct {
	mu     sync.Mutex
	inputs [][]map[string]any
}

func (u *citationsStrict) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	if strings.HasSuffix(r.URL.Path, "/models") {
		io.WriteString(w, `{"models":[{"slug":"gpt-6.1-sol","visibility":"list"}]}`)
		return
	}
	var q struct {
		Input []map[string]any `json:"input"`
	}
	json.Unmarshal(b, &q)
	u.mu.Lock()
	u.inputs = append(u.inputs, q.Input)
	u.mu.Unlock()
	for i, it := range q.Input {
		content, _ := it["content"].([]any)
		for ci, raw := range content {
			part, _ := raw.(map[string]any)
			notes, _ := part["annotations"].([]any)
			for ai, raw := range notes {
				note, _ := raw.(map[string]any)
				if note["start_index"] != nil && note["end_index"] != nil {
					continue
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"error":{"message":"Invalid response.create payload: Missing required parameter: 'input[%d].content[%d].annotations[%d].start_index'.","type":"invalid_request_error","param":"input[%d].content[%d].annotations[%d].start_index","code":"missing_required_parameter"}}`, i, ci, ai, i, ci, ai)
				return
			}
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"DONE"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`))
}

func (u *citationsStrict) asked() [][]map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]map[string]any(nil), u.inputs...)
}

// citationCounts is how many annotations an input's message items carry
// with and without their offsets.
func citationCounts(input []map[string]any) (with, without int) {
	for _, it := range input {
		content, _ := it["content"].([]any)
		for _, raw := range content {
			part, _ := raw.(map[string]any)
			notes, _ := part["annotations"].([]any)
			for _, raw := range notes {
				note, _ := raw.(map[string]any)
				if note["start_index"] == nil || note["end_index"] == nil {
					without++
				} else {
					with++
				}
			}
		}
	}
	return with, without
}

// arkCitedTurn is a Codex conversation a routing group served once on
// Volcengine Ark (#4921): its answer's first citation carries the shape
// Ark writes — url_citation with no offsets — as Codex stored it, beside a
// citation with its offsets and plain turns.
func arkCitedTurn(model string) string {
	return `{"model":"` + model + `","stream":true,"store":false,"include":["reasoning.encrypted_content"],"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"what changed in go 1.27?"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Go 1.27.2 shipped, on go.dev.","annotations":[` +
		`{"type":"url_citation","url":"https://go.dev/doc/devel/release","title":"Release History","site_name":"go.dev","publish_time":"2026-10-08","summary":"Go 1.27.2 released October 8."},` +
		`{"type":"url_citation","start_index":25,"end_index":31,"url":"https://go.dev","title":"go.dev"}]}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"and 1.26?"}]}]}`
}

// #4921: a citation another vendor's Responses reply wrote without the
// offsets OpenAI's schema wants turned every later turn of the thread away
// where that schema holds. Asked again without it — everything else, the
// citation that carries its offsets included, kept — and never sent one
// again.
func TestCitationsWithoutOffsetsAskedAgainWithout(t *testing.T) {
	fresh(t)
	u := &citationsStrict{}
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "ar", Name: "AgentRouter", Key: "k", Models: []string{"gpt-6-luna"}, Responses: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "medium", Name: "Medium", Members: []string{"ar/gpt-6-luna"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := codexPostTo(s, arkCitedTurn("group/medium"))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "DONE") && !strings.Contains(rec.Body.String(), "RE9ORQ") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := u.asked()
	if len(got) != 2 {
		t.Fatalf("asked the relay %d times, want again once after its 400: %v", len(got), got)
	}
	if with, without := citationCounts(got[0]); without != 1 || with != 1 {
		t.Fatalf("first ask: %d citations with offsets, %d without", with, without)
	}
	if with, without := citationCounts(got[1]); without != 0 || with != 1 {
		t.Fatalf("asked again: %d citations with offsets, %d without", with, without)
	}
	for _, k := range []string{"message"} {
		before, after := kinds(got[0])[k], kinds(got[1])[k]
		if after == 0 || after != before {
			t.Errorf("%s: %d asked again, %d first", k, after, before)
		}
	}

	// the next turn goes without it at once
	rec = codexPostTo(s, arkCitedTurn("group/medium"))
	if gotNow := u.asked(); rec.Code != 200 || len(gotNow) != 3 {
		t.Fatalf("again: %d, %d asks", rec.Code, len(gotNow))
	} else if with, without := citationCounts(gotNow[2]); without != 0 || with != 1 {
		t.Fatalf("asked at once: %d citations with offsets, %d without", with, without)
	}
}

// The ChatGPT backend a group's codex member answers on is never sent the
// citation: asked once, the citation with its offsets kept, the text kept.
func TestCitationsWithoutOffsetsNotSentToCodexBackend(t *testing.T) {
	fresh(t)
	home := os.Getenv("HOME")
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":      claims(map[string]any{"email": "me@example.com"}),
		"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
		"refresh_token": "r", "account_id": "acct-1"}}), 0o600)
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	u := &citationsStrict{}
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	was := provider.CodexBase
	provider.CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	if err := provider.SaveGroup(provider.Group{ID: "medium", Name: "Medium", Members: []string{"codex/gpt-6.1-sol"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := codexPostTo(s, arkCitedTurn("group/medium"))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := u.asked()
	if len(got) != 1 {
		t.Fatalf("the ChatGPT backend was asked %d times: %v", len(got), got)
	}
	if with, without := citationCounts(got[0]); without != 0 || with != 1 || kinds(got[0])["message"] != 3 {
		t.Fatalf("asked with %d citations with offsets, %d without: %v", with, without, got[0])
	}

	// and on Codex's own sign-in alike
	u2 := &citationsStrict{}
	srv2 := httptest.NewServer(u2)
	t.Cleanup(srv2.Close)
	provider.CodexBase = srv2.URL + "/backend-api/codex"
	rec = codexPostTo(s, arkCitedTurn("gpt-6.1-sol"))
	if rec.Code != 200 {
		t.Fatalf("own sign-in: %d %s", rec.Code, rec.Body)
	}
	if got := u2.asked(); len(got) != 1 {
		t.Fatalf("the backend was asked %d times on Codex's own sign-in: %v", len(got), got)
	} else if with, without := citationCounts(got[0]); without != 0 || with != 1 {
		t.Fatalf("own sign-in asked with %d citations with offsets, %d without", with, without)
	}
}

// Only the kinds of annotation OpenAI's schema wants a start and end
// index of are dropped without them: a file_citation or file_path names
// its place by a single index, as OpenAI's own file search writes it, and
// goes back to the ChatGPT backend as it came.
func TestCitationsWithOneIndexKept(t *testing.T) {
	for _, c := range []struct {
		note string
		drop bool
	}{
		{`{"type":"url_citation","url":"https://go.dev","title":"go.dev"}`, true},
		{`{"type":"url_citation","start_index":null,"end_index":3,"url":"https://go.dev","title":"go.dev"}`, true},
		{`{"type":"container_file_citation","container_id":"cntr_1","file_id":"cfile_1","filename":"a.csv"}`, true},
		{`{"type":"url_citation","start_index":0,"end_index":3,"url":"https://go.dev","title":"go.dev"}`, false},
		{`{"type":"file_citation","file_id":"file-1","filename":"notes.md","index":12}`, false},
		{`{"type":"file_path","file_id":"file-2","index":4}`, false},
	} {
		if got := annotationLacksOffsets(json.RawMessage(c.note)); got != c.drop {
			t.Errorf("%s: dropped %v, want %v", c.note, got, c.drop)
		}
	}
	body := `{"model":"gpt-6.1","input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"See notes.md.","annotations":[` +
		`{"type":"file_citation","file_id":"file-1","filename":"notes.md","index":12},` +
		`{"type":"url_citation","url":"https://go.dev","title":"go.dev"}]}]}]}`
	got := string(withoutOffsetlessCitations([]byte(body)))
	if !strings.Contains(got, `"file_citation"`) || strings.Contains(got, `"url_citation"`) {
		t.Fatalf("the file citation went, or the offsetless url citation stayed:\n%s", got)
	}
	only := `{"model":"gpt-6.1","input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"See notes.md.","annotations":[` +
		`{"type":"file_citation","file_id":"file-1","filename":"notes.md","index":12}]}]}]}`
	if got := string(withoutOffsetlessCitations([]byte(only))); got != only {
		t.Fatalf("a history whose citations all name their place was rewritten:\n%s", got)
	}
}
