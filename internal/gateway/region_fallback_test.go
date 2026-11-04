package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// googleRegion400 is Google's answer from a place it doesn't serve the API
// in, as Gemini, Code Assist and Antigravity send it.
const googleRegion400 = `{"error":{"code":400,"message":"User location is not supported for the API use.","status":"FAILED_PRECONDITION"}}`

// antigravityRegion400 is #1621's reproduction, the error as the agent was
// shown it.
const antigravityRegion400 = `{"error":{"message":"Antigravity: User location is not supported for the API use."}}`

// A refusal for where the request comes from is not about the request: it
// is retryable at 400, and the boundaries #1621 lists stay as they were.
func TestRegionRefusalIsRetryable(t *testing.T) {
	for _, x := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, antigravityRegion400, true},
		{400, googleRegion400, true},
		{400, `{"error":{"message":"user location is NOT supported for the API use"}}`, true},
		{400, `{"error":{"code":"unsupported_country_region_territory","message":"Country, region, or territory not supported","type":"request_forbidden"}}`, true},
		{400, `{"error":{"message":"location field is not supported"}}`, false},
		{400, `{"error":{"message":"Invalid value for location parameter"}}`, false},
		{400, `{"error":{"message":"messages: field required"}}`, false},
		{200, antigravityRegion400, false},
	} {
		if got := retryable(x.status, []byte(x.body)); got != x.want {
			t.Errorf("retryable(%d, %s) = %v, want %v", x.status, x.body, got, x.want)
		}
	}
}

// A conversation past the model's window that is refused for its region
// is told as the region refusal, not as a prompt too long to compact.
func TestRegionRefusalIsNotTooLong(t *testing.T) {
	p := provider.Provider{ID: "fake", Name: "Fake", Contexts: map[string]int{"m": 100}}
	for _, body := range []string{antigravityRegion400, googleRegion400} {
		if said, ok := tooLongUnsaid(p, "m", 1000, 400, []byte(body)); ok {
			t.Errorf("region refusal read as too long: %s", said)
		}
	}
	// the same size, refused without saying why, still is
	if _, ok := tooLongUnsaid(p, "m", 1000, 400, []byte(`{"error":{"message":"Invalid request parameters"}}`)); !ok {
		t.Error("an unsaid overflow is no longer inferred")
	}
}

// #1621: a group's member refused for its region hands the request to the
// next member, and rests not at all (#5182): not as one out of credit or
// quota, nor as any failure.
func TestGroupFailsOverARegionRefusal(t *testing.T) {
	for _, body := range []string{antigravityRegion400, googleRegion400} {
		fresh(t)
		a := &scripted{replies: []reply{{400, "", body}}}
		b := &scripted{replies: []reply{{200, "", chatOK}}}
		scriptedOn(t, "a", provider.Chat, a)
		scriptedOn(t, "b", provider.Chat, b)
		if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
			t.Fatal(err)
		}
		s := New()
		code, got := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(got, "hello") || a.n != 1 || b.n != 1 {
			t.Fatalf("%d %s (a %d, b %d)", code, got, a.n, b.n)
		}
		r := s.trace.routes[len(s.trace.routes)-1]
		if len(r.Tries) != 2 || r.Tries[0].Status != 400 || r.Tries[0].Rest == nil {
			t.Fatalf("tries %+v", r.Tries)
		}
		if rest := r.Tries[0].Rest; rest.Why != failOther || rest.By != "off" || rest.Failures != 0 || time.Until(rest.Until) > 0 {
			t.Fatalf("rested %+v", rest)
		}
	}
}

// #1621: a provider refused for its region goes to its fallback.
func TestFallbackOnRegionRefusal(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 400, reply: antigravityRegion400}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
}
