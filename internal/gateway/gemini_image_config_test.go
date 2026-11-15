package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// imageAsk is #3820's request (GuoJinguang): a frame and the words to redraw
// it, with gen as its generationConfig, none when "".
func imageAsk(gen string) string {
	ask := `{"contents":[{"parts":[{"text":"Re-photorealize this render."},{"inlineData":{"mimeType":"image/png","data":"UE5HIGZyYW1l"}}]}]`
	if gen != "" {
		ask += `,"generationConfig":` + gen
	}
	return ask + `}`
}

// imageAsks are generationConfigs a Gemini client sends, each with the
// imageConfig a Gemini upstream should be sent for it.
var imageAsks = []struct{ gen, want string }{
	// the reporter's
	{`{"imageConfig":{"imageSize":"4K"}}`, `{"imageSize":"4K"}`},
	{`{"thinkingConfig":{"thinkingLevel":"high"},"imageConfig":{"aspectRatio":"16:9","imageSize":"2K"}}`, `{"aspectRatio":"16:9","imageSize":"2K"}`},
	// snake_case, as Google's API also reads it (geminiCamel)
	{`{"image_config":{"image_size":"4K","aspect_ratio":"1:1"}}`, `{"imageSize":"4K","aspectRatio":"1:1"}`},
	// none asked, none sent: no generationConfig, or one without it
	{"", ""},
	{`{"temperature":0.4}`, ""},
}

// drawnGemini is a Gemini API's stream with one image drawn.
const drawnGemini = `data: {"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1290,"candidatesTokenCount":2000}}`

// upstreamAsk is one request an upstream was sent.
type upstreamAsk struct {
	path string
	body []byte
}

// asksKept keeps what each request to an upstream sent it.
type asksKept struct {
	mu  sync.Mutex
	got []upstreamAsk
}

func (rec *asksKept) keep(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	rec.mu.Lock()
	rec.got = append(rec.got, upstreamAsk{r.URL.Path, b})
	rec.mu.Unlock()
}

// last is the latest request, and how many there were.
func (rec *asksKept) last() (upstreamAsk, int) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.got) == 0 {
		return upstreamAsk{}, 0
	}
	return rec.got[len(rec.got)-1], len(rec.got)
}

