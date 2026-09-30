package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n fake")

// easel is a vendor with an images API model and a chat model that answers
// with an image; what each path was sent is kept.
type easel struct {
	mu   sync.Mutex
	sent map[string][]string
	ct   map[string]string
}

func (e *easel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	e.mu.Lock()
	if e.sent == nil {
		e.sent, e.ct = map[string][]string{}, map[string]string{}
	}
	e.sent[r.URL.Path] = append(e.sent[r.URL.Path], string(body))
	e.ct[r.URL.Path] = r.Header.Get("Content-Type")
	e.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer key" {
		w.WriteHeader(401)
		return
	}
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/images/generations", "/v1/images/edits":
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+b64+`"}],"usage":{"input_tokens":7,"output_tokens":100}}`)
	case "/v1/chat/completions":
		if !strings.Contains(string(body), `"modalities":["image","text"]`) {
			w.WriteHeader(400)
			return
		}
		if strings.Contains(string(body), "gemini") {
			// AIHubMix's shape: Gemini's parts, under multi_mod_content
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Here you go!","multi_mod_content":[{"text":"Here you go! ","inline_data":{}},{"inline_data":{"data":"`+b64+`","mime_type":"image/png"}}]}}]}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Here it is.","images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`+b64+`"}}]}}],"usage":{"prompt_tokens":3,"completion_tokens":50}}`)
	default:
		w.WriteHeader(404)
	}
}

func (e *easel) got(path string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.sent[path]...)
}

func easeled(t *testing.T) (*Server, *easel) {
	t.Helper()
	fresh(t)
	e := &easel{}
	up := httptest.NewServer(e)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "art", Name: "Art", Chat: up.URL + "/v1", Key: "key", Models: []string{"text", "gpt-image-1", "painter-image-preview", "gemini-2.5-flash-image"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("art", up.URL+"/v1", []catalog.Model{{ID: "text"}}); err != nil {
		t.Fatal(err)
	}
	return New(), e
}

type imagesAnswer struct {
	Model string `json:"model"`
	Data  []struct {
		B64 string `json:"b64_json"`
	} `json:"data"`
	Text  string `json:"text"`
	Usage struct {
		Input  int `json:"input_tokens"`
		Output int `json:"output_tokens"`
	} `json:"usage"`
}

func postImages(t *testing.T, s *Server, path, ct, body string) (int, imagesAnswer, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", ct)
	s.Handler().ServeHTTP(rec, req)
	var a imagesAnswer
	json.Unmarshal(rec.Body.Bytes(), &a)
	return rec.Code, a, rec.Body.String()
}

func TestImagesAPIModelDraws(t *testing.T) {
	s, e := easeled(t)
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"art/gpt-image-1","prompt":"a magpie","size":"1024x1024","n":2}`)
	if code != 200 || len(a.Data) != 1 || a.Usage.Output != 100 {
		t.Fatalf("%d %s", code, raw)
	}
	if b, _ := base64.StdEncoding.DecodeString(a.Data[0].B64); string(b) != string(pngBytes) {
		t.Fatalf("image %q", b)
	}
	sent := e.got("/v1/images/generations")
	if len(sent) != 1 || !strings.Contains(sent[0], `"model":"gpt-image-1"`) || !strings.Contains(sent[0], `"n":2`) || strings.Contains(sent[0], "response_format") {
		t.Fatalf("vendor was sent %v", sent)
	}
}

func TestChatModelDrawsWithModalities(t *testing.T) {
	s, e := easeled(t)
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"art/painter-image-preview","prompt":"a magpie","size":"1536x1024","n":2}`)
	if code != 200 || len(a.Data) != 2 || a.Text != "Here it is." || a.Usage.Input != 6 {
		t.Fatalf("%d %s", code, raw)
	}
	sent := e.got("/v1/chat/completions")
	if len(sent) != 2 || !strings.Contains(sent[0], "Aspect ratio: 3:2") {
		t.Fatalf("vendor was sent %v", sent)
	}
}

func TestChatModelsGeminiPartsAreImages(t *testing.T) {
	s, _ := easeled(t)
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"art/gemini-2.5-flash-image","prompt":"a magpie"}`)
	if code != 200 || len(a.Data) != 1 || a.Text != "Here you go!" {
		t.Fatalf("%d %s", code, raw)
	}
	if b, _ := base64.StdEncoding.DecodeString(a.Data[0].B64); string(b) != string(pngBytes) {
		t.Fatalf("image %q", b)
	}
}

