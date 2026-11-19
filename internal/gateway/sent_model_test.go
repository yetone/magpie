package gateway

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
	"github.com/yetone/magpie/internal/usage"
)

// The model id a request went out to the vendor under, when it is another
// than the model magpie asked the provider for, is kept with the try and the
// ledger's row, and named in the CSV: a name the user gave the model
// upstream goes out in place of magpie's (daoche_wuzui on X asked to see
// whether a request went out as a model with a suffix). A request that went
// out as the model keeps none, and a vendor saying a sent model in magpie's
// header is not taken at its word: only a plugin says what it sent.
func TestUpstreamNameSentIsKept(t *testing.T) {
	fresh(t)

	var seen asked
	relay := asking(t, &seen)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set(provider.SentModelHeader, "spoofed")
		io.WriteString(w, `data: {"id":"x","model":"`+b.Model+`","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":5,"completion_tokens":3}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(other.Close)

	for _, p := range []provider.Provider{
		{ID: "relay-b", Name: "Relay B", Key: "k", Models: []string{"model-2"}, Chat: relay.URL},
		{ID: "other", Name: "Other", Key: "k", Models: []string{"model-2"}, Chat: other.URL},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetUpstreamName("relay-b/model-2", "vendor-c/model-2-preview"); err != nil {
		t.Fatal(err)
	}

	s := New()
	ask := func(model string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
			`{"model":"`+model+`","max_tokens":1024,"stream":true,`+
				`"messages":[{"role":"user","content":"hi"}]}`))
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", model, rec.Code, rec.Body.String())
		}
		return rec
	}

	ask("relay-b/model-2")
	if got := seen.all(); len(got) != 1 || got[0] != "vendor-c/model-2-preview" {
		t.Fatalf("the relay was asked for %v; want the upstream name", got)
	}
	if tr := lastRoute(s).Tries; len(tr) != 1 || tr[0].Wire != "vendor-c/model-2-preview" || tr[0].Model != "model-2" {
		t.Errorf("the try keeps %+v; want model-2 sent as vendor-c/model-2-preview", tr)
	}

	rec := ask("other/model-2")
	if got := rec.Header().Get(provider.SentModelHeader); got != "" {
		t.Errorf("the agent was told the sent model %q", got)
	}
	if tr := lastRoute(s).Tries; len(tr) != 1 || tr[0].Wire != "" {
		t.Errorf("a try that went out as its model keeps %+v; want no sent name, and none a vendor said", tr)
	}

	rows := usage.Load(time.Time{})
	wire := map[string]string{}
	for _, r := range rows {
		wire[r.Provider] = r.Wire
	}
	if len(rows) != 2 || wire["relay-b"] != "vendor-c/model-2-preview" || wire["other"] != "" {
		t.Fatalf("the ledger keeps %v; want relay-b's sent name alone", wire)
	}

	var buf bytes.Buffer
	var led []usage.Row
	for _, r := range rows {
		led = append(led, usage.Row{Record: r})
	}
	if err := usage.WriteCSV(&buf, led); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := slices.Index(recs[0], "sent_model")
	pcol := slices.Index(recs[0], "provider")
	if col < 0 {
		t.Fatalf("the CSV has no sent_model column: %v", recs[0])
	}
	for _, r := range recs[1:] {
		want := map[string]string{"relay-b": "vendor-c/model-2-preview"}[r[pcol]]
		if r[col] != want {
			t.Errorf("the CSV's %s row says sent_model %q, want %q", r[pcol], r[col], want)
		}
	}
}

// A plugin that sends the vendor another id than the model it was asked for
// — Antigravity's sends a family as the variant the effort picks from the
// account's own list — says so in provider.SentModelHeader, and the try and
// the ledger keep it. The header is magpie's: the agent never sees it.
func TestPluginSentModelIsKept(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)

	say := "fake-1-high"
	up := testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if say != "" {
			w.Header().Set(provider.SentModelHeader, say)
		}
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"hi there"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := provider.StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": "full"})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.SubmitSignInCallback(st.ID, "good"); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}

	s := New()
	for _, c := range []struct{ said, want string }{
		{"fake-1-high", "fake-1-high"},
		// a plugin saying the model it was asked for sent nothing else
		{"fake-1", ""},
		{"", ""},
	} {
		say = c.said
		for _, stream := range []bool{false, true} {
			body := `{"model":"fakeco/fake-1","messages":[{"role":"user","content":"hello"}]`
			if stream {
				body += `,"stream":true`
			}
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body+"}"))
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi there") {
				t.Fatalf("said %q, stream %v: %d %s", c.said, stream, rec.Code, rec.Body)
			}
			if got := rec.Header().Get(provider.SentModelHeader); got != "" {
				t.Errorf("said %q, stream %v: the agent was told the sent model %q", c.said, stream, got)
			}
			if tr := lastRoute(s).Tries; len(tr) != 1 || tr[0].Wire != c.want {
				t.Errorf("said %q, stream %v: the try keeps %+v; want sent as %q", c.said, stream, tr, c.want)
			}
			rows := usage.Load(time.Time{})
			if len(rows) == 0 || rows[len(rows)-1].Wire != c.want {
				t.Errorf("said %q, stream %v: the ledger's last row keeps %q; want %q", c.said, stream, rows[len(rows)-1].Wire, c.want)
			}
		}
	}
}
