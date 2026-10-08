package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Claude Desktop shows a gateway model of 1M tokens or more twice, its
// plain entry and a "1M context window" one it adds itself (#1272: 21
// models made 34 entries). With DesktopLongest on, such a model is listed
// by its "[1m]" id alone, so Desktop's menu has one entry per model; one
// under 1M (872K) keeps its own id and window, and a request on the plain id
// a session was saved with is still served.
func TestClaudeDesktopLongestOnly(t *testing.T) {
	f := &fake{reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	up := setup(t, provider.Anthropic, f)
	p := provider.Provider{ID: "vend", Name: "Vend", Key: "k", Anthropic: up.URL, Models: []string{"deepseek-v4-pro", "exo-free", "claude-opus-4-8"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	for model, n := range map[string]int{"deepseek-v4-pro": 1_048_576, "exo-free": 872_000, "claude-opus-4-8": 1_000_000} {
		now, err := provider.Find(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := provider.SetContext(*now, model, n); err != nil {
			t.Fatal(err)
		}
	}
	type row struct {
		ID       string `json:"id"`
		MaxInput int    `json:"max_input_tokens"`
	}
	list := func() map[string]row {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/models?limit=1000", nil)
		req.Header.Set("x-api-key", TokenFor("claude-desktop"))
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		var out struct{ Data []row }
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		got := map[string]row{}
		for _, d := range out.Data {
			got[DesktopCatalogID(strings.TrimSuffix(d.ID, "[1m]"))] = d
		}
		return got
	}
	// the entries Desktop's menu makes of the list: one per row, and one
	// more for a row of 1M or more whose id has no "[1m]"
	entries := func(rows map[string]row) int {
		n := 0
		for _, r := range rows {
			n++
			if r.MaxInput >= 1_000_000 && !strings.HasSuffix(r.ID, "[1m]") {
				n++
			}
		}
		return n
	}

	off := list()
	if entries(off) != len(off)+2 {
		t.Fatalf("off: %d entries of %d rows, want two 1M extras: %v", entries(off), len(off), off)
	}
	for id, r := range off {
		if strings.HasSuffix(r.ID, "[1m]") {
			t.Fatalf("off: %s listed as %s", id, r.ID)
		}
	}

	s := settings.Load()
	s.DesktopLongest = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	on := list()
	if entries(on) != len(on) || len(on) != len(off) {
		t.Fatalf("on: %d entries of %d rows (off had %d rows): %v", entries(on), len(on), len(off), on)
	}
	for id, want := range map[string]string{
		"vend/deepseek-v4-pro": off["vend/deepseek-v4-pro"].ID + "[1m]",
		"vend/claude-opus-4-8": off["vend/claude-opus-4-8"].ID + "[1m]",
		"vend/exo-free":        off["vend/exo-free"].ID,
		"fake/m1":              off["fake/m1"].ID,
	} {
		if r := on[id]; r.ID != want {
			t.Errorf("on: %s listed as %q, want %q", id, r.ID, want)
		}
	}
	if r := on["vend/exo-free"]; r.MaxInput != 872_000 {
		t.Errorf("exo-free's window told as %d, want its own 872000", r.MaxInput)
	}

	// the 1M id and the plain id an older session holds both reach the model
	for _, id := range []string{on["vend/deepseek-v4-pro"].ID, off["vend/deepseek-v4-pro"].ID} {
		f.calls = 0
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"`+id+`","max_tokens":10,"stream":true,"tools":[{"name":"t","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("x-api-key", TokenFor("claude-desktop"))
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		var sent struct{ Model string }
		if rec.Code != 200 || f.calls != 1 || json.Unmarshal(f.got, &sent) != nil || sent.Model != "deepseek-v4-pro" {
			t.Fatalf("%s: %d %s, %d calls, sent %s", id, rec.Code, rec.Body, f.calls, f.got)
		}
	}
}
