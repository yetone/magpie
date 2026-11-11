package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// openCodeGeminiCatalog is models.dev's opencode-go and opencode as they
// list the Gemini models of #2168 (https://models.dev/api.json,
// 2026-10-14): Google's SDK for each, Go's with low, medium and high
// effort, beside a GLM on the provider's own OpenAI-compatible one.
const openCodeGeminiCatalog = `{
  "opencode-go": {"id":"opencode-go","npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/zen/go/v1","models": {
    "gemini-4-argon-contributor": {"id":"gemini-4-argon-contributor","name":"Gemini 4 Argon","reasoning":true,"provider":{"npm":"@ai-sdk/google"},
      "reasoning_options":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]},
    "glm-5.3": {"id":"glm-5.3","name":"GLM-5.3"}}},
  "opencode": {"id":"opencode","npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/zen/v1","models": {
    "gemini-3-flash": {"id":"gemini-3-flash","name":"Gemini 3 Flash","reasoning":true,"provider":{"npm":"@ai-sdk/google"}},
    "glm-5.3": {"id":"glm-5.3","name":"GLM-5.3"}}}
}`

// openCodeGeminiSSE is Zen's own answer on
// /zen/v1/models/gemini-3-flash:streamGenerateContent?alt=sse to "Say hi
// in one word" (2026-10-14), byte for byte but for the thought signature,
// shortened.
const openCodeGeminiSSE = `data: {"createTime":"2026-10-14T12:18:31.471766Z","responseId":"F3PPatblHLDOsbwPqZPe-QU","modelVersion":"gemini-3-flash","candidates":[{"content":{"role":"model","parts":[{"text":"Hi."}]}}],"usageMetadata":{}}

data: {"createTime":"2026-10-14T12:18:31.471766Z","responseId":"F3PPatblHLDOsbwPqZPe-QU","modelVersion":"gemini-3-flash","candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"","thoughtSignature":"AY89a18MP3lS4K3OArBbSNxbDAHYyTJ2"}]}}],"usageMetadata":{"candidatesTokensDetails":[{"modality":"TEXT","tokenCount":2}],"promptTokensDetails":[{"modality":"TEXT","tokenCount":5}],"candidatesTokenCount":2,"promptTokenCount":5,"thoughtsTokenCount":67,"totalTokenCount":74}}

`

type openCodeAsk struct {
	url, key, auth, session string
	body                    []byte
}

