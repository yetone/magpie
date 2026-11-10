package gui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/memdistill"
	"github.com/yetone/magpie/internal/memory"
	"github.com/yetone/magpie/internal/provider"
)

// Conversations are read into memory through the gateway this process
// serves: the request reaches the model's provider with the instructions
// and the conversation, is counted under the memory agent, and the
// reply's text alone comes back; a refusal comes back as an error that
// says the provider's reason. With no gateway served here, it says so.
func TestDistillCompleteThroughGateway(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var got map[string]any
	refuse := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "application/json")
		if refuse {
			w.WriteHeader(400)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"model not allowed on this plan"}}`)
			return
		}
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[{"type":"thinking","thinking":"hmm","signature":"s"},{"type":"text","text":"[{\"text\":\"Likes tabs\"}]"}],"stop_reason":"end_turn","usage":{"input_tokens":90,"output_tokens":9}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "dt", Name: "DT", Anthropic: up.URL, Models: []string{"m1"}, Key: "k"}); err != nil {
		t.Fatal(err)
	}
	prev := served.Swap(nil)
	t.Cleanup(func() { served.Store(prev) })
	if _, err := distillComplete(context.Background(), "dt/m1", "", "sys", "conv"); err == nil || !strings.Contains(err.Error(), "isn't served") {
		t.Fatalf("no gateway: %v", err)
	}
	gw := gateway.New()
	served.Store(gw)

	reply, err := distillComplete(context.Background(), "dt/m1", "", "Write down what lasts.", "Conversation:\n\nUser: use tabs")
	if err != nil || reply != `[{"text":"Likes tabs"}]` {
		t.Fatalf("%q %v", reply, err)
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), "Write down what lasts.") || !strings.Contains(string(b), "User: use tabs") {
		t.Fatalf("sent: %s", b)
	}
	rs := gw.Trace(context.Background(), 0, 0).Routes
	if len(rs) != 1 || rs[0].Agent != memdistill.Agent {
		t.Fatalf("counted as %+v", rs)
	}

	// the gateway's own choice for a model it knows no levels of, as before
	if gjson.GetBytes(b, "thinking.type").String() == "enabled" || got["max_tokens"] != float64(4096) {
		t.Fatalf("no level picked, but sent %s", b)
	}

	// a level picked (RealZeroP27 on X) is asked for, fitted to the
	// model's own, with room to think above the reply; none turns
	// thinking off; one whose levels aren't known is sent as before
	if err := catalog.SaveLive("dt", up.URL, []catalog.Model{{ID: "m1", Efforts: []string{"none", "low", "medium", "high"}}}); err != nil {
		t.Fatal(err)
	}
	if lv := gateway.ModelEfforts("dt/m1"); len(lv) != 4 {
		t.Fatalf("levels %q", lv)
	}
	if _, err := distillComplete(context.Background(), "dt/m1", "xhigh", "s", "u"); err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(got); gjson.GetBytes(b, "thinking.type").String() != "enabled" || gjson.GetBytes(b, "thinking.budget_tokens").Int() != 24000 || gjson.GetBytes(b, "max_tokens").Int() != 4096+24000 {
		t.Fatalf("high sent %s", b)
	}
	if _, err := distillComplete(context.Background(), "dt/m1", "none", "s", "u"); err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(got); gjson.GetBytes(b, "thinking.type").String() != "disabled" || gjson.GetBytes(b, "max_tokens").Int() != 4096 {
		t.Fatalf("none sent %s", b)
	}

	refuse = true
	if _, err := distillComplete(context.Background(), "dt/m1", "", "s", "u"); err == nil || !strings.Contains(err.Error(), "model not allowed on this plan") {
		t.Fatalf("refused: %v", err)
	}
}

// The nightly tidy's routes: switched on and off, and the last tidy
// undone once, the memories it set aside back where they were.
func TestDreamRoutes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	memoryDistillRoutes(mux)
	post := func(path, body string) distillView {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		var v distillView
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &v) != nil {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		return v
	}
	if v := post("/api/memory/dream", `{"on":true}`); !v.Dream || v.On {
		t.Fatalf("on: %+v", v.State)
	}
	text, kind, scope, st := "Old", memory.Fact, memory.Global, memory.Candidate
	m, _, err := memory.Save(memory.Input{Text: &text, Kind: &kind, Scope: &scope, Status: &st}, memory.FromInferred, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	text2 := "Other"
	if _, _, err := memory.Save(memory.Input{Text: &text2, Kind: &kind, Scope: &scope, Status: &st}, memory.FromInferred, nil, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := memdistill.Dream(context.Background(), "a/m", func(context.Context, string, string, string, string) (string, error) {
		return `{"stale":[{"id":"n1"}]}`, nil
	})
	if err != nil || len(cs) != 1 {
		t.Fatal(cs, err)
	}
	if got, _ := memory.Get(m.ID); got.Status != memory.Archived {
		t.Fatalf("not set aside: %s", got.Status)
	}
	if v := post("/api/memory/dream/undo", `{}`); !v.Undone {
		t.Fatalf("undo: %+v", v.State)
	}
	if got, _ := memory.Get(m.ID); got.Status != memory.Candidate {
		t.Fatalf("not back: %s", got.Status)
	}
	if v := post("/api/memory/dream", `{"on":false}`); v.Dream {
		t.Fatalf("off: %+v", v.State)
	}
}

// The row's route picks how far back reading looks and the level its
// model is asked at, each only when sent: the switch's own {on, model}
// keeps both. A longer range reads at once; a range or level not
// offered is refused, and the view offers the ranges and the model's
// levels.
func TestDistillRangeAndEffortRoutes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	reads := 0
	prev := distillNow
	distillNow = func() { reads++ }
	t.Cleanup(func() { distillNow = prev })
	mux := http.NewServeMux()
	memoryDistillRoutes(mux)
	post := func(body string) (int, distillView) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/memory/distill", strings.NewReader(body)))
		var v distillView
		json.Unmarshal(rec.Body.Bytes(), &v)
		return rec.Code, v
	}
	if _, v := post(`{"on":true,"model":""}`); !v.On || v.Days != 0 || v.Effort != "" || reads != 1 || len(v.Ranges) != 5 {
		t.Fatalf("on: %+v %d", v, reads)
	}
	if _, v := post(`{"on":true,"model":"","days":90,"effort":"high"}`); v.Days != 90 || v.Effort != "high" || reads != 2 || !v.Running {
		t.Fatalf("90 days, high: %+v %d", v.State, reads)
	}
	if _, v := post(`{"on":true,"model":""}`); v.Days != 90 || v.Effort != "high" || reads != 2 {
		t.Fatalf("the switch's body changed them: %+v %d", v.State, reads)
	}
	if _, v := post(`{"on":true,"model":"","days":30}`); v.Days != 30 || reads != 2 {
		t.Fatalf("a shorter range read again: %+v %d", v.State, reads)
	}
	if _, v := post(`{"on":true,"model":"","effort":""}`); v.Effort != "" {
		t.Fatalf("default: %+v", v.State)
	}
	for _, bad := range []string{`{"on":true,"days":12}`, `{"on":true,"effort":"hard"}`} {
		if code, _ := post(bad); code == 200 {
			t.Fatalf("%s taken", bad)
		}
	}
}
