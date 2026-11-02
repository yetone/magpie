package gateway

import (
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func TestDrawingResolvesLiveImageModels(t *testing.T) {
	fresh(t)
	e := &easel{}
	up := httptest.NewServer(e)
	defer up.Close()
	p := provider.Provider{ID: "relay", Name: "Relay", Responses: up.URL + "/v1", Key: "key"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(p.ID, p.Responses, []catalog.Model{
		{ID: "text"}, {ID: "gpt-image-2", Draws: true}, {ID: "gpt-image-2.5-flare", Draws: true},
	}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range provider.Catalog() {
		if strings.Contains(entry.Model, "gpt-image") {
			t.Fatalf("image model in chat catalog: %s", entry.ID)
		}
	}
	st := settings.Load()
	st.ImageGen = "relay/gpt-image-2.5-flare"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, model := range []string{"gpt-image-2", "gpt-image-2.5-flare", "relay/gpt-image-2"} {
		t.Run(model, func(t *testing.T) {
			code, answer, raw := postImages(t, s, "/v1/images/generations", "application/json",
				fmt.Sprintf(`{"model":%q,"prompt":"a magpie"}`, model))
			upstreamModel := strings.TrimPrefix(model, "relay/")
			if code != 200 || answer.Model != "relay/"+upstreamModel {
				t.Fatalf("%d %s", code, raw)
			}
			requests := e.got("/v1/images/generations")
			if !strings.Contains(requests[len(requests)-1], fmt.Sprintf(`"model":%q`, upstreamModel)) {
				t.Fatalf("wrong upstream model: %v", requests)
			}
		})
	}
	code, answer, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"prompt":"a magpie"}`)
	if code != 200 || answer.Model != st.ImageGen {
		t.Fatalf("default: %d %s", code, raw)
	}
	before := len(e.got("/v1/images/generations"))
	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"painter-missing","prompt":"a magpie"}`)
	if code != 404 || len(e.got("/v1/images/generations")) != before {
		t.Fatalf("unknown model: %d %s", code, raw)
	}
	st.ImageGen = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"gpt-image-2","prompt":"a magpie"}`)
	if code != 200 {
		t.Fatalf("explicit model with default off: %d %s", code, raw)
	}
	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"prompt":"a magpie"}`)
	if code != 400 {
		t.Fatalf("default off: %d %s", code, raw)
	}
	image := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	code, _, raw = postImages(t, s, "/v1/images/edits", "application/json",
		fmt.Sprintf(`{"model":"gpt-image-2","prompt":"a blue magpie","image":%q}`, image))
	if code != 200 || len(e.got("/v1/images/edits")) != 1 || len(e.got("/v1/chat/completions")) != 0 {
		t.Fatalf("edit: %d %s", code, raw)
	}
	e.mu.Lock()
	contentType := e.ct["/v1/images/edits"]
	body := e.sent["/v1/images/edits"][0]
	e.mu.Unlock()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	defer request.MultipartForm.RemoveAll()
	if request.FormValue("model") != "gpt-image-2" || len(request.MultipartForm.File["image"]) != 1 {
		t.Fatalf("edit model or image lost: %v", request.MultipartForm)
	}
	var editBody strings.Builder
	writer := multipart.NewWriter(&editBody)
	if err := writer.WriteField("model", "gpt-image-2.5-flare"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("prompt", "a blue magpie"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("image", "input.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(pngBytes); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	code, _, raw = postImages(t, s, "/v1/images/edits", writer.FormDataContentType(), editBody.String())
	if code != 200 || len(e.got("/v1/images/edits")) != 2 {
		t.Fatalf("multipart edit: %d %s", code, raw)
	}
}

// A Codex backend relay (sub2api) serves the ChatGPT backend's images API,
// which draws the same for any gpt-image-* name (#1865): the one Codex CLI
// asks its provider for (gpt-image-2) draws on the relay before the relay's
// list names it, as on a ChatGPT account (TestCodexDrawsANewerModelByIts-
// BareName), not turned away by magpie (#4432).
func TestRelayDrawsTheGPTImageFamilyByItsBareName(t *testing.T) {
	fresh(t)
	e := &easel{}
	up := httptest.NewServer(e)
	defer up.Close()
	p := provider.Provider{ID: "relay", Name: "Relay", Responses: up.URL + "/v1", Key: "key"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(p.ID, p.Responses, []catalog.Model{
		{ID: "text"}, {ID: "gpt-image-2.5-flare", Draws: true},
	}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"gpt-image-2","prompt":"a magpie"}`)
	if code != 200 || a.Model != "relay/gpt-image-2" {
		t.Fatalf("%d %s", code, raw)
	}
	requests := e.got("/v1/images/generations")
	if len(requests) != 1 || !strings.Contains(requests[0], `"model":"gpt-image-2"`) {
		t.Fatalf("asked %v", requests)
	}
}

// A keyed relay whose list draws no GPT Image is not taken to draw one by
// its bare name: a chat-only relay ahead of the one that draws would take
// every gpt-image-* and fail it at its vendor (#4432).
func TestRelayDrawingNoGPTImageClaimsNoneOfTheFamily(t *testing.T) {
	fresh(t)
	plain := drawingRelay(t, "plain")
	painter := drawingRelay(t, "painter", "gpt-image-2.5-flare")
	s := New()
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"gpt-image-2","prompt":"a magpie"}`)
	if code != 200 || a.Model != "painter/gpt-image-2" {
		t.Fatalf("%d %s", code, raw)
	}
	if got := plain.got("/v1/images/generations"); len(got) != 0 {
		t.Fatalf("the chat-only relay was asked: %v", got)
	}
	if got := painter.got("/v1/images/generations"); len(got) != 1 {
		t.Fatalf("painter asked %v", got)
	}
	if err := provider.Delete("painter"); err != nil {
		t.Fatal(err)
	}
	code, _, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"gpt-image-2","prompt":"a magpie"}`)
	if code != 404 || len(plain.got("/v1/images/generations")) != 0 {
		t.Fatalf("chat-only relay alone: %d %s", code, raw)
	}
}

// loscoy's relay (v0.1.2670) namespaces its list — openai/gpt-image-1,
// openai/gpt-image-2 and the rest, all Draws (#4432): the family is the
// name's own past the namespace, as the bare one a Codex harness asks
// its provider for. A relay whose list draws none of the family is still
// not taken to (TestRelayDrawingNoGPTImageClaimsNoneOfTheFamily).
func TestRelayDrawsTheGPTImageFamilyFromANamespacedList(t *testing.T) {
	fresh(t)
	e := &easel{}
	up := httptest.NewServer(e)
	defer up.Close()
	p := provider.Provider{ID: "relay", Name: "Relay", Responses: up.URL + "/v1", Key: "key"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(p.ID, p.Responses, []catalog.Model{
		{ID: "text"},
		{ID: "openai/gpt-image-1", Draws: true},
		{ID: "openai/gpt-image-1.5", Draws: true},
		{ID: "openai/gpt-image-2", Draws: true},
		{ID: "openai/gpt-image-2.5-flare", Draws: true},
	}); err != nil {
		t.Fatal(err)
	}
	s := New()
	// the name its list has it by goes upstream, as the relay serves it;
	// one the list doesn't name goes as asked, for the relay to answer
	for _, c := range []struct{ asked, want string }{
		{"gpt-image-2", "openai/gpt-image-2"},
		{"gpt-image-future", "gpt-image-future"},
	} {
		before := len(e.got("/v1/images/generations"))
		code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"`+c.asked+`","prompt":"a magpie"}`)
		if code != 200 || a.Model != "relay/"+c.want {
			t.Fatalf("%s: %d %s", c.asked, code, raw)
		}
		requests := e.got("/v1/images/generations")
		if len(requests) != before+1 || !strings.Contains(requests[before], `"model":"`+c.want+`"`) {
			t.Fatalf("%s: asked %v", c.asked, requests)
		}
	}
}

