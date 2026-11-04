package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// wbRestrictedBody is WorkBuddy's 403 for an account it restricted, byte
// for byte as #1849 gives it.
const wbRestrictedBody = `{"code":11140,"msg":"request illegal",
 "displayMsg":{"en":"Your account has been restricted due to unusual activity",
               "zh":"账号存在异常行为，已被限制使用","zh-hant":"帳號存在異常行為，已被限制使用"},
 "displayTips":{"en":"We detected unusual activity on this account. Submit feedback to appeal this restriction",
                "zh":"系统检测到该账号存在异常使用行为，如需申诉请提交反馈"},
 "actions":["EDIT_INPUT","SUBMIT_FEEDBACK"]}`

// restedFor is how long r rests from when it was decided.
func restedFor(r Rest, from time.Time) time.Duration { return r.Until.Sub(from) }

// WorkBuddy's 403 11140 says the account itself is restricted (#1849): in
// each shape the gateway reads it — the vendor's JSON as it came and the
// plugin's — the account rests two hours, as "restricted", not a minute's
// backoff, and the Routing page can lift it.
func TestWorkBuddyRestrictedRestsHours(t *testing.T) {
	fresh(t)
	p := provider.Provider{ID: provider.WorkBuddyAIID, Name: "WorkBuddy AI", Account: &provider.Account{Agent: provider.WorkBuddyAIID, User: "u1"}}
	s := New()

	// the plugin's explained(), as it puts the same answer
	// (@magpie-community/opencode-workbuddy-auth 0.1.17)
	plugin := []byte(`{"error":{"message":"request illegal — ` + provider.WBRestrictedHint + `","type":"permission_error","code":11140}}`)

	escaped := []byte(`{"code":"11140","msg":"request illegal","displayMsg":{"zh":"账号存在异常行为，已被限制使用"}}`)
	worded := []byte(`{"msg":"request illegal","displayMsg":{"zh-hant":"帳號存在異常行為，已被限制使用"}}`)
	c := candidate{p: p, model: "deepseek-v4.1-flash", rest: p.ID + "#u1"}
	for name, body := range map[string][]byte{
		"vendor's JSON": []byte(wbRestrictedBody), "plugin's": plugin,
		"code as a string, Chinese escaped": escaped, "words alone": worded,
	} {
		fresh(t)
		now := time.Now()
		rest := s.restAfter(c, 403, nil, body)
		if rest.Why != failRestricted || rest.By != "restricted" {
			t.Fatalf("%s: rests %+v", name, rest)
		}
		if d := restedFor(rest, now); d < restrictedRest-time.Minute || d > restrictedRest+time.Minute {
			t.Fatalf("%s: rests %v", name, d)
		}
		// the account, not one model of it
		if rest.Key != c.restKey() {
			t.Fatalf("%s: rests %q, not the account %q", name, rest.Key, c.restKey())
		}
		// a user whose appeal went through lifts it at once
		if !s.Unrest(rest.Key) {
			t.Fatalf("%s: its rest can't be lifted", name)
		}
		if _, ok := restOf(rest.Key); ok {
			t.Fatalf("%s: still resting once lifted", name)
		}
	}
}

// Another vendor's code 11140, and WorkBuddy's other refusals, aren't an
// account restricted: a key's 403 with code 11140 rests as any failure
// does, and WorkBuddy's 11128 "unapproved channel" (#182) still rests
// nobody, as the agent's prompt is at fault.
func TestOnlyWorkBuddyRestrictedRestsAsRestricted(t *testing.T) {
	fresh(t)
	s := New()
	other := candidate{p: provider.Provider{ID: "acme", Name: "Acme"}, model: "m", rest: "acme"}
	for _, body := range []string{
		`{"code":11140,"msg":"request illegal"}`,
		`{"error":{"code":11140,"message":"forbidden"}}`,
		`{"error":{"message":"Acme: request illegal","type":"permission_error"},"type":"error"}`,
	} {
		if f := failureOf(other, 403, []byte(body)); f == failRestricted {
			t.Fatalf("another vendor's %s read as restricted", body)
		}
		rest := s.restAfter(other, 403, nil, []byte(body))
		if rest.Why != failOther || rest.By == "restricted" || restedFor(rest, time.Now()) > longestRetry {
			t.Fatalf("another vendor's %s rests %+v", body, rest)
		}
	}
	// another plugin's account (fakeco) with WorkBuddy's own body is that
	// vendor's answer too
	if restrictedRefused(provider.Provider{ID: "fakeco", Account: &provider.Account{Agent: "plugin"}}, 403, []byte(wbRestrictedBody)) {
		t.Fatal("a non-WorkBuddy account's 11140 read as restricted")
	}

	wb := provider.Provider{ID: provider.WorkBuddyAIID, Account: &provider.Account{Agent: provider.WorkBuddyAIID, User: "u1"}}
	for _, body := range []string{
		`{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`,
		`{"code":14018,"msg":"Credits exhausted. Please visit the link to purchase."}`,
		`{"code":111400,"msg":"something else"}`,
	} {
		if restrictedRefused(wb, 403, []byte(body)) || restrictedRefused(wb, 400, []byte(body)) {
			t.Fatalf("WorkBuddy's %s read as restricted", body)
		}
	}
	// 11128 is still the agent's prompt, which rests nobody
	if !promptRefused(400, []byte(`{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`)) {
		t.Fatal("11128 no longer read as the agent's prompt turned away")
	}
	// an answer that went through is nobody's refusal
	if restrictedRefused(wb, 200, []byte(wbRestrictedBody)) {
		t.Fatal("a 200 read as restricted")
	}
}

