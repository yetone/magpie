package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func TestKnownTextOnlyModelRejectsImagesBeforeUpstream(t *testing.T) {
	fresh(t)
	noVision(t)
	var sent int
	var model string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent++
		var req struct{ Model string }
		json.NewDecoder(r.Body).Decode(&req)
		model = req.Model
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "probe", Name: "Probe", Chat: up.URL + "/v1", Key: "key", Models: []string{"text", "vision", "unknown"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("probe", up.URL+"/v1", []catalog.Model{
		{ID: "text", ImageInput: imageInputBool(false)},
		{ID: "vision", Images: true, ImageInput: imageInputBool(true)},
		{ID: "unknown"},
	}); err != nil {
		t.Fatal(err)
	}
	s := New()
	cases := []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"probe/text","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`},
		{"/v1/responses", `{"model":"probe/text","input":[{"role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}]}`},
		{"/v1/messages", `{"model":"probe/text","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`},
		{"/v1beta/models/probe/text:generateContent", `{"contents":[{"role":"user","parts":[{"text":"read"},{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}]}`},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "does not support image input") {
			t.Errorf("%s: %d %s", tc.path, rec.Code, rec.Body.String())
		}
		// it says why magpie takes it for text-only, and where that is
		// changed (KimYx0207 on X)
		if b := rec.Body.String(); !strings.Contains(b, "Probe's model list marks it as not taking images") || !strings.Contains(b, "Providers → Probe → Names, prices & levels") {
			t.Errorf("%s: doesn't say why or where: %s", tc.path, b)
		}
	}
	if sent != 0 {
		t.Fatalf("text-only images reached upstream %d times", sent)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Mixed", Members: []string{"probe/text", "probe/vision"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	groupBody := strings.Replace(cases[0].body, "probe/text", "group/mixed", 1)
	group := httptest.NewRecorder()
	s.Handler().ServeHTTP(group, httptest.NewRequest("POST", cases[0].path, strings.NewReader(groupBody)))
	// a group with a member that sees takes the image, and gives it to
	// that member, though the text-only one comes first (#756)
	if group.Code != 200 || sent != 1 || model != "vision" {
		t.Fatalf("mixed-capability group: %d %s; upstream sent %d, to %q", group.Code, group.Body.String(), sent, model)
	}
	code, body := postAs(t, s, "", `{"model":"probe/text","messages":[{"role":"user","content":"the text image_url is not an image"}]}`)
	if code != 200 {
		t.Fatalf("text-only model with text input: %d %s", code, body)
	}
	for _, model := range []string{"vision", "unknown"} {
		body := strings.Replace(cases[0].body, "probe/text", "probe/"+model, 1)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", cases[0].path, strings.NewReader(body)))
		if rec.Code != 200 {
			t.Errorf("%s image: %d %s", model, rec.Code, rec.Body.String())
		}
	}
	if sent != 4 {
		t.Fatalf("group, text, vision, and unknown requests sent %d times", sent)
	}
}

func imageInputBool(v bool) *bool { return &v }

