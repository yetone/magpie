package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// antigravitySuspended is Google's answer for an Antigravity account it
// suspended, as bestony's agent showed it on Discord (#feedback, 11-06):
// "Error 403, Message: Antigravity: This service has been disabled in this
// account for violation of Terms of Service. Please submit an appeal to
// continue using this product., Status: PERMISSION_DENIED".
const antigravitySuspended = `{"error":{"code":403,"message":"This service has been disabled in this account for violation of Terms of Service. Please submit an appeal to continue using this product.","status":"PERMISSION_DENIED"}}`

// An account Google suspended is out until an appeal lifts it: the request
// goes on to the next account, and the suspended one rests as restricted
// (two hours, lifted by hand on the Routing page), not the minute of an
// unknown failure that doubled to ten and had it asked, and refused, every
// ten minutes for good.
func TestAntigravitySuspendedRestsAsRestricted(t *testing.T) {
	var asked []string
	s, askedUsage := antigravityGroupFor(t, []string{"u1@example.com", "u2@example.com"}, "gemini-3.8-flash", func(w http.ResponseWriter, r *http.Request) {
		who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		asked = append(asked, who)
		if who == "u1@example.com" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, antigravitySuspended)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"from u2"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}}`))
	})
	turn := func(n int) (int, string) {
		body := fmt.Sprintf(`{"model":"antigravity/gemini-3.8-flash","stream":false,"messages":[{"role":"user","content":"hi %d"}]}`, n)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	if code, out := turn(1); code != 200 || !strings.Contains(out, "from u2") || !slices.Equal(asked, []string{"u1@example.com", "u2@example.com"}) {
		t.Fatalf("the turn: %d %s, asked %v", code, out, asked)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Status != 403 || r.Tries[0].Fail != failRestricted || r.Tries[0].Rest == nil {
		t.Fatalf("the suspended try: %+v", r.Tries)
	}
	rest := r.Tries[0].Rest
	if d := time.Until(rest.Until); rest.By != "restricted" || rest.Key != "antigravity@u1@example.com" || d < restrictedRest-time.Minute || d > restrictedRest {
		t.Fatalf("u1 rests %v by %q under %q, not %v as restricted", d, rest.By, rest.Key, restrictedRest)
	}
	if *askedUsage != 0 {
		t.Fatalf("the suspended account's usage was asked again %d times", *askedUsage)
	}
	for n := 2; n <= 4; n++ {
		asked = nil
		if code, _ := turn(n); code != 200 || !slices.Equal(asked, []string{"u2@example.com"}) {
			t.Fatalf("turn %d: %d, asked %v", n, code, asked)
		}
	}
	// past the ten minutes an unknown failure rests at most
	if got, ok := restOf("antigravity@u1@example.com"); !ok || got.Why != failRestricted || time.Until(got.Until) <= longestRetry {
		t.Fatalf("u1's rest %+v %v", got, ok)
	}
}

// The last account left, suspended, rests too, and the agent is told
// Google's own words; lifted on the Routing page (an appeal went through),
// it is asked again.
func TestAntigravitySuspendedAloneRests(t *testing.T) {
	n, back := 0, false
	s, _ := antigravityGroupFor(t, []string{"u1@example.com"}, "gemini-3.8-flash", func(w http.ResponseWriter, r *http.Request) {
		n++
		if back {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"back"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1}}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, antigravitySuspended)
	})
	turn := func() (int, string) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"antigravity/gemini-3.8-flash","stream":false,"messages":[{"role":"user","content":"hi"}]}`)))
		return rec.Code, rec.Body.String()
	}
	if code, out := turn(); code != 403 || !strings.Contains(out, "violation of Terms of Service") || n != 1 {
		t.Fatalf("%d after %d: %s", code, n, out)
	}
	if got, has := restOf("antigravity@u1@example.com"); !has || got.Why != failRestricted || got.By != "restricted" {
		t.Fatalf("the last one's rest %+v %v", got, has)
	}
	if !s.Unrest("antigravity@u1@example.com") {
		t.Fatal("rest not lifted")
	}
	back = true
	if code, out := turn(); code != 200 || !strings.Contains(out, "back") {
		t.Fatalf("after the appeal: %d %s", code, out)
	}
}

// Only Google's suspension of a Google sign-in is read so: another 403 from
// Antigravity, and the same words from a key's relay, fail as before.
func TestAntigravitySuspendedIsGooglesAlone(t *testing.T) {
	ag := candidate{p: provider.Provider{ID: "antigravity", Account: &provider.Account{Agent: "antigravity", User: "u"}}}
	gem := candidate{p: provider.Provider{ID: "gemini", Account: &provider.Account{Agent: "gemini", User: "u"}}}
	key := candidate{p: provider.Provider{ID: "relay", Key: "k"}}
	said := "Antigravity: This service has been disabled in this account for violation of Terms of Service. Please submit an appeal to continue using this product."
	for _, c := range []struct {
		c      candidate
		status int
		body   string
		want   string
	}{
		{ag, 403, antigravitySuspended, failRestricted},
		{ag, 403, said, failRestricted},
		{gem, 403, antigravitySuspended, failRestricted},
		{ag, 403, `{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`, failOther},
		{ag, 400, antigravitySuspended, failOther},
		{key, 403, antigravitySuspended, failOther},
	} {
		if got := failureOf(c.c, c.status, []byte(c.body)); got != c.want {
			t.Errorf("%s %d %s: %s, want %s", c.c.p.ID, c.status, c.body, got, c.want)
		}
	}
}
