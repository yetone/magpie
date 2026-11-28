package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// predictionSchema is the schema ChatGPT.app's composer predictions ask
// for, as its main bundle builds it ({suggestion: string|null}).
const predictionSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"suggestion":{"anyOf":[{"type":"string"},{"type":"null"}]}},"required":["suggestion"],"additionalProperties":false}`

// codexPredictionRequest is a composer prediction as ChatGPT.app's Codex
// sends one through its ChatGPT sign-in (#1970, pluo): a hidden turn on a
// fork of the thread, on the thread's own model, its thread_source
// composer_predictions, its prompt ending in the schema.
func codexPredictionRequest(t *testing.T, s *Server, path, model string) *httptest.ResponseRecorder {
	t.Helper()
	prompt := "Predict the user's next message.\n\nReturn only JSON matching this schema: " + predictionSchema + "."
	in, _ := json.Marshal(prompt)
	body := `{"model":"` + model + `","stream":true,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the login bug"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Fixed it; the tests pass."}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":` + string(in) + `}]}],` +
		`"text":{"format":{"type":"json_schema","name":"codex_output_schema","strict":true,"schema":` + predictionSchema + `}}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("session_id", "prediction-thread")
	req.Header.Set("x-codex-turn-metadata", `{"thread_source":"composer_predictions","forked_from_thread_id":"main-chat"}`)
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func predictionReply(text string) string {
	b, _ := json.Marshal(text)
	return sse(
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":`+string(b)+`}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":8}}`,
		`data: [DONE]`)
}

func saveCodexPredictions(t *testing.T, v string) {
	t.Helper()
	st := settings.Load()
	st.CodexPredictions = v
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
}

// Codex's composer predictions go where Settings says (#1970): as they
// came, to Codex's own sign-in, by default; off, answered here with no
// prediction and sent nowhere; or to a model of magpie's, its answer
// handed back as the {"suggestion": …} Codex reads. Each stays a
// prediction in the Usage view.
func TestCodexPredictionsSetting(t *testing.T) {
	f := &fake{t: t, reply: predictionReply(`{"suggestion":"Run the tests again"}`)}
	setup(t, provider.Chat, f)
	chatgptCalls := 0
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		chatgptCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"suggestion\":\"From ChatGPT\"}"}]}],"usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	s := New()

	rec := codexPredictionRequest(t, s, CodexPath+"/responses", "gpt-6-astra")
	if text, _ := outputText(t, rec.Body.String()); chatgptCalls != 1 || f.calls != 0 || text != `{"suggestion":"From ChatGPT"}` {
		t.Fatalf("default: chatgpt %d, magpie model %d, %q", chatgptCalls, f.calls, text)
	}

	saveCodexPredictions(t, "off")
	rec = codexPredictionRequest(t, s, CodexPath+"/responses", "gpt-6-astra")
	if text, n := outputText(t, rec.Body.String()); rec.Code != 200 || n != 1 || text != `{"suggestion":null}` || chatgptCalls != 1 || f.calls != 0 {
		t.Fatalf("off: %d %q, %d items, chatgpt %d, magpie model %d", rec.Code, rec.Body.String(), n, chatgptCalls, f.calls)
	}

	saveCodexPredictions(t, "fake/m1")
	rec = codexPredictionRequest(t, s, CodexPath+"/responses", "gpt-6-astra")
	if text, _ := outputText(t, rec.Body.String()); rec.Code != 200 || text != `{"suggestion":"Run the tests again"}` || f.calls != 1 || chatgptCalls != 1 {
		t.Fatalf("routed: %d %q, magpie model %d, chatgpt %d", rec.Code, rec.Body.String(), f.calls, chatgptCalls)
	}
	var sent struct {
		Model string `json:"model"`
	}
	json.Unmarshal(f.got, &sent)
	if sent.Model != "m1" {
		t.Errorf("the magpie model was asked for %q", sent.Model)
	}

	// a turn of the conversation is not a prediction: it goes as before
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-6-astra","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	s.Handler().ServeHTTP(rec, req)
	if chatgptCalls != 2 || f.calls != 1 {
		t.Errorf("a plain turn: chatgpt %d, magpie model %d", chatgptCalls, f.calls)
	}

	var kinds []string
	for _, u := range usage.Load(time.Time{}) {
		kinds = append(kinds, u.Provider+":"+u.Kind)
		if u.Provider == "magpie" && (!u.IsRejected() || u.Status != 200) {
			t.Errorf("an answered-here prediction %+v", u)
		}
		// and says where the setting is: Codex's row on the Agents page
		// since f1e4516d, not Settings (#3425)
		if u.Provider == "magpie" && !strings.Contains(u.Error, "in Codex's settings on magpie's Agents page") {
			t.Errorf("an answered-here prediction says %q", u.Error)
		}
	}
	want := "openai:composer_predictions magpie:composer_predictions fake:composer_predictions openai:"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("ledger %s\nwant   %s", got, want)
	}
}