func TestVisionModelKeepsAnthropicToolResultImage(t *testing.T) {
	fresh(t)
	var sent string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sent = string(body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "vision", Anthropic: up.URL, Key: "key", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("vision", up.URL, []catalog.Model{{ID: "m", Images: true, ImageInput: imageInputBool(true)}}); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"vision/m","max_tokens":16,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}]}`
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
	if rec.Code != 200 || !strings.Contains(sent, "aGVsbG8=") {
		t.Fatalf("vision tool-result image: %d %s; upstream %s", rec.Code, rec.Body.String(), sent)
	}
}

func TestAnthropicTextOnlyToolResultImage(t *testing.T) {
	fresh(t)
	var sent []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sent = append(sent, string(body))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "text", Anthropic: up.URL, Key: "key", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("text", up.URL, []catalog.Model{{ID: "m", ImageInput: imageInputBool(false)}}); err != nil {
		t.Fatal(err)
	}
	image := `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`
	current := `{"model":"text/m","max_tokens":16,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]},{"type":"text","text":"now answer"}]}]}`
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(current)))
	if rec.Code != 200 || len(sent) != 1 || strings.Contains(sent[0], "aGVsbG8=") || !strings.Contains(sent[0], "Image omitted") {
		t.Fatalf("current tool-result image: %d %s; upstream %v", rec.Code, rec.Body.String(), sent)
	}
	history := `{"model":"text/m","max_tokens":16,"messages":[` + image + `,{"role":"assistant","content":"seen"},{"role":"user","content":"now answer"}]}`
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(history)))
	if rec.Code != 200 || len(sent) != 2 || strings.Contains(sent[1], "aGVsbG8=") || !strings.Contains(sent[1], "Image omitted") {
		t.Fatalf("historical tool-result image: %d %s; upstream %v", rec.Code, rec.Body.String(), sent)
	}
	mixed := `{"model":"text/m","max_tokens":16,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(mixed)))
	if rec.Code != 400 || len(sent) != 2 {
		t.Fatalf("current user image beside tool result: %d %s; upstream %v", rec.Code, rec.Body.String(), sent)
	}
}

func TestTextOnlyModelOmitsToolImages(t *testing.T) {
	for _, tc := range []struct {
		name, path, endpoint, body string
	}{
		{"responses-current", "/v1/responses", "responses", `{"model":"probe/text","input":[{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"screenshot"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]},{"role":"user","content":[{"type":"input_text","text":"answer"}]}]}`},
		{"responses-last", "/v1/responses", "responses", `{"model":"probe/text","input":[{"role":"user","content":[{"type":"input_text","text":"read"}]},{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}]}`},
		{"responses-to-chat", "/v1/responses", "chat", `{"model":"probe/text","input":[{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"screenshot"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]},{"role":"user","content":[{"type":"input_text","text":"answer"}]}]}`},
		{"chat-tool", "/v1/chat/completions", "chat", `{"model":"probe/text","messages":[{"role":"user","content":"look"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"shot","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"screenshot"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			var sent string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				sent = string(body)
				if strings.Contains(sent, `"stream":true`) {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, sse(
						`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
						`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
						`data: [DONE]`,
					))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"id":"x","output":[],"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer up.Close()
			p := provider.Provider{ID: "probe", Key: "key", Models: []string{"text"}}
			if tc.endpoint == "responses" {
				p.Responses = up.URL + "/v1"
			} else {
				p.Chat = up.URL + "/v1"
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			if err := catalog.SaveLive("probe", up.URL+"/v1", []catalog.Model{{ID: "text", ImageInput: imageInputBool(false)}}); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
			if rec.Code != 200 || strings.Contains(sent, "aGVsbG8=") || !strings.Contains(sent, "Image omitted") {
				t.Fatalf("tool image: %d %s; upstream %s", rec.Code, rec.Body.String(), sent)
			}
			if tc.name == "responses-to-chat" {
				// an orphaned tool result reaches a Chat upstream as a
				// user message: no call pairs with it, no id is made up
				if strings.Contains(sent, "call_1") || !strings.Contains(sent, "screenshot") {
					t.Fatalf("orphaned tool result: %s", sent)
				}
			} else if !strings.Contains(sent, "call_1") {
				t.Fatalf("tool result lost its call id: %s", sent)
			}
			if tc.endpoint == "responses" {
				var request struct {
					Input []struct {
						Type   string
						CallID string `json:"call_id"`
						Output []struct{ Type, Text string }
					}
				}
				if err := json.Unmarshal([]byte(sent), &request); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, item := range request.Input {
					if item.Type != "function_call_output" {
						continue
					}
					found = true
					if item.CallID != "call_1" || len(item.Output) == 0 || item.Output[len(item.Output)-1].Type != "input_text" || !strings.Contains(item.Output[len(item.Output)-1].Text, "Image omitted") {
						t.Fatalf("invalid Responses tool output: %+v", item)
					}
				}
				if !found {
					t.Fatalf("Responses function_call_output missing: %s", sent)
				}
			}
		})
	}
}

func TestTextOnlyModelOmitsHistoricalImages(t *testing.T) {
	fresh(t)
	var sent []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sent = append(sent, string(body))
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
				`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				`data: [DONE]`,
			))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "probe", Chat: up.URL + "/v1", Key: "key", Models: []string{"text"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("probe", up.URL+"/v1", []catalog.Model{{ID: "text", ImageInput: imageInputBool(false)}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"probe/text","custom_flag":12345678901234567890,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]},{"role":"assistant","content":"seen"},{"role":"user","content":"now answer"}]}`},
		{"/v1/responses", `{"model":"probe/text","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]},{"role":"assistant","content":[{"type":"output_text","text":"seen"}]},{"role":"user","content":[{"type":"input_text","text":"now answer"}]}]}`},
		{"/v1/messages", `{"model":"probe/text","max_tokens":16,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]},{"role":"assistant","content":[{"type":"text","text":"seen"}]},{"role":"user","content":[{"type":"text","text":"now answer"}]}]}`},
		{"/messages", `{"model":"probe/text","max_tokens":16,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]},{"role":"assistant","content":[{"type":"text","text":"seen"}]},{"role":"user","content":[{"type":"text","text":"now answer"}]}]}`},
		{"/v1beta/models/probe/text:generateContent", `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]},{"role":"model","parts":[{"text":"seen"}]},{"role":"user","parts":[{"text":"now answer"}]}]}`},
	} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
		if rec.Code != 200 {
			t.Errorf("%s: %d %s", tc.path, rec.Code, rec.Body.String())
		}
	}
	if len(sent) != 5 {
		t.Fatalf("upstream received %d requests, want 5", len(sent))
	}
	for i, body := range sent {
		if strings.Contains(body, "aGVsbG8=") || !strings.Contains(body, "Image omitted") || !strings.Contains(body, "now answer") {
			t.Errorf("upstream request %d: %s", i, body)
		}
	}
	if !strings.Contains(sent[0], `"custom_flag":12345678901234567890`) {
		t.Errorf("Chat request lost unrelated field: %s", sent[0])
	}
}

