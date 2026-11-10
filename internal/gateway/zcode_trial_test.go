package gateway

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// The ZCode plugin's refusals of a Start Plan trial entry, as it writes them
// (zcode 0.1.31's fetch, JSON.stringify of its error): the trial ended, in
// the words of #3661's routing log, and a trial's bucket spent.
const (
	zcodeFlashTrialEnded = `{"type":"error","error":{"type":"invalid_request_error","message":"ZCode has no active limited quota for GLM-5.3-Flash-Trial"}}`
	zcodeTrialEnded      = `{"type":"error","error":{"type":"invalid_request_error","message":"ZCode has no active limited quota for GLM-5.3-Trial"}}`
	zcodeTrialSpent      = `{"type":"error","error":{"type":"rate_limit_error","message":"ZCode's limited quota for glm-5.3 is exhausted or temporarily unavailable"}}`
	// zcode 0.1.49's words for an entry it rests a minute after a quota
	// answer: why goes in front, so the spent words stay last (Can4ry_ on
	// X). 0.1.48 put "(ZCode answered 429 …)" after them, which these
	// words didn't match, and the account rested whole.
	zcodeTrialRested    = `{"type":"error","error":{"type":"rate_limit_error","message":"ZCode said the last request for GLM-5.3 was over its quota, so it is not asked again for a minute: ZCode's limited quota for GLM-5.3 is exhausted or temporarily unavailable"}}`
	zcodeTrialRested048 = `{"type":"error","error":{"type":"rate_limit_error","message":"ZCode's limited quota for GLM-5.3 is exhausted or temporarily unavailable (ZCode answered 429 to the last request for it, so it is not asked again for a minute)"}}`
	// zcode.z.ai's edge firewall's block page, as the plugin's startBlocked
	// puts it (windy_66454 on Discord, 10-31: "405 Method Not Allowed — the
	// provider's network firewall blocked …"), and ZCode's risk control
	// blocking the account (code 3012, plugins#348)
	zcodeStartEdge = `{"type":"error","error":{"type":"permission_error","message":"405 Method Not Allowed — the provider's network firewall blocked requests from this IP; wait a while, or switch to another network or proxy"}}`
	zcodeStartRisk = `{"type":"error","error":{"type":"permission_error","message":"request has been blocked due to unusual activity. (405, code 3012) — ZCode's risk control blocked this account for unusual activity (code 3012); it rests, and the group's other providers are used. If the ZCode app fails on this account too, only ZCode support can lift the block"}}`
)

