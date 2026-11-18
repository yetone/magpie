package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// Google's Decisions API's choice answer, as its reference gives it
// (aistudio.google.com/api-reference/decisions/create-decision), with
// the usage its yes-or-no example answers with.
const googleChoiceReply = `{"answers":{"ticket_route":{"type":"choice","choice":"technical_support","confidence":0.95,"probabilities":{"account_access":0.02,"billing":0.03,"technical_support":0.95}}},"usage":{"input_tokens_by_modality":[{"modality":"text","tokens":152}],"total_input_tokens":152,"total_output_tokens":0,"total_tokens":152}}`

// googleDecisionsUp stands in for generativelanguage.googleapis.com, in
// process: POST /v1beta/decisions answers reply to the key as
// x-goog-api-key and an input that is not an object (Google reads an
// object as a content block, and refuses one without its type); anything
// else is refused as Google refuses it. No other host is reached.
type googleDecisionsUp struct {
	mu     sync.Mutex
	reply  string
	asked  []string
	bodies []map[string]any
}

func (u *googleDecisionsUp) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() != "generativelanguage.googleapis.com" {
		return nil, errors.New("test reached " + r.URL.Host)
	}
	b, _ := io.ReadAll(r.Body)
	var q map[string]any
	json.Unmarshal(b, &q)
	u.mu.Lock()
	u.asked = append(u.asked, r.URL.Path+" key="+r.Header.Get("x-goog-api-key")+" auth="+r.Header.Get("Authorization"))
	u.bodies = append(u.bodies, q)
	u.mu.Unlock()
	reply := func(code int, s string) (*http.Response, error) {
		return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json; charset=UTF-8"}},
			Body: io.NopCloser(strings.NewReader(s)), Request: r}, nil
	}
	if r.URL.Path != "/v1beta/decisions" {
		return reply(404, `{"error":{"code":404,"message":"Not found","status":"NOT_FOUND"}}`)
	}
	if r.Header.Get("x-goog-api-key") != "AIza-test" {
		return reply(401, `{"error":{"code":401,"message":"Expected OAuth 2 access token","status":"UNAUTHENTICATED"}}`)
	}
	if _, isObject := q["input"].(map[string]any); isObject || q["input"] == nil {
		return reply(400, `{"error":{"code":400,"message":"type parameter is required at input[0]","status":"INVALID_ARGUMENT"}}`)
	}
	return reply(200, u.reply)
}

func (u *googleDecisionsUp) got() ([]string, []map[string]any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.asked), slices.Clone(u.bodies)
}

// googleKey saves WangYeruo's provider, the google preset with an AI
// Studio key, and has the gateway's Google be up.
func googleKey(t *testing.T, up *googleDecisionsUp) *Server {
	t.Helper()
	p, err := provider.FromPreset("google")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "AIza-test"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = up
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	s := New()
	s.client = &http.Client{Transport: up}
	return s
}

// WangYeruo on X: magpie's System One API, asked for
// google/gemini-decision-1 with a Google AI Studio key's provider, went to
// OpenRouter. It reaches Google's Decisions API on that key, as Google
// takes it — the state an input of text, the key as x-goog-api-key — and
// Google's answer comes back as System One's, its usage as System One
// names it.
func TestGoogleSystemOne(t *testing.T) {
	fresh(t)
	up := &googleDecisionsUp{reply: googleChoiceReply}
	s := googleKey(t, up)
	code, out := sendTo(s, "/v1/systemone", `{"model":"google/`+provider.GoogleDecision+`","state":{"message":"Every time I click Export to PDF, the app freezes and crashes with error code 504."},`+
		`"questions":{"ticket_route":{"type":"choice","instructions":"Route the support ticket to the most appropriate team.","criteria":{"billing":"Invoices, charges, refunds, or subscription plan changes.","technical_support":"Bugs, crashes, errors, or product functionality issues.","account_access":null}}}}`)
	if code != 200 || !strings.Contains(out, `"choice":"technical_support"`) || !strings.Contains(out, `"input_tokens":152`) {
		t.Fatalf("System One: %d %s", code, out)
	}
	asked, bodies := up.got()
	if !slices.Equal(asked, []string{"/v1beta/decisions key=AIza-test auth="}) {
		t.Fatalf("Google asked: %q", asked)
	}
	if in := bodies[0]["input"]; in != `{"message":"Every time I click Export to PDF, the app freezes and crashes with error code 504."}` {
		t.Fatalf("input %v", in)
	}
	if _, ok := bodies[0]["state"]; ok || bodies[0]["model"] != provider.GoogleDecision {
		t.Fatalf("sent %v", bodies[0])
	}
	c := bodies[0]["questions"].(map[string]any)["ticket_route"].(map[string]any)["criteria"].(map[string]any)
	if c["account_access"] != "" || c["billing"] != "Invoices, charges, refunds, or subscription plan changes." {
		t.Fatalf("criteria %v", c)
	}
	if recs := usage.Load(time.Time{}); len(recs) != 1 || recs[0].Provider != "google" || recs[0].Model != provider.GoogleDecision || recs[0].Input+recs[0].CacheRead+recs[0].CacheWrite != 152 {
		t.Fatalf("recorded %+v", recs)
	}
}

// OpenAI's Decisions API asked for Google's decision model is asked as
// System One there, its message's image an image block of the input,
// which gemini-decision-1 reads.
func TestGoogleDecisionsTakesImages(t *testing.T) {
	fresh(t)
	up := &googleDecisionsUp{reply: `{"answers":{"visible_damage":{"type":"noul","noul":0.92,"confidence":0.84},"condition":{"type":"choice","choice":"used","confidence":0.6,"probabilities":{"new":0.2,"used":0.8}}},"usage":{"total_input_tokens":1290,"total_output_tokens":0,"total_tokens":1290}}`}
	s := googleKey(t, up)
	code, out := sendTo(s, "/v1/decisions", decisionAsked("google/"+provider.GoogleDecision))
	if code != 200 || !strings.Contains(out, `"choice":"used"`) {
		t.Fatalf("Decisions: %d %s", code, out)
	}
	_, bodies := up.got()
	if len(bodies) != 1 {
		t.Fatalf("Google asked %d times", len(bodies))
	}
	in, _ := json.Marshal(bodies[0]["input"])
	if want := `[{"text":"{\"messages\":[{\"content\":\"Inspect the product in this photo.\",\"role\":\"user\"}]}","type":"text"},{"data":"iVBORw0KGgo=","mime_type":"image/png","type":"image"}]`; string(in) != want {
		t.Fatalf("input %s\nwant %s", in, want)
	}
}