// Through a plugin, as the moved WorkBuddy is served: an account whose
// answer is the plugin's restricted refusal rests two hours, and the
// request it refused isn't sent on to the provider's other account: the
// vendor's risk control reads the request too, and restricted each account
// it was sent on to in turn (_teamtaylor_ on Discord). The agent is told;
// the next request goes to the other account, never the restricted one
// (before #1849 it was back within minutes and asked by every request).
// The same vendor's raw 11140 from a plugin that isn't WorkBuddy's
// (fakeco) rests as any failure does, and the next account answers it.
func TestPluginRestrictedAccountSkipped(t *testing.T) {
	bun := testenv.Bun(t)
	for _, tc := range []struct {
		name, body, why string
	}{
		{"WorkBuddy plugin's words", `{"error":{"message":"request illegal — ` + provider.WBRestrictedHint + `","type":"permission_error","code":11140}}`, failRestricted},
		{"another vendor's 11140", wbRestrictedBody, failOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			t.Setenv("MAGPIE_BUN", bun)
			t.Cleanup(plugin.Settle)
			var mu sync.Mutex
			var tried []string
			up := testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fresh-r-")
				mu.Lock()
				tried = append(tried, who)
				mu.Unlock()
				if who == "a" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(403)
					fmt.Fprint(w, tc.body)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+who+`"},"finish_reason":null}]}`+"\n\n")
				fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			}))
			defer up.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
			if _, err := plugin.Add(ctx, abs); err != nil {
				t.Fatal(err)
			}
			if _, err := plugin.Providers(ctx); err != nil {
				t.Fatal(err)
			}
			for _, team := range []string{"a", "b"} {
				st, err := provider.StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": team})
				if err != nil {
					t.Fatal(err)
				}
				if err := provider.SubmitSignInCallback(st.ID, "good"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := plugin.Providers(ctx); err != nil {
				t.Fatal(err)
			}
			if err := provider.SwitchLogin("fakeco", "a@fake"); err != nil {
				t.Fatal(err)
			}
			s := New()
			code, body := postAs(t, s, "", `{"model":"fakeco/fake-1","messages":[{"role":"user","content":"hi"}]}`)
			r := s.trace.routes[len(s.trace.routes)-1]
			mu.Lock()
			first := strings.Join(tried, ",")
			mu.Unlock()
			if tc.why == failRestricted {
				// the agent is told, and b never sent the request
				if code != http.StatusForbidden || !strings.Contains(body, provider.WBRestrictedHint) || first != "a" || len(r.Tries) != 1 {
					t.Fatalf("%d %s; asked %s; tries %+v", code, body, first, r.Tries)
				}
			} else if code != 200 || !strings.Contains(body, "from b") || len(r.Tries) != 2 {
				t.Fatalf("%d %s; tries %+v", code, body, r.Tries)
			}
			f := r.Tries[0]
			if f.Fail != tc.why || f.Rest == nil || f.Rest.Why != tc.why {
				t.Fatalf("a's try %+v rest %+v", f, f.Rest)
			}
			d := f.Rest.Until.Sub(time.Now())
			if tc.why == failRestricted && (d < restrictedRest-time.Minute || f.Rest.By != "restricted") {
				t.Fatalf("a rests %v by %q", d, f.Rest.By)
			}
			if tc.why == failOther && (d > longestRetry || f.Rest.By != "backoff") {
				t.Fatalf("another vendor's 11140 rests %v by %q", d, f.Rest.By)
			}
			// the next request doesn't ask a again
			mu.Lock()
			tried = nil
			mu.Unlock()
			if code, body := postAs(t, s, "", `{"model":"fakeco/fake-1","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from b") {
				t.Fatalf("again: %d %s", code, body)
			}
			mu.Lock()
			defer mu.Unlock()
			if strings.Join(tried, ",") != "b" {
				t.Fatalf("the next request asked %v", tried)
			}
		})
	}
}
