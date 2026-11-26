package provider

import (
	"context"
	"encoding/json"
	"testing"
)

// An OpenAI API key's gpt-6-luna is a decision model at api.openai.com's
// /v1/decisions (zyh6036 on X); nothing else is, and no other provider's
// decision API is asked another way for it.
func TestOpenAIDecider(t *testing.T) {
	key := Provider{ID: "openai", Name: "OpenAI", Key: "sk", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	d, ok := key.OpenAIDecider("gpt-6-luna")
	if !ok || d.DecideVia() != ViaOpenAI || key.Decides() {
		t.Fatalf("%v %+v", ok, d)
	}
	if u, err := d.DecideURL(context.Background()); err != nil || u != "https://api.openai.com/v1/decisions" {
		t.Fatalf("%s %v", u, err)
	}
	// a decision base set to OpenAI's API itself (#4316)
	set := key
	set.Decide = "https://api.openai.com/v1"
	if d, ok := set.OpenAIDecider("gpt-6-luna"); !ok || d.Decide != set.Decide || d.DecideVia() != ViaOpenAI {
		t.Fatalf("decide set to OpenAI: %v %+v", ok, d)
	}
	for _, p := range []struct {
		name  string
		p     Provider
		model string
	}{
		{"another model", key, "gpt-6-sol"},
		{"a signed-in account", Provider{ID: ChatGPTAPIID, Chat: "https://api.openai.com/v1", Account: &Account{}}, "gpt-6-luna"},
		{"a relay", Provider{ID: "relay", Chat: "https://relay.example/v1"}, "gpt-6-luna"},
		{"Azure", Provider{ID: "az", Preset: AzurePreset, Chat: "https://api.openai.com/v1"}, "gpt-6-luna"},
		{"a decision API of its own", Provider{ID: "ts", Chat: "https://api.openai.com/v1", Decide: "https://api.typesafe.ai/v1"}, "gpt-6-luna"},
	} {
		if _, ok := p.p.OpenAIDecider(p.model); ok {
			t.Errorf("%s asked at OpenAI's Decisions API", p.name)
		}
	}
	for decide, want := range map[string]string{
		"https://api.openai.com":              "https://api.openai.com/v1/decisions",
		"https://api.openai.com/":             "https://api.openai.com/v1/decisions",
		"https://api.openai.com/v1/":          "https://api.openai.com/v1/decisions",
		"https://api.openai.com/v1/decisions": "https://api.openai.com/v1/decisions",
	} {
		p := Provider{ID: "o", Key: "sk", Decide: decide}
		if u, err := p.DecideURL(context.Background()); p.DecideVia() != ViaOpenAI || err != nil || u != want {
			t.Errorf("%s: %s %s %v", decide, p.DecideVia(), u, err)
		}
	}
	// the others' ways are as they were
	for decide, via := range map[string]string{
		"https://api.typesafe.ai/v1":                                ViaSystemOne,
		"https://ai-gateway.vercel.sh/typesafe":                     ViaVercel,
		"https://api.cloudflare.com/client/v4/accounts/acc7/ai/run": ViaCloudflare,
		"https://generativelanguage.googleapis.com/v1beta":          ViaGoogle,
		"https://openai.example/v1":                                 ViaSystemOne,
	} {
		if got := (Provider{ID: "g", Decide: decide}).DecideVia(); got != via {
			t.Errorf("%s: %s, want %s", decide, got, via)
		}
	}
}

// A System One request goes to OpenAI as its Decisions API takes one
// (openai-node's DecisionCreateParams: model, input, a list of named
// predicate, choice and score questions), and OpenAI's answer, the API
// reference's own example, comes back as System One's.
func TestOpenAIDecisionsWire(t *testing.T) {
	ask := `{"model":"gpt-6-luna","state":{"message":"The package arrived with a broken screen."},"questions":{` +
		`"damaged":{"type":"noul","instructions":"Does the customer report a damaged item?"},` +
		`"intent":{"type":"choice","instructions":"Which kind?","criteria":{"refund":null,"none of these":"The message is none of the other kinds"}},` +
		`"effort":{"type":"score","instructions":"How hard?","criteria":["little","much"]}}}`
	got := DecideAsk(ViaOpenAI, "gpt-6-luna", []byte(ask))
	want := `{"input":[{"content":[{"data":{"message":"The package arrived with a broken screen."},"type":"input_json"}],"role":"user","type":"message"}],"model":"gpt-6-luna","questions":[` +
		`{"instructions":"Does the customer report a damaged item?","name":"damaged","type":"predicate"},` +
		`{"choices":[{"value":"refund"},{"description":"The message is none of the other kinds","value":"none of these"}],"instructions":"Which kind?","name":"intent","type":"choice"},` +
		`{"instructions":"How hard?","levels":[{"label":"little"},{"label":"much"}],"name":"effort","type":"score"}]}`
	if string(got) != want {
		t.Fatalf("asked\n%s\nwant\n%s", got, want)
	}
	// the API reference's example answer
	ans := `{"model":"gpt-6-luna","answers":[{"type":"predicate","name":"damaged","probability":0.95}],"usage":{"input_tokens":42,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":42}}`
	code, b := DecideResult(ViaOpenAI, 200, []byte(ans))
	var out struct {
		Answers map[string]map[string]any `json:"answers"`
		Usage   map[string]int            `json:"usage"`
	}
	if code != 200 || json.Unmarshal(b, &out) != nil || out.Answers["damaged"]["noul"] != 0.95 || out.Usage["input_tokens"] != 42 {
		t.Fatalf("%d %s", code, b)
	}
	// choice and score answers, and a refusal, by name
	ans = `{"model":"gpt-6-luna","answers":[` +
		`{"type":"choice","name":"intent","choice":"refund","confidence":0.8,"probabilities":[{"value":"refund","probability":0.9},{"value":"none of these","probability":0.1}]},` +
		`{"type":"score","name":"effort","score":0.7,"confidence":0.4,"probabilities":[{"label":"little","value":0,"probability":0.3},{"label":"much","value":1,"probability":0.7}]},` +
		`{"type":"refusal","name":"damaged"}],"usage":{"input_tokens":50,"output_tokens":0,"total_tokens":50}}`
	_, b = DecideResult(ViaOpenAI, 200, []byte(ans))
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	c, s, r := out.Answers["intent"], out.Answers["effort"], out.Answers["damaged"]
	if c["choice"] != "refund" || c["confidence"] != 0.8 || c["probabilities"].(map[string]any)["refund"] != 0.9 ||
		s["score"] != 0.7 || s["probabilities"].(map[string]any)["much"] != 0.7 || r["type"] != "refusal" {
		t.Fatalf("%s", b)
	}
	// an error passes as OpenAI gave it
	if code, b := DecideResult(ViaOpenAI, 401, []byte(`{"error":{"message":"Incorrect API key provided"}}`)); code != 401 || string(b) != `{"error":{"message":"Incorrect API key provided"}}` {
		t.Fatalf("%d %s", code, b)
	}
}
