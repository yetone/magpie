package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// openaiUp stands in for api.openai.com, reached as the OpenAI
// provider's proxy so its host is OpenAI's own and no other is reached.
// POST /v1/decisions answers each question as OpenAI's Decision does
// (openai-node's Decision: answers in question order, each typed and
// named as asked; usage as the API reference's example gives it): a
// predicate with yes, a choice with choice when it is one of the
// question's values, a score with score; refuse names questions answered
// {"type":"refusal"}. A key other than sk-openai is refused 401, and a
// request not in the API's shape 400, as OpenAI refuses them.
// /v1/chat/completions answers "chat".
type openaiUp struct {
	mu     sync.Mutex
	yes    float64
	choice string
	score  float64
	refuse []string
	hits   []string
	bodies []map[string]any
}

func (u *openaiUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var q map[string]any
	json.Unmarshal(raw, &q)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.hits = append(u.hits, r.Host+r.URL.Path+" "+r.Header.Get("Authorization"))
	u.bodies = append(u.bodies, q)
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer sk-openai" {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`)
		return
	}
	switch r.URL.Path {
	case "/v1/chat/completions":
		io.WriteString(w, `{"id":"c","object":"chat.completion","model":"`+q["model"].(string)+`","choices":[{"index":0,"message":{"role":"assistant","content":"chat"},"finish_reason":"stop"}]}`)
		return
	case "/v1/decisions":
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"error":{"message":"Invalid URL","type":"invalid_request_error"}}`)
		return
	}
	bad := func(msg string) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"`+msg+`","type":"invalid_request_error"}}`)
	}
	for k := range q {
		if k != "model" && k != "input" && k != "questions" {
			bad("Unknown parameter: '" + k + "'.")
			return
		}
	}
	qs, _ := q["questions"].([]any)
	if q["model"] != "gpt-6-luna" || q["input"] == nil || len(qs) == 0 {
		bad("model, input and questions are required")
		return
	}
	var answers []map[string]any
	for _, x := range qs {
		x, _ := x.(map[string]any)
		name, _ := x["name"].(string)
		if slices.Contains(u.refuse, name) {
			answers = append(answers, map[string]any{"type": "refusal", "name": name})
			continue
		}
		switch x["type"] {
		case "predicate":
			answers = append(answers, map[string]any{"type": "predicate", "name": name, "probability": u.yes})
		case "choice":
			cs, _ := x["choices"].([]any)
			if len(cs) < 2 {
				bad("choices must have at least 2 items")
				return
			}
			var ps []map[string]any
			pick := cs[0].(map[string]any)["value"]
			for _, c := range cs {
				if v := c.(map[string]any)["value"]; v == u.choice {
					pick = v
				}
			}
			for _, c := range cs {
				v := c.(map[string]any)["value"]
				p := 0.1 / float64(len(cs)-1)
				if v == pick {
					p = 0.9
				}
				ps = append(ps, map[string]any{"value": v, "probability": p})
			}
			answers = append(answers, map[string]any{"type": "choice", "name": name, "choice": pick, "confidence": 0.86, "probabilities": ps})
		case "score":
			ls, _ := x["levels"].([]any)
			if len(ls) < 2 || len(ls) > 10 {
				bad("levels must have 2 to 10 items")
				return
			}
			var ps []map[string]any
			for i, l := range ls {
				ps = append(ps, map[string]any{"label": l.(map[string]any)["label"], "value": i, "probability": 1 / float64(len(ls))})
			}
			answers = append(answers, map[string]any{"type": "score", "name": name, "score": u.score, "confidence": 0.7, "probabilities": ps})
		default:
			bad("unknown question type")
			return
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"model": "gpt-6-luna", "answers": answers, "usage": map[string]any{
		"input_tokens": 42, "input_tokens_details": map[string]int{"cached_tokens": 0, "cache_write_tokens": 0},
		"output_tokens": 0, "output_tokens_details": map[string]int{"reasoning_tokens": 0}, "total_tokens": 42}})
}

func (u *openaiUp) got() ([]string, []map[string]any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.hits), slices.Clone(u.bodies)
}