// A model that talks instead of answering with the object gives Codex no
// prediction, never its words as the user's next message, and the call
// is in the Usage view as the failure it is.
func TestNoPrediction(t *testing.T) {
	f := &fake{t: t, reply: predictionReply("Sure! The user will probably ask you to run the tests again.")}
	setup(t, provider.Chat, f)
	saveCodexPredictions(t, "fake/m1")
	s := New()
	rec := codexPredictionRequest(t, s, CodexPath+"/responses", "gpt-6-astra")
	if text, n := outputText(t, rec.Body.String()); rec.Code != 200 || n != 1 || text != `{"suggestion":null}` {
		t.Fatalf("talking reply: %d %q", rec.Code, rec.Body.String())
	}
	var last usage.Record
	for _, u := range usage.Load(time.Time{}) {
		last = u
	}
	if last.Kind != "composer_predictions" || !strings.HasPrefix(last.Error, "prediction: ") || !last.Failed() {
		t.Fatalf("unusable reply was not flagged: %+v", last)
	}
}

// Codex on a magpie model (an "OpenAI" provider table pointed at magpie,
// #1301), with nothing set: the model asked for writes the prediction,
// its answer wrapped as Codex reads it.
func TestCodexPredictionOnMagpie(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/backend-api/codex/responses"} {
		t.Run(path, func(t *testing.T) {
			f := &fake{t: t, reply: predictionReply("```json\n{\"suggestion\": \"  Commit it  \"}\n```")}
			setup(t, provider.Chat, f)
			s := New()
			rec := codexPredictionRequest(t, s, path, "fake/m1")
			if text, _ := outputText(t, rec.Body.String()); rec.Code != 200 || text != `{"suggestion":"Commit it"}` || f.calls != 1 {
				t.Fatalf("on magpie: %d %q, calls %d", rec.Code, rec.Body.String(), f.calls)
			}
		})
	}
}

func TestPredictionJSON(t *testing.T) {
	for in, want := range map[string]struct {
		out string
		ok  bool
	}{
		`{"suggestion":"Run it"}`:                         {`{"suggestion":"Run it"}`, true},
		`{"suggestion":null}`:                             {`{"suggestion":null}`, true},
		`{"suggestion":"  "}`:                             {`{"suggestion":null}`, true},
		"<think>hm</think>\n{\"suggestion\":\"Ship it\"}": {`{"suggestion":"Ship it"}`, true},
		"Run the tests":                                   {`{"suggestion":null}`, false},
		`{"description":"x"}`:                             {`{"suggestion":null}`, false},
		`{"suggestion":3}`:                                {`{"suggestion":null}`, false},
		"":                                                {`{"suggestion":null}`, false},
	} {
		if got, ok := predictionJSON(in); got != want.out || ok != want.ok {
			t.Errorf("predictionJSON(%q) = %s %v, want %s %v", in, got, ok, want.out, want.ok)
		}
	}
}

// orcaPredictionPrompt is Orca's PREDICTION_PROMPT as v1.4.278 to v1.4.281
// build it (stablyai/orca
// src/main/codex/structured/turns/codex-composer-predictions.ts): plain
// text, NO_PREDICTION for none, no schema.
const orcaPredictionPrompt = "Do not call any tools and do not continue the task. " +
	"Write the message the user is most likely to send you next, worded the way they write to " +
	"you: a short, direct request of at most 20 words. Prefer a concrete next step that follows " +
	"from your last reply. Reply with only that message, no quotes or preamble. " +
	"Reply NO_PREDICTION only if the user has said they are done."

