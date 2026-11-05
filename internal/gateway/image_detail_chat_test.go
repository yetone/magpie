package gateway

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// chatImageDetails is the detail of every image_url in a Chat body, in
// order, "" for one without.
func chatImageDetails(t *testing.T, body []byte) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("body: %v\n%s", err, body)
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if x["type"] == "image_url" {
				u, _ := x["image_url"].(map[string]any)
				d, _ := u["detail"].(string)
				out = append(out, d)
			}
			for k, c := range x {
				if k != "image_url" {
					walk(c)
				}
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(v)
	return out
}

// A Responses request's image detail reaches a Chat Completions upstream:
// Chat's image_url takes low, high and auto as they are, and Responses'
// original, which Chat lacks, goes as high, the closest it has. An image
// with no detail goes without one. A tool's image keeps its detail too
// (mysterious_dove, Codex: detail=high and detail=original were dropped).
func TestResponsesImageDetailReachesChat(t *testing.T) {
	img := `"image_url":"data:image/png;base64,` + pngA + `"`
	body := `{"model":"m","input":[
		{"role":"user","content":[{"type":"input_text","text":"look"},
			{"type":"input_image","detail":"high",` + img + `},
			{"type":"input_image","detail":"original",` + img + `},
			{"type":"input_image","detail":"low",` + img + `},
			{"type":"input_image","detail":"auto",` + img + `},
			{"type":"input_image",` + img + `}]},
		{"type":"function_call","call_id":"c1","name":"view_image","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":[{"type":"input_image","detail":"original",` + img + `}]}]}`
	r, err := parseResponses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	out := buildChat(r, "m", "api.example.com", false)
	got := chatImageDetails(t, out)
	want := []string{"high", "high", "low", "auto", "", "high"}
	if len(got) != len(want) {
		t.Fatalf("want %d images, got %v\n%s", len(want), got, out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("image %d: detail %q, want %q\n%s", i, got[i], want[i], out)
		}
	}
}

// A Chat request's image detail reaches a Responses upstream as the client
// asked, not as auto; one without detail still goes as auto (#1474).
func TestChatImageDetailReachesResponses(t *testing.T) {
	url := `"data:image/png;base64,` + pngA + `"`
	body := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"look"},
		{"type":"image_url","image_url":{"url":` + url + `,"detail":"low"}},
		{"type":"image_url","image_url":{"url":` + url + `,"detail":"high"}},
		{"type":"image_url","image_url":{"url":` + url + `}}]}]}`
	r, err := parseChat([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	out := buildResponses(r, "m", "api.example.com", false)
	images := inputImages(t, string(out))
	want := []string{"low", "high", "auto"}
	if len(images) != len(want) {
		t.Fatalf("want %d input_image, got %d\n%s", len(want), len(images), out)
	}
	for i, im := range images {
		if d, _ := im["detail"].(string); d != want[i] {
			t.Errorf("image %d: detail %q, want %q\n%s", i, d, want[i], out)
		}
	}
}

// Codex's screenshot, detail "original", goes to a Responses API other
// than OpenAI's or DeepSeek's own as high, the closest it has: OpenCode
// Go answered 400 "invalid image detail" for DeepSeek V4.1 Flash
// (qiu_eth). The rest of the body goes as it came, low and auto too.
func TestOriginalDetailHighOnARelay(t *testing.T) {
	sent := toolImageUp(t, provider.Responses)
	img := `"image_url":"data:image/png;base64,` + pngA + `"`
	postToolImages(t, "/v1/responses", `{"model":"probe/eye","stream":true,"input":[
		{"role":"user","content":[{"type":"input_text","text":"look"},
			{"type":"input_image","detail":"original",`+img+`},
			{"type":"input_image","detail":"low",`+img+`}]},
		{"type":"function_call","call_id":"c1","name":"view_image","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":[{"type":"input_image","detail":"original",`+img+`}]},
		{"role":"user","content":[{"type":"input_text","text":"and now?"}]}]}`)
	var got []string
	for _, im := range inputImages(t, sent()) {
		d, _ := im["detail"].(string)
		got = append(got, d)
	}
	if want := []string{"high", "low", "high"}; !slices.Equal(got, want) {
		t.Errorf("details %q, want %q\n%s", got, want, sent())
	}
}

// OpenAI's own API, a ChatGPT account's and DeepSeek's take "original";
// a relay's, OpenCode Go's among them, isn't known to.
func TestTakesOriginalDetail(t *testing.T) {
	for base, want := range map[string]bool{
		"https://api.openai.com/v1":             true,
		"https://chatgpt.com/backend-api/codex": true,
		"https://api.deepseek.com/v1":           true,
		"https://opencode.ai/zen/go/v1":         false,
		"https://openrouter.ai/api/v1":          false,
		"":                                      false,
	} {
		if got := takesOriginalDetail(provider.Provider{ID: "x", Responses: base}); got != want {
			t.Errorf("%q: %v, want %v", base, got, want)
		}
	}
	if !takesOriginalDetail(provider.Provider{ID: "x", Preset: provider.RemoteMagpiePreset, Responses: "http://10.0.0.2:3425/v1"}) {
		t.Error("a remote magpie decides for its own upstream")
	}
}