func TestEditSendsTheImages(t *testing.T) {
	s, e := easeled(t)
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	code, _, raw := postImages(t, s, "/v1/images/edits", "application/json", `{"model":"art/gpt-image-1","prompt":"make it blue","images":[{"image_url":"`+img+`"},{"image_url":"`+img+`"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	e.mu.Lock()
	ct := e.ct["/v1/images/edits"]
	e.mu.Unlock()
	sent := e.got("/v1/images/edits")
	if !strings.HasPrefix(ct, "multipart/form-data") || len(sent) != 1 || strings.Count(sent[0], `name="image[]"`) != 2 || !strings.Contains(sent[0], "make it blue") {
		t.Fatalf("vendor was sent %s %v", ct, sent)
	}
	// the chat model is given them in its message
	code, _, raw = postImages(t, s, "/v1/images/edits", "application/json", `{"model":"art/painter-image-preview","prompt":"make it blue","image":"`+img+`"}`)
	if code != 200 || !strings.Contains(e.got("/v1/chat/completions")[0], img) {
		t.Fatalf("%d %s", code, raw)
	}
	// an edit with no image is turned away
	if code, _, _ := postImages(t, s, "/v1/images/edits", "application/json", `{"model":"art/gpt-image-1","prompt":"x"}`); code != 400 {
		t.Fatalf("edit without image: %d", code)
	}
}

func TestDrawerIsTheSettingOrAutomatic(t *testing.T) {
	s, e := easeled(t)
	// no model named: magpie picks one the provider can draw with
	if m, ok := drawer(); !ok || !strings.HasPrefix(m, "art/") {
		t.Fatalf("drawer = %q %v", m, ok)
	}
	st := settings.Load()
	st.ImageGen = "art/painter-image-preview"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"prompt":"a magpie"}`)
	if code != 200 || a.Model != "art/painter-image-preview" || len(e.got("/v1/chat/completions")) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	st.ImageGen = "off"
	settings.Save(st)
	if code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"prompt":"a magpie"}`); code != 400 || !strings.Contains(raw, "Image generation") {
		t.Fatalf("off: %d %s", code, raw)
	}
}

func TestVendorFailureIsSaid(t *testing.T) {
	s, _ := easeled(t)
	p, _ := provider.Find("art")
	p.Key = "wrong"
	provider.Save(*p)
	code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"art/gpt-image-1","prompt":"a magpie"}`)
	if code != 401 || !strings.Contains(raw, "401") {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestViaFor(t *testing.T) {
	google := provider.Provider{Chat: "https://generativelanguage.googleapis.com/v1beta/openai"}
	router := provider.Provider{Chat: "https://openrouter.ai/api/v1"}
	openai := provider.Provider{Chat: "https://api.openai.com/v1"}
	for _, tc := range []struct {
		p     provider.Provider
		model string
		want  drawVia
	}{
		{google, "gemini-2.5-flash-image", viaGemini},
		{google, "imagen-4.0-generate-001", viaImages},
		{router, "openai/gpt-5-image", viaChat},
		{router, "google/gemini-2.5-flash-image", viaChat},
		{openai, "gpt-image-1", viaImages},
		{openai, "dall-e-3", viaImages},
		{openai, "gpt-5-image", viaChat},
	} {
		if got := viaFor(tc.p, tc.model); got != tc.want {
			t.Errorf("%s at %s: %s, want %s", tc.model, tc.p.Chat, got, tc.want)
		}
	}
}

func TestAspectOf(t *testing.T) {
	for size, want := range map[string]string{"1024x1024": "1:1", "1536x1024": "3:2", "1024x1536": "2:3", "1792x1024": "16:9", "auto": "", "": "", "4:3": "4:3"} {
		if got := aspectOf(size); got != want {
			t.Errorf("aspectOf(%q) = %q, want %q", size, got, want)
		}
	}
}

func TestCatalogDrawers(t *testing.T) {
	fresh(t)
	for _, id := range catalog.Providers() {
		for _, m := range catalog.Drawers(id) {
			if l := strings.ToLower(m.ID); strings.Contains(l, "deep-research") || strings.HasSuffix(l, "/auto") {
				t.Errorf("%s/%s is a drawer", id, m.ID)
			}
		}
	}
}

// A vendor's own list that names image models: they draw, and they aren't
// among the models offered to talk to.
func TestListedImageModelsDraw(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"gpt-5.5"},{"id":"gpt-image-2"},{"id":"flux-kontext-pro"},{"id":"text-embedding-3-small"}]}`)
	}))
	defer up.Close()
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Key: "key"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	ms, err := p.Fetch(t.Context())
	if err != nil || len(ms) != 1 || ms[0].ID != "gpt-5.5" {
		t.Fatalf("fetched %v %v", ms, err)
	}
	if live, _, _ := catalog.Live("relay"); len(live) != 1 {
		t.Fatalf("live %v", live)
	}
	var ids []string
	for _, m := range Drawers(p) {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "gpt-image-2,flux-kontext-pro" {
		t.Fatalf("drawers %v", ids)
	}
}

// A ChatGPT account draws with GPT Image at its Codex backend's images API,
// signed as Codex, and Automatic picks it, as the plan pays for it.
func TestCodexAccountDraws(t *testing.T) {
	codexSignedIn(t)
	var mu sync.Mutex
	var paths, bodies []string
	var head http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths, bodies, head = append(paths, r.URL.Path), append(bodies, string(b)), r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"created":1,"output_format":"png","data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`","generation_id":"g1"}],"usage":{"input_tokens":5,"output_tokens":196}}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if ds := Drawers(*p); len(ds) != 2 || ds[0].ID != "gpt-image-2" || ds[1].ID != "gpt-image-2.5" {
		t.Fatalf("drawers %v", ds)
	}
	if m, ok := drawer(); !ok || m != "codex/gpt-image-2.5" {
		t.Fatalf("drawer = %q %v", m, ok)
	}
	s := New()
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"codex/gpt-image-2","prompt":"a magpie","size":"1024x1024"}`)
	if code != 200 || len(a.Data) != 1 || a.Usage.Output != 196 {
		t.Fatalf("%d %s", code, raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if paths[0] != "/backend-api/codex/images/generations" || !strings.Contains(bodies[0], `"model":"gpt-image-2"`) {
		t.Fatalf("asked %v %v", paths, bodies)
	}
	if head.Get("chatgpt-account-id") != "acct-1" || head.Get("Accept") != "application/json" || head.Get("originator") != "codex_cli_rs" || head.Get("x-codex-imagegen-request-id") == "" {
		t.Fatalf("headers %v", head)
	}
	// an edit goes as JSON, the image a data URL: the backend turns multipart away
	mu.Unlock()
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	code, _, raw = postImages(t, s, "/v1/images/edits", "application/json", `{"model":"codex/gpt-image-2","prompt":"bluer","images":[{"image_url":"`+img+`"}]}`)
	mu.Lock()
	if code != 200 || paths[1] != "/backend-api/codex/images/edits" || head.Get("Content-Type") != "application/json" || !strings.Contains(bodies[1], `"images":[{"image_url":"data:image/png;base64,`) {
		t.Fatalf("%d %s; asked %v %s", code, raw, paths, head.Get("Content-Type"))
	}
}

