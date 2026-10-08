package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Discover decision models through a real gateway, including names that
// aren't Jev's, then ask them through another gateway with caller attribution.
func TestRemoteMagpieSystemOne(t *testing.T) {
	fresh(t)
	type hit struct {
		path, body string
		head       http.Header
	}
	var mu sync.Mutex
	var vendor, hops []hit
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		vendor = append(vendor, hit{r.URL.Path, string(b), r.Header.Clone()})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"answers":{"ok":{"value":true}},"usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	models := []string{"jev-latest", "@cf/cloudflare/clef", "decision-model-preview", "custom-image-decision"}
	if err := provider.Save(provider.Provider{ID: "judge", Name: "Judge", Key: "vendor-key", Decide: up.URL + "/v1", Models: models}); err != nil {
		t.Fatal(err)
	}
	remoteHandler := New().Handler()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		mu.Lock()
		hops = append(hops, hit{r.URL.Path, string(b), r.Header.Clone()})
		mu.Unlock()
		remoteHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: remote.URL, Key: "remote-key"}); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("office")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range provider.Deciders() {
		if e.Provider.ID == "office" {
			got = append(got, e.Model)
		}
	}
	want := make([]string, len(models))
	for i, m := range models {
		want[i] = "judge/" + m
	}
	if !slices.Equal(got, want) {
		t.Fatalf("remote decision models: %v; want %v", got, want)
	}
	if ms := p.DecisionModels(); len(ms) != len(models) {
		t.Fatalf("decision picker has %v", ms)
	}
	for _, e := range provider.Catalog() {
		if e.Provider.ID == "office" && slices.Contains(want, e.Model) {
			t.Fatalf("decision model offered for chat: %s", e.ID)
		}
	}
	// Ordinary agent lists never acquire decision models.
	plain := httptest.NewRecorder()
	remoteHandler.ServeHTTP(plain, httptest.NewRequest("GET", "/v1/models", nil))
	if strings.Contains(plain.Body.String(), `"kind":"decision"`) || strings.Contains(plain.Body.String(), "judge/") {
		t.Fatalf("decisions leaked into ordinary models: %s", plain.Body.String())
	}
	s := New()
	for _, model := range models {
		req := httptest.NewRequest("POST", "/v1/systemone", strings.NewReader(`{"model":"office/judge/`+model+`","state":{"message":"hello"},"questions":{"ok":{"type":"noul"}}}`))
		req.Header.Set("User-Agent", "pi/1.0")
		req.Header.Set(SessionHeader, "decision-session")
		req.Header.Set("session_id", "native-session")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"value":true`) {
			t.Fatalf("%s: %d %s", model, rec.Code, rec.Body.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(vendor) != len(models) {
		t.Fatalf("vendor calls: %v", vendor)
	}
	for i, h := range vendor {
		if h.path != "/v1/systemone" || gjsonModel([]byte(h.body)) != models[i] || h.head.Get("Authorization") != "Bearer vendor-key" || !strings.Contains(h.body, `"message":"hello"`) {
			t.Errorf("vendor call: %+v", h)
		}
		if h.head.Get(AgentHeader) != "" || h.head.Get(SessionHeader) != "" || h.head.Get("session_id") != "" {
			t.Errorf("caller headers reached vendor: %v", h.head)
		}
	}
	for _, h := range hops {
		if h.path != "/v1/systemone" {
			continue
		}
		if h.head.Get("Authorization") != "Bearer remote-key" || h.head.Get(AgentHeader) != "pi" || h.head.Get(ViaHeader) == "" || h.head.Get(SessionHeader) != "decision-session" || h.head.Get("session_id") != "native-session" || !strings.HasPrefix(h.head.Get("User-Agent"), "magpie/") {
			t.Errorf("remote caller: %v", h.head)
		}
	}
	if route := s.trace.routes[0]; route.Agent != "pi" || route.Session != "decision-session" || !route.Done {
		t.Errorf("decision trace: %+v", route)
	}
}

// Decision discovery applies the gateway key's model restrictions, while
// an ordinary /models request continues to contain conversation models only.
func TestRemoteMagpieDecisionListPermissions(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "judge", Decide: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"custom-a", "custom-b"}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"judge/custom-a"}}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secrets[0])
	req.Header.Set(provider.DecidersHeader, "1")
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, req)
	var list struct {
		Data []struct{ ID, Kind string }
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Data) != 1 || list.Data[0].ID != "judge/custom-a" || list.Data[0].Kind != "decision" {
		t.Fatalf("held decision list: %d %s", rec.Code, rec.Body.String())
	}
}

// A remote's exposed retrieval models survive discovery, and their requests
// keep the input and options through both gateways.
func TestRemoteMagpieRetrieval(t *testing.T) {
	s, lib := shelved(t)
	remote := httptest.NewServer(s.Handler())
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Preset: provider.RemoteMagpiePreset, Chat: remote.URL, Key: "remote-key"}); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("office")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	live, _, _ := catalog.Live("office")
	if !slices.ContainsFunc(live, func(m catalog.Model) bool { return m.ID == "lib/embed-1" }) {
		t.Fatalf("remote embedding model lost: %v", live)
	}
	for _, tc := range []struct{ path, body, result, model string }{
		{"/v1/embeddings", `{"model":"office/lib/embed-1","input":["hello","world"],"dimensions":2,"encoding_format":"float"}`, `"embedding":[0.1,-0.2]`, "embed-1"},
		{"/v1/rerank", `{"model":"office/lib/rerank-1","query":"magpie","documents":["a crow","a magpie"],"top_n":2,"return_documents":true}`, `"relevance_score":0.9`, "rerank-1"},
	} {
		code, raw := postRetrieval(t, New(), tc.path, tc.body)
		if code != 200 || !strings.Contains(raw, tc.result) {
			t.Fatalf("%s: %d %s", tc.path, code, raw)
		}
		got := lib.got(tc.path)
		if len(got) != 1 || gjsonModel([]byte(got[0])) != tc.model {
			t.Fatalf("vendor request: %v", got)
		}
		var asked, sent map[string]any
		json.Unmarshal([]byte(tc.body), &asked)
		json.Unmarshal([]byte(got[0]), &sent)
		asked["model"] = tc.model
		a, _ := json.Marshal(asked)
		b, _ := json.Marshal(sent)
		if string(a) != string(b) {
			t.Errorf("retrieval options changed: %s; want %s", b, a)
		}
	}
}
