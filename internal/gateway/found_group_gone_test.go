package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A conversation saved on a group magpie found keeps working once the group
// is gone (canlee886 on X: a Codex thread on group/auto-gpt-6-1-sol failed
// every turn with 404 once one of its two providers no longer listed
// gpt-6.1-sol, and switching the thread's model did not help). While a
// provider still serves the model, the group's id goes to that model there;
// it is the same model, so nothing is switched. With none left the 404 says
// the model is gone and to pick another, not every id magpie has.
func TestFoundGroupGoneGoesToItsModel(t *testing.T) {
	fresh(t)
	a, b := &keyed{}, &keyed{}
	serveOn(t, "a", "ka", []string{"gpt-6.1-sol", "gpt-5.6-sol"}, a)
	serveOn(t, "b", "kb", []string{"gpt-6.1-sol", "gpt-5.6-sol"}, b)
	ask := func(model string) (int, string) {
		t.Helper()
		return postAs(t, New(), "", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	}
	for _, m := range []string{"group/auto-gpt-6-1-sol", "auto/gpt-6.1-sol"} {
		if code, out := ask(m); code != 200 {
			t.Fatalf("%s while found: %d %s", m, code, out)
		}
	}

	// a's list no longer has the model: the group is gone, b still serves it
	p, err := provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	p.Models = []string{"gpt-5.6-sol"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if g, _, ok := provider.FindGroup("group/auto-gpt-6-1-sol"); ok {
		t.Fatalf("the group is still found: %+v", g)
	}
	for _, m := range []string{"group/auto-gpt-6-1-sol", "auto/gpt-6.1-sol", "auto/gpt-6.1-sol:high"} {
		a.tried, b.tried = nil, nil
		code, out := ask(m)
		if code != 200 || !strings.Contains(out, "from kb") || len(a.tried) != 0 {
			t.Fatalf("%s with b alone serving it: %d %s (a asked %v)", m, code, out, a.tried)
		}
	}

	// a group the user removed stays removed: no stand-in for it
	if err := provider.DeleteGroup("auto-gpt-5-6-sol"); err != nil {
		t.Fatal(err)
	}
	if code, _ := ask("group/auto-gpt-5-6-sol"); code != 404 {
		t.Fatalf("a removed group answered %d", code)
	}

	// nothing serves it any more: 404, saying what to do, not every id
	if err := provider.Delete("b"); err != nil {
		t.Fatal(err)
	}
	code, out := ask("group/auto-gpt-6-1-sol")
	if code != 404 {
		t.Fatalf("no provider serves it: %d %s", code, out)
	}
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal([]byte(out), &e)
	msg := e.Error.Message
	if !strings.Contains(msg, `"group/auto-gpt-6-1-sol"`) || !strings.Contains(msg, "pick another model for this conversation") || strings.Contains(msg, "it has ") {
		t.Fatalf("the 404 for a gone group: %s", msg)
	}
}

// Codex, switched off a model magpie no longer serves, compacts the thread
// on the old model first (model_downshift): that compaction is magpie's own
// local summary, never the turn's 404, so the thread goes on on the model
// it was switched to.
func TestCodexLeavingGoneGroupCompactsLocally(t *testing.T) {
	fresh(t)
	serveOn(t, "a", "ka", []string{"gpt-5.6-sol"}, &keyed{})
	meta := `{"session_id":"th1","thread_id":"th1","turn_id":"tu1","request_kind":"compaction","compaction":{"trigger":"auto","reason":"model_downshift","implementation":"responses_compaction_v2","phase":"pre_turn","strategy":"memento"}}`
	cm, _ := json.Marshal(map[string]string{"x-codex-turn-metadata": meta})
	body := `{"model":"group/auto-gpt-6-1-sol","stream":true,"prompt_cache_key":"th1","client_metadata":` + string(cm) + `,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"build the parser"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"the parser is built"}]},` +
		`{"type":"compaction_trigger"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req.Header.Set(turnMetadataHeader, meta)
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatalf("compaction on a gone group: %d %s", rec.Code, rec.Body.String())
	}
}