// grokSignedIn gives the test's HOME the Grok CLI's sign-in, and a CLI to find.
func grokSignedIn(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	os.MkdirAll(filepath.Join(home, ".grok"), 0o755)
	os.WriteFile(filepath.Join(home, ".grok", "auth.json"), mustJSON(map[string]any{
		"https://auth.x.ai": map[string]any{"key": "k-me", "email": "me@x.ai", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}}), 0o600)
	was := provider.GrokExecutable
	provider.GrokExecutable = func() string { return "/nonexistent/grok" }
	t.Cleanup(func() { provider.GrokExecutable = was })
}

// A Grok subscription draws at the Imagine API of the backend Grok Build talks
// to: the aspect ratio replaces the size, the image comes back as base64, an
// edit is JSON with the images as data URLs.
func TestGrokAccountDraws(t *testing.T) {
	grokSignedIn(t)
	var mu sync.Mutex
	var paths, bodies []string
	var head http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths, bodies, head = append(paths, r.URL.Path), append(bodies, string(b)), r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
	}))
	defer up.Close()
	was := provider.GrokBase
	provider.GrokBase = up.URL + "/v1"
	defer func() { provider.GrokBase = was }()
	p, err := provider.Find("grok")
	if err != nil {
		t.Fatal(err)
	}
	if ds := Drawers(*p); len(ds) != 2 || ds[0].ID != "grok-imagine-image" || ds[1].ID != "grok-imagine-image-quality" || ds[0].Provider != "grok" {
		t.Fatalf("drawers %v", ds)
	}
	if m, ok := drawer(); !ok || m != "grok/grok-imagine-image" {
		t.Fatalf("drawer = %q %v", m, ok)
	}
	s := New()
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"grok/grok-imagine-image","prompt":"a magpie","size":"1792x1024"}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if paths[0] != "/v1/images/generations" || !strings.Contains(bodies[0], `"model":"grok-imagine-image"`) ||
		!strings.Contains(bodies[0], `"response_format":"b64_json"`) || !strings.Contains(bodies[0], `"aspect_ratio":"16:9"`) || strings.Contains(bodies[0], `"size"`) {
		t.Fatalf("asked %v %v", paths, bodies)
	}
	if head.Get("Authorization") != "Bearer k-me" || head.Get("x-grok-client-identifier") != "grok-shell" || !strings.HasPrefix(head.Get("User-Agent"), "grok-shell/") {
		t.Fatalf("headers %v", head)
	}
	mu.Unlock()
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	code, _, raw = postImages(t, s, "/v1/images/edits", "application/json", `{"model":"grok/grok-imagine-image","prompt":"bluer","images":[{"image_url":"`+img+`"}]}`)
	mu.Lock()
	if code != 200 || paths[1] != "/v1/images/edits" || head.Get("Content-Type") != "application/json" || !strings.Contains(bodies[1], `"images":[{"type":"image_url","url":"data:image/png;base64,`) {
		t.Fatalf("%d %s; asked %v %s", code, raw, paths, bodies[1])
	}
}