func TestGroupWithUnknownImageCapabilityReachesUpstream(t *testing.T) {
	fresh(t)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"models":{"claude-sonnet-4-5":{"id":"claude-sonnet-4-5","modalities":{"input":["text","image"]}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	var sent []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"claude-sonnet-4-5"}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		sent = append(sent, string(body))
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
				`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				`data: [DONE]`,
			))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	for _, p := range []provider.Provider{
		{ID: "confirmed", Catalog: "anthropic", Chat: up.URL + "/v1", Key: "key", Models: []string{"claude-sonnet-4-5"}},
		{ID: "unknown", Chat: up.URL + "/v1", Key: "key", Models: []string{"claude-sonnet-4-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	unknown, err := provider.Find("unknown")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unknown.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"group/auto-claude-sonnet-4-5","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`},
		{"/v1/responses", `{"model":"group/auto-claude-sonnet-4-5","input":[{"role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}]}`},
		{"/v1/messages", `{"model":"group/auto-claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`},
		{"/messages", `{"model":"group/auto-claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`},
	} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
		if rec.Code != 200 {
			t.Errorf("%s: %d %s", tc.path, rec.Code, rec.Body.String())
		}
	}
	if len(sent) != 4 {
		t.Fatalf("upstream received %d requests, want 4", len(sent))
	}
	for i, body := range sent {
		if !strings.Contains(body, "aGVsbG8=") {
			t.Errorf("upstream request %d lost image: %s", i, body)
		}
	}
	for _, e := range provider.Catalog() {
		if e.ID == "auto/claude-sonnet-4-5" {
			if !e.Images || e.ImageInput == nil || !*e.ImageInput {
				t.Fatalf("group image capability: %+v", e)
			}
			return
		}
	}
	t.Fatal("auto group missing")
}