// openaiClassified is ruled's group with an OpenAI API key's model as
// its classifier, the rules given, the effort Auto: the key's provider is
// reached at api.openai.com through up, and a relay's lists gpt-6-luna as
// a chat model of its own.
func openaiClassified(t *testing.T, classifier string, rules ...provider.Rule) (*Server, *openaiUp, *openaiUp) {
	t.Helper()
	s, _, _ := ruled(t)
	decisionsOffForTest(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	up := &openaiUp{yes: 0.1}
	ou := httptest.NewServer(up)
	t.Cleanup(ou.Close)
	if err := provider.Save(provider.Provider{ID: "openai", Name: "OpenAI", Key: "sk-openai", Chat: "http://api.openai.com/v1", Proxy: ou.URL, Models: []string{"gpt-6-luna", "gpt-6-sol"}}); err != nil {
		t.Fatal(err)
	}
	relay := &openaiUp{yes: 0.1}
	ru := httptest.NewServer(http.StripPrefix("/relay", relay))
	t.Cleanup(ru.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-openai", Chat: ru.URL + "/relay/v1", Models: []string{"gpt-6-luna"}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := provider.FindGroup("group/r")
	g.Rules, g.Classifier, g.Effort = rules, classifier, provider.EffortAuto
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	return s, up, relay
}

// zyh6036 on X: with an OpenAI API key in magpie, gpt-6-luna as a routing
// rule's classifier was asked through chat completions, never OpenAI's
// Decisions API. It is asked at api.openai.com/v1/decisions on the key,
// as openai-node's client.decisions.create sends a request: the turn an
// input_json part of a user message, magpie's questions typed and named
// in a list (two kinds: whether they are levels, and how much work each
// is; then the intent, the level and the effort). OpenAI's typed answers
// pick the rule and the effort, and the call is the routing classifier's
// own, as Jev's is.
func TestOpenAIDecisionsClassify(t *testing.T) {
	s, up, _ := openaiClassified(t, "openai/gpt-6-luna",
		provider.Rule{Use: "b/big", Intent: "debugging"}, provider.Rule{Use: "a/small", Intent: "renames"})
	up.choice, up.score = "debugging", 2.2
	out, r := postOK(t, s, "s1", chat("why does this crash?", nil, 0, `,"reasoning_effort":"low"`))
	if c := r.Rule.Classified; !strings.Contains(out, "from kb") || c.Intent != "debugging" || c.Error != "" || r.Rule.Pick != "high" {
		t.Fatalf("%s %+v %+v", out, r.Rule, c)
	}
	hits, bodies := up.got()
	if !slices.Equal(hits, []string{"api.openai.com/v1/decisions Bearer sk-openai", "api.openai.com/v1/decisions Bearer sk-openai"}) {
		t.Fatalf("OpenAI asked %q", hits)
	}
	kinds, turn := bodies[0], bodies[1]
	names := func(q map[string]any) (out []string) {
		for _, x := range q["questions"].([]any) {
			x := x.(map[string]any)
			out = append(out, x["type"].(string)+":"+x["name"].(string))
		}
		return out
	}
	if got := names(kinds); !slices.Equal(got, []string{"predicate:levels", "score:work0", "score:work1"}) {
		t.Fatalf("kinds asked %v", got)
	}
	if got := strings.Join(names(turn), ","); got != "score:effort,choice:intent,choice:level" {
		t.Fatalf("turn asked %v", got)
	}
	in, _ := json.Marshal(turn["input"])
	if want := `[{"content":[{"data":{"message":"why does this crash?"},"type":"input_json"}],"role":"user","type":"message"}]`; string(in) != want {
		t.Fatalf("input %s\nwant %s", in, want)
	}
	var intent map[string]any
	for _, x := range turn["questions"].([]any) {
		if x := x.(map[string]any); x["name"] == "intent" {
			intent = x
		}
	}
	cs, _ := json.Marshal(intent["choices"])
	if want := `[{"value":"debugging"},{"description":"The message is none of the other kinds","value":"none of these"},{"value":"renames"}]`; string(cs) != want {
		t.Fatalf("intent choices %s\nwant %s", cs, want)
	}
	if turn["model"] != "gpt-6-luna" || turn["state"] != nil {
		t.Fatalf("sent %v", turn)
	}
	n := 0
	for _, rec := range usage.Load(time.Time{}) {
		if rec.Provider == "openai" {
			if rec.Kind != "routing" || !rec.ByMagpie() || rec.Model != "gpt-6-luna" || rec.Input != 42 {
				t.Fatalf("classifier's record: %+v", rec)
			}
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d records of the classifier's, want 2", n)
	}
}

// A question OpenAI declined ({"type":"refusal"}) is no answer: the
// effort it refused is not the lowest, nor an intent "none of these".
func TestOpenAIDecisionsRefusal(t *testing.T) {
	s, up, _ := openaiClassified(t, "openai/gpt-6-luna", provider.Rule{Use: "b/big", Intent: "debugging"})
	up.choice, up.refuse = "debugging", []string{"effort"}
	_, r := postOK(t, s, "s1", chat("why does this crash?", nil, 0, `,"reasoning_effort":"medium"`))
	if c := r.Rule.Classified; c.Intent != "debugging" || c.Effort != "" || r.Rule.Pick == "low" {
		t.Fatalf("%+v %+v", r.Rule, c)
	}
	if v, err := readJev([]byte(`{"answers":{"effort":{"type":"refusal"},"intent":{"type":"refusal"}}}`), []string{"a", "b"}, false, before{}); err != nil || v.Effort != "" || v.Said != "" || v.Intent != "" {
		t.Fatalf("refusals read as %+v %v", v, err)
	}
}

// Only an OpenAI key's gpt-6-luna is asked at the Decisions API: the
// key's other models, and a relay's gpt-6-luna, are asked through chat as
// they were, and gpt-6-luna still holds a conversation.
func TestOpenAIDecisionsOnlyLuna(t *testing.T) {
	s, up, relay := openaiClassified(t, "openai/gpt-6-sol")
	if _, err := s.askClassifier("openai/gpt-6-sol", []string{"feature", "bug"}, before{}, false, "fix this"); err == nil {
		t.Fatal("a chat classifier answering \"chat\" read as an intent")
	}
	if _, err := s.askClassifier("relay/gpt-6-luna", []string{"feature", "bug"}, before{}, false, "fix this"); err == nil {
		t.Fatal("a chat classifier answering \"chat\" read as an intent")
	}
	hits, _ := up.got()
	rhits, _ := relay.got()
	if len(hits) == 0 || slices.ContainsFunc(append(hits, rhits...), func(h string) bool { return strings.Contains(h, "/decisions") }) ||
		len(rhits) == 0 || !strings.HasSuffix(strings.Fields(rhits[0])[0], "/v1/chat/completions") {
		t.Fatalf("OpenAI asked %q, the relay %q", hits, rhits)
	}
	if code, out := postAs(t, s, "c1", `{"model":"openai/gpt-6-luna","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(out, `"content":"chat"`) {
		t.Fatalf("chat to gpt-6-luna: %d %s", code, out)
	}
	if hits, _ := up.got(); !strings.HasPrefix(hits[len(hits)-1], "api.openai.com/v1/chat/completions ") {
		t.Fatalf("OpenAI asked %q", hits)
	}
}
