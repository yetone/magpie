package gateway

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// vertexSSE is a streamGenerateContent reply as Vertex AI sends it: each
// chunk on a data line, the blank line after it, in CRLF.
func vertexSSE(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: " + c + "\r\n\r\n")
	}
	return b.String()
}

// vertexCallSig is the thought signature gemini-3.8-flash gave the first
// of vertexCalls' two calls.
const vertexCallSig = "AY89a19eCdKurFEZ2PceOgYiyMwbMm8TLwl8DZzdn3Ie0nS/ZbkvknSZpfyQAGrkATnFm+UEFu0E3u1mq2ibcTRDR+OeTI1lJHgtGpX6GDlhcIixdZ20"

// vertexCalls is gemini-3.8-flash's reply on Vertex AI (2026-10-06) to
// "Weather in Paris and in Tokyo? Call the tool for both cities at once.":
// two calls made at once, the first one signed and the second not.
var vertexCalls = vertexSSE(
	`{"candidates": [{"content": {"role": "model","parts": [{"functionCall": {"name": "get_weather","args": {"city": "Paris"},"id": "call_463646"},"thoughtSignature": "`+vertexCallSig+`"}]}}],"usageMetadata": {"trafficType": "ON_DEMAND"},"modelVersion": "gemini-3.8-flash","createTime": "2026-10-06T19:23:17.651275Z","responseId": "pUrFaovgJ-iV4_UP_reImQI"}`,
	`{"candidates": [{"content": {"role": "model","parts": [{"functionCall": {"name": "get_weather","args": {"city": "Tokyo"},"id": "call_463649"}}]}}],"usageMetadata": {"trafficType": "ON_DEMAND"},"modelVersion": "gemini-3.8-flash","createTime": "2026-10-06T19:23:17.651275Z","responseId": "pUrFaovgJ-iV4_UP_reImQI"}`,
	`{"candidates": [{"content": {"role": "model","parts": [{"text": ""}]},"finishReason": "STOP"}],"usageMetadata": {"promptTokenCount": 38,"candidatesTokenCount": 32,"totalTokenCount": 70,"trafficType": "ON_DEMAND","promptTokensDetails": [{"modality": "TEXT","tokenCount": 38}],"candidatesTokensDetails": [{"modality": "TEXT","tokenCount": 32}]},"modelVersion": "gemini-3.8-flash","createTime": "2026-10-06T19:23:17.651275Z","responseId": "pUrFaovgJ-iV4_UP_reImQI"}`,
)

// vertexThought is its reply to "Is 391 prime? Answer in one short
// sentence." at medium, with thoughts asked for: the summary of its
// thinking a part of its own, then the answer, then a signature on an
// empty part (cut short here).
var vertexThought = vertexSSE(
	`{"candidates": [{"content": {"role": "model","parts": [{"text": "**Testing Divisibility**\n\nI'm currently evaluating the primality of 391. My process involves checking for divisibility by prime numbers up to its square root, which is approximately 19.77. I've begun testing with primes 2, 3, 5, 7, 11, 13, 17, and 19.\n\n","thought": true}]}}],"usageMetadata": {"trafficType": "ON_DEMAND"},"modelVersion": "gemini-3.8-flash","createTime": "2026-10-06T19:23:34.699816Z","responseId": "tkrFaqjbKvuW4_UPjdTc0QI"}`,
	`{"candidates": [{"content": {"role": "model","parts": [{"text": "No, 391 is not prime because it is"}]}}],"usageMetadata": {"trafficType": "ON_DEMAND"},"modelVersion": "gemini-3.8-flash","createTime": "2026-10-06T19:23:34.699816Z","responseId": "tkrFaqjbKvuW4_UPjdTc0QI"}`,
	`{"candidates": [{"content": {"role": "model","parts": [{"text": " divisible by 17 and 23 ($17 \\times 23 = 391$)."}]}}],"usageMetadata": {"trafficType": "ON_DEMAND"},"modelVersion": "gemini-3.8-flash","createTime": "2026-10-06T19:23:34.699816Z","responseId": "tkrFaqjbKvuW4_UPjdTc0QI"}`,
	`{"candidates": [{"content": {"role": "model","parts": [{"text": "","thoughtSignature": "AY89a18F86qTRNOglIpYqN/kZnCtY22JzpO9qF7Q27EsQ3bPInT9r2XEy8I6A9+V"}]},"finishReason": "STOP"}],"usageMetadata": {"promptTokenCount": 13,"candidatesTokenCount": 35,"totalTokenCount": 518,"trafficType": "ON_DEMAND","promptTokensDetails": [{"modality": "TEXT","tokenCount": 13}],"candidatesTokensDetails": [{"modality": "TEXT","tokenCount": 35}],"thoughtsTokenCount": 470},"modelVersion": "gemini-3.8-flash","createTime": "2026-10-06T19:23:34.699816Z","responseId": "tkrFaqjbKvuW4_UPjdTc0QI"}`,
)

