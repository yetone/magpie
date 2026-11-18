package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Google's Decisions API, as its reference gives it
// (aistudio.google.com/api-reference/decisions/create-decision, 11-11):
// the yes-or-no example's request and its answer.
const (
	googleNoulAsk    = `{"model": "gemini-decision-1","input": "Customer message: Hi, I was charged twice for my monthly subscription this morning. Can you refund the duplicate charge?","questions": {"needs_billing_refund": {"type": "noul","instructions": "Determine whether the customer is requesting a billing refund.","criteria": {"true_description": "The customer explicitly asks for a refund or reversal of a charge.","false_description": "The customer is asking a general question or reporting a non-billing issue."}}}}`
	googleNoulAnswer = `{
    "answers": {
      "needs_billing_refund": {
        "type": "noul",
        "confidence": 0.97,
        "noul": 0.97
      }
    },
    "usage": {
      "input_tokens_by_modality": [
        {
          "modality": "text",
          "tokens": 152
        }
      ],
      "total_input_tokens": 152,
      "total_output_tokens": 0,
      "total_tokens": 152
    }
  }`
	// what an object as the input was answered with (WangYeruo on X)
	googleNoType = `{"error":{"code":400,"message":"type parameter is required at input[0]","status":"INVALID_ARGUMENT"}}`
)

func sameJSON(t *testing.T, what string, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: %v in %s", what, err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("%s:\n got %s\nwant %s", what, got, want)
	}
}

// A System One request goes to Google as its Decisions API takes it: the
// state as the input, text as it is and an object as its JSON text, a
// choice's undescribed option with an empty description, images as image
// blocks after the text; and its answer comes back as System One's, with
// its usage named as System One names it.
func TestGoogleDecideWire(t *testing.T) {
	ask := strings.Replace(googleNoulAsk, `"input"`, `"state"`, 1)
	sameJSON(t, "text state", DecideAsk(ViaGoogle, GoogleDecision, []byte(ask)), googleNoulAsk)

	obj := `{"model":"gemini-decision-1","state":{"message":"fix the build","previous_message_kind":"coding"},"questions":{"intent":{"type":"choice","instructions":"Which?","criteria":{"coding":null,"chat":"small talk"}}},"images":["data:image/png;base64,iVBORw0KGgo=","https://example.com/a.jpg"]}`
	sameJSON(t, "object state", DecideAsk(ViaGoogle, GoogleDecision, []byte(obj)),
		`{"model":"gemini-decision-1","input":[{"type":"text","text":"{\"message\":\"fix the build\",\"previous_message_kind\":\"coding\"}"},{"type":"image","data":"iVBORw0KGgo=","mime_type":"image/png"},{"type":"image","uri":"https://example.com/a.jpg"}],"questions":{"intent":{"type":"choice","instructions":"Which?","criteria":{"coding":"","chat":"small talk"}}}}`)

	status, b := DecideResult(ViaGoogle, 200, []byte(googleNoulAnswer))
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	sameJSON(t, "answer", b, `{"answers":{"needs_billing_refund":{"type":"noul","confidence":0.97,"noul":0.97}},"usage":{"input_tokens":152,"output_tokens":0}}`)

	// an input its filters blocked answers nothing: a 400 that says why
	status, b = DecideResult(ViaGoogle, 200, []byte(`{"answers":{},"prompt_feedback":{"block_reason":"safety","block_reason_message":"Harmful content."}}`))
	if status != 400 || APIError(b, "") != "Google blocked the input (safety): Harmful content." {
		t.Fatalf("blocked: %d %s", status, b)
	}
	// a refusal is passed on as Google gave it
	if status, b = DecideResult(ViaGoogle, 400, []byte(googleNoType)); status != 400 || string(b) != googleNoType {
		t.Fatalf("refusal: %d %s", status, b)
	}
	// and nothing changes for any other way of asking
	for _, via := range []string{ViaSystemOne, ViaVercel} {
		if got := DecideAsk(via, "jev-latest", []byte(obj)); string(got) != obj {
			t.Fatalf("%s: %s", via, got)
		}
		if status, got := DecideResult(via, 200, []byte(googleNoulAnswer)); status != 200 || string(got) != googleNoulAnswer {
			t.Fatalf("%s answer: %d %s", via, status, got)
		}
	}
}

// googleUp stands in for generativelanguage.googleapis.com, in process:
// POST /v1beta/decisions with the key as x-goog-api-key, the input not
// an object (an object is a content block, refused without its type), is
// answered with Google's documented answer; a key as a Bearer token is
// refused as Google refuses it. Any other host is never reached.
type googleUp struct {
	mu    sync.Mutex
	asked []string
	body  []byte
}

func (u *googleUp) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() != "generativelanguage.googleapis.com" {
		return nil, errors.New("test reached " + r.URL.Host)
	}
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.asked = append(u.asked, r.Method+" "+r.URL.Path+" key="+r.Header.Get("x-goog-api-key")+" auth="+r.Header.Get("Authorization"))
	u.body = b
	u.mu.Unlock()
	reply := func(code int, s string) (*http.Response, error) {
		return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json; charset=UTF-8"}},
			Body: io.NopCloser(strings.NewReader(s)), Request: r}, nil
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1beta/decisions" {
		return reply(404, `{"error":{"code":404,"message":"Not found","status":"NOT_FOUND"}}`)
	}
	if r.Header.Get("x-goog-api-key") != "AIza-test" {
		return reply(401, `{"error":{"code":401,"message":"Request had invalid authentication credentials. Expected OAuth 2 access token, login cookie or other valid authentication credential.","status":"UNAUTHENTICATED"}}`)
	}
	var q struct {
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(b, &q) != nil || len(q.Input) == 0 || q.Input[0] == '{' {
		return reply(400, googleNoType)
	}
	return reply(200, googleNoulAnswer)
}

