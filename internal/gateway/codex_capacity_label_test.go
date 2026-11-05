package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// 蛋仔 on Discord: a Codex turn over two ChatGPT accounts, the first's
// model at capacity (529), the last out of its allowance. Codex is told
// the overload ("Selected model is at capacity. Please try a different
// model."), as it should be, but the Requests row read "529 ·
// usage_limit_reached": the status and words were the overload's, the
// kind the last account's. The row's kind is the failure Codex was told,
// and only the account out of its allowance rests for long.
func TestCodexCapacityNotLabelledUsageLimit(t *testing.T) {
	for name, overload := range map[string]string{
		"backend's overload":     overloadedStatus,
		"the words Codex showed": `{"error":{"type":"server_error","code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}`,
	} {
		t.Run(name, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			var tried []string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				acct := r.Header.Get("chatgpt-account-id")
				tried = append(tried, acct)
				w.Header().Set("Content-Type", "application/json")
				if acct == "acct-1" {
					w.WriteHeader(529)
					io.WriteString(w, overload)
					return
				}
				w.WriteHeader(429)
				io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":7200}}`)
			}))
			t.Cleanup(up.Close)
			was := provider.CodexBase
			provider.CodexBase = up.URL + "/backend-api/codex"
			t.Cleanup(func() { provider.CodexBase = was })
			pause := retryPause
			retryPause = time.Millisecond
			t.Cleanup(func() { retryPause = pause })

			code, body := codexPost(t, oneAccountTurn)
			if code != 529 || strings.Contains(strings.ToLower(body), "usage limit") {
				t.Fatalf("Codex told %d %s (tried %v)", code, body, tried)
			}
			if tried[0] != "acct-1" || tried[len(tried)-1] != "acct-2" {
				t.Fatalf("tried %v", tried)
			}
			rec := lastUsage(t)
			if rec.Status != 529 || rec.ErrType == "usage_limit_reached" || !strings.Contains(strings.ToLower(rec.Error), "overloaded") && !strings.Contains(rec.Error, "at capacity") {
				t.Fatalf("row: %d · %q · %q", rec.Status, rec.ErrType, rec.Error)
			}
			if rec.ErrType != "server_error" {
				t.Fatalf("row's kind %q, want the overload's server_error", rec.ErrType)
			}
			// the overloaded account sits out a moment at most; the one out
			// of its allowance rests till ChatGPT says it is back
			restingUntil.Lock()
			defer restingUntil.Unlock()
			long := 0
			for id, u := range restingUntil.m {
				switch d := time.Until(u); {
				case d > time.Hour:
					long++
				case d > overloadRestMost:
					t.Errorf("%s rests %v for an overload", id, d)
				}
			}
			if long != 1 {
				t.Errorf("%d accounts rest for hours, want the one out of its allowance: %v", long, restingUntil.m)
			}
		})
	}
}

// danzi002 on Discord, the error as the request's row showed it: an
// account with its allowance left answered 529 with ChatGPT's
// server_is_overloaded, the last account was out of its allowance, and
// the Routing page's row read "529 · 额度用尽" (quota used up): the
// status the overload's, the kind the last try's. The route now says the
// failure the agent was told was the vendor overloaded, the row's kind
// stays the overload's, and the account that answered it doesn't rest as
// spent.
func TestToldOverloadIsNotQuota(t *testing.T) {
	const said = `{"message":"Our servers are currently overloaded. Please try again later.","type":"service_unavailable_error","param":null,"code":"server_is_overloaded"}`
	codexSignedIn(t, "spare@example.com")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("chatgpt-account-id") == "acct-1" {
			w.WriteHeader(529)
			io.WriteString(w, `{"error":`+said+`}`)
			return
		}
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":7200}}`)
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	pause := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = pause })

	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(oneAccountTurn))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 529 {
		t.Fatalf("Codex told %d %s", rec.Code, rec.Body)
	}
	rt := lastRoute(s)
	if rt.Status != 529 || len(rt.Tries) < 2 {
		t.Fatalf("route %d with %d tries", rt.Status, len(rt.Tries))
	}
	if last := rt.Tries[len(rt.Tries)-1]; !last.Earlier || last.Fail != failQuota {
		t.Fatalf("last try: earlier %v, fail %q", last.Earlier, last.Fail)
	}
	if rt.Fail != failOverloaded {
		t.Fatalf("the route says the agent was told a %q failure, want %q: its row reads 529 · %s", rt.Fail, failOverloaded, cmpOr(rt.Fail, rt.Tries[len(rt.Tries)-1].Fail))
	}
	if u := lastUsage(t); u.Status != 529 || u.ErrType != "service_unavailable_error" {
		t.Fatalf("row: %d · %q · %q", u.Status, u.ErrType, u.Error)
	}
	for _, tr := range rt.Tries[:len(rt.Tries)-1] {
		if tr.Rest != nil && (tr.Rest.Why == failQuota || tr.Rest.Why == failCredit || time.Until(tr.Rest.Until) > overloadRestMost) {
			t.Errorf("the overloaded account rests %+v", *tr.Rest)
		}
	}
	restingUntil.Lock()
	defer restingUntil.Unlock()
	long := 0
	for id, u := range restingUntil.m {
		switch d := time.Until(u); {
		case d > time.Hour:
			long++
		case d > overloadRestMost:
			t.Errorf("%s rests %v for an overload", id, d)
		}
	}
	if long != 1 {
		t.Errorf("%d accounts rest for hours, want the one out of its allowance: %v", long, restingUntil.m)
	}
}

// Each kind toldFail gives: the vendor overloaded, whether said by a 529
// or a 5xx's words, the agent's prompt turned away, and the rest as
// failure reads them.
func TestToldFailKinds(t *testing.T) {
	for _, c := range []struct {
		o    Try
		want string
	}{
		{Try{Status: 529, Error: `Codex: {"message":"Our servers are currently overloaded. Please try again later.","type":"service_unavailable_error","param":null,"code":"server_is_overloaded"}`}, failOverloaded},
		{Try{Status: 503, Error: `Gemini: The model is overloaded. Please try again later.`}, failOverloaded},
		{Try{Status: 503, Error: `Relay: upstream busy`}, failOther},
		{Try{Status: turnedAwayStatus, Error: "Antigravity: turned away", errType: turnedAwayErrType}, failPrompt},
		{Try{Status: 429, Error: "Relay: Rate limit exceeded, please retry after 1 minute"}, failRate},
		{Try{Status: 401, Error: "Relay: invalid api key"}, failOther},
	} {
		if got := toldFail(&c.o); got != c.want {
			t.Errorf("toldFail(%d %q) = %q, want %q", c.o.Status, c.o.Error, got, c.want)
		}
	}
}