// samplePredictionRequest is a composer prediction as Codex's
// thread/sample sends it (codex-rs app-server thread_sample.rs): the
// prompt is the user's message of a turn on a fork of the thread, with
// no text.format. Orca (agent orca_desktop, #8017) and ChatGPT.app's
// Codex both ask this way; only the prompt tells them apart. The earlier
// turn quotes "suggestion" as the user might.
func samplePredictionRequest(t *testing.T, s *Server, agent, prompt string) *httptest.ResponseRecorder {
	t.Helper()
	in, _ := json.Marshal(prompt)
	body := `{"model":"fake/m1","stream":true,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"帮我算两组数的平均值，先算第一组 (key \"suggestion\" in the config)"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"第一组的平均值是 4。"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":` + string(in) + `}]}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("originator", agent)
	req.Header.Set("session_id", "prediction-fork")
	req.Header.Set("x-codex-turn-metadata", `{"thread_source":"composer_predictions","forked_from_thread_id":"main-chat"}`)
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// Orca asks for the predicted message as plain text and shows whatever
// text comes back (#8017, Jasyl0519; L0NE-6's reading): its suggestion
// goes back as that text, and NO_PREDICTION or nothing goes back as no
// message, which thread/sample hands Orca as no text, so its composer
// stays empty instead of showing {"suggestion":null}.
func TestOrcaPredictionIsPlainText(t *testing.T) {
	for _, c := range []struct {
		said, want string
		items      int
		failed     bool
	}{
		{"算第二组的平均值", "算第二组的平均值", 1, false},
		{"<think>they want the second set</think>\n  算第二组的平均值  ", "算第二组的平均值", 1, false},
		{"NO_PREDICTION", "", 0, false},
		{"`NO_PREDICTION`.", "", 0, false},
		{"", "", 0, true},
	} {
		t.Run(c.said, func(t *testing.T) {
			f := &fake{t: t, reply: predictionReply(c.said)}
			setup(t, provider.Chat, f)
			s := New()
			rec := samplePredictionRequest(t, s, "orca_desktop", orcaPredictionPrompt)
			text, n := outputText(t, rec.Body.String())
			if rec.Code != 200 || text != c.want || n != c.items || f.calls != 1 {
				t.Fatalf("Orca got %d %q in %d items (calls %d), want %q in %d", rec.Code, text, n, f.calls, c.want, c.items)
			}
			var last usage.Record
			for _, u := range usage.Load(time.Time{}) {
				last = u
			}
			if last.Kind != "composer_predictions" || last.Failed() != c.failed {
				t.Errorf("ledger %+v, failed want %v", last, c.failed)
			}
		})
	}

	// Off answers Orca with no message, not the object
	f := &fake{t: t, reply: predictionReply("x")}
	setup(t, provider.Chat, f)
	saveCodexPredictions(t, "off")
	rec := samplePredictionRequest(t, New(), "orca_desktop", orcaPredictionPrompt)
	if text, n := outputText(t, rec.Body.String()); rec.Code != 200 || n != 0 || text != "" || f.calls != 0 {
		t.Fatalf("off: %d %q in %d items, calls %d", rec.Code, text, n, f.calls)
	}
}

// ChatGPT.app's Codex asks through thread/sample too, with no text.format:
// its prompt ends in the schema (the bundle's "Return only JSON matching
// this schema: …", $schema left out). Its rule stays strict, byte for
// byte: a talking model's words never reach the composer, and Off is
// {"suggestion":null}, whatever the client's name.
func TestCodexSamplePredictionStaysJSON(t *testing.T) {
	var shape map[string]any
	json.Unmarshal([]byte(predictionSchema), &shape)
	delete(shape, "$schema")
	schema, _ := json.Marshal(shape)
	prompt := "Predict the user's next message.\n\nReturn only JSON matching this schema: " + string(schema) + "."
	for said, want := range map[string]string{
		`{"suggestion":"Run the tests again"}`:                         `{"suggestion":"Run the tests again"}`,
		"```json\n{\"suggestion\": \"  Commit it  \"}\n```":            `{"suggestion":"Commit it"}`,
		`{"suggestion":null}`:                                          `{"suggestion":null}`,
		"Sure! The user will probably ask you to run the tests again.": `{"suggestion":null}`,
		"NO_PREDICTION": `{"suggestion":null}`,
		"":              `{"suggestion":null}`,
	} {
		for _, agent := range []string{"Codex Desktop", "orca_desktop"} {
			f := &fake{t: t, reply: predictionReply(said)}
			setup(t, provider.Chat, f)
			rec := samplePredictionRequest(t, New(), agent, prompt)
			if text, n := outputText(t, rec.Body.String()); rec.Code != 200 || n != 1 || text != want {
				t.Errorf("%s, %q: %d %q in %d items, want %s", agent, said, rec.Code, text, n, want)
			}
		}
	}
	f := &fake{t: t, reply: predictionReply("x")}
	setup(t, provider.Chat, f)
	saveCodexPredictions(t, "off")
	rec := samplePredictionRequest(t, New(), "Codex Desktop", prompt)
	if text, n := outputText(t, rec.Body.String()); n != 1 || text != `{"suggestion":null}` || f.calls != 0 {
		t.Fatalf("off: %q in %d items, calls %d", text, n, f.calls)
	}
}

func TestPredictionWantsObject(t *testing.T) {
	msg := func(role, text string) string {
		b, _ := json.Marshal(text)
		return `{"type":"message","role":"` + role + `","content":[{"type":"input_text","text":` + string(b) + `}]}`
	}
	for name, c := range map[string]struct {
		body string
		want bool
	}{
		"text.format's schema":    {`{"input":[` + msg("user", "hi") + `],"text":{"format":{"type":"json_schema","schema":` + predictionSchema + `}}}`, true},
		"the prompt's schema":     {`{"input":[` + msg("user", `Return only JSON matching this schema: {"type":"object","properties":{"suggestion":{}}}.`) + `]}`, true},
		"a note after the prompt": {`{"input":[` + msg("user", `{"suggestion"}`) + `,` + msg("developer", "snapshot") + `]}`, true},
		"a string input":          {`{"input":"Return {\"suggestion\": …}"}`, true},
		"unreadable":              {`{"input":`, true},
		"Orca's prompt":           {`{"input":[` + msg("user", orcaPredictionPrompt) + `]}`, false},
		"quoted before the fork":  {`{"input":[` + msg("user", `the "suggestion" key`) + `,` + msg("assistant", "ok") + `,` + msg("user", orcaPredictionPrompt) + `]}`, false},
		"after a tool call":       {`{"input":[` + msg("user", `"suggestion"`) + `,{"type":"function_call_output","call_id":"c","output":"x"},` + msg("user", orcaPredictionPrompt) + `]}`, false},
	} {
		if got := predictionWantsObject([]byte(c.body)); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
