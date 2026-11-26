package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// clinePassServer answers the Cline API's account endpoints for a
// ClinePass key (sk-pass: 5 hours 20 used, the week 62, the month 30) and
// for a key whose account has no ClinePass (sk-free: usage-limits 404),
// counting the asks of usage-limits. down has every limits read fail.
func clinePassServer(t *testing.T, down *atomic.Bool) (limits *atomic.Int32, week *atomic.Int32) {
	t.Helper()
	limits, week = &atomic.Int32{}, &atomic.Int32{}
	week.Store(62)
	five := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	weekly := time.Now().Add(4 * 24 * time.Hour).UTC().Format(time.RFC3339)
	month := time.Now().Add(20 * 24 * time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch r.URL.Path {
		case "/api/v1/users/me":
			w.Write([]byte(`{"success":true,"data":{"id":"usr_` + key + `"}}`))
		case "/api/v1/users/usr_" + key + "/balance":
			w.Write([]byte(`{"success":true,"data":{"balance":4250000}}`))
		case "/api/v1/users/me/plan/usage-limits":
			limits.Add(1)
			switch {
			case down.Load():
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"success":false,"error":"try again later"}`))
			case key == "sk-pass":
				w.Write([]byte(`{"success":true,"data":{"limits":[
					{"type":"five_hour","percentUsed":20,"resetsAt":"` + five + `"},
					{"type":"weekly","percentUsed":` + strconv.Itoa(int(week.Load())) + `,"resetsAt":"` + weekly + `"},
					{"type":"monthly","percentUsed":30,"resetsAt":"` + month + `"}]}}`))
			default:
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"success":false,"error":"no active plan"}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := clineUsageAPI
	clineUsageAPI = srv.URL + "/api/v1"
	t.Cleanup(func() { clineUsageAPI = old })
	return limits, week
}

var clinePassKey = Provider{ID: "clinepass", Name: "ClinePass", Preset: "clinepass", Key: "sk-pass",
	Chat: "https://api.cline.bot/api/v1"}

// xzhih on X: on the Routing page a ClinePass key's seat had no bar and
// no reset beside a Command Code key's, though the Usage page's card read
// its 5-hour, weekly and monthly limits. Routing reads them as it reads a
// plan key's (KeyAllowance), behind the request; they count ClinePass's
// models only, never Cline's free ones or those its credits pay for; and
// a key whose account has no ClinePass stays not known.
func TestClinePassKeyAllowance(t *testing.T) {
	keyLimitsHome(t)
	var down atomic.Bool
	limits, _ := clinePassServer(t, &down)
	if err := Save(clinePassKey); err != nil {
		t.Fatal(err)
	}
	if id := KeyAllowanceID(clinePassKey); id == "" {
		t.Fatal("a ClinePass key has no id for its windows renewed")
	}
	a := waitAllowance(t, clinePassKey, func(Allowance) bool { return true })
	now := time.Now()
	used, renews := a.For("cline-pass/deepseek-v4.1-flash-1017", now)
	if used != 62 || len(renews) != 3 {
		t.Fatalf("a ClinePass model: used %v, renews %v of %+v", used, renews, a)
	}
	if w := a.Fullest("cline-pass/deepseek-v4.1-flash-1017", now); w.Sub(now) < 3*24*time.Hour || w.Sub(now) > 5*24*time.Hour {
		t.Fatalf("the fullest window renews at %v, not in about 4 days", w)
	}
	for _, m := range []string{"cline-free/deepseek-v4.1-flash", "anthropic/claude-sonnet-5.5"} {
		if used, _ := a.For(m, now); used != 0 {
			t.Errorf("%s counted against ClinePass's limits: %v", m, used)
		}
	}
	if n := limits.Load(); n != 1 {
		t.Fatalf("usage-limits asked %d times", n)
	}

	// a Cline key with no ClinePass: read, and not known
	free := clinePassKey
	free.ID, free.Key = "cline-credits", "sk-free"
	if err := Save(free); err != nil {
		t.Fatal(err)
	}
	if _, ok := KeyAllowance(free); ok {
		t.Fatal("known before it was read")
	}
	waitRead(t)
	if a, ok := KeyAllowance(free); ok {
		t.Fatalf("a key with no ClinePass is known: %+v", a)
	}

	// a Cline key with a Balance URL of the user's own is read there, as
	// its card is: not asked at the Cline API
	own := clinePassKey
	own.ID, own.BalanceURL = "cline-own", "https://example.invalid/balance"
	if KeyAllowanceID(own) != "" {
		t.Fatal("a Cline key with its own Balance URL reads ClinePass's limits")
	}
}

// The Usage page's read of a ClinePass key's card is what routing goes
// by at once, without asking Cline again, over what routing read before
// it; after a restart, with Cline failing, the card kept on disk stands
// in, and a failed read leaves it known.
func TestClinePassKeyAllowanceFromItsCard(t *testing.T) {
	keyLimitsHome(t)
	var down atomic.Bool
	limits, week := clinePassServer(t, &down)
	if err := Save(clinePassKey); err != nil {
		t.Fatal(err)
	}
	// routing read the week at 62 a moment ago; the card then reads 80
	waitAllowance(t, clinePassKey, func(Allowance) bool { return true })
	week.Store(80)
	qs := KeyBalances(context.Background())
	if len(qs) != 1 || len(qs[0].Windows) != 3 || qs[0].Balance != "$4.25" {
		t.Fatalf("cards: %+v", qs)
	}
	a, ok := KeyAllowance(clinePassKey)
	if used, _ := a.For("cline-pass/glm-5.3", time.Now()); !ok || used != 80 {
		t.Fatalf("after the card's read: %v %+v", ok, a)
	}
	if n := limits.Load(); n != 2 {
		t.Fatalf("usage-limits asked %d times: routing asked again what the card had just read", n)
	}

	down.Store(true)
	keyAllowances.Lock()
	keyAllowances.m = nil
	keyAllowances.Unlock()
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
	a, ok = KeyAllowance(clinePassKey)
	if used, _ := a.For("cline-pass/glm-5.3", time.Now()); !ok || used != 80 {
		t.Fatalf("from the card on disk: %v %+v", ok, a)
	}
	waitRead(t)
	if a, ok := KeyAllowance(clinePassKey); !ok || len(a) != 3 {
		t.Fatalf("after a failed read: %v %+v", ok, a)
	}
}