// openCodeGeminiServer answers as OpenCode does (2026-10-14): a Gemini
// model on Gemini's generateContent at /zen/v1 or /inference/go/v1, a
// GLM on chat, and "Model does not support this protocol." for a model
// asked on an API it isn't served on.
func openCodeGeminiServer(t *testing.T, s *Server) func() []openCodeAsk {
	t.Helper()
	var mu sync.Mutex
	var asked []openCodeAsk
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		mu.Lock()
		asked = append(asked, openCodeAsk{r.URL.Host + r.URL.RequestURI(), r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization"), r.Header.Get("x-opencode-session"), b})
		mu.Unlock()
		reply := func(code int, ct, body string) (*http.Response, error) {
			return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		path := r.URL.Path
		gemini := strings.Contains(path, "/models/gemini-")
		switch {
		case r.Method != http.MethodPost:
			return reply(404, "application/json", `{}`)
		case gemini && (strings.HasPrefix(path, "/zen/v1/models/") || strings.HasPrefix(path, "/inference/go/v1/models/")):
			return reply(200, "text/event-stream", openCodeGeminiSSE)
		case strings.Contains(path, ":") || strings.Contains(path, "/models/"):
			return reply(404, "text/html", `<html>Not Found</html>`)
		case modelOf(b) == "glm-5.3" && strings.HasSuffix(path, "/chat/completions"):
			return reply(200, "application/json", `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from glm"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
		}
		return reply(400, "application/json", `{"type":"error","error":{"type":"invalid_request_error","message":"Model does not support this protocol."}}`)
	})}
	return func() []openCodeAsk {
		mu.Lock()
		defer mu.Unlock()
		out := asked
		asked = nil
		return out
	}
}

func urlsOf(asked []openCodeAsk) []string {
	var out []string
	for _, a := range asked {
		out = append(out, a.url)
	}
	return out
}

func serve(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// #2168 (@bugyaluwang on X too): OpenCode Go's gemini-4-argon-contributor
// went to /zen/go/v1/chat/completions, the one API it isn't served on,
// and Go answered "Model does not support this protocol". models.dev
// lists it for Google's SDK, so it is asked on Gemini's
// streamGenerateContent at /inference/go/v1 (Go's /zen/go/v1 has none),
// signed as OpenCode signs one: the key in x-goog-api-key and the
// conversation in x-opencode-session. Whatever API the client spoke, it
// is answered in it, and a client's reasoning effort is Gemini's
// thinkingLevel.
func TestOpenCodeGoGeminiModelOnGeminiAPI(t *testing.T) {
	fresh(t)
	writeCatalog(t, openCodeGeminiCatalog)
	if err := provider.Save(provider.Provider{ID: "opencode-go", Name: "OpenCode Go", Key: "sk-go", Chat: "https://opencode.ai/zen/go/v1",
		Models: []string{"gemini-4-argon-contributor", "glm-5.3"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	took := openCodeGeminiServer(t, s)
	const want = "opencode.ai/inference/go/v1/models/gemini-4-argon-contributor:streamGenerateContent?alt=sse"
	check := func(what string, code int, body string) openCodeAsk {
		t.Helper()
		asked := took()
		if len(asked) != 1 || asked[0].url != want {
			t.Fatalf("%s: asked %v, want %s alone (%d %s)", what, urlsOf(asked), want, code, body)
		}
		a := asked[0]
		if a.key != "sk-go" || a.auth != "" || a.session == "" {
			t.Errorf("%s: x-goog-api-key %q Authorization %q x-opencode-session %q", what, a.key, a.auth, a.session)
		}
		if gjson.GetBytes(a.body, "model").Exists() || !strings.Contains(gjson.GetBytes(a.body, "contents").Raw, "Say hi") {
			t.Errorf("%s: body %s", what, a.body)
		}
		if code != 200 {
			t.Errorf("%s: %d %s", what, code, body)
		}
		return a
	}

	chat := `{"model":"opencode-go/gemini-4-argon-contributor","messages":[{"role":"user","content":"Say hi in one word"}]}`
	code, body := serve(s, "/v1/chat/completions", chat)
	first := check("chat", code, body)
	if gjson.Get(body, "choices.0.message.content").String() != "Hi." || gjson.Get(body, "usage.prompt_tokens").Int() != 5 {
		t.Errorf("chat answer: %s", body)
	}
	// the next turn of the same conversation keeps its session
	code, body = serve(s, "/v1/chat/completions", `{"model":"opencode-go/gemini-4-argon-contributor","messages":[{"role":"user","content":"Say hi in one word"},{"role":"assistant","content":"Hi."},{"role":"user","content":"again"}]}`)
	if again := check("chat, next turn", code, body); again.session != first.session {
		t.Errorf("session %q then %q", first.session, again.session)
	}

	code, body = serve(s, "/v1/chat/completions", `{"model":"opencode-go/gemini-4-argon-contributor","stream":true,"messages":[{"role":"user","content":"Say hi in one word"}]}`)
	check("chat stream", code, body)
	if !strings.Contains(body, `"content":"Hi."`) || !strings.Contains(body, "[DONE]") {
		t.Errorf("chat stream answer: %s", body)
	}

	code, body = serve(s, "/v1/messages", `{"model":"opencode-go/gemini-4-argon-contributor","max_tokens":64,"messages":[{"role":"user","content":"Say hi in one word"}]}`)
	check("messages", code, body)
	if gjson.Get(body, "content.#(type==\"text\").text").String() != "Hi." {
		t.Errorf("messages answer: %s", body)
	}

	code, body = serve(s, "/v1/responses", `{"model":"opencode-go/gemini-4-argon-contributor","input":"Say hi in one word"}`)
	check("responses", code, body)
	if !strings.Contains(body, "Hi.") {
		t.Errorf("responses answer: %s", body)
	}

	code, body = serve(s, "/v1beta/models/opencode-go/gemini-4-argon-contributor:generateContent", `{"contents":[{"role":"user","parts":[{"text":"Say hi in one word"}]}]}`)
	check("gemini", code, body)
	if gjson.Get(body, "candidates.0.content.parts.0.text").String() != "Hi." {
		t.Errorf("gemini answer: %s", body)
	}

	for _, effort := range []string{"low", "medium", "high"} {
		code, body = serve(s, "/v1/chat/completions", `{"model":"opencode-go/gemini-4-argon-contributor","reasoning_effort":"`+effort+`","messages":[{"role":"user","content":"Say hi in one word"}]}`)
		a := check("effort "+effort, code, body)
		if got := gjson.GetBytes(a.body, "generationConfig.thinkingConfig.thinkingLevel").String(); got != effort {
			t.Errorf("effort %s: thinkingLevel %q in %s", effort, got, gjson.GetBytes(a.body, "generationConfig").Raw)
		}
	}
}

// The sibling at Zen: its Gemini models (gemini-3-flash here) are asked
// on Gemini's generateContent at /zen/v1, which relays them to Google.
// Each provider's other models stay on the API the client spoke: a GLM
// is never asked on Gemini's.
func TestOpenCodeZenGeminiAndOtherModelsUnchanged(t *testing.T) {
	fresh(t)
	writeCatalog(t, openCodeGeminiCatalog)
	for _, p := range []provider.Provider{
		{ID: "opencode-zen", Name: "OpenCode Zen", Key: "sk-zen", Chat: "https://opencode.ai/zen/v1", Models: []string{"gemini-3-flash", "glm-5.3", "kimi-k3"}},
		{ID: "opencode-go", Name: "OpenCode Go", Key: "sk-go", Chat: "https://opencode.ai/zen/go/v1", Models: []string{"gemini-4-argon-contributor", "glm-5.3"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	took := openCodeGeminiServer(t, s)

	code, body := serve(s, "/v1/chat/completions", `{"model":"opencode-zen/gemini-3-flash","messages":[{"role":"user","content":"Say hi in one word"}]}`)
	asked := took()
	if len(asked) != 1 || asked[0].url != "opencode.ai/zen/v1/models/gemini-3-flash:streamGenerateContent?alt=sse" || asked[0].key != "sk-zen" || asked[0].session == "" {
		t.Errorf("zen gemini: asked %v (%d %s)", urlsOf(asked), code, body)
	}
	if code != 200 || gjson.Get(body, "choices.0.message.content").String() != "Hi." {
		t.Errorf("zen gemini: %d %s", code, body)
	}

	for _, id := range []string{"opencode-zen", "opencode-go"} {
		code, body := serve(s, "/v1/chat/completions", `{"model":"`+id+`/glm-5.3","messages":[{"role":"user","content":"hi"}]}`)
		asked := took()
		root := map[string]string{"opencode-zen": "opencode.ai/zen/v1", "opencode-go": "opencode.ai/zen/go/v1"}[id]
		if len(asked) != 1 || asked[0].url != root+"/chat/completions" || asked[0].auth != "Bearer "+map[string]string{"opencode-zen": "sk-zen", "opencode-go": "sk-go"}[id] {
			t.Errorf("%s glm: asked %v", id, urlsOf(asked))
		}
		if code != 200 || gjson.Get(body, "choices.0.message.content").String() != "from glm" {
			t.Errorf("%s glm: %d %s", id, code, body)
		}
	}

	// a model the list names no API for, turned away on all three, is
	// still never tried on Gemini's: that serves the Gemini models alone
	code, body = serve(s, "/v1/chat/completions", `{"model":"opencode-zen/kimi-k3","messages":[{"role":"user","content":"hi"}]}`)
	for _, a := range took() {
		if strings.Contains(a.url, "/models/") {
			t.Errorf("kimi-k3 asked on Gemini's API: %s (%d %s)", a.url, code, body)
		}
	}
}
