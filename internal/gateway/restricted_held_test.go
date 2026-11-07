package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// agyTurn sends one chat turn for an Antigravity model to s.
func agyTurn(s *Server) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"antigravity/gemini-3.8-flash","stream":false,"messages":[{"role":"user","content":"hi"}]}`)))
	return rec.Code, rec.Body.String()
}

// Every account Google suspended, resting as restricted, is not asked
// again while it rests: the agent is given Google's refusal at once, and
// the rest isn't taken anew, so it ends when it would. Before, restLast
// and probeOne kept one resting account for each request, and every
// request was sent to one of them and refused (leowang on Discord: two
// suspended Antigravity accounts, 23 and 22 tries, none answered). Lifted
// on the Routing page, the account is asked again.
func TestSuspendedAccountsNotAskedWhileTheyRest(t *testing.T) {
	var asked []string
	s, _ := antigravityGroupFor(t, []string{"u1@example.com", "u2@example.com"}, "gemini-3.8-flash", func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, antigravitySuspended)
	})
	if code, out := agyTurn(s); code != 403 || !strings.Contains(out, "violation of Terms of Service") || !slices.Equal(asked, []string{"u1@example.com", "u2@example.com"}) {
		t.Fatalf("first turn: %d %s, asked %v", code, out, asked)
	}
	r1, ok1 := restOf("antigravity@u1@example.com")
	r2, ok2 := restOf("antigravity@u2@example.com")
	if !ok1 || !ok2 || r1.Why != failRestricted || r2.Why != failRestricted {
		t.Fatalf("rests %+v %v, %+v %v", r1, ok1, r2, ok2)
	}
	for n := 2; n <= 6; n++ {
		asked = nil
		code, out := agyTurn(s)
		if code != 403 || !strings.Contains(out, "violation of Terms of Service") || len(asked) != 0 {
			t.Fatalf("turn %d: %d %s, asked %v", n, code, out, asked)
		}
		if tr := lastRoute(s).Tries; len(tr) == 0 || tr[len(tr)-1].Fail != failRestricted {
			t.Fatalf("turn %d's tries %+v", n, tr)
		}
	}
	if r, _ := restOf("antigravity@u1@example.com"); !r.Until.Equal(r1.Until) {
		t.Fatalf("u1's rest moved from %v to %v", r1.Until, r.Until)
	}
	if r, _ := restOf("antigravity@u2@example.com"); !r.Until.Equal(r2.Until) {
		t.Fatalf("u2's rest moved from %v to %v", r2.Until, r.Until)
	}
	// an appeal went through for u1: it is asked again, and u2 still not
	if !s.Unrest("antigravity@u1@example.com") {
		t.Fatal("u1's rest not lifted")
	}
	asked = nil
	if code, _ := agyTurn(s); code != 403 || !slices.Equal(asked, []string{"u1@example.com"}) {
		t.Fatalf("after the lift: %d, asked %v", code, asked)
	}
}

// The same for any vendor's account restriction (here magpie's words for
// ZCode's risk control, on a ChatGPT account): the lone account resting
// as restricted is not asked by the next requests.
func TestRestrictedLoneAccountNotAskedWhileItRests(t *testing.T) {
	codexSignedIn(t)
	var tried []string
	chatgptRefusing(t, &tried, riskBlocked)
	s := New()
	for n := 1; n <= 3; n++ {
		code, body := postAs(t, s, "", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
		if code != http.StatusForbidden || !strings.Contains(body, provider.ZCodeRestrictedHint) || strings.Join(tried, ",") != "acct-1" {
			t.Fatalf("request %d: %d %s; asked %v", n, code, body, tried)
		}
	}
}

// A refusal of the request's content is not the account's: it rests
// nobody, and the next request asks the same account again.
func TestContentRefusalStillAsksTheAccount(t *testing.T) {
	n := 0
	s, _ := antigravityGroupFor(t, []string{"u1@example.com"}, "gemini-3.8-flash", func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"code":400,"message":"`+highRiskRefusal+`","status":"INVALID_ARGUMENT"}}`)
	})
	for want := 1; want <= 3; want++ {
		if code, out := agyTurn(s); code < 400 || n != want {
			t.Fatalf("turn %d: %d %s, asked %d times", want, code, out, n)
		}
	}
	if r, ok := restOf("antigravity@u1@example.com"); ok {
		t.Fatalf("a content refusal rests the account: %+v", r)
	}
}

// A conversation kept strictly on its account (Stays: Strict) whose
// account Google suspended is given the refusal again without asking, and
// the account's rest isn't taken anew by each such turn: it ends when it
// would, rather than never while the conversation goes on.
func TestSuspendedStrictConversationKeepsItsRest(t *testing.T) {
	var asked []string
	suspended := false
	s, _ := antigravityGroupFor(t, []string{"u1@example.com", "u2@example.com"}, "gemini-3.8-flash", func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if suspended {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, antigravitySuspended)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"answered"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}}`))
	})
	p, err := provider.Find("antigravity")
	if err != nil {
		t.Fatal(err)
	}
	p.Affinity = provider.AffinityStrict
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	turn := func() (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"antigravity/gemini-3.8-flash","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("x-session-id", "s1")
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, out := turn(); code != 200 || len(asked) != 1 {
		t.Fatalf("first turn: %d %s, asked %v", code, out, asked)
	}
	kept := asked[0]
	suspended, asked = true, nil
	if code, out := turn(); code != 403 || !slices.Equal(asked, []string{kept}) {
		t.Fatalf("suspended turn: %d %s, asked %v", code, out, asked)
	}
	first, ok := restOf("antigravity@" + kept)
	if !ok || first.Why != failRestricted {
		t.Fatalf("%s rests %+v %v", kept, first, ok)
	}
	for n := 3; n <= 5; n++ {
		asked = nil
		code, out := turn()
		if code != 403 || !strings.Contains(out, "violation of Terms of Service") || len(asked) != 0 {
			t.Fatalf("turn %d: %d %s, asked %v", n, code, out, asked)
		}
		if r, _ := restOf("antigravity@" + kept); !r.Until.Equal(first.Until) {
			t.Fatalf("turn %d moved the rest from %v to %v", n, first.Until, r.Until)
		}
	}
}
