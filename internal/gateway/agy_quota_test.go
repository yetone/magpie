package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// @Airan_FS on X (11-17): an agy-cli account out of its quota answered
// "⚠ Individual quota reached. Please upgrade your subscription to
// increase your limits. Resets in …" as the model's reply, and the next
// account was never asked. The agy-cli plugin (plugins packages/agy-cli,
// 0.1.87) now ends such a turn with a rate_limit_error in these words,
// after the message_start it has already streamed; the reset is Google's,
// written as Go prints a duration ("Resets in 1h18m40s.", the words a
// user quoted on discuss.ai.google.dev). The account rests until then and
// the next member is asked.
const agyQuotaNotice = "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h18m40s."

func agyQuotaStream(text string) string {
	lines := []string{
		"event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":0,"output_tokens":0}}}`,
	}
	if text != "" {
		lines = append(lines,
			"event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+text+`"}}`,
			"event: content_block_stop\ndata: "+`{"type":"content_block_stop","index":0}`)
	}
	return sse(append(lines, "event: error\ndata: "+`{"type":"error","error":{"type":"rate_limit_error","message":"agy bridge: Antigravity quota reached: `+agyQuotaNotice+`"}}`)...)
}

func TestAgyCLIQuotaNoticeRestsUntilItsResetAndFallsOver(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", agyQuotaStream("")}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || strings.Contains(body, "quota") || a.n != 1 || b.n != 1 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Fail != failQuota || r.Tries[0].Rest == nil {
		t.Fatalf("the used-up try: %+v", r.Tries)
	}
	want := time.Hour + 18*time.Minute + 40*time.Second
	if d := time.Until(r.Tries[0].Rest.Until); r.Tries[0].Rest.By != "resets" || d < want-time.Minute || d > want {
		t.Fatalf("a rests %v by %q, not the %v its quota resets in", d, r.Tries[0].Rest.By, want)
	}
	// the next turn goes to b without asking a
	if code, _ := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"again"}]}`); code != 200 || a.n != 1 || b.n != 2 {
		t.Fatalf("next turn %d (a %d, b %d)", code, a.n, b.n)
	}
}

// A reply that had begun before the quota ran out can't be taken back: the
// client keeps its words and is told why it stopped, and the model rests
// until its reset all the same.
func TestAgyCLIQuotaNoticeMidReplyStillRests(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", agyQuotaStream("PARTIAL answer")}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	s := New()
	_, body := postAs(t, s, "", `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(body, "PARTIAL answer") || !strings.Contains(body, "Individual quota reached") {
		t.Fatalf("the client got %s", body)
	}
	rest, ok := restOf("a")
	want := time.Hour + 18*time.Minute + 40*time.Second
	if !ok || rest.By != "resets" {
		t.Fatalf("a rests %+v (%v)", rest, ok)
	}
	if d := time.Until(rest.Until); d < want-time.Minute || d > want {
		t.Fatalf("a rests %v, not %v", d, want)
	}
}

// The reset as agy's notice writes it, as Go prints a duration.
func TestAgyQuotaNoticeResetIsRead(t *testing.T) {
	for _, c := range []struct {
		body string
		want time.Duration
	}{
		{agyQuotaNotice, time.Hour + 18*time.Minute + 40*time.Second},
		{"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 45m2s.", 45*time.Minute + 2*time.Second},
		{"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 26s.", 26 * time.Second},
		{"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 120h5m0s.", 120*time.Hour + 5*time.Minute},
		{"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 4h.", 4 * time.Hour},
		{"Individual quota reached. Please upgrade your subscription to increase your limits.", 0},
	} {
		if got := resetsAfter([]byte(c.body)); got != c.want {
			t.Errorf("%s: %v, want %v", c.body, got, c.want)
		}
	}
}

