package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Google Vertex AI's editor asks for a project, a location, a credentials
// file and a service account in place of a key. Its preset says so, a
// Save sends them as vertex and the provider's row has them back as they
// are kept; an edit that sends none keeps them. Test before a Save asks at
// the project typed, with the credentials typed, and saves nothing.
func TestVertexProviderEditor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", "")

	// Google's token endpoint and Vertex AI, both here
	var mu sync.Mutex
	var asked []string
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			w.Write([]byte(`{"access_token":"ya29.typed","expires_in":3600}`))
			return
		}
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer ya29.typed" {
			http.Error(w, `{"error":{"code":401,"message":"no token"}}`, http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`))
	}))
	defer google.Close()
	defer provider.VertexForTest(google.URL, google.URL+"/token", google.URL+"/iam")()

	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(action, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/"+action, strings.NewReader(body)))
		return w
	}
	type row struct {
		ID     string           `json:"id"`
		Vertex *provider.Vertex `json:"vertex"`
		Ready  bool             `json:"ready"`
		Key    struct {
			Set bool `json:"set"`
		} `json:"key"`
	}
	state := func() (map[string]row, map[string]bool) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
		var s struct {
			Providers []row `json:"providers"`
			Presets   []struct {
				ID     string `json:"id"`
				Vertex bool   `json:"vertex"`
			} `json:"presets"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
			t.Fatalf("providers: %v %s", err, w.Body)
		}
		rows, presets := map[string]row{}, map[string]bool{}
		for _, p := range s.Providers {
			rows[p.ID] = p
		}
		for _, p := range s.Presets {
			presets[p.ID] = p.Vertex
		}
		return rows, presets
	}

	_, presets := state()
	if !presets[provider.VertexPreset] || presets["google"] || presets["openai"] {
		t.Fatalf("the presets that say they are Vertex AI's: %v", presets)
	}

	if w := post("save", `{"preset":"google-vertex","new":true,"vertex":{"project":" My-Project ","location":"US-Central1","credentials":" ~/keys/sa.json ","impersonate":"Bot@My-Project.iam.gserviceaccount.com"}}`); w.Code != 200 {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	want := provider.Vertex{Project: "my-project", Location: "us-central1", Credentials: "~/keys/sa.json", Impersonate: "bot@my-project.iam.gserviceaccount.com"}
	rows, _ := state()
	got := rows[provider.VertexPreset]
	if got.Vertex == nil || *got.Vertex != want || !got.Ready || got.Key.Set {
		t.Fatalf("the row: %+v vertex %+v", got, got.Vertex)
	}

	// an edit that sends none keeps them; one that does changes them
	if w := post("save", `{"id":"google-vertex","preset":"google-vertex","name":"Google Vertex AI","headers":{"X-Vertex-AI-LLM-Request-Type":"shared"}}`); w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if rows, _ = state(); rows[provider.VertexPreset].Vertex == nil || *rows[provider.VertexPreset].Vertex != want {
		t.Fatalf("an edit without them lost them: %+v", rows[provider.VertexPreset].Vertex)
	}
	if w := post("save", `{"id":"google-vertex","preset":"google-vertex","name":"Google Vertex AI","vertex":{"project":"my-project","location":"","credentials":"","impersonate":""}}`); w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if rows, _ = state(); rows[provider.VertexPreset].Vertex == nil || *rows[provider.VertexPreset].Vertex != (provider.Vertex{Project: "my-project", Location: "global"}) {
		t.Fatalf("the edit's: %+v", rows[provider.VertexPreset].Vertex)
	}

	// Test before a Save: at the project typed, with the credentials typed
	creds := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(creds, []byte(`{"type":"authorized_user","client_id":"c","client_secret":"s","refresh_token":"r"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	vertex, _ := json.Marshal(provider.Vertex{Project: " Typed-Project ", Location: "", Credentials: creds})
	w := post("test", `{"id":"google-vertex","typed":true,"vertex":`+string(vertex)+`}`)
	var res struct {
		Results []provider.Result `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || len(res.Results) != 1 || !res.Results[0].OK || res.Results[0].Protocol != provider.Gemini {
		t.Fatalf("test: %d %s", w.Code, w.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "/v1/projects/typed-project/locations/global/publishers/google/models/") {
		t.Fatalf("asked at %q, not the project typed", asked)
	}
	if p, err := provider.Find(provider.VertexPreset); err != nil || p.Vertex == nil || p.Vertex.Project != "my-project" || p.Vertex.Credentials != "" {
		t.Fatalf("the test saved what was typed: %v %+v", err, p)
	}
}
