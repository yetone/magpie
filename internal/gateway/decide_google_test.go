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

// bunnybit0121 on X (#7788): a Google key's provider with two keys, each
// given its own list of models (picked from the chat models, which never
// name gemini-decision-1), answered google/gemini-decision-1 with a 403
// telling the user to add it to a key's models, which no editor can. The
// decision model is in no list a key can have, so no list bars it.
func TestGoogleDecisionKeyLists(t *testing.T) {
	fresh(t)
	up := &googleDecisionsUp{reply: googleChoiceReply}
	s := googleKey(t, up)
	p, err := provider.Find("google")
	if err != nil {
		t.Fatal(err)
	}
	p.Keys = []provider.KeyAccount{{Key: "AIza-other"}}
	p.AccountModels = map[string][]string{
		provider.KeyID("AIza-test"):  {"gemini-3-flash"},
		provider.KeyID("AIza-other"): {"gemini-3-flash", "gemini-3-pro"},
	}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	code, out := sendTo(s, "/v1/systemone", `{"model":"google/`+provider.GoogleDecision+`","state":{"message":"hi"},"questions":{"ticket_route":{"type":"choice","instructions":"Pick one","criteria":{"billing":"b","technical_support":"t"}}}}`)
	if code != 200 || !strings.Contains(out, `"choice":"technical_support"`) {
		t.Fatalf("System One with each key's own list: %d %s", code, out)
	}
	if asked, _ := up.got(); !slices.Equal(asked, []string{"/v1beta/decisions key=AIza-test auth="}) {
		t.Fatalf("Google asked: %q", asked)
	}
}

// bunnybit0121 on X (#7789, #7790): a noul's criteria named true and
// false, as Pi sends them, reach Google as its true_description and
// false_description, and the request's audio as audio blocks of the
// input after its images.
func TestGoogleSystemOneNoulAndAudio(t *testing.T) {
	fresh(t)
	up := &googleDecisionsUp{reply: `{"answers":{"b":{"type":"noul","noul":0.05,"confidence":0.94}},"usage":{"total_input_tokens":242,"total_output_tokens":0}}`}
	s := googleKey(t, up)
	code, out := sendTo(s, "/v1/systemone", `{"model":"google/`+provider.GoogleDecision+`","state":{"message":"The export crashes the app."},`+
		`"questions":{"b":{"type":"noul","instructions":"Is it about billing?","criteria":{"true":"billing","false":"other"}}},`+
		`"images":["data:image/png;base64,iVBORw0KGgo="],"audio":["data:audio/wav;base64,UklGRg==","https://example.com/call.mp3?sig=1"]}`)
	if code != 200 || !strings.Contains(out, `"noul":0.05`) || !strings.Contains(out, `"input_tokens":242`) {
		t.Fatalf("System One: %d %s", code, out)
	}
	_, bodies := up.got()
	if len(bodies) != 1 {
		t.Fatalf("Google asked %d times", len(bodies))
	}
	c, _ := json.Marshal(bodies[0]["questions"])
	if want := `{"b":{"criteria":{"false_description":"other","true_description":"billing"},"instructions":"Is it about billing?","type":"noul"}}`; string(c) != want {
		t.Fatalf("questions %s\nwant %s", c, want)
	}
	in, _ := json.Marshal(bodies[0]["input"])
	if want := `[{"text":"{\"message\":\"The export crashes the app.\"}","type":"text"},{"data":"iVBORw0KGgo=","mime_type":"image/png","type":"image"},{"data":"UklGRg==","mime_type":"audio/wav","type":"audio"},{"mime_type":"audio/mp3","type":"audio","uri":"https://example.com/call.mp3?sig=1"}]`; string(in) != want {
		t.Fatalf("input %s\nwant %s", in, want)
	}
	if _, left := bodies[0]["audio"]; left {
		t.Fatalf("audio left as a field: %v", bodies[0])
	}
}