// drawsBoth is a relay with a Gemini API and a Chat API, as #3820's: the
// Gemini API draws, and chat completions answers with the image in its
// text, as a relay that turns Gemini's parts into markdown does.
func drawsBoth(t *testing.T) (*httptest.Server, *asksKept) {
	t.Helper()
	rec := &asksKept{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.keep(r)
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case strings.HasSuffix(r.URL.Path, ":streamGenerateContent"):
			io.WriteString(w, sse(drawnGemini))
		case strings.HasSuffix(r.URL.Path, ":generateContent"):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, strings.TrimPrefix(drawnGemini, "data: "))
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			io.WriteString(w, sse(`data: {"id":"c1","object":"chat.completion.chunk","model":"gemini-3-pro-image-preview","choices":[{"index":0,"delta":{"role":"assistant","content":"![image](data:image/png;base64,aGVsbG8=)"},"finish_reason":"stop"}]}`, `data: [DONE]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	return up, rec
}

// sameConfig says whether two imageConfigs hold the same fields and values.
func sameConfig(got, want string) bool {
	var g, w any
	return json.Unmarshal([]byte(got), &g) == nil && json.Unmarshal([]byte(want), &w) == nil && jsonEqual(g, w)
}

// A Gemini client's generationConfig.imageConfig reaches a Gemini API as
// it was sent (#3820, GuoJinguang: imageSize 4K asked through magpie drew
// 1024x1024, where their relay asked itself drew 4096x4096). A request
// asked for one body is translated, and was built again without it; a
// streamed one is relayed. Beside a Chat API, one asked for one body
// goes to the Gemini API when the user set the model's API to Gemini. A
// request that asks for none is sent none.
func TestGeminiImageConfigReachesTheGeminiAPI(t *testing.T) {
	const model = "gemini-3-pro-image-preview"
	for _, tc := range []struct {
		name   string
		chat   bool // a Chat API beside the Gemini one
		set    bool // the user set the model's API to Gemini
		method string
	}{
		{"its only API, generateContent", false, false, "generateContent"},
		{"its only API, streamed", false, false, "streamGenerateContent?alt=sse"},
		{"beside a Chat API, streamed", true, false, "streamGenerateContent?alt=sse"},
		{"beside a Chat API, set to Gemini, generateContent", true, true, "generateContent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			up, rec := drawsBoth(t)
			p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{model}, Gemini: up.URL + "/v1beta"}
			if tc.chat {
				p.Chat = up.URL + "/v1"
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			if tc.set {
				if err := provider.SetModelAPI("relay/"+model, "gemini"); err != nil {
					t.Fatal(err)
				}
			}
			asked := 0
			for _, ask := range imageAsks {
				code, body := post(t, "/v1beta/models/relay/"+model+":"+tc.method, imageAsk(ask.gen))
				if code != 200 || !strings.Contains(body, "aGVsbG8=") {
					t.Fatalf("asked %s: %d %s", ask.gen, code, body)
				}
				got, n := rec.last()
				if n <= asked {
					t.Fatalf("asked %s: nothing reached the relay", ask.gen)
				}
				asked = n
				if !strings.HasPrefix(got.path, "/v1beta/models/") {
					t.Fatalf("asked %s: the relay was asked at %s, not its Gemini API: %s", ask.gen, got.path, got.body)
				}
				switch sent := gjson.GetBytes(got.body, "generationConfig.imageConfig").Raw; {
				case ask.want == "" && sent != "":
					t.Errorf("asked for none: %s was sent %s", got.path, sent)
				case ask.want != "" && !sameConfig(sent, ask.want):
					t.Errorf("asked %s: %s was sent %q: %s", ask.gen, got.path, sent, got.body)
				}
			}
		})
	}
}

// The same reaches a Google sign-in's image model inside Code Assist's
// envelope: Antigravity is asked as a Gemini API is, its request a Gemini
// request.
func TestGeminiImageConfigReachesCodeAssist(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	rec := &asksKept{}
	s, _ := antigravityGroupFor(t, []string{"u@example.com"}, agDrawer, func(w http.ResponseWriter, r *http.Request) {
		rec.keep(r)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(drawnPNG))
	})
	asked := 0
	for _, method := range []string{"generateContent", "streamGenerateContent?alt=sse"} {
		for _, ask := range imageAsks {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/antigravity/"+agDrawer+":"+method, strings.NewReader(imageAsk(ask.gen))))
			if w.Code != 200 || !strings.Contains(w.Body.String(), "iVBORw0KGgo=") {
				t.Fatalf("%s asked %s: %d %s", method, ask.gen, w.Code, w.Body.String())
			}
			got, n := rec.last()
			if n <= asked {
				t.Fatalf("%s asked %s: nothing reached Code Assist", method, ask.gen)
			}
			asked = n
			switch sent := gjson.GetBytes(got.body, "request.generationConfig.imageConfig").Raw; {
			case ask.want == "" && sent != "":
				t.Errorf("%s asked for none: sent %s", method, sent)
			case ask.want != "" && !sameConfig(sent, ask.want):
				t.Errorf("%s asked %s: sent %q: %s", method, ask.gen, sent, got.body)
			}
		}
	}
}

// A request asked for one body that asks an image's size or shape goes to
// the provider's Gemini API, not to the Chat API beside it, which has no
// field for it (#3820: GuoJinguang's relay has both, and asked on its chat
// completions imageSize 4K drew 1024x1024). One that asks for none goes to
// Chat as before.
func TestGeminiImageConfigGoesToTheGeminiAPIBesideChat(t *testing.T) {
	const model = "gemini-3-pro-image-preview"
	fresh(t)
	up, rec := drawsBoth(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{model}, Chat: up.URL + "/v1", Gemini: up.URL + "/v1beta"}); err != nil {
		t.Fatal(err)
	}
	asked := 0
	for _, ask := range imageAsks {
		code, body := post(t, "/v1beta/models/relay/"+model+":generateContent", imageAsk(ask.gen))
		if code != 200 || !strings.Contains(body, "aGVsbG8=") {
			t.Fatalf("asked %s: %d %s", ask.gen, code, body)
		}
		got, n := rec.last()
		if n <= asked {
			t.Fatalf("asked %s: nothing reached the relay", ask.gen)
		}
		asked = n
		want := "/v1beta/models/" + model + ":streamGenerateContent"
		if ask.want == "" {
			want = "/v1/chat/completions"
		}
		if got.path != want {
			t.Errorf("asked %s: the relay was asked at %s, want %s", ask.gen, got.path, want)
			continue
		}
		if sent := gjson.GetBytes(got.body, "generationConfig.imageConfig").Raw; ask.want != "" && !sameConfig(sent, ask.want) {
			t.Errorf("asked %s: sent %q: %s", ask.gen, sent, got.body)
		}
	}
}