// Codex's native image tool draws with its turn's provider's own list,
// namespaced or not: the bare gpt-image-2 it asks for draws there as
// itself, not as the Settings' pick instead of it (#4432).
func TestCodexNativeDrawsItsOwnModelOnANamespacedRelay(t *testing.T) {
	fresh(t)
	relay := drawingRelay(t, "relay", "openai/gpt-image-2.5-flare")
	st := settings.Load()
	st.ImageGen = "relay/openai/gpt-image-2.5-flare"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := New()
	drawingMainTurn(t, s, "relay/text", "session-a", "turn-a", "", "magpie-codex")
	code, answer, raw := drawingToolCall(t, s, "/v1/images/generations", "gpt-image-2", "", "turn-a", "magpie-codex")
	if code != 200 || answer.Model != "relay/gpt-image-2" {
		t.Fatalf("%d %s", code, raw)
	}
	requests := relay.got("/v1/images/generations")
	if len(requests) != 1 || !strings.Contains(requests[0], `"model":"gpt-image-2"`) {
		t.Fatalf("asked %v", requests)
	}
}

// Codex's native image tool on a turn whose relay lists the one it asks
// for under a namespace draws it by that listed name (loscoy's
// openai/gpt-image-2, #4432), as the relay serves it.
func TestCodexNativeDrawsByTheRelaysNamespacedName(t *testing.T) {
	fresh(t)
	relay := drawingRelay(t, "relay", "openai/gpt-image-2.5-flare", "openai/gpt-image-2")
	s := New()
	drawingMainTurn(t, s, "relay/text", "session-a", "turn-a", "", "magpie-codex")
	code, answer, raw := drawingToolCall(t, s, "/v1/images/generations", "gpt-image-2", "", "turn-a", "magpie-codex")
	if code != 200 || answer.Model != "relay/openai/gpt-image-2" {
		t.Fatalf("%d %s", code, raw)
	}
	requests := relay.got("/v1/images/generations")
	if len(requests) != 1 || !strings.Contains(requests[0], `"model":"openai/gpt-image-2"`) {
		t.Fatalf("asked %v", requests)
	}
}