// A ZCode account's Start Plan trial entry that ended, or whose bucket is
// spent, rests that entry alone: the next request for the account's GLM
// Coding Plan model is still served by it (#3661,
// orion-nebula-dev: GLM-5.3-Flash-Trial and GLM-5.3-Trial rested
// "zcode@zcode", and GLM-5.3 and GLM-5.3-Flash with it). A refusal of the
// account — its quota, its key, another model named, a model that isn't a
// trial — and the same words from another vendor still rest the account.
// After skygunner's TestZCodeTrialQuotaRestsTheModelAlone (e04a1b994).
func TestZCodeTrialRefusalRestsTheTrialAlone(t *testing.T) {
	for _, tc := range []struct {
		name, vendor, model, body string
		status                    int
		listed                    bool // the plugin still lists the trial entry
		accountRest               bool
	}{
		{"flash trial ended", "zcode", "GLM-5.3-Flash-Trial", zcodeFlashTrialEnded, 400, false, false},
		{"trial ended", "zcode", "GLM-5.3-Trial", zcodeTrialEnded, 400, false, false},
		{"trial ended, still listed", "zcode", "GLM-5.3-Trial", zcodeTrialEnded, 400, true, false},
		{"trial spent", "zcode", "GLM-5.3-Trial", zcodeTrialSpent, 429, true, false},
		{"trial rested", "zcode", "GLM-5.3-Trial", zcodeTrialRested, 429, true, false},
		{"trial rested, 0.1.48's words", "zcode", "GLM-5.3-Trial", zcodeTrialRested048, 429, true, true},
		{"account quota", "zcode", "GLM-5.3", `{"error":{"type":"rate_limit_error","message":"quota exhausted"}}`, 429, false, true},
		{"account quota 400", "zcode", "GLM-5.3", `{"error":{"type":"invalid_request_error","message":"quota exhausted for this account"}}`, 400, false, true},
		{"account key", "zcode", "GLM-5.3", `{"error":{"message":"invalid api key"}}`, 401, false, true},
		{"another trial named", "zcode", "GLM-5.3-Flash-Trial", zcodeTrialEnded, 400, false, true},
		{"spent names another", "zcode", "GLM-5.3-Flash-Trial", zcodeTrialSpent, 429, true, true},
		{"not a trial", "zcode", "GLM-5.3", `{"type":"error","error":{"type":"invalid_request_error","message":"ZCode has no active limited quota for GLM-5.3"}}`, 400, false, true},
		{"ended words on a 429", "zcode", "GLM-5.3-Trial", zcodeTrialEnded, 429, false, true},
		{"another vendor's words", "factory", "GLM-5.3-Trial", zcodeTrialEnded, 400, false, true},
		// a trial entry goes to zcode.z.ai's Start Plan, and a dual
		// account's GLM Coding Plan to api.z.ai: zcode.z.ai's firewall
		// blocking the trial says nothing of the plan's host, so the trial
		// rests alone and the plan's model is asked. So does ZCode's risk
		// control refusing the trial (3012, young02036 on Discord: the
		// gift refused, the plan's models served). Blocking the plain
		// model, or its 3012, rests the account
		{"trial edge block", "zcode", "GLM-5.3-Flash-Trial", zcodeStartEdge, 405, true, false},
		{"plain edge block", "zcode", "GLM-5.3", zcodeStartEdge, 405, false, true},
		{"trial risk control", "zcode", "GLM-5.3-Flash-Trial", zcodeStartRisk, 405, true, false},
		{"plain risk control", "zcode", "GLM-5.3", zcodeStartRisk, 405, false, true},
		{"another vendor's risk control", "factory", "GLM-5.3-Trial", zcodeStartRisk, 405, false, true},
		{"another vendor's block", "factory", "GLM-5.3-Trial", zcodeStartEdge, 405, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// a ZCode list with no free model, which would rest a paid
			// model alone whatever it said (PaidBesideFree)
			t.Setenv("FAKE_ZCODE", "1")
			t.Setenv("FAKE_NO_FREE", "1")
			if tc.listed {
				t.Setenv("FAKE_ZCODE_TRIAL", "1")
			}
			var mu sync.Mutex
			var asked []string
			good := &adaptiveVendor{}
			movedFake(t, tc.vendor, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				model := modelOf(body)
				mu.Lock()
				asked = append(asked, model)
				mu.Unlock()
				if model == tc.model {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
					return
				}
				r.Body = io.NopCloser(strings.NewReader(string(body)))
				good.ServeHTTP(w, r)
			}))
			p, err := provider.Find(tc.vendor)
			if err != nil {
				t.Fatal(err)
			}
			c := candidate{p: *p, rest: tc.vendor, model: tc.model}
			// the account's windows were read, so its rest isn't narrowed
			// later by a read that comes in
			old := allowances
			allowances = func(string) map[string]provider.Allowance {
				return map[string]provider.Allowance{p.Account.User: {{Used: 10}}}
			}
			t.Cleanup(func() { allowances = old })
			b := &scripted{replies: []reply{{200, "", chatOK}, {200, "", chatOK}}}
			scriptedOn(t, "b", provider.Chat, b)
			// the trial, then the account's own model, each with another
			// provider behind it
			for name, first := range map[string]string{"T": tc.model, "S": "GLM-5.3"} {
				if err := provider.SaveGroup(provider.Group{Name: name, Members: []string{tc.vendor + "/" + first, "b/m"}, Routing: provider.Ordered}); err != nil {
					t.Fatal(err)
				}
			}
			s := New()
			// the trial refused, and the other provider answers
			code, body := postAs(t, s, "", `{"model":"group/t","messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 {
				t.Fatalf("trial request: %d %s", code, body)
			}
			tries := s.trace.routes[len(s.trace.routes)-1].Tries
			if len(tries) != 2 || tries[0].Status != tc.status || tries[0].Rest == nil {
				t.Fatalf("trial request tries: %+v", tries)
			}
			want := c.restID()
			if tc.accountRest {
				want = c.restKey()
			}
			if tries[0].Rest.Key != want {
				t.Fatalf("the refusal rested %q, want %q", tries[0].Rest.Key, want)
			}
			if _, ok := restOf(c.restKey()); ok != tc.accountRest {
				t.Fatalf("account resting = %v, want %v", ok, tc.accountRest)
			}
			if _, ok := restOf(c.restID()); !tc.accountRest && !ok {
				t.Fatalf("%s isn't resting", tc.model)
			}
			// then the account's own GLM-5.3: served by the account unless
			// the account was refused
			if code, body := postAs(t, s, "", `{"model":"group/s","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
				t.Fatalf("GLM-5.3 request: %d %s", code, body)
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.accountRest {
				if b.n != 2 || len(asked) != 1 {
					t.Fatalf("account fault: asked %v, the other provider %d times", asked, b.n)
				}
				return
			}
			if b.n != 1 || len(asked) != 2 || asked[1] != "GLM-5.3" {
				t.Fatalf("the trial's refusal kept the account's GLM-5.3 out: asked %v, the other provider %d times", asked, b.n)
			}
		})
	}
}