func TestFailedKeyFetchKeepsUnknownImageCapabilityAtGateway(t *testing.T) {
	fresh(t)
	failing := false
	var sent int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if failing && r.Header.Get("Authorization") == "Bearer failed-key" {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			modalities := `,"modalities":{"input":["text","image"]}`
			if r.Header.Get("Authorization") == "Bearer failed-key" {
				modalities = ""
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"shared"`+modalities+`}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "aGVsbG8=") {
			sent++
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	p := provider.Provider{ID: "relay", Chat: up.URL + "/v1", Key: "vision-key", Keys: []provider.KeyAccount{{Key: "failed-key"}}, Models: []string{"shared"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	failing = true
	models, err := p.Fetch(t.Context())
	if err != nil || len(models) != 1 || models[0].ImageInput != nil {
		t.Fatalf("failed key fallback became text-only: %+v, %v", models, err)
	}
	body := `{"model":"relay/shared","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != 200 || sent != 1 {
		t.Fatalf("image after failed key fetch: %d %s; upstream got %d images", rec.Code, rec.Body.String(), sent)
	}
}

// Gemini fileData also carries documents and audio. A text-only model must
// reject images, but it must not silently discard a non-image file part.
func TestGeminiTextOnlyBodyKeepsNonImageFileData(t *testing.T) {
	for _, tc := range []struct {
		mime    string
		blocked bool
	}{
		{"application/pdf", false},
		{"audio/wav", false},
		{"image/png", true},
	} {
		source := `{"contents":[{"role":"user","parts":[{"text":"read this"},{"fileData":{"mimeType":"` + tc.mime + `","fileUri":"gs://bucket/file"}}]}]}`
		body, blocked := textOnlyBody(provider.Gemini, []byte(source))
		if blocked != tc.blocked {
			t.Errorf("%s blocked=%v, want %v", tc.mime, blocked, tc.blocked)
		}
		if !tc.blocked && !strings.Contains(string(body), "gs://bucket/file") {
			t.Errorf("%s was stripped: %s", tc.mime, body)
		}
	}
}

// A non-image Gemini file must not become image_url when routed to Chat.
func TestGeminiNonImageFileTranslation(t *testing.T) {
	for _, tc := range []struct {
		name, mime, field, value string
	}{
		{"file-pdf", "application/pdf", "fileData", `"fileUri":"gs://bucket/file.pdf"`},
		{"file-audio", "audio/wav", "fileData", `"fileUri":"gs://bucket/file.wav"`},
		{"inline-pdf", "application/pdf", "inlineData", `"data":"cGRm"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := `{"contents":[{"role":"user","parts":[{"text":"read this"},{"` + tc.field + `":{"mimeType":"` + tc.mime + `",` + tc.value + `}}]}]}`
			body, blocked := textOnlyBody(provider.Gemini, []byte(source))
			if blocked {
				t.Fatal("non-image attachment blocked as image")
			}
			r, err := parseGemini(body)
			if err != nil {
				t.Fatal(err)
			}
			claude, err := renderClaudePrompt(r)
			if err != nil {
				t.Fatal(err)
			}
			prompt, err := json.Marshal(claude)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(prompt), "[attachment "+tc.mime) || strings.Contains(string(prompt), `"type":"image"`) {
				t.Fatalf("Claude subscription lost or mislabeled file: %s", prompt)
			}
			chat := buildChat(r, "text", "", false)
			if strings.Contains(string(chat), `"image_url"`) || !strings.Contains(string(chat), "[attachment "+tc.mime) {
				t.Fatalf("non-image attachment translated as an image or lost: %s", chat)
			}
			// Code Assist speaks Gemini's file format and must keep the data.
			assist := buildCodeAssist(r, "gemini-2.5-pro", "gemini")
			if !strings.Contains(string(assist), `"`+tc.field+`"`) || !strings.Contains(string(assist), tc.value) {
				t.Fatalf("Gemini attachment lost in Code Assist: %s", assist)
			}
			if strings.Contains(string(assist), "[attachment ") {
				t.Fatalf("Code Assist received a placeholder instead of the file: %s", assist)
			}
			responses := buildResponses(r, "text", "", false)
			anthropic := buildAnthropic(r, "text")
			for protocol, out := range map[string][]byte{"Responses": responses, "Anthropic": anthropic} {
				if strings.Contains(string(out), `"image_url"`) || strings.Contains(string(out), `"type":"image"`) || !strings.Contains(string(out), "[attachment "+tc.mime) {
					t.Errorf("%s lost or mislabeled file: %s", protocol, out)
				}
			}
		})
	}
}