// vertexPong is its reply to "Reply with exactly: pong".
var vertexPong = vertexSSE(
	`{"candidates": [{"content": {"role": "model","parts": [{"text": "pong"}]}}],"usageMetadata": {"trafficType": "ON_DEMAND"},"modelVersion": "gemini-3.8-flash","createTime": "2026-10-06T19:24:49.613681Z","responseId": "AUvFarG6Ja6Z4_UP_LrtmQ8"}`,
	`{"candidates": [{"content": {"role": "model","parts": [{"text": "","thoughtSignature": "AY89a19YHSyyko7R+JBFg2gQERtPZVqEh2b7wGoISK4KuSyD7e/9GTg2CbOhNOI+"}]},"finishReason": "STOP"}],"usageMetadata": {"promptTokenCount": 5,"candidatesTokenCount": 1,"totalTokenCount": 6,"trafficType": "ON_DEMAND","promptTokensDetails": [{"modality": "TEXT","tokenCount": 5}],"candidatesTokensDetails": [{"modality": "TEXT","tokenCount": 1}]},"modelVersion": "gemini-3.8-flash","createTime": "2026-10-06T19:24:49.613681Z","responseId": "AUvFarG6Ja6Z4_UP_LrtmQ8"}`,
)

// vertexColours is gemini-2.5-flash's generateContent reply on Vertex AI
// (2026-10-07), whole, to "List three primary colours." asked for a JSON
// array of strings with its thinking off, as Vertex AI sent it.
const vertexColours = `{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [
          {
            "text": "[\"red\",\"yellow\",\"blue\"]"
          }
        ]
      },
      "finishReason": "STOP",
      "safetyRatings": [
        {
          "category": "HARM_CATEGORY_HATE_SPEECH",
          "probability": "NEGLIGIBLE",
          "probabilityScore": 3.7527707e-06,
          "severity": "HARM_SEVERITY_NEGLIGIBLE"
        },
        {
          "category": "HARM_CATEGORY_DANGEROUS_CONTENT",
          "probability": "NEGLIGIBLE",
          "probabilityScore": 0.00010157355,
          "severity": "HARM_SEVERITY_NEGLIGIBLE"
        },
        {
          "category": "HARM_CATEGORY_HARASSMENT",
          "probability": "NEGLIGIBLE",
          "probabilityScore": 3.213228e-05,
          "severity": "HARM_SEVERITY_NEGLIGIBLE",
          "severityScore": 0.035694223
        },
        {
          "category": "HARM_CATEGORY_SEXUALLY_EXPLICIT",
          "probability": "NEGLIGIBLE",
          "probabilityScore": 4.7470326e-07,
          "severity": "HARM_SEVERITY_NEGLIGIBLE"
        }
      ]
    }
  ],
  "usageMetadata": {
    "promptTokenCount": 5,
    "candidatesTokenCount": 7,
    "totalTokenCount": 12,
    "trafficType": "ON_DEMAND",
    "promptTokensDetails": [
      {
        "modality": "TEXT",
        "tokenCount": 5
      }
    ],
    "candidatesTokensDetails": [
      {
        "modality": "TEXT",
        "tokenCount": 7
      }
    ]
  },
  "modelVersion": "gemini-2.5-flash",
  "createTime": "2026-10-07T07:05:35.106720Z",
  "responseId": "P-_FauDBBvjGodAPuNLUuA0"
}
`

// What Vertex AI's streamGenerateContent answered requests it turned away
// with (2026-10-06), as JSON, under a Content-Type of text/event-stream.
const (
	vertexMinimalFlash = "{\n  \"error\": {\n    \"code\": 400,\n    \"message\": \"Thinking level is unsupported: THINKING_LEVEL_MINIMAL\",\n    \"status\": \"INVALID_ARGUMENT\"\n  }\n}\n"
	vertexMinimalPro   = "{\n  \"error\": {\n    \"code\": 400,\n    \"message\": \"Unable to submit request because thinking_level MINIMAL is not supported by this model. Learn more: https://cloud.google.com/vertex-ai/generative-ai/docs/model-reference/gemini\",\n    \"status\": \"INVALID_ARGUMENT\"\n  }\n}\n"
	vertexBadSig       = "{\n  \"error\": {\n    \"code\": 400,\n    \"message\": \"Invalid thought signature.\",\n    \"status\": \"INVALID_ARGUMENT\"\n  }\n}\n"
	vertexMissingSig   = "{\n  \"error\": {\n    \"code\": 400,\n    \"message\": \"Function call is missing a thought_signature in functionCall parts. This is required for tools to work correctly, and missing thought_signature may lead to degraded model performance. Additional data, function call `default_api:get_weather` , position 2. Please refer to https://ai.google.dev/gemini-api/docs/thought-signatures for more details.\",\n    \"status\": \"INVALID_ARGUMENT\"\n  }\n}\n"
)

// vertexLevels are the reasoning levels models.dev gives Gemini's models.
const vertexLevels = `{"google":{"models":{
	"gemini-3.8-flash":{"id":"gemini-3.8-flash","reasoning_options":[{"type":"effort","values":["low","medium","high"]}]},
	"gemini-3.6-flash":{"id":"gemini-3.6-flash","reasoning_options":[{"type":"effort","values":["minimal","low","medium","high"]}]},
	"gemini-3.1-pro-preview":{"id":"gemini-3.1-pro-preview","reasoning_options":[{"type":"effort","values":["low","medium","high"]}]},
	"gemini-2.5-pro":{"id":"gemini-2.5-pro","reasoning_options":[{"type":"effort","values":["low","medium","high"]}]},
	"gemini-2.5-flash":{"id":"gemini-2.5-flash","reasoning_options":[{"type":"effort","values":["low","medium","high"]}]},
	"gemini-2.5-flash-lite":{"id":"gemini-2.5-flash-lite","reasoning_options":[{"type":"effort","values":["low","medium","high"]}]}}}}`