// Codex's native image tool asks its turn's provider for the one it names
// (gpt-image-2, #870): on a Codex backend whose list names another of the
// family, it draws there as the one it asked for, not as magpie's pick
// instead of it (#4432).
func TestCodexNativeDrawsItsOwnModelOnARelay(t *testing.T) {
	fresh(t)
	relay := drawingRelay(t, "relay", "gpt-image-2.5-flare")
	st := settings.Load()
	st.ImageGen = "relay/gpt-image-2.5-flare"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := New()
	drawingMainTurn(t, s, "relay/text", "session-a", "turn-a", "", "magpie-codex")
	code, answer, raw := drawingToolCall(t, s, "/v1/images/generations", "gpt-image-2", "", "turn-a", "magpie-codex")
	if code != 200 || answer.Model != "relay/gpt-image-2" {
		t.Fatalf("%d %s", code, raw)
	}
	requests := relay.got("/v1/images/generations")
	if len(requests) != 1 || !strings.Contains(requests[0], `"model":"gpt-image-2"`) {
		t.Fatalf("asked %v", requests)
	}
}

func TestDrawingResolutionUsesEnabledProvidersInOrder(t *testing.T) {
	fresh(t)
	for _, p := range []provider.Provider{
		{ID: "off", Name: "Off", Responses: "https://off.example/v1", Key: "key", Off: true},
		{ID: "first", Name: "First", Responses: "https://first.example/v1", Key: "key"},
		{ID: "second", Name: "Second", Responses: "https://second.example/v1", Key: "key"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive(p.ID, p.Responses, []catalog.Model{{ID: "gpt-image-2", Draws: true}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct{ requested, provider, model string }{
		{"gpt-image-2", "first", "gpt-image-2"},
		{"second/gpt-image-2", "second", "gpt-image-2"},
		{"second/gpt-image-new", "second", "gpt-image-new"},
	} {
		p, model, ok := resolveDrawing(test.requested)
		if !ok || p.ID != test.provider || model != test.model {
			t.Fatalf("resolve %q = %s/%s, %v", test.requested, p.ID, model, ok)
		}
	}
	if _, _, ok := resolveDrawing("off/gpt-image-2"); ok {
		t.Fatal("resolved disabled provider")
	}
}