// A model the user marked text-only themselves is turned away saying so,
// and a group with no member that takes images says that, each with where
// it is changed (KimYx0207 on X).
func TestTextOnlyRefusalSaysWhyAndWhere(t *testing.T) {
	fresh(t)
	noVision(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("an image for a text-only model reached upstream: %s", r.URL)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "probe", Name: "Probe", Chat: up.URL + "/v1", Key: "key", Models: []string{"sol", "text"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("probe", up.URL+"/v1", []catalog.Model{
		{ID: "sol", Images: true, ImageInput: imageInputBool(true)},
		{ID: "text", ImageInput: imageInputBool(false)},
	}); err != nil {
		t.Fatal(err)
	}
	s0 := settings.Load()
	s0.ModelImages = map[string]bool{"probe/sol": false}
	if err := settings.Save(s0); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Words", Members: []string{"probe/text", "probe/sol"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	body := `{"model":"MODEL","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`
	for model, want := range map[string]string{
		"probe/sol":   "Accepts images is turned off for it in magpie, in Providers → Probe → Names, prices & levels",
		"group/words": "no model in the group Words is marked as taking images",
	} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(strings.Replace(body, "MODEL", model, 1))))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s, want %q", model, rec.Code, rec.Body.String(), want)
		}
	}
}

// Every request's images are looked for several times (hasImage,
// textOnlyBody, the vision checks), and its conversation's id hashed:
// without an image, the turns are read where they are, not decoded turn by
// turn and block by block. A 1.5 MB conversation cost ~2.6 MB a walk and
// ~2.8 MB an id (yg6888 on X). An image is still found where it hides:
// last, in a tool's output, under an escaped key, under a key given twice.
func TestImageWalkReadsTextOnlyTurnsInPlace(t *testing.T) {
	var turns []string
	for i := range 200 {
		turns = append(turns, `{"role":"user","content":[{"type":"text","text":"`+strings.Repeat("read this file ", 300)+`"}]}`,
			`{"role":"assistant","content":"ok `+strings.Repeat("x", i)+`"}`)
	}
	body := []byte(`{"model":"m","messages":[` + strings.Join(turns, ",") + `]}`)
	ctx, done := context.WithCancel(context.Background())
	defer done()
	holdBody(ctx, body) // as readRequestBody holds a request's
	id := conversationID(http.Header{}, body)
	if hasImage(provider.Chat, body) {
		t.Fatal("an image found in a text-only body")
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	const n = 5
	for range n {
		hasImage(provider.Chat, body)
		textOnlyBody(provider.Chat, body)
		if conversationID(http.Header{}, body) != id {
			t.Fatal("the conversation's id changed")
		}
	}
	runtime.ReadMemStats(&after)
	if per := (after.TotalAlloc - before.TotalAlloc) / n; per > uint64(len(body))/8 {
		t.Fatalf("two image walks and an id of a %d-byte text-only body allocated %d bytes", len(body), per)
	}

	head := `{"model":"m","messages":[` + strings.Join(turns[:4], ",") + ","
	for i, c := range []struct {
		proto provider.Protocol
		body  string
	}{
		{provider.Chat, head + `{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/a.png"}}]}]}`},
		{provider.Chat, head + `{"role":"user","content":[{"%u0074ype":"%u0069mage_url","image_url":{"url":"https://x/a.png"}}]}]}`},
		{provider.Chat, head + `{"role":"user","content":"hi","content":[{"type":"text","type":"image_url","image_url":"https://x/a.png"}]}]}`},
		{provider.Chat, head + `{"role":"tool","tool_call_id":"c","content":[{"type":"image_url","image_url":{"url":"https://x/a.png"}}]},{"role":"user","content":"go"}]}`},
		{provider.Responses, `{"input":[{"type":"function_call_output","call_id":"c","output":[{"type":"input_image","image_url":"https://x/a.png"}]},{"role":"user","content":"go"}]}`},
		{provider.Anthropic, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"url","url":"https://x/a.png"}}]}]}]}`},
		{provider.Gemini, `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"f","parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}]}]}`},
		{provider.Gemini, `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}]}`},
	} {
		if !hasImage(c.proto, []byte(strings.ReplaceAll(c.body, "%u", "\x5cu"))) {
			t.Errorf("case %d (%v): its image was not found", i, c.proto)
		}
	}
}