// vertexUp is Vertex AI and Google's token endpoint on one server. It
// keeps each request made to a model, and answers it with reply; it turns
// a call's signature away as Vertex AI does when the call is a step's
// first and it gave the signature to no call.
type vertexUp struct {
	mu    sync.Mutex
	asked []vertexAsked
	mints int
	reply func(n int, body map[string]any) (int, string)
}

type vertexAsked struct {
	path, query, auth string
	body              map[string]any
}

func (v *vertexUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	v.mu.Lock()
	defer v.mu.Unlock()
	switch {
	case r.URL.Path == "/token":
		v.mints++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"ya29.vertex-test","expires_in":3599,"scope":"https://www.googleapis.com/auth/cloud-platform","token_type":"Bearer"}`)
	case strings.HasPrefix(r.URL.Path, "/v1/projects/"):
		var m map[string]any
		json.Unmarshal(b, &m)
		v.asked = append(v.asked, vertexAsked{r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), m})
		code, out, kind := http.StatusOK, vertexPong, "text/event-stream"
		if strings.HasSuffix(r.URL.Path, ":generateContent") {
			out, kind = vertexColours, "application/json; charset=UTF-8"
		}
		if refused := vertexSigRefused(m); refused != "" {
			code, out = http.StatusBadRequest, refused
		} else if v.reply != nil {
			code, out = v.reply(len(v.asked), m)
		}
		// an error too comes as text/event-stream, its body JSON
		w.Header().Set("Content-Type", kind)
		w.WriteHeader(code)
		io.WriteString(w, out)
	default:
		http.NotFound(w, r)
	}
}

// vertexSigRefused is Vertex AI's refusal of a request whose step's first
// call has no signature, or one it didn't give; "" when there is none.
func vertexSigRefused(body map[string]any) string {
	contents, _ := body["contents"].([]any)
	for _, c := range contents {
		c, _ := c.(map[string]any)
		if c["role"] != "model" {
			continue
		}
		parts, _ := c["parts"].([]any)
		for _, p := range parts {
			p, _ := p.(map[string]any)
			if p["functionCall"] == nil {
				continue
			}
			switch p["thoughtSignature"] {
			case nil:
				return vertexMissingSig
			case skipSignature, vertexCallSig:
			default:
				return vertexBadSig
			}
			break // the step's first call is the one checked
		}
	}
	return ""
}

func (v *vertexUp) last(t *testing.T) vertexAsked {
	t.Helper()
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.asked) == 0 {
		t.Fatal("Vertex AI was asked nothing")
	}
	return v.asked[len(v.asked)-1]
}

func (v *vertexUp) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.asked)
}

func (v *vertexUp) minted() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.mints
}

// vertexServe is a gateway with a Google Vertex AI provider at project
// proj-1, global, backed by v, whose models have levels as models.dev
// gives them (levels).
func vertexServe(t *testing.T, v *vertexUp, levels string) *Server {
	t.Helper()
	fresh(t)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(levels), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	t.Cleanup(provider.VertexForTest(up.URL, up.URL+"/token", up.URL+"/iam"))
	creds := filepath.Join(t.TempDir(), "application_default_credentials.json")
	os.WriteFile(creds, []byte(`{"type":"authorized_user","client_id":"test.apps.googleusercontent.com","client_secret":"test-secret","refresh_token":"1//test-refresh"}`), 0o600)
	if err := provider.Save(provider.Provider{ID: "google-vertex", Name: "Google Vertex AI", Preset: provider.VertexPreset,
		Vertex: &provider.Vertex{Project: "proj-1", Credentials: creds}}); err != nil {
		t.Fatal(err)
	}
	return New()
}

func vertexAsk(t *testing.T, s *Server, path, body string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// thinkingOf is the thinkingConfig a request to Vertex AI had.
func thinkingOf(a vertexAsked) map[string]any {
	gen, _ := a.body["generationConfig"].(map[string]any)
	tc, _ := gen["thinkingConfig"].(map[string]any)
	return tc
}

// vertexCallIDs are the ids of vertexCalls' two calls as a client was
// given them, in the order given.
var vertexCallIDs = regexp.MustCompile(`call_46364[69][A-Za-z0-9_-]*`)

// Claude Code, Codex and a Chat client on Vertex AI, streamed or not: the
// request goes to the model's streamGenerateContent in the project, signed
// with the token minted from the user's Google credentials, with the
// model in the path alone. Gemini 3 signs the first of the calls it makes
// at once; the client is given that signature in the call's id, and when
// it sends the calls back, the signature goes on the first and nothing on
// the second, as Vertex AI gave them: a call with the signature missing,
// or one it didn't give, is a 400 there.
func TestVertexToolCallsRoundTrip(t *testing.T) {
	const tools = `"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]`
	for _, c := range []struct {
		name, path string
		first      func(stream string) string
		second     func(stream string, ids []string) string
	}{
		{"chat", "/v1/chat/completions",
			func(stream string) string {
				return `{"model":"google-vertex/gemini-3.8-flash","stream":` + stream + `,"reasoning_effort":"low",` + tools + `,
					"messages":[{"role":"user","content":"Weather in Paris and in Tokyo?"}]}`
			},
			func(stream string, ids []string) string {
				return `{"model":"google-vertex/gemini-3.8-flash","stream":` + stream + `,"reasoning_effort":"low",` + tools + `,"messages":[
					{"role":"user","content":"Weather in Paris and in Tokyo?"},
					{"role":"assistant","content":null,"tool_calls":[
						{"id":"` + ids[0] + `","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}},
						{"id":"` + ids[1] + `","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}}]},
					{"role":"tool","tool_call_id":"` + ids[0] + `","content":"18C, clear"},
					{"role":"tool","tool_call_id":"` + ids[1] + `","content":"21C, rain"}]}`
			}},
		{"anthropic", "/v1/messages",
			func(stream string) string {
				return `{"model":"google-vertex/gemini-3.8-flash","max_tokens":4000,"stream":` + stream + `,"thinking":{"type":"enabled","budget_tokens":2000},
					"tools":[{"name":"get_weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],
					"messages":[{"role":"user","content":"Weather in Paris and in Tokyo?"}]}`
			},
			func(stream string, ids []string) string {
				return `{"model":"google-vertex/gemini-3.8-flash","max_tokens":4000,"stream":` + stream + `,"thinking":{"type":"enabled","budget_tokens":2000},
					"tools":[{"name":"get_weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],"messages":[
					{"role":"user","content":"Weather in Paris and in Tokyo?"},
					{"role":"assistant","content":[
						{"type":"tool_use","id":"` + ids[0] + `","name":"get_weather","input":{"city":"Paris"}},
						{"type":"tool_use","id":"` + ids[1] + `","name":"get_weather","input":{"city":"Tokyo"}}]},
					{"role":"user","content":[
						{"type":"tool_result","tool_use_id":"` + ids[0] + `","content":"18C, clear"},
						{"type":"tool_result","tool_use_id":"` + ids[1] + `","content":"21C, rain"}]}]}`
			}},
		{"responses", "/v1/responses",
			func(stream string) string {
				return `{"model":"google-vertex/gemini-3.8-flash","stream":` + stream + `,"reasoning":{"effort":"low"},
					"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}],
					"input":[{"role":"user","content":"Weather in Paris and in Tokyo?"}]}`
			},
			func(stream string, ids []string) string {
				return `{"model":"google-vertex/gemini-3.8-flash","stream":` + stream + `,"reasoning":{"effort":"low"},
					"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}],"input":[
					{"role":"user","content":"Weather in Paris and in Tokyo?"},
					{"type":"function_call","call_id":"` + ids[0] + `","name":"get_weather","arguments":"{\"city\":\"Paris\"}"},
					{"type":"function_call","call_id":"` + ids[1] + `","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"},
					{"type":"function_call_output","call_id":"` + ids[0] + `","output":"18C, clear"},
					{"type":"function_call_output","call_id":"` + ids[1] + `","output":"21C, rain"}]}`
			}},
	} {
		for _, stream := range []string{"true", "false"} {
			t.Run(c.name+" stream "+stream, func(t *testing.T) {
				v := &vertexUp{reply: func(n int, body map[string]any) (int, string) {
					if n == 1 {
						return http.StatusOK, vertexCalls
					}
					return http.StatusOK, vertexPong
				}}
				s := vertexServe(t, v, vertexLevels)
				out := vertexAsk(t, s, c.path, c.first(stream))
				a := v.last(t)
				if a.path != "/v1/projects/proj-1/locations/global/publishers/google/models/gemini-3.8-flash:streamGenerateContent" || a.query != "alt=sse" {
					t.Fatalf("asked at %s?%s", a.path, a.query)
				}
				if a.auth != "Bearer ya29.vertex-test" {
					t.Fatalf("signed %q", a.auth)
				}
				if _, ok := a.body["model"]; ok {
					t.Errorf("the model is in the body as well as the path: %v", a.body)
				}
				if _, ok := a.body["stream"]; ok {
					t.Errorf("a stream field: %v", a.body)
				}
				if tc := thinkingOf(a); tc["thinkingLevel"] != "low" || tc["includeThoughts"] != true {
					t.Errorf("thinkingConfig %v", tc)
				}
				var ids []string
				for _, id := range vertexCallIDs.FindAllString(out, -1) {
					if !slices.Contains(ids, id) {
						ids = append(ids, id)
					}
				}
				if want := []string{signedID("call_463646", vertexCallSig), "call_463649"}; !slices.Equal(ids, want) {
					t.Fatalf("the client was given the calls %v, want %v:\n%s", ids, want, out)
				}

				out = vertexAsk(t, s, c.path, c.second(stream, ids))
				if !strings.Contains(out, "pong") {
					t.Fatalf("the answer after the calls: %s", out)
				}
				a = v.last(t)
				contents, _ := a.body["contents"].([]any)
				if len(contents) != 3 {
					t.Fatalf("sent %v", contents)
				}
				calls := contents[1].(map[string]any)["parts"].([]any)
				first, second := calls[0].(map[string]any), calls[1].(map[string]any)
				if first["thoughtSignature"] != vertexCallSig || first["functionCall"].(map[string]any)["id"] != "call_463646" {
					t.Errorf("the first call went back as %v", first)
				}
				if _, ok := second["thoughtSignature"]; ok || second["functionCall"].(map[string]any)["id"] != "call_463649" {
					t.Errorf("the second call went back as %v", second)
				}
				var answered []any
				for _, p := range contents[2].(map[string]any)["parts"].([]any) {
					answered = append(answered, p.(map[string]any)["functionResponse"].(map[string]any)["id"])
				}
				if !slices.Equal(answered, []any{"call_463646", "call_463649"}) {
					t.Errorf("the results went back for %v", answered)
				}
				if v.count() != 2 || v.minted() != 1 {
					t.Errorf("%d requests and %d tokens minted, want 2 and 1", v.count(), v.minted())
				}
			})
		}
	}
}

