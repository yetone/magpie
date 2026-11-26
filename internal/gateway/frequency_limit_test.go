package gateway

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// zoecats on Discord (10-27): their LAN aggregator's 429, word for word.
const frequencyLimited = "Usage has exceeded the frequency limit. We will switch you to another model. Your usage limit for the original model will reset in 1 minutes."

// A 429 that says it is back within a wait a provider's patience can
// cover is a rate limit, waited for, whatever plan words it has besides;
// one that says it is back in hours, or that the money is spent, is not.
func TestShortResetIsARateLimit(t *testing.T) {
	for body, want := range map[string]string{
		`{"error":{"message":"` + frequencyLimited + `","type":"rate_limit_error"}}`: failRate,
		frequencyLimited: failRate,
		`{"error":{"message":"Usage limit exceeded, try again in 30 seconds"}}`: failRate,
		`{"error":{"message":"Daily usage limit hit. Retry after 20s."}}`:       failRate,
		`{"error":{"message":"用量超出频率限制，请 1 分钟后重试"}}`:                            failRate,
		`{"error":{"message":"额度已用完，30秒后再试"}}`:                                  failRate,
		`{"error":{"message":"Usage has exceeded the frequency limit."}}`:       failRate,
		// back in hours: the plan's, not waited
		`{"error":{"message":"Your usage limit for the original model will reset in 5 hours."}}`: failQuota,
		`{"error":{"message":"Your usage limit will reset in 0h 30min"}}`:                        failQuota,
		`{"error":{"message":"Daily free limit reached on model x. Try again in 23h 59m"}}`:      failQuota,
		`{"error":{"message":"You've hit your usage limit."}}`:                                   failQuota,
		// money spent, however soon it says to come back
		`{"error":{"message":"Insufficient balance. Please try again in 30 seconds"}}`:                        failCredit,
		`{"error":{"message":"余额不足，请充值后 1 分钟后重试"}}`:                                                           failCredit,
		`{"error":{"message":"You exceeded your current quota, please check your plan and billing details"}}`: failQuota,
	} {
		if got := failure(429, []byte(body)); got != want {
			t.Errorf("%s: %s, want %s", body, got, want)
		}
		if got := busy(429, []byte(body)); got != (want == failRate) {
			t.Errorf("%s: busy %v, want %v", body, got, want == failRate)
		}
	}
	// a 403 saying the same isn't a rate limit
	if got := failure(403, []byte(frequencyLimited)); got == failRate {
		t.Errorf("403: %s", got)
	}
}

// The reporter's 429 rests its provider about the minute it says, not
// the quarter hour of a plan used up.
func TestShortResetRestsWhatItSays(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	allowances = func(string) map[string]provider.Allowance { return nil }
	s := &Server{}
	c := candidate{p: provider.Provider{ID: "lan"}, model: "m", rest: "lan#k"}
	clearRest(c.restKey())
	t.Cleanup(func() { clearRest(c.restKey()) })
	r := s.restAfter(c, 429, http.Header{}, []byte(`{"error":{"message":"`+frequencyLimited+`"}}`))
	if d := time.Until(r.Until); r.Why != failRate || d > time.Minute || d < 50*time.Second {
		t.Fatalf("rests %v by %s (%s), want about a minute as a rate limit", d, r.By, r.Why)
	}
}

// The reporter's 429, with the Retry-After magpie gives it from its words
// (keepRetry), is waited out within the minute a lone provider waits by
// default (singlePatience), and asked again when the vendor said: the
// vendor's whole minute is not a hair past the budget. A time a second and
// more past it is still given up on.
func TestReporterResetFitsTheDefaultPatience(t *testing.T) {
	h := http.Header{}
	keepRetry(h, http.Header{}, []byte(`{"error":{"message":"`+frequencyLimited+`"}}`))
	if h.Get("Retry-After") != "60" {
		t.Fatalf("Retry-After %q, want the minute it says", h.Get("Retry-After"))
	}
	c := candidate{p: provider.Provider{ID: "lan"}, model: "m"}
	waitFrom := time.Now()
	list := noteBusy(nil, c, h)
	next, wait, ok := replan(list, 0, waitFrom, singlePatience)
	if !ok || len(next) != 1 || wait < 59*time.Second || wait > time.Minute {
		t.Fatalf("replan %v %v %v: want the one asked again after its minute", next, wait, ok)
	}
	for _, past := range []time.Duration{1500 * time.Millisecond, time.Hour} {
		list := []busyTry{{c: c, ready: waitFrom.Add(singlePatience + past)}}
		if _, _, ok := replan(list, 0, waitFrom, singlePatience); ok {
			t.Errorf("back %v past the budget: asked again", past)
		}
	}
}

