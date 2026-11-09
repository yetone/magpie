package gui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if _, err := distillComplete(context.Background(), "dt/m1", "sys", "conv"); err == nil || !strings.Contains(err.Error(), "isn't served") {
		t.Fatalf("no gateway: %v", err)
	}
	gw := gateway.New()
	served.Store(gw)

	reply, err := distillComplete(context.Background(), "dt/m1", "Write down what lasts.", "Conversation:\n\nUser: use tabs")
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

	refuse = true
	if _, err := distillComplete(context.Background(), "dt/m1", "s", "u"); err == nil || !strings.Contains(err.Error(), "model not allowed on this plan") {
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
	cs, err := memdistill.Dream(context.Background(), "a/m", func(context.Context, string, string, string) (string, error) {
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
