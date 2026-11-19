package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// upItems is a request's input, each item as the upstream got it.
func upItems(t *testing.T, body []byte) []json.RawMessage {
	t.Helper()
	var q struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("%v: %.300s", err, body)
	}
	return q.Input
}

// upText is what the model reads of an input item: its parts' text.
func upText(t *testing.T, item json.RawMessage) string {
	t.Helper()
	var it struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(item, &it)
	var b strings.Builder
	for _, c := range it.Content {
		b.WriteString(c.Text)
		b.WriteString("\x00")
	}
	return b.String()
}

// A Codex conversation's prefix is the same on every request magpie
// sends of it, so the upstream's prompt cache holds past it (@m0eaki on
// X: LobeHub's Codex 0.169 on magpie, its cache holding only through the
// base instructions). Codex 0.169 puts its skills, permissions, deferred
// tools and, with features.model_catalog_in_context, its <model_catalog>
// in one developer message; magpie widens that catalog with the models a
// subagent may be put on (spawnAgentModels), and so writes the message
// again on every request. It must come out the same on each: a turn's
// first request and its next, the turn after (the conversation grown), and
// after a restart. The catalog has two providers and a routing group,
// whose order has to hold from request to request. What Codex itself
// writes there is its own: the same message names the sandbox's writable
// roots, the working folder among them (Codex 0.169.0 in a sandbox
// CODEX_HOME, workspace-write, two folders), and the skill roots, so a
// host that starts each turn in another folder changes it on Codex's side.
func TestCodexPrefixSameOnEveryRequest(t *testing.T) {
	var models []string
	for i := 1; i <= 12; i++ {
		models = append(models, fmt.Sprintf("m%d", i))
	}
	f := &fake{t: t, reply: sse(`data: {"type":"response.created","response":{"id":"r1"}}`,
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`)}
	up := setup(t, provider.Responses, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Responses: up.URL + "/v1", Models: models}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k", Responses: up.URL + "/v1", Models: []string{"z9", "a1", "k5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Both", Members: []string{"fake/m1", "other/a1"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	// what Codex 0.169.0 sends under LobeHub's app-server
	head := map[string]string{
		"User-Agent":        "lobehub-desktop/0.169.0 (Mac OS 26.5.0; arm64) unknown (lobehub-desktop; 2.1.40)",
		"originator":        "lobehub-desktop",
		"session_id":        "019e1a2b-3c4d-7e5f-8a9b-0c1d2e3f4a5b",
		"x-codex-window-id": "019e1a2b-3c4d-7e5f-8a9b-0c1d2e3f4a5b:0",
	}
	post := func(t *testing.T, s *Server, body []byte) []byte {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
		for k, v := range head {
			req.Header.Set(k, v)
		}
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return bytes.Clone(f.got)
	}

	b, err := os.ReadFile("testdata/workdir/codex-0.169.0-model-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	turn1 := withModel(b, "fake/m1")
	// the next turn: the conversation so far, Codex's answer and the
	// user's next words after it
	q, err := rawObject(turn1)
	if err != nil {
		t.Fatal(err)
	}
	var in []json.RawMessage
	if err := json.Unmarshal(q["input"], &in); err != nil {
		t.Fatal(err)
	}
	in = append(in, json.RawMessage(codexItem("assistant", "Spawned it.")), json.RawMessage(codexItem("user", "and again")))
	q["input"], _ = json.Marshal(in)
	turn2, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}

	s := New()
	first := post(t, s, turn1)
	items := upItems(t, first)
	// the developer message with the skills and the model catalog
	at := -1
	for i, it := range items {
		if strings.HasPrefix(upText(t, it), "<skills_instructions>") {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatalf("no skills message upstream: %.500s", first)
	}
	if txt := upText(t, items[at]); !strings.Contains(txt, spawnMore) || !strings.Contains(txt, "- `fake/m12`") || !strings.Contains(txt, "- `other/z9`") {
		t.Fatalf("the catalog isn't widened, so this tests nothing:\n%s", txt)
	}
	same := func(t *testing.T, name string, got []byte) {
		t.Helper()
		gi := upItems(t, got)
		if len(gi) < len(items) {
			t.Fatalf("%s: %d items, the first request had %d", name, len(gi), len(items))
		}
		for i := range items {
			if !bytes.Equal(gi[i], items[i]) {
				t.Fatalf("%s: input[%d] differs from the first request's:\n%s\n---\n%s", name, i, items[i], gi[i])
			}
		}
	}
	// the same request again, as Codex sends a turn's next request
	if again := post(t, s, turn1); !bytes.Equal(again, first) {
		same(t, "the same request again", again)
		t.Fatalf("the same request went differently:\n%s\n---\n%s", first, again)
	}
	// the next turn: what was sent before is sent the same, byte for byte
	same(t, "the next turn", post(t, s, turn2))
	// after a restart
	if again := post(t, New(), turn1); !bytes.Equal(again, first) {
		same(t, "after a restart", again)
		t.Fatalf("after a restart the request went differently:\n%s\n---\n%s", first, again)
	}
	// and many times over: an order taken from a map would show here
	for i := range 20 {
		same(t, fmt.Sprintf("request %d", i), post(t, New(), turn2))
	}
}