// A request to one provider whose vendor answers the reporter's 429 is
// asked again when it says and answered, not handed the 429. Its minute
// is two seconds here, the rest of its words as they were.
func TestSingleProviderWaitsOutAFrequencyLimit(t *testing.T) {
	said := strings.Replace(frequencyLimited, "reset in 1 minutes", "reset in 2 seconds", 1)
	u := &busyUpstream{replies: []busyReply{
		{code: 429, body: `{"type":"error","error":{"type":"rate_limit_error","message":"` + said + `"}}`},
		{code: 200, body: anthropicAnswer},
	}}
	s := busyProvider(t, u)
	scaled(t, 5*time.Second)
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	start := time.Now()
	res, err := http.Post(gw.URL+"/v1/messages", "application/json", strings.NewReader(singleAsk(true)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body strings.Builder
	rd := bufio.NewReader(res.Body)
	for {
		line, err := rd.ReadString('\n')
		body.WriteString(line)
		if err != nil {
			break
		}
	}
	if res.StatusCode != 200 || !strings.Contains(body.String(), "message_stop") || u.asked() != 2 {
		t.Fatalf("agent got %d %q, upstream asked %d times; want the answer on the second ask; %+v", res.StatusCode, body.String(), u.asked(), lastRoute(s).Tries)
	}
	if took := time.Since(start); took < 2*time.Second {
		t.Errorf("asked again after %v, before the 2s it said", took)
	}
	if r := lastRoute(s); len(r.Tries) != 2 || r.Tries[0].Fail != failRate || r.Tries[0].Again < 1900 {
		t.Errorf("tries %+v: want the first asked again as a rate limit after its 2s", r.Tries)
	}
}

// The #7274 reporter's 429 from OpenAI, the body as it came with only the
// organization redacted: a tokens-per-minute limit whose closing advice,
// "adding a payment method … /account/billing", the credit words match.
const tpmWithPaymentAdvice = `{
  "message": "Rate limit reached for gpt-6.2-sol in organization <redacted> on tokens per min (TPM): Limit 1000000, Used 736214, Requested 401420. Please try again in 8.259s. Visit https://platform.openai.com/account/rate-limits to learn more. You can increase your rate limit by adding a payment method to your account at https://platform.openai.com/account/billing.",
  "type": "tokens",
  "param": null,
  "code": "rate_limit_exceeded"
}`

// A limit per minute is a rate limit its billing advice doesn't make
// money: the same one is asked again, and patience waits for it (#7274).
// A 429 that says the money is spent, or names no rate per minute beside
// its billing words, is still not waited for.
func TestRateLimitWithBillingAdviceIsWaitedFor(t *testing.T) {
	body := []byte(tpmWithPaymentAdvice)
	if got := failure(429, body); got != failRate {
		t.Fatalf("failure %s, want %s", got, failRate)
	}
	if !busy(429, body) {
		t.Error("busy false: patience won't wait for a TPM limit with billing advice")
	}
	if _, ok := passing(429, http.Header{}, body, 0); !ok {
		t.Error("passing false: the same one isn't asked again")
	}
	for _, b := range []string{
		`{"error":{"code":"credit_balance_exhausted","message":"Credits exhausted. Requested 40000 above your TPM limit."}}`,
		`{"error":{"message":"Insufficient balance. Please try again in 30 seconds"}}`,
		`{"error":{"message":"Rate limit reached. Please add a payment method to continue."}}`,
		`{"error":{"message":"Too many requests: your account is suspended for billing."}}`,
	} {
		if busy(429, []byte(b)) {
			t.Errorf("%s: busy", b)
		}
		if _, ok := passing(429, http.Header{}, []byte(b), 0); ok {
			t.Errorf("%s: passing", b)
		}
	}
}

// The reporter's case through the gateway: one provider whose vendor
// answers the TPM 429 and then the answer gets the answer, not the 429.
// The wait it names is shortened to keep the test quick.
func TestTPMWithBillingAdviceAskedAgain(t *testing.T) {
	said := strings.Replace(tpmWithPaymentAdvice, "8.259s", "0.5s", 1)
	u := &busyUpstream{replies: []busyReply{
		{code: 429, body: said},
		{code: 200, body: anthropicAnswer},
	}}
	s := busyProvider(t, u)
	scaled(t, 5*time.Second)
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	res, err := http.Post(gw.URL+"/v1/messages", "application/json", strings.NewReader(singleAsk(true)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(b), "message_stop") || u.asked() != 2 {
		t.Fatalf("agent got %d %q, upstream asked %d times; want the answer on the second ask; %+v", res.StatusCode, b, u.asked(), lastRoute(s).Tries)
	}
	if r := lastRoute(s); len(r.Tries) != 2 || r.Tries[0].Fail != failRate {
		t.Errorf("tries %+v: want the first a rate limit, asked again", r.Tries)
	}
}
