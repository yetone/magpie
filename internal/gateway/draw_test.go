package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
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

// Every account of a subscription held at its usage cap is told when to
// come back, as the text path tells it (cappedError's soonest, in a
// Retry-After): drawOnAccounts worked the same message out and dropped the
// time, so a client backing off a drawing had nothing to wait by.
func TestDrawCappedSaysWhenItIsBack(t *testing.T) {
	codexSignedIn(t)
	capUsage(t, map[string]float64{"me@example.com": 90}) // five hours at 90% of a 70% cap, renewing in three
	if err := provider.SetAccountCap("codex", "me@example.com", 70); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	s := New()
	_, _, code, back, err := s.drawOnAccounts(context.Background(), httptest.NewRequest("POST", "/v1/images/generations", nil), *p, "gpt-image-1", drawing{})
	if code != http.StatusTooManyRequests || err == nil {
		t.Fatalf("%d %v", code, err)
	}
	if !strings.Contains(err.Error(), "past its 70% cap") {
		t.Fatalf("error: %v", err)
	}
	if d := time.Until(back); d < 2*time.Hour || d > 4*time.Hour {
		t.Fatalf("back in %v, not the five hours' renewal", d)
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
		{router, "openai/gpt-image-2", viaImages},
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

// A model added by hand to a custom provider and marked Makes: Image model
// (a relay's jimeng-4.0, named for nothing magpie knows) is asked at the
// provider's images API, as the editor's menu says, not on chat
// completions first; listed under Image generation, it draws as the
// Settings' pick. On a provider with no images API (404) it is drawn on
// chat completions all the same.
func TestMarkedImageModelDrawsAtTheImagesAPI(t *testing.T) {
	s, e := easeled(t)
	p, _ := provider.Find("art")
	p.Models = append(p.Models, "jimeng-4.0")
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelKind("art/jimeng-4.0", provider.KindImage); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("art")
	if !slices.ContainsFunc(Drawers(*p), func(m catalog.Model) bool { return m.ID == "jimeng-4.0" }) {
		t.Fatalf("not listed to draw: %v", Drawers(*p))
	}
	st := settings.Load()
	st.ImageGen = "art/jimeng-4.0"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"prompt":"a magpie"}`)
	if code != 200 || a.Model != "art/jimeng-4.0" || len(a.Data) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	if got := e.got("/v1/images/generations"); len(got) != 1 || !strings.Contains(got[0], `"model":"jimeng-4.0"`) {
		t.Fatalf("images API sent %v", got)
	}
	if got := e.got("/v1/chat/completions"); len(got) != 0 {
		t.Fatalf("asked on chat first: %v", got)
	}

	// a relay with chat completions alone: the 404 sends it there
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"message":"not found"}}`)
			return
		}
		b64 := base64.StdEncoding.EncodeToString(pngBytes)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok","images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`+b64+`"}}]}}]}`)
	}))
	t.Cleanup(chat.Close)
	if err := provider.Save(provider.Provider{ID: "talk", Name: "Talk", Chat: chat.URL + "/v1", Key: "key", Models: []string{"painter-x"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelKind("talk/painter-x", provider.KindImage); err != nil {
		t.Fatal(err)
	}
	if code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"talk/painter-x","prompt":"a magpie"}`); code != 200 || len(a.Data) != 1 {
		t.Fatalf("chat-only relay: %d %s", code, raw)
	}
}

func TestOpenRouterImagesAPIDrawsImageModels(t *testing.T) {
	fresh(t)
	p := provider.Provider{ID: "openrouter", Name: "OpenRouter", Chat: "https://openrouter.ai/api/v1", Key: "key", Models: []string{"openai/gpt-image-2"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	var path, requestBody string
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		requestBody = string(b)
		response := `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(pngBytes) + `","media_type":"image/png"}],"usage":{"prompt_tokens":3,"completion_tokens":7}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	ref := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	code, answer, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"openrouter/openai/gpt-image-2","prompt":"a magpie","images":["`+ref+`"]}`)
	if code != http.StatusOK || len(answer.Data) != 1 || answer.Usage.Input != 3 || answer.Usage.Output != 7 {
		t.Fatalf("%d %s", code, raw)
	}
	if path != "/api/v1/images" {
		t.Fatalf("OpenRouter was asked at %q, want /api/v1/images", path)
	}
	var got struct {
		Model           string `json:"model"`
		Prompt          string `json:"prompt"`
		InputReferences []struct {
			Type     string `json:"type"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"input_references"`
	}
	if err := json.Unmarshal([]byte(requestBody), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "openai/gpt-image-2" || got.Prompt != "a magpie" || len(got.InputReferences) != 1 || got.InputReferences[0].Type != "image_url" || got.InputReferences[0].ImageURL.URL != ref {
		t.Fatalf("OpenRouter request: %s", requestBody)
	}
}

// An OpenRouter image model whose id names no images API (Riverflow,
// Recraft) draws at its /images when its list says it answers with an
// image alone, as /images/models lists it (iwinux on Discord); a chat image
// model stays on chat completions. A size in pixels goes as aspect_ratio
// and resolution: Riverflow V2 answered "1024x1024" with 400 "does not
// support explicit pixel sizes" on 2026-11-06.
func TestOpenRouterImageOnlyModelsDrawAtItsImageAPI(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "openrouter", Name: "OpenRouter", Chat: "https://openrouter.ai/api/v1", Key: "key"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("openrouter", "https://openrouter.ai/api/v1", []catalog.Model{
		{ID: "sourceful/riverflow-v2-fast", Draws: true, OutputModalities: []string{"image"}},
		{ID: "recraft/recraft-v4-vector", Draws: true, OutputModalities: []string{"image"}},
		{ID: "google/gemini-2.5-flash-image", Draws: true, OutputModalities: []string{"image", "text"}},
	}); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("openrouter")
	for model, want := range map[string]drawVia{
		"sourceful/riverflow-v2-fast":   viaImages,
		"recraft/recraft-v4-vector":     viaImages,
		"google/gemini-2.5-flash-image": viaChat,
		"openai/gpt-5-image":            viaChat,
	} {
		if got := viaFor(*p, model); got != want {
			t.Errorf("%s: %s, want %s", model, got, want)
		}
	}
	var path string
	var got map[string]any
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		json.NewDecoder(r.Body).Decode(&got)
		response := `{"created":1,"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(pngBytes) + `","media_type":"image/png"}],"usage":{"prompt_tokens":0,"completion_tokens":1,"total_tokens":1,"cost":0.02}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	for size, want := range map[string]map[string]any{
		"1024x1024": {"aspect_ratio": "1:1"},
		"1536x1024": {"aspect_ratio": "3:2", "resolution": "2K"},
		"16:9":      {"aspect_ratio": "16:9"},
		"auto":      {"size": "auto"},
	} {
		got, path = nil, ""
		code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"openrouter/sourceful/riverflow-v2-fast","prompt":"a magpie","size":"`+size+`"}`)
		if code != http.StatusOK || path != "/api/v1/images" {
			t.Fatalf("%s: %d at %q: %s", size, code, path, raw)
		}
		for _, k := range []string{"size", "aspect_ratio", "resolution"} {
			if got[k] != want[k] {
				t.Errorf("%s: %s = %v, want %v (%v)", size, k, got[k], want[k], got)
			}
		}
	}
}

func TestOpenRouterImagesAPIFailureIsReturnedWithoutChatRetry(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "openrouter", Name: "OpenRouter", Chat: "https://openrouter.ai/api/v1", Key: "key", Models: []string{"openai/gpt-image-2"}}); err != nil {
		t.Fatal(err)
	}
	var paths []string
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		message := `{"error":{"message":"image model is unavailable on the images endpoint"}}`
		return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(message))}, nil
	})}
	code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"openrouter/openai/gpt-image-2","prompt":"a magpie"}`)
	if code != http.StatusNotFound || !strings.Contains(raw, "images endpoint") {
		t.Fatalf("%d %s", code, raw)
	}
	if len(paths) != 1 || paths[0] != "/api/v1/images" {
		t.Fatalf("OpenRouter image request paths: %v", paths)
	}
}