// Calls another model made (no signature in their ids) go to Vertex AI
// waved through, each with the value Google documents for history it
// didn't sign. A signature it didn't give — one a client made up, or one
// from another API, whose conversation moved here — is a 400 "Invalid
// thought signature."; the request is made again with every call waved
// through.
func TestVertexCallsItDidNotSign(t *testing.T) {
	v := &vertexUp{}
	s := vertexServe(t, v, vertexLevels)
	ask := func(a, b string) {
		vertexAsk(t, s, "/v1/chat/completions", `{"model":"google-vertex/gemini-3.8-flash","stream":true,"messages":[
			{"role":"user","content":"Weather in Paris and in Tokyo?"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"`+a+`","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}},
				{"id":"`+b+`","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}}]},
			{"role":"tool","tool_call_id":"`+a+`","content":"18C"},
			{"role":"tool","tool_call_id":"`+b+`","content":"21C"}],
			"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]}`)
	}
	waved := func(n int) {
		t.Helper()
		v.mu.Lock()
		a := v.asked[n]
		v.mu.Unlock()
		for _, p := range a.body["contents"].([]any)[1].(map[string]any)["parts"].([]any) {
			if sig := p.(map[string]any)["thoughtSignature"]; sig != skipSignature {
				t.Errorf("request %d: a call went with %v", n, sig)
			}
		}
	}

	ask("toolu_01", "toolu_02")
	if v.count() != 1 {
		t.Fatalf("%d requests for calls another model made", v.count())
	}
	waved(0)

	ask(signedID("call_1", "c2lnbmF0dXJlLW9uZQ=="), "call_2")
	if v.count() != 3 {
		t.Fatalf("%d requests, want the one turned away and the one after it", v.count()-1)
	}
	v.mu.Lock()
	sent := v.asked[1].body["contents"].([]any)[1].(map[string]any)["parts"].([]any)[0].(map[string]any)["thoughtSignature"]
	v.mu.Unlock()
	if sent != "c2lnbmF0dXJlLW9uZQ==" {
		t.Errorf("the call's own signature wasn't tried first: %v", sent)
	}
	waved(2)
}

