package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// systemOneUp is a vendor's System One API: it keeps each request and
// answers with reply, as Ark answered on 2026-11-01 (a score's
// probabilities by level index, a choice's by name, a noul a probability;
// usage in input and output tokens).
type systemOneUp struct {
	mu    sync.Mutex
	paths []string
	got   []map[string]any
	reply string
}

func (u *systemOneUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(b, &body)
	u.mu.Lock()
	u.paths, u.got = append(u.paths, r.URL.Path), append(u.got, body)
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	io.WriteString(w, u.reply)
}

func (u *systemOneUp) asked() []map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]map[string]any(nil), u.got...)
}

const parcelSystemOneReply = `{"model":"decision-model-preview","answers":{"damaged":{"type":"noul","noul":0.91},"condition":{"type":"choice","choice":"used","probabilities":{"new":0.12,"used":0.88},"confidence":0.76},"severity":{"type":"score","score":2.05,"probabilities":{"0":0.01,"1":0.09,"2":0.73,"3":0.17},"confidence":0.43}},"usage":{"input_tokens":452,"output_tokens":0}}`

// qwenDecision saves wudi_boy's provider, the Qwen AI platform's decision
// model, its System One API at up.
func qwenDecision(t *testing.T, up http.Handler) {
	t.Helper()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p, err := provider.FromPreset("qwen-decision")
	if err != nil {
		t.Fatal(err)
	}
	// the stand-in host isn't Bailian's, so the model is the one picked
	p.Key, p.Decide, p.Models = "sk-qwen", srv.URL+"/compatible-mode/v1", []string{"decision-model-preview"}
	if id, err := provider.Add(p); err != nil || id != "qwen-decision" {
		t.Fatalf("saved as %q: %v", id, err)
	}
}

// parcelDecision is a Decisions request (openai-python's
// DecisionCreateParams) with a predicate, a choice and a score, and the
// prompt-cache hints and safety_identifier that change no answer.
const parcelDecision = `{"model":"qwen-decision/decision-model-preview","input":"The parcel arrived torn open, one corner crushed.","questions":[` +
	`{"type":"predicate","name":"damaged","instructions":"Was the parcel damaged?"},` +
	`{"type":"choice","name":"condition","instructions":"Its condition","choices":[{"value":"new"},{"value":"used","description":"opened or worn"}]},` +
	`{"type":"score","name":"severity","instructions":"How bad is the damage?","levels":[{"label":"none"},{"label":"minor"},{"label":"major","description":"the item is hurt"},{"label":"total"}]}],` +
	`"prompt_cache_key":"parcels","safety_identifier":"u-1"}`

