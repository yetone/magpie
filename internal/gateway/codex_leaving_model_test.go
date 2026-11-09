package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/codexsummary"
	"github.com/yetone/magpie/internal/provider"
)

// hiwise_aigc on X: Codex's allowance used up, an existing thread switched
// to a magpie model failed every turn with ChatGPT's usage limit, "还会走
// gpt模型", restarts or not; a new thread worked. Before a turn on a model
// with a smaller window Codex compacts the thread on the model it was on
// (reason model_downshift). Under magpie's provider, the one Codex has
// while its sign-in is used up, that went to the used-up account, and
// Codex asks the turn's own model instead only under its provider named
// "OpenAI": the refusal was the turn's. Compacted locally, the thread goes
// on on the model it was switched to. A compaction for the thread's own
// model running out of room (context_limit) still says the refusal, and
// so does the old model's compaction on CodexPath, where Codex's "OpenAI"
// provider asks the turn's model itself.
func TestCodexLeavingModelCompactsLocally(t *testing.T) {
	codexSignedIn(t)
	old := codexHeldKnown
	codexHeldKnown = func() bool { return true }
	t.Cleanup(func() { codexHeldKnown = old })
	var tried, models []string
	usedUp(t, &tried, &models)
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	meta := func(reason string) string {
		return `{"session_id":"th1","thread_id":"th1","turn_id":"tu1","request_kind":"compaction","compaction":{"trigger":"auto","reason":"` +
			reason + `","implementation":"responses_compaction_v2","phase":"pre_turn","strategy":"memento"}}`
	}
	body := func(meta string) string {
		cm, _ := json.Marshal(map[string]string{"x-codex-turn-metadata": meta})
		return `{"model":"gpt-5.5","stream":true,"prompt_cache_key":"th1","client_metadata":` + string(cm) + `,"input":[` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"build the parser"}]},` +
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"the parser is built"}]},` +
			`{"type":"compaction_trigger"}]}`
	}
	post := func(path, reason string, header bool) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body(meta(reason))))
		if path == CodexPath+"/responses" {
			req.Header.Set("Authorization", "Bearer chatgpt-token")
			req.Header.Set("chatgpt-account-id", "acct-1")
		}
		if header {
			req.Header.Set(turnMetadataHeader, meta(reason))
		}
		New().Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	summary := func(sse string) string {
		for _, l := range strings.Split(sse, "\n") {
			d, ok := strings.CutPrefix(l, "data: ")
			if !ok || gjson.Get(d, "type").Str != "response.output_item.done" {
				continue
			}
			msg, ours := codexsummary.Message(gjson.Get(d, "item.encrypted_content").Str)
			if !ours {
				return ""
			}
			b, _ := json.Marshal(msg)
			return string(b)
		}
		return ""
	}

	for _, c := range []struct {
		reason string
		header bool
	}{{"model_downshift", true}, {"comp_hash_changed", true}, {"model_downshift", false}} {
		tried = nil
		code, out := post("/v1/responses", c.reason, c.header)
		if code != 200 {
			t.Fatalf("%s (header %v): %d %s", c.reason, c.header, code, out)
		}
		// asked once; refused with the time it is back, it is held till
		// then, and the next compactions are answered from that refusal
		// without asking it again (timedHeld, hsjzg58958 on X)
		want := "acct-1"
		if c.reason != "model_downshift" || !c.header {
			want = ""
		}
		if strings.Join(tried, ",") != want {
			t.Fatalf("%s: the old model was asked of %v, want %q", c.reason, tried, want)
		}
		if s := summary(out); !strings.Contains(s, "build the parser") || !strings.Contains(s, "the parser is built") {
			t.Fatalf("%s: no local summary of the thread: %q\n%s", c.reason, s, out)
		}
	}

	// the thread's own model out of room: the refusal is said, as before
	if code, out := post("/v1/responses", "context_limit", true); code != 429 || !strings.Contains(out, "usage limit") {
		t.Fatalf("context_limit: %d %s", code, out)
	}
	// Codex's own provider asks the turn's model by itself
	if code, out := post(CodexPath+"/responses", "model_downshift", true); code != 429 || !strings.Contains(out, "usage limit") {
		t.Fatalf("CodexPath model_downshift: %d %s", code, out)
	}
}