// A Gemini 3 model's summaries of its thinking reach the client as its
// reasoning, apart from the answer: on Chat as reasoning_content, on
// Anthropic as a thinking block.
func TestVertexThoughtsApart(t *testing.T) {
	const answer = "No, 391 is not prime because it is divisible by 17 and 23 ($17 \\times 23 = 391$)."
	v := &vertexUp{reply: func(int, map[string]any) (int, string) { return http.StatusOK, vertexThought }}
	s := vertexServe(t, v, vertexLevels)

	out := vertexAsk(t, s, "/v1/chat/completions", `{"model":"google-vertex/gemini-3.8-flash","stream":true,"reasoning_effort":"medium",
		"messages":[{"role":"user","content":"Is 391 prime? Answer in one short sentence."}]}`)
	var text, thought string
	for _, e := range events(out) {
		chs, _ := e["choices"].([]any)
		for _, ch := range chs {
			d, _ := ch.(map[string]any)["delta"].(map[string]any)
			s, _ := d["content"].(string)
			r, _ := d["reasoning_content"].(string)
			text, thought = text+s, thought+r
		}
	}
	if text != answer || !strings.HasPrefix(thought, "**Testing Divisibility**") {
		t.Errorf("chat: answered %q, thought %q", text, thought)
	}
	// the log has the path it went to, and the thinking in what the model
	// wrote, as Vertex AI bills it
	rec := lastRecord(t)
	if want := "/v1/chat/completions → /publishers/google/models/gemini-3.8-flash:streamGenerateContent?alt=sse"; rec.Endpoint != want {
		t.Errorf("logged as %q, want %q", rec.Endpoint, want)
	}
	if rec.Provider != "google-vertex" || rec.Model != "gemini-3.8-flash" || rec.Input != 13 || rec.Output != 35+470 || rec.Reasoning != 470 {
		t.Errorf("logged %s/%s, %d in, %d out, %d reasoning", rec.Provider, rec.Model, rec.Input, rec.Output, rec.Reasoning)
	}

	out = vertexAsk(t, s, "/v1/messages", `{"model":"google-vertex/gemini-3.8-flash","max_tokens":4000,
		"thinking":{"type":"enabled","budget_tokens":8000},
		"messages":[{"role":"user","content":"Is 391 prime? Answer in one short sentence."}]}`)
	var msg struct {
		Content []struct{ Type, Text, Thinking string }
	}
	json.Unmarshal([]byte(out), &msg)
	text, thought = "", ""
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			text += b.Text
		case "thinking":
			thought += b.Thinking
		}
	}
	if text != answer || !strings.HasPrefix(thought, "**Testing Divisibility**") {
		t.Errorf("anthropic: answered %q, thought %q in %s", text, thought, out)
	}
}