func TestAspectOf(t *testing.T) {
	for size, want := range map[string]string{"1024x1024": "1:1", "1536x1024": "3:2", "1024x1536": "2:3", "1792x1024": "16:9", "auto": "", "": "", "4:3": "4:3"} {
		if got := aspectOf(size); got != want {
			t.Errorf("aspectOf(%q) = %q, want %q", size, got, want)
		}
	}
}

func TestVolcengineAgentPlanDrawers(t *testing.T) {
	p, err := provider.FromPreset("volcengine")
	if err != nil {
		t.Fatal(err)
	}
	pr := provider.Preset("volcengine")
	p.Chat, p.Responses, p.Anthropic = pr.Regions[1].Chat, pr.Regions[1].Responses, pr.Regions[1].Anthropic
	ds, want := Drawers(p), p.PlanDrawers()
	if len(ds) == 0 || !slices.EqualFunc(ds, want, func(a, b catalog.Model) bool { return a.ID == b.ID }) {
		t.Fatalf("Agent Plan drawers: %+v, want preset plan's %+v", ds, want)
	}
	p.Chat, p.Responses, p.Anthropic = pr.Regions[0].Chat, pr.Regions[0].Responses, pr.Regions[0].Anthropic
	if ds := Drawers(p); len(ds) != 0 {
		t.Fatalf("Coding Plan drawers: %+v", ds)
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
	// one: the backend picks the model itself (#1865)
	if ds := Drawers(*p); len(ds) != 1 || ds[0].ID != "gpt-image-2" || ds[0].Name != "GPT Image" {
		t.Fatalf("drawers %v", ds)
	}
	// Automatic asks for what Codex CLI asks for
	if m, ok := drawer(); !ok || m != "codex/gpt-image-2" {
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
	if head.Get("chatgpt-account-id") != "acct-1" || head.Get("Accept") != "application/json" || head.Get("originator") != "codex_cli_rs" || head.Get("x-codex-imagegen-request-id") == "" || head.Get("x-codex-image-turn-id") == "" {
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

// chatgpt.com's 403 for a ChatGPT account's images says what it likely
// means, and Automatic asks for the model Codex CLI does (#545).
func TestCodexDrawRefused(t *testing.T) {
	codexSignedIn(t)
	var models []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Model string }
		json.NewDecoder(r.Body).Decode(&req)
		models = append(models, req.Model)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"detail":"Forbidden"}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	code, _, raw := postImages(t, New(), "/v1/images/generations", "application/json", `{"prompt":"a magpie"}`)
	if code != 403 || !strings.Contains(raw, "Forbidden") || !strings.Contains(raw, "plan or workspace may not draw") {
		t.Fatalf("%d %s", code, raw)
	}
	if len(models) != 1 || models[0] != "gpt-image-2" {
		t.Fatalf("asked for %v", models)
	}
}

// A ChatGPT account whose plan won't draw (chatgpt.com: 403
// {"detail":"Forbidden"}) hands the drawing to the next account on, as a
// Plus account further down draws it (#545); the one refused doesn't rest
// for text over it, and the next drawing asks it first again.
func TestCodexDrawMovesToNextAccount(t *testing.T) {
	codexSignedIn(t, "plus@example.com")
	var mu sync.Mutex
	var tried []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		tried = append(tried, r.Header.Get("chatgpt-account-id"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("chatgpt-account-id") == "acct-1" {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"detail":"Forbidden"}`)
			return
		}
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}],"usage":{"input_tokens":5,"output_tokens":196}}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	s := New()
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"prompt":"a magpie"}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	mu.Lock()
	if strings.Join(tried, ",") != "acct-1,acct-2" {
		t.Fatalf("tried %v", tried)
	}
	tried = nil
	mu.Unlock()
	restingUntil.Lock()
	rests := len(restingUntil.m)
	restingUntil.Unlock()
	if rests != 0 {
		t.Fatalf("%d accounts rest after a drawing was refused", rests)
	}
	postImages(t, s, "/v1/images/generations", "application/json", `{"prompt":"a magpie"}`)
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(tried, ",") != "acct-1,acct-2" {
		t.Fatalf("second drawing tried %v", tried)
	}
}

// grokSignedIn gives the test Grok served the way it is now that its built-in
// is retired: the fake plugin moved onto "grok", its requests sent on to
// provider.GrokBase as it is when each arrives, so a test points GrokBase at
// its upstream as it did for the built-in.
func grokSignedIn(t *testing.T) {
	t.Helper()
	t.Setenv("FAKE_RESPONSES", "1")
	movedFake(t, "grok", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		to, err := url.Parse(provider.GrokBase + strings.TrimPrefix(r.URL.Path, "/v1"))
		if err == nil && to.Hostname() != "127.0.0.1" {
			err = fmt.Errorf("GrokBase %s isn't the test's: a test never reaches the network", provider.GrokBase)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		to.RawQuery = r.URL.RawQuery
		(&httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL, pr.Out.Host = to, to.Host
		}}).ServeHTTP(w, r)
	}))
	// the vendor's own host was the built-in's, whose video it fetched from
	// there; the plugin's host is the fake's, so the test's upstream on
	// loopback is named as a user names a private host
	allowPrivate(t, "127.0.0.1")
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
	// the plugin signs the request (the fake with its own key); the real
	// one's headers are the plugin's to test
	if head.Get("Authorization") != "Bearer k1" {
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

// #1857: grok-imagine-image-2.0 named bare, as the vendor names it, drew
// nothing ("magpie knows no model ... to draw with") while
// grok/grok-imagine-image-2.0 drew: the bare name was looked up in
// grokDrawers alone. Any grok-imagine-image* goes to the Grok subscription
// now, as any grok-imagine-video* does for videos; a name of no image
// family is still turned away.
func TestGrokDrawsANewerModelByItsBareName(t *testing.T) {
	grokSignedIn(t)
	up := newGrokMedia(t)
	s := New()
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"grok-imagine-image-2.0","prompt":"a red apple on a table","n":1,"size":"1024x1024"}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	if sent := up.body("/v1/images/generations", 0); !strings.Contains(sent, `"model":"grok-imagine-image-2.0"`) {
		t.Fatalf("asked %s", sent)
	}
	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"grok-imagine-nothing","prompt":"a red apple"}`)
	if code != 404 || !strings.Contains(raw, "knows no model") {
		t.Fatalf("a name of no image family: %d %s", code, raw)
	}
}

// The ChatGPT account's sibling of #1857: a gpt-image-* magpie doesn't list
// yet, named bare, is asked of the account, not turned away by magpie.
func TestCodexDrawsANewerModelByItsBareName(t *testing.T) {
	codexSignedIn(t)
	var mu sync.Mutex
	var bodies []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"created":1,"output_format":"png","data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	code, a, raw := postImages(t, New(), "/v1/images/generations", "application/json", `{"model":"gpt-image-3","prompt":"a magpie"}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"model":"gpt-image-3"`) {
		t.Fatalf("asked %v", bodies)
	}
}

// #1837: Agnes answers its image model on chat completions with 400 "is an
// image model. Use /v1/images/generations" (the reporter's words, as omp
// showed them). Its name reads like gemini's -flash image models, which
// chat, so magpie asks chat first; the vendor's refusal sends it to the
// images API, where it draws. The other way round, a refusal on the images
// API naming chat completions sends it to chat.
func TestDrawGoesWhereTheVendorPointsIt(t *testing.T) {
	fresh(t)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	var mu sync.Mutex
	var paths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		chatOnly := strings.Contains(string(body), "chat-drawer")
		switch r.URL.Path {
		case "/v1/chat/completions":
			if !chatOnly {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":{"message":"Model agnes-image-2.5-flash is an image model. Use /v1/images/generations","type":"invalid_request_error"}}`)
				return
			}
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok","images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`+b64+`"}}]}}]}`)
		case "/v1/images/generations":
			if chatOnly {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":{"message":"seedream-chat-drawer draws on /v1/chat/completions"}}`)
				return
			}
			io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+b64+`"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "agnes", Name: "Agnes", Chat: up.URL + "/v1", Key: "key", Models: []string{"agnes-image-2.5-flash", "seedream-chat-drawer"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, tc := range []struct {
		model string
		want  []string
	}{
		{"agnes/agnes-image-2.5-flash", []string{"/v1/chat/completions", "/v1/images/generations"}},
		{"agnes/seedream-chat-drawer", []string{"/v1/images/generations", "/v1/chat/completions"}},
	} {
		mu.Lock()
		paths = nil
		mu.Unlock()
		code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"`+tc.model+`","prompt":"a magpie"}`)
		if code != 200 || len(a.Data) != 1 {
			t.Fatalf("%s: %d %s", tc.model, code, raw)
		}
		mu.Lock()
		got := slices.Clone(paths)
		mu.Unlock()
		if !slices.Equal(got, tc.want) {
			t.Fatalf("%s asked %v, want %v", tc.model, got, tc.want)
		}
	}
}

// An account's own list of models (#474) is picked from the chat models
// its editor shows, which a ChatGPT account's GPT Image is never among: a
// list kept for some chat models doesn't turn its drawing away as "set
// not to be served by any account" (heyiwei5 on X). Each account draws
// as it did with no list.
func TestCodexDrawsPastAccountModelLists(t *testing.T) {
	codexSignedIn(t, "plus@example.com")
	var mu sync.Mutex
	var tried []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		tried = append(tried, r.Header.Get("chatgpt-account-id"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	for _, u := range []string{"me@example.com", "plus@example.com"} {
		if err := provider.SetAccountModels("codex", u, []string{"gpt-5.5"}); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	for _, model := range []string{"codex/gpt-image-2", "codex/gpt-image-2.5"} {
		code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"`+model+`","prompt":"a magpie"}`)
		if code != 200 || len(a.Data) != 1 {
			t.Fatalf("%s: %d %s", model, code, raw)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tried) != 2 {
		t.Fatalf("asked %v", tried)
	}
}