func TestAspectAmong(t *testing.T) {
	for size, want := range map[string]string{
		"1792x1024": "16:9", "1024x1792": "9:16", "1024x1024": "1:1", "2048x1024": "2:1", "1536x1024": "3:2",
		"16:9": "16:9", "garbage": "", "": "", "0x10": "",
	} {
		if got := aspectAmong(size, grokAspects); got != want {
			t.Errorf("aspectAmong(%q, grok) = %q, want %q", size, got, want)
		}
	}
	// the ratios the other vendors take are unchanged
	if got := aspectOf("1024x1280"); got != "4:5" {
		t.Errorf("aspectOf(1024x1280) = %q, want 4:5", got)
	}
}

// What a Grok account's image requests meet beyond the first drawing: several
// images at once, fields its Imagine API has no use for, and the ways the
// vendor says no.
func TestGrokDrawsSeveralAndIgnoresWhatItHasNoFieldFor(t *testing.T) {
	grokSignedIn(t)
	up := newGrokMedia(t)
	s := New()
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json",
		`{"model":"grok/grok-imagine-image","prompt":"two magpies","n":2,"quality":"high","background":"transparent","output_format":"png","size":"1024x1024"}`)
	if code != 200 || len(a.Data) != 2 {
		t.Fatalf("%d %s", code, raw)
	}
	sent := up.body("/v1/images/generations", 0)
	for _, no := range []string{`"quality"`, `"background"`, `"output_format"`, `"size"`} {
		if strings.Contains(sent, no) {
			t.Fatalf("Grok was sent %s: %s", no, sent)
		}
	}
	if !strings.Contains(sent, `"n":2`) || !strings.Contains(sent, `"aspect_ratio":"1:1"`) {
		t.Fatalf("asked %s", sent)
	}
}

func TestGrokVendorFailuresAreSaid(t *testing.T) {
	grokSignedIn(t)
	newGrokMedia(t)
	s := New()
	for _, c := range []struct {
		prompt string
		code   int
		want   string
	}{
		{"RATELIMITED", 429, "image generation limit"},
		{"REFUSED", 400, "content moderation"},
		{"BROKEN", 500, "upstream exploded"},
	} {
		code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"grok/grok-imagine-image","prompt":"`+c.prompt+`"}`)
		if code != c.code || !strings.Contains(raw, c.want) {
			t.Errorf("%s: %d %s", c.prompt, code, raw)
		}
	}
}
