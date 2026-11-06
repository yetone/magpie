package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// genAsks are generationConfigs a Gemini client sends, each with the keys a
// Gemini upstream should be sent beside what magpie rebuilds, and the keys
// it must not be sent that the client didn't ask for.
var genAsks = []struct {
	name   string
	gen    string
	want   map[string]string
	absent []string
}{
	{
		"the reporter's fields",
		`{"topK":40,"seed":42,"candidateCount":2,"presencePenalty":0.5,"frequencyPenalty":-0.5,` +
			`"responseModalities":["TEXT"],"mediaResolution":"MEDIA_RESOLUTION_LOW",` +
			`"speechConfig":{"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"Aoede"}}},"responseFormat":{"text":{}}}`,
		map[string]string{
			"topK": `40`, "seed": `42`,
			"presencePenalty": `0.5`, "frequencyPenalty": `-0.5`,
			"responseModalities": `["TEXT"]`,
			"mediaResolution":    `"MEDIA_RESOLUTION_LOW"`,
			"speechConfig":       `{"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"Aoede"}}}`,
			"responseFormat":     `{"text":{}}`,
		},
		// magpie reads one answer: a second candidate would be mixed in
		[]string{"candidateCount"},
	},
	{
		"snake_case, as Google's API also reads it (geminiCamel)",
		`{"top_k":40,"candidate_count":2}`,
		map[string]string{"topK": `40`},
		[]string{"candidateCount"},
	},
	{
		"beside what magpie reads",
		`{"temperature":0.4,"topK":40}`,
		map[string]string{"topK": `40`, "temperature": `0.4`},
		nil,
	},
	{
		"none asked, none sent",
		`{"temperature":0.4}`,
		map[string]string{},
		[]string{"topK", "seed", "candidateCount", "presencePenalty", "frequencyPenalty",
			"responseModalities", "mediaResolution", "speechConfig", "responseFormat"},
	},
}

// saysGemini is a Gemini API's stream with a plain text answer.
const saysGemini = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1}}`

// genUpstream is a relay whose only API is a Gemini one that answers saysGemini,
// with what its upstream was asked kept in rec.
func genUpstream(t *testing.T, model string) (*asksKept, error) {
	t.Helper()
	rec := &asksKept{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.keep(r)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(saysGemini))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{model}, Gemini: up.URL + "/v1beta"}); err != nil {
		return nil, err
	}
	return rec, nil
}

// A Gemini client's generationConfig fields magpie doesn't read — topK,
// seed, the penalties, responseModalities, mediaResolution,
// speechConfig, responseFormat — reach a Gemini API as they were sent
// (#3937, yinwu01): a request asked for one body is translated, and was
// built again without everything else the client asked. What magpie reads
// or writes itself goes as magpie builds it.
func TestGeminiGenConfigReachesTheGeminiAPI(t *testing.T) {
	const model = "gemini-3-pro"
	fresh(t)
	rec, err := genUpstream(t, model)
	if err != nil {
		t.Fatal(err)
	}
	asked := 0
	for _, ask := range genAsks {
		code, body := post(t, "/v1beta/models/relay/"+model+":generateContent", imageAsk(ask.gen))
		if code != 200 || !strings.Contains(body, "hello") {
			t.Fatalf("%s: asked %s: %d %s", ask.name, ask.gen, code, body)
		}
		got, n := rec.last()
		if n <= asked {
			t.Fatalf("%s: asked %s: nothing reached the relay", ask.name, ask.gen)
		}
		asked = n
		for k, want := range ask.want {
			if sent := gjson.GetBytes(got.body, "generationConfig."+k).Raw; !sameConfig(sent, want) {
				t.Errorf("%s: asked %s: %s was sent generationConfig.%s = %s, want %s: %s", ask.name, ask.gen, got.path, k, sent, want, got.body)
			}
		}
		for _, k := range ask.absent {
			if sent := gjson.GetBytes(got.body, "generationConfig."+k).Raw; sent != "" {
				t.Errorf("%s: asked %s: %s was sent generationConfig.%s = %s, asked for none", ask.name, ask.gen, got.path, k, sent)
			}
		}
	}
}

// A model whose id says it draws is sent magpie's own responseModalities
// even when the client asked for its own: magpie's word for a draw model
// stands (buildCodeAssistSent), and the rest of what the client asked goes
// beside it.
func TestGeminiGenConfigDrawModelKeepsMagpiesModalities(t *testing.T) {
	const model = "gemini-3-pro-image-preview"
	fresh(t)
	rec := &asksKept{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.keep(r)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(drawnGemini))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{model}, Gemini: up.URL + "/v1beta"}); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1beta/models/relay/"+model+":generateContent", imageAsk(`{"responseModalities":["TEXT"],"topK":40}`))
	if code != 200 || !strings.Contains(body, "aGVsbG8=") {
		t.Fatalf("asked: %d %s", code, body)
	}
	got, _ := rec.last()
	if sent := gjson.GetBytes(got.body, "generationConfig.responseModalities").Raw; !sameConfig(sent, `["TEXT","IMAGE"]`) {
		t.Errorf("a draw model was sent responseModalities %s, want [\"TEXT\",\"IMAGE\"]: %s", sent, got.body)
	}
	if sent := gjson.GetBytes(got.body, "generationConfig.topK").Raw; !sameConfig(sent, `40`) {
		t.Errorf("a draw model was sent topK %s, want 40: %s", sent, got.body)
	}
}