// Each client's effort reaches Vertex AI as a thinkingConfig the model
// takes. Gemini 3 is asked for a level of its own: xhigh, max and ultra,
// which none has, for its highest, and reasoning turned off for its
// lowest, without its thoughts (gemini-3.8-flash has no minimal: 400
// "Thinking level is unsupported: THINKING_LEVEL_MINIMAL"). Gemini 2.5 is
// asked for a budget, as it takes no level: none turns Flash's thinking
// off, and Pro, which can't stop, thinks its least.
func TestVertexEffort(t *testing.T) {
	v := &vertexUp{}
	s := vertexServe(t, v, vertexLevels)
	chat := func(model, effort string) [2]string {
		e := ""
		if effort != "" {
			e = `"reasoning_effort":"` + effort + `",`
		}
		return [2]string{"/v1/chat/completions", `{"model":"google-vertex/` + model + `","stream":true,` + e + `"messages":[{"role":"user","content":"hi"}]}`}
	}
	responses := func(model, effort string) [2]string {
		return [2]string{"/v1/responses", `{"model":"google-vertex/` + model + `","stream":true,"reasoning":{"effort":"` + effort + `"},"input":"hi"}`}
	}
	messages := func(model, thinking string) [2]string {
		return [2]string{"/v1/messages", `{"model":"google-vertex/` + model + `","max_tokens":1000,"stream":true,"thinking":` + thinking + `,"messages":[{"role":"user","content":"hi"}]}`}
	}
	level := func(l string, thoughts bool) map[string]any {
		return map[string]any{"thinkingLevel": l, "includeThoughts": thoughts}
	}
	for _, c := range []struct {
		name string
		ask  [2]string
		want map[string]any
	}{
		{"chat high", chat("gemini-3.8-flash", "high"), level("high", true)},
		{"chat xhigh", chat("gemini-3.8-flash", "xhigh"), level("high", true)},
		{"chat minimal", chat("gemini-3.8-flash", "minimal"), level("low", true)},
		{"chat none", chat("gemini-3.8-flash", "none"), level("low", false)},
		{"chat none, minimal had", chat("gemini-3.6-flash", "none"), level("minimal", false)},
		{"chat none, levels not known", chat("gemini-3.9-flash", "none"), level("low", false)},
		{"chat xhigh, levels not known", chat("gemini-3.9-flash", "xhigh"), level("high", true)},
		{"chat, no effort", chat("gemini-3.8-flash", ""), nil},
		{"responses ultra", responses("gemini-3.8-flash", "ultra"), level("high", true)},
		{"responses none", responses("gemini-3.8-flash", "none"), level("low", false)},
		{"messages disabled", messages("gemini-3.8-flash", `{"type":"disabled"}`), level("low", false)},
		{"2.5 flash none", chat("gemini-2.5-flash", "none"), map[string]any{"thinkingBudget": 0.0}},
		{"2.5 flash high", chat("gemini-2.5-flash", "high"), map[string]any{"thinkingBudget": 24000.0, "includeThoughts": true}},
		{"2.5 flash budget", messages("gemini-2.5-flash", `{"type":"enabled","budget_tokens":2000}`), map[string]any{"thinkingBudget": 4096.0, "includeThoughts": true}},
		{"2.5 flash-lite none", responses("gemini-2.5-flash-lite", "none"), map[string]any{"thinkingBudget": 0.0}},
		{"2.5 pro none", chat("gemini-2.5-pro", "none"), map[string]any{"thinkingBudget": 128.0}},
		{"2.5 pro disabled", messages("gemini-2.5-pro", `{"type":"disabled"}`), map[string]any{"thinkingBudget": 128.0}},
		{"2.5 pro xhigh", chat("gemini-2.5-pro", "xhigh"), map[string]any{"thinkingBudget": 24000.0, "includeThoughts": true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			vertexAsk(t, s, c.ask[0], c.ask[1])
			if got := thinkingOf(v.last(t)); !maps.Equal(got, c.want) {
				t.Errorf("thinkingConfig %v, want %v", got, c.want)
			}
		})
	}
}

// A model models.dev gives minimal that Vertex AI turns minimal away for,
// in either of its words for that, is asked again at its lowest when
// reasoning is turned off, and asked at that from then on.
func TestVertexMinimalTurnedAway(t *testing.T) {
	for _, refusal := range []string{vertexMinimalFlash, vertexMinimalPro} {
		v := &vertexUp{reply: func(_ int, body map[string]any) (int, string) {
			gen, _ := body["generationConfig"].(map[string]any)
			tc, _ := gen["thinkingConfig"].(map[string]any)
			if tc["thinkingLevel"] == "minimal" {
				return http.StatusBadRequest, refusal
			}
			return http.StatusOK, vertexPong
		}}
		s := vertexServe(t, v, strings.Replace(vertexLevels, `"values":["low","medium","high"]}]},
	"gemini-3.6-flash"`, `"values":["minimal","low","medium","high"]}]},
	"gemini-3.6-flash"`, 1))
		off := `{"model":"google-vertex/gemini-3.8-flash","stream":true,"reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`
		vertexAsk(t, s, "/v1/chat/completions", off)
		if v.count() != 2 {
			t.Fatalf("%d requests, want minimal turned away and low after it", v.count())
		}
		if tc := thinkingOf(v.last(t)); tc["thinkingLevel"] != "low" || tc["includeThoughts"] != false {
			t.Errorf("asked again with %v", tc)
		}
		vertexAsk(t, s, "/v1/chat/completions", off)
		if v.count() != 3 || thinkingOf(v.last(t))["thinkingLevel"] != "low" {
			t.Errorf("minimal asked for again: %d requests, the last %v", v.count(), thinkingOf(v.last(t)))
		}
	}
}