// BruceLu73129013 on X (magpie 0.1.4342, agy-cli 0.1.87): Opus's quota used
// up, Gemini 4 Carbon on the same account, its quota left, was answered the
// same 429 from magpie's rest (timedHeld on agy-cli@<account>). Antigravity
// counts quota per pool of models (Claude and GPT, Gemini apart), so its
// refusal rests the model it was for, and the account's other models are
// still asked. agy-cli's usage windows are display only (aside), so no
// reading narrows the rest for it.
func TestAgyCLIQuotaRestsTheModelNotTheAccount(t *testing.T) {
	for _, said := range []string{
		"agy bridge: Antigravity quota reached: " + agyQuotaNotice,
		// Code Assist's words for the same, through any route
		"You have exhausted your capacity on this model. Your quota will reset after 1h18m40s.",
	} {
		t.Run(said[:20], func(t *testing.T) {
			fresh(t)
			acct := func(model string) candidate {
				return candidate{p: provider.PluginForTest("agy-cli", "agy-cli"), model: model, rest: "agy-cli"}
			}
			opus, carbon := acct("claude-opus-5-5"), acct("gemini-4-carbon")
			s := &Server{}
			r := s.restAfter(opus, http.StatusTooManyRequests, http.Header{}, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"`+said+`"}}`))
			if r.Why != failQuota || r.By != "resets" || r.Key != opus.restID() || r.Model != opus.model {
				t.Fatalf("rest: %+v", r)
			}
			if _, _, held := timedHeld([]candidate{opus}); !held {
				t.Fatal("Opus, refused until its reset, is asked again")
			}
			if r, ok := restingOf(carbon); ok {
				t.Fatalf("Gemini 4 Carbon rests with Opus: %+v", r)
			}
			if _, _, held := timedHeld([]candidate{carbon}); held {
				t.Fatal("Gemini 4 Carbon is answered Opus's 429 from the rest")
			}
		})
	}
	// a quota refusal in other words, which tells no pool, still rests the
	// account it came from; and so do Antigravity's words from another
	// vendor, which has no pools
	for _, c := range []struct {
		p    provider.Provider
		said string
	}{
		{provider.PluginForTest("agy-cli", "agy-cli"), "Quota exceeded. Resets in 1h."},
		{provider.PluginForTest("other", "other"), agyQuotaNotice},
	} {
		fresh(t)
		other := candidate{p: c.p, model: "m", rest: c.p.ID}
		if r := (&Server{}).restAfter(other, http.StatusTooManyRequests, http.Header{}, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"`+c.said+`"}}`)); r.Key != other.restKey() {
			t.Fatalf("%s %q rested %+v, want the account", c.p.ID, c.said, r)
		}
	}
}

// Through the gateway and the plugin host: the agy-cli plugin's Claude
// model refused with agy's notice, the next request for its other model on
// the same account goes to it and is answered.
func TestAgyCLIQuotaLeavesTheOtherPoolAsked(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	// no model of it free, as agy-cli lists none: a paid model beside a
	// free one rests alone anyway (PaidBesideFree)
	t.Setenv("FAKE_NO_FREE", "1")
	pid := besideFake(t, "agy-cli", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/messages") {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"agy bridge: Antigravity quota reached: `+agyQuotaNotice+`"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"choices":[{"delta":{"content":"hello"}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`))
	}))
	s := New()
	if code, body := postAs(t, s, "", `{"model":"`+pid+`/fake-claude","messages":[{"role":"user","content":"hi"}]}`); code != http.StatusTooManyRequests {
		t.Fatalf("the Claude model answered %d %s", code, body)
	}
	code, body := postAs(t, s, "", `{"model":"`+pid+`/fake-1","messages":[{"role":"user","content":"hi"}]}`)
	mu.Lock()
	defer mu.Unlock()
	if code != 200 || !strings.Contains(body, "hello") {
		t.Fatalf("the other pool's model: %d %s (vendor asked %v)", code, body, paths)
	}
}