func (u *googleUp) got() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.asked)
}

func fakeGoogleDecisions(t *testing.T) *googleUp {
	up := &googleUp{}
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = up
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	return up
}

// WangYeruo on X: with a Google AI Studio key's provider (the google
// preset), google/gemini-decision-1 went to OpenRouter, the google
// provider having no decision API. Google answers it at the Gemini API's
// /v1beta/decisions on the same key: a provider asked at the Gemini
// Developer API, the preset's or a custom one, offers it with no URL
// typed, keeps it out of its chat models, and is the one asked for it;
// Vertex and other hosts are as they were.
func TestGoogleKeyDecides(t *testing.T) {
	arkHome(t)
	up := fakeGoogleDecisions(t)
	g, err := FromPreset("google")
	if err != nil {
		t.Fatal(err)
	}
	g.Key, g.Models = "AIza-test", []string{"gemini-3.5-flash"}
	or, err := FromPreset("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	or.Key = "sk-or-1"
	for _, p := range []Provider{g,
		{ID: "gem-native", Name: "Gemini native", Gemini: AIStudioGemini, Key: "AIza-test"},
		{ID: "other", Name: "Other", Chat: "https://api.example.com/v1beta/openai", Key: "k"},
		{ID: "own", Name: "Own", Chat: AIStudioChat, Decide: "https://api.typesafe.ai/v1", Key: "AIza-test"},
		or,
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	// OpenRouter lists Google's decision model as its own, as it does
	if err := catalog.SaveLive(decisionsID("openrouter"), "https://openrouter.ai/api/v1", []catalog.Model{{ID: "google/gemini-decision-1"}, {ID: OpenRouterJev}}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"google": AIStudioGemini, "gem-native": AIStudioGemini, "other": "", "own": "https://api.typesafe.ai/v1"} {
		p, err := Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Decide != want {
			t.Errorf("%s: decisions at %q, want %q", id, p.Decide, want)
		}
	}
	p, err := Find("google")
	if err != nil {
		t.Fatal(err)
	}
	if p.DecideVia() != ViaGoogle || p.Jev() != GoogleDecision || !p.DecidesModel(GoogleDecision) || p.DecidesModel("gemini-3.5-flash") {
		t.Fatalf("google: via %s, asked with %s", p.DecideVia(), p.Jev())
	}
	if ms := p.decideModels(); len(ms) != 1 || ms[0].Context != 262_144 || ms[0].ImageInput == nil || !*ms[0].ImageInput {
		t.Fatalf("decision models: %+v", ms)
	}
	if u, _ := p.DecideURL(context.Background()); u != "https://generativelanguage.googleapis.com/v1beta/decisions" {
		t.Fatalf("asked at %s", u)
	}
	// the user's model reaches Google, on the provider it names; Google's
	// name alone, served by both here, asks which, and is never OpenRouter's
	for _, id := range []string{"google", "gem-native"} {
		got, m, err := RouteDecider(id + "/" + GoogleDecision)
		if err != nil || got.ID != id || m != GoogleDecision {
			t.Fatalf("%s routed to %s %s %v", id, got.ID, m, err)
		}
	}
	if _, _, err := RouteDecider(GoogleDecision); err == nil || !strings.Contains(err.Error(), "name it gem-native/gemini-decision-1 or google/gemini-decision-1") {
		t.Fatalf("Google's name alone: %v", err)
	}
	// OpenRouter's own is still OpenRouter's
	if got, m, err := RouteDecider("openrouter/google/gemini-decision-1"); err != nil || got.ID != "openrouter" || m != "google/gemini-decision-1" {
		t.Fatalf("openrouter's routed to %s %s %v", got.ID, m, err)
	}
	for _, e := range providerEntries() {
		if e.ID == "google/"+GoogleDecision {
			t.Fatalf("an agent is offered %s", e.ID)
		}
	}
	// the provider's test asks it the smallest question, with the key as
	// Google takes it
	if err := p.AskSystemOne(context.Background(), GoogleDecision); err != nil {
		t.Fatal(err)
	}
	if got := up.got(); !slices.Equal(got, []string{"POST /v1beta/decisions key=AIza-test auth="}) {
		t.Fatalf("Google asked: %q", got)
	}
	sameJSON(t, "smallest question", up.body, `{"model":"gemini-decision-1","input":"{\"text\":\"ping\"}","questions":{"ok":{"type":"noul","instructions":"Is this a test?"}}}`)

	// a signed-in account and Vertex say their own
	if d := (Provider{Chat: AIStudioChat, Account: &Account{}}).googleDecisions(); d != "" {
		t.Fatalf("an account: %q", d)
	}
	if d := (Provider{Preset: VertexPreset, Gemini: AIStudioGemini}).googleDecisions(); d != "" {
		t.Fatalf("vertex: %q", d)
	}
}