// plugins#438 (takomitkm): a dual ZCode account's GLM Coding Plan model
// refused for its weekly or monthly limit (Zhipu's 1310) rested the whole
// account, so its Start Plan -Trial entries, which the gift's own buckets
// serve, were answered the cached 1310 without being asked. zcode 0.1.58
// lists the trial entries free: the coding plan model's refusal rests that
// model alone (PaidBesideFree), and the trial entry is still asked. A list
// with no free model — another vendor's, or ZCode's before 0.1.58 — still
// rests the account on the same refusal.
func TestZCodeCodingPlanLimitLeavesTrials(t *testing.T) {
	resets := time.Now().In(time.FixedZone("UTC+8", 8*3600)).Add(72 * time.Hour).Format("2006-01-02 15:04:05")
	limit := `{"error":{"code":"1310","message":"Weekly/Monthly Limit Exhausted. Your limit will reset at ` + resets + `"}}`
	for _, tc := range []struct {
		name, vendor string
		free         bool // the plugin lists the trial entries free
		accountRest  bool
	}{
		{"zcode 0.1.58", "zcode", true, false},
		{"no free entries", "zcode", false, true},
		{"another vendor", "factory", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKE_ZCODE", "1")
			t.Setenv("FAKE_NO_FREE", "1")
			t.Setenv("FAKE_ZCODE_TRIAL", "1")
			if tc.free {
				t.Setenv("FAKE_ZCODE_FREE", "1")
			}
			var mu sync.Mutex
			var asked []string
			good := &adaptiveVendor{}
			movedFake(t, tc.vendor, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				model := modelOf(body)
				mu.Lock()
				asked = append(asked, model)
				mu.Unlock()
				if model == "GLM-5.3" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(429)
					io.WriteString(w, limit)
					return
				}
				r.Body = io.NopCloser(strings.NewReader(string(body)))
				good.ServeHTTP(w, r)
			}))
			p, err := provider.Find(tc.vendor)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.PaidBesideFree("GLM-5.3"); got != tc.free {
				t.Fatalf("GLM-5.3 paid beside free = %v, want %v", got, tc.free)
			}
			c := candidate{p: *p, rest: tc.vendor, model: "GLM-5.3"}
			old := allowances
			allowances = func(string) map[string]provider.Allowance {
				return map[string]provider.Allowance{p.Account.User: {{Used: 10}}}
			}
			t.Cleanup(func() { allowances = old })
			b := &scripted{replies: []reply{{200, "", chatOK}, {200, "", chatOK}}}
			scriptedOn(t, "b", provider.Chat, b)
			for name, first := range map[string]string{"S": "GLM-5.3", "T": "GLM-5.3-Trial"} {
				if err := provider.SaveGroup(provider.Group{Name: name, Members: []string{tc.vendor + "/" + first, "b/m"}, Routing: provider.Ordered}); err != nil {
					t.Fatal(err)
				}
			}
			s := New()
			// the coding plan's model refused for its weekly limit, and the
			// other provider answers
			if code, body := postAs(t, s, "", `{"model":"group/s","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
				t.Fatalf("GLM-5.3 request: %d %s", code, body)
			}
			tries := s.trace.routes[len(s.trace.routes)-1].Tries
			if len(tries) != 2 || tries[0].Status != 429 || tries[0].Rest == nil || tries[0].Rest.Why != failQuota {
				t.Fatalf("GLM-5.3 tries: %+v", tries)
			}
			want := c.restID()
			if tc.accountRest {
				want = c.restKey()
			}
			if tries[0].Rest.Key != want {
				t.Fatalf("the 1310 rested %q, want %q", tries[0].Rest.Key, want)
			}
			// then the trial entry: asked unless the account was rested
			if code, body := postAs(t, s, "", `{"model":"group/t","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
				t.Fatalf("trial request: %d %s", code, body)
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.accountRest {
				if b.n != 2 || len(asked) != 1 {
					t.Fatalf("account rest: asked %v, the other provider %d times", asked, b.n)
				}
				return
			}
			if b.n != 1 || len(asked) != 2 || asked[1] != "GLM-5.3-Trial" {
				t.Fatalf("the 1310 kept the trial entry out: asked %v, the other provider %d times", asked, b.n)
			}
		})
	}
}