// @Airan_FS on X (11-23): with an Antigravity account's Claude quota used
// up, agy ends the turn "⚠ Third-party quota reached. Please switch to
// Gemini models or upgrade your subscription to increase your limits.
// Resets in 3h22m19s." The quota is of the Claude and GPT-OSS pool, not
// the account: Gemini's is still there. The agy-cli plugin (0.1.114)
// relays it as a rate_limit_error in agy's words.
const agyThirdPartyNotice = "Third-party quota reached. Please switch to Gemini models or upgrade your subscription to increase your limits. Resets in 3h22m19s."

const agyThirdPartyReset = 3*time.Hour + 22*time.Minute + 19*time.Second

// The model rests until the reset the notice says, not quotaRest's quarter
// of an hour, and the account's Gemini model isn't rested with it.
func TestAgyThirdPartyQuotaRestsTheModelUntilItsReset(t *testing.T) {
	if got := resetsAfter([]byte(agyThirdPartyNotice)); got != agyThirdPartyReset {
		t.Fatalf("the notice's reset reads %v, want %v", got, agyThirdPartyReset)
	}
	for _, said := range []string{
		"agy bridge: Antigravity quota reached: " + agyThirdPartyNotice,
		agyThirdPartyNotice,
	} {
		fresh(t)
		acct := func(model string) candidate {
			return candidate{p: provider.PluginForTest("agy-cli", "agy-cli"), model: model, rest: "agy-cli"}
		}
		opus, gemini := acct("claude-opus-5-5"), acct("gemini-4-carbon")
		r := (&Server{}).restAfter(opus, http.StatusTooManyRequests, http.Header{}, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"`+said+`"}}`))
		if r.Why != failQuota || r.Key != opus.restID() || r.Model != opus.model {
			t.Fatalf("%q rested %+v, want the model %s", said, r, opus.restID())
		}
		if d := time.Until(r.Until); r.By != "resets" || d < agyThirdPartyReset-time.Minute || d > agyThirdPartyReset {
			t.Fatalf("%q: Opus rests %v by %q, not the %v the notice says", said, d, r.By, agyThirdPartyReset)
		}
		if r, ok := restingOf(gemini); ok {
			t.Fatalf("Gemini rests with Claude's pool: %+v", r)
		}
		if _, _, held := timedHeld([]candidate{gemini}); held {
			t.Fatal("Gemini is answered Claude's 429 from the rest")
		}
	}
}

// Through the gateway and the plugin host: the Claude model refused with
// agy's third-party notice is answered a 429 whose Retry-After is the
// notice's reset, is not asked again before it, and the account's other
// model is asked and answers.
func TestAgyThirdPartyQuotaThroughThePlugin(t *testing.T) {
	var mu sync.Mutex
	var claude, other int
	t.Setenv("FAKE_NO_FREE", "1")
	pid := besideFake(t, "agy-cli", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/messages") {
			claude++
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"agy bridge: Antigravity quota reached: `+agyThirdPartyNotice+`"}}`)
			return
		}
		other++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"choices":[{"delta":{"content":"hello"}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`))
	}))
	s := New()
	post := func(model string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+pid+`/`+model+`","messages":[{"role":"user","content":"hi"}]}`)))
		return rec
	}
	for i := range 2 {
		rec := post("fake-claude")
		if rec.Code != http.StatusTooManyRequests || strings.Contains(rec.Body.String(), `"content":"`) {
			t.Fatalf("the Claude model, ask %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
		ra, _ := strconv.Atoi(rec.Header().Get("Retry-After"))
		if d := time.Duration(ra) * time.Second; d < agyThirdPartyReset-time.Minute || d > agyThirdPartyReset+time.Second {
			t.Fatalf("ask %d: Retry-After %q, want the notice's %v", i+1, rec.Header().Get("Retry-After"), agyThirdPartyReset)
		}
	}
	rec := post("fake-1")
	mu.Lock()
	defer mu.Unlock()
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("the other pool's model: %d %s", rec.Code, rec.Body.String())
	}
	if claude != 1 || other != 1 {
		t.Fatalf("the vendor was asked for Claude %d times and the other model %d, want once each", claude, other)
	}
}