// Gemini CLI, and any client speaking Gemini's API, on Vertex AI: a
// request is sent on as it came, with no model in its body, a streamed one
// to the model's streamGenerateContent in the project and one asked whole
// to its generateContent, everything the client asked for kept (a
// translation would drop its safetySettings, topK and schema, and think
// with thinkingBudget 0); Vertex AI's reply is the client's, counted.
func TestVertexGeminiClients(t *testing.T) {
	v := &vertexUp{}
	s := vertexServe(t, v, vertexLevels)
	body := `{"contents":[{"role":"user","parts":[{"text":"Reply with exactly: pong"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"LOW"}}}`

	out := vertexAsk(t, s, "/v1beta/models/google-vertex/gemini-3.8-flash:streamGenerateContent?alt=sse", body)
	a := v.last(t)
	if a.path != "/v1/projects/proj-1/locations/global/publishers/google/models/gemini-3.8-flash:streamGenerateContent" || a.query != "alt=sse" {
		t.Fatalf("asked at %s?%s", a.path, a.query)
	}
	if _, ok := a.body["model"]; ok {
		t.Errorf("a model in the body: %v", a.body)
	}
	if _, ok := a.body["stream"]; ok {
		t.Errorf("a stream field: %v", a.body)
	}
	if tc := thinkingOf(a); tc["thinkingLevel"] != "LOW" {
		t.Errorf("the client's own thinkingConfig became %v", tc)
	}
	if !strings.Contains(out, `"pong"`) {
		t.Errorf("streamed back %s", out)
	}

	const gen = `{"topK":5,"maxOutputTokens":400,"thinkingConfig":{"thinkingBudget":0},"responseMimeType":"application/json","responseJsonSchema":{"type":"array","items":{"type":"string"}}}`
	const safety = `[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_NONE"}]`
	out = vertexAsk(t, s, "/v1beta/models/google-vertex/gemini-2.5-flash:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"List three primary colours."}]}],"safetySettings":`+safety+`,"generationConfig":`+gen+`}`)
	if out != vertexColours {
		t.Errorf("answered whole %s", out)
	}
	a = v.last(t)
	if a.path != "/v1/projects/proj-1/locations/global/publishers/google/models/gemini-2.5-flash:generateContent" || a.query != "" {
		t.Fatalf("asked whole at %s?%s", a.path, a.query)
	}
	sent, _ := json.Marshal(map[string]any{"generationConfig": a.body["generationConfig"], "safetySettings": a.body["safetySettings"]})
	var want map[string]any
	json.Unmarshal([]byte(`{"generationConfig":`+gen+`,"safetySettings":`+safety+`}`), &want)
	wantb, _ := json.Marshal(want)
	if string(sent) != string(wantb) {
		t.Errorf("sent %s, want the client's own %s", sent, wantb)
	}
	if _, ok := a.body["model"]; ok {
		t.Errorf("a model in the body: %v", a.body)
	}
	if _, ok := a.body["stream"]; ok {
		t.Errorf("a stream field: %v", a.body)
	}
	if rec := lastRecord(t); rec.Input != 5 || rec.Output != 7 || rec.Endpoint != "/v1beta/models/google-vertex/gemini-2.5-flash:generateContent" {
		t.Errorf("logged %d in, %d out, at %q", rec.Input, rec.Output, rec.Endpoint)
	}
}

// What Vertex AI takes that no other upstream does stays Vertex AI's:
// Factory's generateContent is still sent the model in its body and goes to
// its own path. Thought signatures are every Gemini path's (#1445), so
// Vertex AI's calls go back as it signed them and reach the client in
// their ids.
func TestVertexOnlyForVertex(t *testing.T) {
	r := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}},
		{Role: "assistant", Parts: []Part{
			{Kind: ToolCall, ID: "call_1", Name: "bash", Args: json.RawMessage(`{}`), Signature: vertexCallSig},
			{Kind: ToolCall, ID: "call_2", Name: "bash", Args: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "call_1", Text: "a"}, {Kind: ToolResult, CallID: "call_2", Text: "b"}}},
	}, Effort: "xhigh", Thinking: true}
	calls := func(b []byte) []any {
		var m map[string]any
		json.Unmarshal(b, &m)
		return m["contents"].([]any)[1].(map[string]any)["parts"].([]any)
	}

	factory := provider.Provider{ID: "factory", Account: &provider.Account{}} // signed in
	b, err := buildFor(factory, provider.Gemini, r, "gemini-3.1-pro-preview")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"model":"gemini-3.1-pro-preview"`) {
		t.Errorf("Factory's body has no model: %s", b)
	}
	if got := upstreamPath(factory, provider.Gemini, "gemini-3.1-pro-preview"); got != "/generate" {
		t.Errorf("Factory's path %s", got)
	}

	vertex := provider.Provider{ID: "google-vertex", Preset: provider.VertexPreset}
	b, _ = buildFor(vertex, provider.Gemini, r, "gemini-3.1-pro-preview")
	var top map[string]any
	json.Unmarshal(b, &top)
	if _, ok := top["model"]; ok {
		t.Errorf("Vertex AI's body has a model: %s", b)
	}
	got := calls(b)
	if got[0].(map[string]any)["thoughtSignature"] != vertexCallSig || got[1].(map[string]any)["thoughtSignature"] != nil {
		t.Errorf("Vertex AI was sent %v", got)
	}

	ids := func(dec func(string, func(Event)) error) []string {
		var out []string
		for _, line := range strings.Split(vertexCalls, "\r\n\r\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				dec(data, func(e Event) {
					if e.Kind == KToolStart {
						out = append(out, e.ID)
					}
				})
			}
		}
		return out
	}
	if got := ids(decoder(provider.Gemini)); !slices.Equal(got, []string{signedID("call_463646", vertexCallSig), "call_463649"}) {
		t.Errorf("Vertex AI's calls came out as %v", got)
	}
}

// A Gemini before 2.5 is sent no thinkingConfig, as it doesn't think, and
// a 2.5 is held to its own budget range whatever effort it is asked for.
func TestVertexThinkingRange(t *testing.T) {
	for _, c := range []struct {
		name, model string
		r           Request
		want        map[string]any
	}{
		{"2.0 high", "gemini-2.0-flash-001", Request{Effort: "high", Thinking: true}, nil},
		{"2.0 off", "gemini-2.0-flash-001", Request{Effort: "none", ThinkOff: true}, nil},
		{"1.5 thinking", "gemini-1.5-pro-002", Request{Thinking: true}, nil},
		{"2.5 flash xhigh", "gemini-2.5-flash", Request{Effort: "xhigh"}, map[string]any{"includeThoughts": true, "thinkingBudget": 24576}},
		{"2.5 flash-lite max", "gemini-2.5-flash-lite-preview", Request{Effort: "max"}, map[string]any{"includeThoughts": true, "thinkingBudget": 24576}},
		{"2.5 pro max", "gemini-2.5-pro", Request{Effort: "max"}, map[string]any{"includeThoughts": true, "thinkingBudget": 32000}},
	} {
		if got := vertexThinking(&c.r, c.model); !maps.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// The name a user gave a Vertex AI model upstream is the one in the path
// it is asked at, from every API, and the one the usage log names.
func TestVertexUpstreamName(t *testing.T) {
	v := &vertexUp{}
	s := vertexServe(t, v, vertexLevels)
	if err := provider.SetUpstreamName("google-vertex/gemini-3.8-flash", "gemini-3.8-flash-001"); err != nil {
		t.Fatal(err)
	}
	const at = "/v1/projects/proj-1/locations/global/publishers/google/models/gemini-3.8-flash-001:streamGenerateContent"
	vertexAsk(t, s, "/v1/chat/completions", `{"model":"google-vertex/gemini-3.8-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if a := v.last(t); a.path != at {
		t.Errorf("chat asked at %s", a.path)
	}
	if want := "/v1/chat/completions → /publishers/google/models/gemini-3.8-flash-001:streamGenerateContent?alt=sse"; lastRecord(t).Endpoint != want {
		t.Errorf("logged as %q, want %q", lastRecord(t).Endpoint, want)
	}
	vertexAsk(t, s, "/v1/messages", `{"model":"google-vertex/gemini-3.8-flash","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	if a := v.last(t); a.path != at {
		t.Errorf("messages asked at %s", a.path)
	}
	vertexAsk(t, s, "/v1beta/models/google-vertex/gemini-3.8-flash:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if a := v.last(t); a.path != at {
		t.Errorf("a Gemini client asked at %s", a.path)
	}
	vertexAsk(t, s, "/v1beta/models/google-vertex/gemini-3.8-flash:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if a := v.last(t); a.path != strings.TrimSuffix(at, ":streamGenerateContent")+":generateContent" {
		t.Errorf("a Gemini client asked whole at %s", a.path)
	}
}

// Vertex AI's retries are its own. Another upstream that answers a request
// with reasoning off with Vertex AI's words for minimal turned away is
// asked once, its 400 the client's, and is asked with reasoning off again
// next time.
func TestVertexMinimalRetryOnlyForVertex(t *testing.T) {
	var sent []any
	f := &fake{t: t, reply: sse(`data: [DONE]`)}
	f.refuse = func(b []byte) (int, string) {
		var v map[string]any
		json.Unmarshal(b, &v)
		sent = append(sent, v["reasoning_effort"])
		return 400, vertexMinimalPro
	}
	up := setup(t, provider.Chat, f)
	if err := catalog.SaveLive("fake", up.URL+"/v1", []catalog.Model{{ID: "m1", Context: 128000, Efforts: []string{"none", "low", "high"}}}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	for turn := range 2 {
		sent = nil
		if code, body := postTo(t, srv, "/v1/messages", autoModeAsk("m1")); code != 400 {
			t.Fatalf("turn %d: %d %s", turn+1, code, body)
		}
		if !slices.Equal(sent, []any{"none"}) {
			t.Errorf("turn %d: efforts sent %v, want none once", turn+1, sent)
		}
	}
}

// Nor is another upstream that answers a signed call with "Invalid thought
// signature." asked again without the signatures: once, its 400 the
// client's.
func TestVertexRetriesOnlyForVertex(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	asked := 0
	refusal := ""
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked++
		out := refusal
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, out)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Key: "key", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, c := range []struct{ name, refusal, path, body string }{
		{"signature", vertexBadSig, "/v1/messages", `{"model":"relay/m1","max_tokens":100,"messages":[
			{"role":"user","content":"weather?"},
			{"role":"assistant","content":[{"type":"tool_use","id":"` + signedID("call_1", vertexCallSig) + `","name":"get_weather","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + signedID("call_1", vertexCallSig) + `","content":"sunny"}]}],
			"tools":[{"name":"get_weather","input_schema":{"type":"object"}}]}`},
	} {
		mu.Lock()
		asked, refusal = 0, c.refusal
		mu.Unlock()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(c.body)))
		mu.Lock()
		n := asked
		mu.Unlock()
		if rec.Code != http.StatusBadRequest || n != 1 {
			t.Errorf("%s: status %d after %d requests, want the 400 after one: %s", c.name, rec.Code, n, rec.Body.String())
		}
	}
}