// wudi_boy on Discord: /v1/decisions to a model that answers System One
// (Python-urllib's call to qwen-decision decision-model-preview) was
// refused. It is asked as System One — a predicate a noul, a choice's
// values its criteria, a score's levels its criteria least first — and
// answers as Decisions does, in the questions' order, with its usage.
func TestDecisionsAskSystemOne(t *testing.T) {
	fresh(t)
	up := &systemOneUp{reply: parcelSystemOneReply}
	qwenDecision(t, up)
	code, out := sendTo(New(), "/v1/decisions", parcelDecision)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	got := up.asked()
	if len(got) != 1 || up.paths[0] != "/compatible-mode/v1/systemone" {
		t.Fatalf("asked %v", up.paths)
	}
	sent, _ := json.Marshal(got[0])
	for _, want := range []string{
		`"model":"decision-model-preview"`,
		`"state":"The parcel arrived torn open, one corner crushed."`,
		`"damaged":{"instructions":"Was the parcel damaged?","type":"noul"}`,
		`"condition":{"criteria":{"new":null,"used":"opened or worn"},"instructions":"Its condition","type":"choice"}`,
		`"severity":{"criteria":["none","minor","major: the item is hurt","total"],"instructions":"How bad is the damage?","type":"score"}`,
	} {
		if !strings.Contains(string(sent), want) {
			t.Fatalf("System One was asked %s\nwant %s", sent, want)
		}
	}
	if strings.Contains(string(sent), "prompt_cache") || strings.Contains(string(sent), "safety") {
		t.Fatalf("a Decisions-only hint went to System One: %s", sent)
	}
	var d struct {
		Answers []map[string]any `json:"answers"`
		Usage   map[string]any   `json:"usage"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil || len(d.Answers) != 3 {
		t.Fatalf("not a Decision: %s", out)
	}
	a, _ := json.Marshal(d.Answers)
	want := `[{"name":"damaged","probability":0.91,"type":"predicate"},` +
		`{"choice":"used","confidence":0.76,"name":"condition","probabilities":[{"probability":0.12,"value":"new"},{"probability":0.88,"value":"used"}],"type":"choice"},` +
		`{"confidence":0.43,"name":"severity","probabilities":[{"label":"none","probability":0.01,"value":0},{"label":"minor","probability":0.09,"value":1},{"label":"major","probability":0.73,"value":2},{"label":"total","probability":0.17,"value":3}],"score":2.05,"type":"score"}]`
	if string(a) != want {
		t.Fatalf("answers\n%s\nwant\n%s", a, want)
	}
	if d.Usage["input_tokens"] != 452.0 || d.Usage["output_tokens"] != 0.0 || d.Usage["total_tokens"] != 452.0 {
		t.Fatalf("usage %v", d.Usage)
	}
}

// A conversation goes as the state's messages, an input_json part's data
// as its JSON and an assistant's refusal as what it is; an unnamed
// question is named for System One and answers with no name, and a
// boolean choice comes back a boolean.
func TestDecisionsMessagesAsSystemOneState(t *testing.T) {
	fresh(t)
	up := &systemOneUp{reply: `{"model":"decision-model-preview","answers":{"question_1":{"type":"choice","choice":"true","probabilities":{"true":0.7,"false":0.3},"confidence":0.4}},"usage":{"input_tokens":40,"output_tokens":2}}`}
	qwenDecision(t, up)
	code, out := sendTo(New(), "/v1/decisions", `{"model":"qwen-decision/decision-model-preview","input":[`+
		`{"role":"developer","content":"Judge the support thread."},`+
		`{"role":"user","content":[{"type":"input_text","text":"Order 7 is late."},{"type":"input_json","data":{"order":7,"days":3}}]},`+
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Checking."},{"type":"refusal","refusal":"I can't refund."}]}],`+
		`"questions":[{"type":"choice","instructions":"Is the user upset?","choices":[{"value":true},{"value":false}]}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	sent, _ := json.Marshal(up.asked()[0]["state"])
	if want := `{"messages":[{"content":"Judge the support thread.","role":"developer"},{"content":"Order 7 is late.\n{\"order\":7,\"days\":3}","role":"user"},{"content":"Checking.","refusal":"I can't refund.","role":"assistant"}]}`; string(sent) != want {
		t.Fatalf("state %s\nwant %s", sent, want)
	}
	if want := `"answers":[{"choice":true,"confidence":0.4,"name":null,"probabilities":[{"probability":0.7,"value":true},{"probability":0.3,"value":false}],"type":"choice"}]`; !strings.Contains(out, want) {
		t.Fatalf("answer %s\nwant %s", out, want)
	}
	if !strings.Contains(out, `"input_tokens":40`) || !strings.Contains(out, `"output_tokens":2`) {
		t.Fatalf("usage %s", out)
	}
}

// What System One has no counterpart for is refused with a 400 that
// names it, and System One is never asked: an image where the model takes
// none, a tool call or its output, an image in a question.
func TestDecisionsUntranslatableRefused(t *testing.T) {
	fresh(t)
	up := &systemOneUp{reply: parcelSystemOneReply}
	qwenDecision(t, up)
	s := New()
	q := `"questions":[{"type":"predicate","name":"damaged","instructions":"Was it damaged?"}]`
	for _, c := range []struct{ body, says string }{
		{decisionAsked("qwen-decision/decision-model-preview"), "input[0].content[1] is an image (input_image), which qwen-decision/decision-model-preview can't take"},
		{`{"model":"qwen-decision/decision-model-preview","input":[{"role":"user","content":"look"},{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"}],` + q + `}`, "input[1] is a function_call"},
		{`{"model":"qwen-decision/decision-model-preview","input":[{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}],` + q + `}`, "input[0] is a custom_tool_call_output"},
		{`{"model":"qwen-decision/decision-model-preview","input":"x","questions":[{"type":"predicate","instructions":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}]}`, "questions[0].instructions[0] is an image"},
		{`{"model":"qwen-decision/decision-model-preview","input":"x","reasoning":{"effort":"low"},` + q + `}`, `\"reasoning\" has no counterpart in System One`},
	} {
		code, out := sendTo(s, "/v1/decisions", c.body)
		if code != 400 || !strings.Contains(out, c.says) {
			t.Fatalf("%d %s\nwant 400 saying %s", code, out, c.says)
		}
	}
	if n := len(up.asked()); n != 0 {
		t.Fatalf("System One was asked %d times", n)
	}
}

// A Clef, which takes images, gets a Decisions request's images as
// Workers AI takes them, and its answer out of Cloudflare's envelope.
func TestDecisionsImagesToClef(t *testing.T) {
	fresh(t)
	up := &systemOneUp{reply: `{"result":{"model":"clef","answers":{"visible_damage":{"type":"noul","noul":0.97},"condition":{"type":"choice","choice":"new","probabilities":{"new":0.6,"used":0.4},"confidence":0.2}},"usage":{"input_tokens":900,"output_tokens":0}},"success":true,"errors":[],"messages":[]}`}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p, err := provider.FromPreset("cloudflare-jev")
	if err != nil {
		t.Fatal(err)
	}
	p.Key, p.Decide = "k", srv.URL+"/client/v4/accounts/a/ai/run"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, out := sendTo(New(), "/v1/decisions", decisionAsked(p.ID+"/@cf/cloudflare/clef"))
	if code != 200 || !strings.Contains(out, `{"name":"visible_damage","probability":0.97,"type":"predicate"}`) || !strings.Contains(out, `"input_tokens":900`) {
		t.Fatalf("%d %s", code, out)
	}
	got := up.asked()
	if len(got) != 1 || up.paths[0] != "/client/v4/accounts/a/ai/run/@cf/cloudflare/clef" {
		t.Fatalf("asked %v", up.paths)
	}
	if ims, _ := got[0]["images"].([]any); len(ims) != 1 || ims[0] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("the Clef got images %v", got[0]["images"])
	}
	if st, _ := json.Marshal(got[0]["state"]); string(st) != `{"messages":[{"content":"Inspect the product in this photo.","role":"user"}]}` {
		t.Fatalf("state %s", st)
	}
}

// The other way: /v1/systemone to a model only OpenAI's Decisions API
// serves is asked there — a noul a predicate, a choice's criteria its
// choices, the state its input, the images input_image parts — and
// answers as System One does, by name, with its usage.
func TestSystemOneAsksDecisions(t *testing.T) {
	openai, _ := decidingProviders(t)
	s := New()
	code, out := sendTo(s, "/v1/systemone", `{"model":"openai/gpt-6-luna","state":{"photo":"product","order":7},"images":["data:image/png;base64,iVBORw0KGgo="],`+
		`"questions":{"visible_damage":{"type":"noul","instructions":"Is there visible damage?"},"condition":{"type":"choice","instructions":"Its condition","criteria":{"new":null,"used":"opened or worn"}}}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	hits := openai.all()
	if len(hits) != 1 || hits[0].path != "/v1/decisions" || hits[0].auth != "Bearer sk-openai" {
		t.Fatalf("hits %+v", hits)
	}
	sent, _ := json.Marshal(hits[0].body)
	for _, want := range []string{
		`"model":"gpt-6-luna"`,
		`"input":[{"content":[{"data":{"order":7,"photo":"product"},"type":"input_json"},{"image_url":"data:image/png;base64,iVBORw0KGgo=","type":"input_image"}],"role":"user","type":"message"}]`,
		`"questions":[{"instructions":"Is there visible damage?","name":"visible_damage","type":"predicate"},{"choices":[{"value":"new"},{"description":"opened or worn","value":"used"}],"instructions":"Its condition","name":"condition","type":"choice"}]`,
	} {
		if !strings.Contains(string(sent), want) {
			t.Fatalf("Decisions was asked %s\nwant %s", sent, want)
		}
	}
	var a struct {
		Answers map[string]map[string]any `json:"answers"`
		Usage   map[string]any            `json:"usage"`
	}
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatalf("not a System One answer: %s", out)
	}
	if a.Answers["visible_damage"]["noul"] != 0.92 || a.Answers["condition"]["choice"] != "used" || a.Answers["condition"]["probabilities"].(map[string]any)["used"] != 0.8 {
		t.Fatalf("answers %s", out)
	}
	if a.Usage["input_tokens"] != 1200.0 || a.Usage["output_tokens"] != 3.0 {
		t.Fatalf("usage %v", a.Usage)
	}
	if recs := decisionRecords(t, 1); recs[0].Input+recs[0].CacheRead+recs[0].CacheWrite != 1200 {
		t.Fatalf("recorded %+v", recs[0])
	}
	// a question Decisions has no counterpart for is refused, and not sent
	code, out = sendTo(s, "/v1/systemone", `{"model":"openai/gpt-6-luna","state":"x","questions":{"q":{"type":"rank","instructions":"?"}}}`)
	if code != 400 || !strings.Contains(out, `questions.q is a \"rank\" question`) || len(openai.all()) != 1 {
		t.Fatalf("%d %s", code, out)
	}
	// a model no API serves is still System One's 404
	if code, out = sendTo(s, "/v1/systemone", `{"model":"nobody/none","state":"x","questions":{"q":{"type":"noul"}}}`); code != 404 {
		t.Fatalf("%d %s", code, out)
	}
}
