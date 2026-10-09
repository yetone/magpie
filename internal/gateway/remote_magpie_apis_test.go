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
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
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
	peer := New()
	remoteHandler := peer.Handler()
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
			if e.Model == "judge/custom-image-decision" && e.Name != "custom-image-decision · Judge" {
				t.Errorf("unnamed remote decision in the local picker: %q", e.Name)
			}
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
	for _, route := range peer.trace.routes {
		if route.Agent != "pi" || route.Session != "decision-session" {
			t.Errorf("remote decision trace lost caller: %+v", route)
		}
	}
	var recorded int
	for _, r := range usage.Load(time.Time{}) {
		if r.Provider == "judge" {
			recorded++
			if r.Agent != "pi" || r.Via != hostName() {
				t.Errorf("remote decision usage lost caller: %+v", r)
			}
		}
	}
	if recorded != len(models) {
		t.Errorf("remote decision usage: %d records; want %d", recorded, len(models))
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
		Data []struct {
			ID, Kind string
			Name     string `json:"display_name"`
			Label    string `json:"magpie_label"`
		}
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Data) != 1 || list.Data[0].ID != "judge/custom-a" || list.Data[0].Kind != "decision" {
		t.Fatalf("held decision list: %d %s", rec.Code, rec.Body.String())
	}
	if list.Data[0].Name != "custom-a" || list.Data[0].Label != "custom-a · judge" {
		t.Errorf("unnamed decision lost its id: %+v", list.Data[0])
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
	// The shared list has no separate retrieval kind, so these stay in
	// the local catalog just as they do in the remote's agent list.
	entries := provider.Catalog()
	for _, id := range []string{"office/lib/embed-1", "office/lib/rerank-1"} {
		if !slices.ContainsFunc(entries, func(e provider.Entry) bool { return e.ID == id }) {
			t.Errorf("remote retrieval entry missing from catalog: %s", id)
		}
	}
	for _, tc := range []struct{ path, body, result, model string }{
		{"/v1/embeddings", `{"model":"office/lib/embed-1","input":["hello","world"],"dimensions":2,"encoding_format":"float"}`, `"embedding":[0.1,-0.2]`, "embed-1"},
		{"/v1/rerank", `{"model":"office/lib/rerank-1","query":"magpie","documents":["a crow","a magpie"],"top_n":2,"return_documents":true}`, `"relevance_score":0.9`, "rerank-1"},
	} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		req.Header.Set("User-Agent", "pi/1.0")
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		code, raw := rec.Code, rec.Body.String()
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
	var recorded int
	for _, r := range usage.Load(time.Time{}) {
		if r.Provider == "lib" {
			recorded++
			if r.Agent != "pi" || r.Via != hostName() {
				t.Errorf("remote retrieval usage lost caller: %+v", r)
			}
		}
	}
	if recorded != 2 {
		t.Errorf("remote retrieval usage: %d records; want 2", recorded)
	}
}