// An agent's turns are mostly tool results, and nearly all of them text:
// the image walk looks into each one where it is, as it does a turn,
// rather than decoding the request whenever a tool_result or a
// functionResponse is there (#3725). An image a tool gave back is still
// found, however its block is spelled.
func TestImageWalkReadsToolResultsInPlace(t *testing.T) {
	var turns []string
	for i := range 60 {
		turns = append(turns,
			`{"role":"assistant","content":[{"type":"tool_use","id":"t`+fmt.Sprint(i)+`","name":"Read","input":{"path":"a.go"}}]}`,
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t`+fmt.Sprint(i)+`","content":[{"type":"text","text":"`+strings.Repeat("package a ", 100)+`"}]},{"type":"tool_result","tool_use_id":"u","content":"done"}]}`)
	}
	body := []byte(`{"model":"m","messages":[` + strings.Join(turns, ",") + `]}`)
	if hasImage(provider.Anthropic, body) {
		t.Fatal("an image found in a text-only body")
	}
	if per := testing.AllocsPerRun(5, func() { hasImage(provider.Anthropic, body) }); per > 4 {
		t.Fatalf("looking for an image in a %d-byte body of text tool results took %v allocations", len(body), per)
	}
	gem := []byte(`{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"out":"` + strings.Repeat("x", 4000) + `"}}}]}]}`)
	if hasImage(provider.Gemini, gem) {
		t.Fatal("an image found in a Gemini text-only function response")
	}
	if per := testing.AllocsPerRun(5, func() { hasImage(provider.Gemini, gem) }); per > 4 {
		t.Fatalf("looking for an image in a Gemini function response took %v allocations", per)
	}

	img := `{"type":"image","source":{"type":"url","url":"https://x/a.png"}}`
	for i, c := range []struct {
		proto provider.Protocol
		body  string
		want  bool
	}{
		{provider.Anthropic, `{"messages":[{"role":"user","content":[{"content":[` + img + `],"type":"tool_result","tool_use_id":"t"}]}]}`, true},
		{provider.Anthropic, `{"messages":[{"role":"user","content":[{"type":"tool_result","content":[{"type":"text","text":"a"}],"content":[` + img + `]}]}]}`, true},
		{provider.Anthropic, `{"messages":[{"role":"user","content":[{"type":"tool_result","content":[{"type":"tool_result","content":[` + img + `]}]}]}]}`, true},
		{provider.Anthropic, `{"messages":[{"role":"user","content":[{"%u0074ype":"tool_result","content":[` + img + `]}]}]}`, true},
		{provider.Anthropic, `{"messages":[{"role":"user","content":[{"type":"text","content":[` + img + `]}]}]}`, false},
		{provider.Anthropic, `{"messages":[{"role":"user","content":[{"type":"tool_result","content":"see a.png"}]}]}`, false},
		{provider.Gemini, `{"contents":[{"role":"user","parts":[{"functionResponse":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}],"name":"f"}}]}]}`, true},
		{provider.Gemini, `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"f","parts":[{"functionResponse":{"parts":[{"fileData":{"fileUri":"gs://a"}}]}}]}}]}]}`, true},
		{provider.Gemini, `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"f","parts":[{"text":"a"}]}}]}]}`, false},
	} {
		if got := hasImage(c.proto, []byte(strings.ReplaceAll(c.body, "%u", "\x5cu"))); got != c.want {
			t.Errorf("case %d (%v): hasImage = %v, want %v", i, c.proto, got, c.want)
		}
	}
}