// #7790: /v1/systemone carries audio to a model that takes it only. A
// Jev, which has no place for it, is never asked: the request is a 400
// that says so. A decision group asks only its members that take audio,
// and one with none of them is a 400 too. An "audio" that is not a list
// of URLs is a 400, sent nowhere.
func TestSystemOneAudio(t *testing.T) {
	fresh(t)
	f := &decideFleet{status: map[string]int{}}
	jev := f.serve()
	defer jev.Close()
	up := &googleDecisionsUp{reply: `{"answers":{"intent":{"type":"choice","choice":"bug","confidence":0.9,"probabilities":{"bug":0.95,"chat":0.05}}},"usage":{"total_input_tokens":120,"total_output_tokens":0}}`}
	s := googleKey(t, up)
	s.client = &http.Client{Transport: hostSplit{google: up}}
	if err := provider.Save(provider.Provider{ID: "ja", Name: "JA", Key: "ka", Decide: jev.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []provider.Group{
		{ID: "mixed", Name: "Mixed", Members: []string{"ja/jev-latest", "google/" + provider.GoogleDecision}},
		{ID: "jevs", Name: "Jevs", Members: []string{"ja/jev-latest"}},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(model, audio string) (int, string) {
		return sendTo(s, "/v1/systemone", `{"model":"`+model+`","state":{"message":"listen"},"questions":{"intent":{"type":"choice","criteria":{"bug":"a bug","chat":"small talk"}}}`+audio+`}`)
	}
	const wav = `,"audio":["data:audio/wav;base64,UklGRg=="]`

	if code, out := ask("ja/jev-latest", wav); code != 400 || !strings.Contains(out, "can't take audio") || !strings.Contains(out, "gemini-decision-1") {
		t.Fatalf("Jev with audio: %d %s", code, out)
	}
	if code, out := ask("group/jevs", wav); code != 400 || !strings.Contains(out, "no decision model of the group Jevs can take audio") {
		t.Fatalf("Jev group with audio: %d %s", code, out)
	}
	if code, out := ask("google/"+provider.GoogleDecision, `,"audio":"data:audio/wav;base64,UklGRg=="`); code != 400 || !strings.Contains(out, "list of audio URLs") {
		t.Fatalf("audio not a list: %d %s", code, out)
	}
	if code, out := ask("group/jevs", `,"audio":{"a":1}`); code != 400 || !strings.Contains(out, "list of audio URLs") {
		t.Fatalf("group, audio not a list: %d %s", code, out)
	}
	if got := f.took(); len(got) != 0 {
		t.Fatalf("Jev asked %q", got)
	}
	if asked, _ := up.got(); len(asked) != 0 {
		t.Fatalf("Google asked %q", asked)
	}

	// the mixed group, its Jev first, asks Google alone
	if code, out := ask("group/mixed", wav); code != 200 || !strings.Contains(out, `"choice":"bug"`) {
		t.Fatalf("mixed group with audio: %d %s", code, out)
	}
	if got := f.took(); len(got) != 0 {
		t.Fatalf("Jev asked %q", got)
	}
	if asked, bodies := up.got(); len(asked) != 1 || !strings.Contains(fmtJSON(bodies[0]["input"]), `"type":"audio"`) {
		t.Fatalf("Google asked %q %v", asked, bodies)
	}
	// and without audio, its Jev as before; an empty list is no audio
	for _, audio := range []string{"", `,"audio":[]`, `,"audio":null`} {
		if code, out := ask("group/mixed", audio); code != 200 {
			t.Fatalf("mixed group, audio %q: %d %s", audio, code, out)
		}
		if got := f.took(); len(got) != 1 {
			t.Fatalf("audio %q: Jev asked %q", audio, got)
		}
		if code, out := ask("ja/jev-latest", audio); code != 200 {
			t.Fatalf("Jev, audio %q: %d %s", audio, code, out)
		}
		f.took()
	}
}

// hostSplit sends Google's host to google, and loopback (a test's own
// servers) as it is; nothing else is reached.
type hostSplit struct{ google http.RoundTripper }

func (h hostSplit) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.URL.Hostname() {
	case "generativelanguage.googleapis.com":
		return h.google.RoundTrip(r)
	case "127.0.0.1", "localhost", "::1":
		return http.DefaultTransport.RoundTrip(r)
	}
	return nil, errors.New("test reached " + r.URL.Host)
}

func fmtJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
