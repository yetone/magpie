package gateway

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Out-of-money refusals as vendors send them. OpenAI's: its legacy body
// (type and code insufficient_quota, the message every client has seen),
// and its billing codes (developers.openai.com/api/docs/guides/error-codes:
// credit_balance_exhausted, organization_/project_spend_limit_exceeded,
// organization_usage_limit_exceeded, each a 429 whose type is still
// insufficient_quota; the page gives the cause, not the message, so the
// message here is the cause it gives). The others are the bodies magpie's
// tests already hold from their reporters.
const (
	openAIQuotaLegacy = `{"error":{"message":"You exceeded your current quota, please check your plan and billing details. For more information on this error, read the docs: https://platform.openai.com/docs/guides/error-codes/api-errors.","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`
	openAICreditsGone = `{"error":{"message":"Your organization has no prepaid credits remaining.","type":"insufficient_quota","param":null,"code":"credit_balance_exhausted"}}`
	openAISpendLimit  = `{"error":{"message":"Your project reached its enforced spend limit.","type":"insufficient_quota","param":null,"code":"project_spend_limit_exceeded"}}`
	deepSeekNoBalance = `{"error":{"message":"Insufficient Balance","type":"unknown_error","param":null,"code":"invalid_request_error"}}`
	anthropicTooLow   = `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`
	// OpenRouter: the request's size, not the balance spent
	openRouterAfford = `{"error":{"message":"This request requires more credits, or fewer max_tokens. You requested up to 65536 tokens, but can only afford 12000. To increase, visit https://openrouter.ai/settings/credits and add more credits","code":402,"metadata":{"provider_name":null}}}`
	// OpenRouter: its in-flight budget, a 402 sent with Retry-After
	openRouterInFlight = `{"error":{"code":402,"message":"Insufficient credits","metadata":{"limit_source":"openrouter_in_flight_budget"}}}`
)

// expire ends id's rest as if its time had passed, its note kept.
func expire(id string) {
	restingUntil.Lock()
	restingUntil.m[id] = time.Now().Add(-time.Second)
	restingUntil.Unlock()
}

// PaulT on Discord: an OpenAI key out of credit answered 402 and was
// asked again, refused the same, every 30 minutes. A key its vendor says
// is out of money now rests longer each time it is refused so again,
// 30 minutes to a week; "Try now" and an answer start it over.
func TestKeyOutOfCreditRestsLongerEachTime(t *testing.T) {
	near := func(d, want time.Duration) bool { return d > want-time.Minute && d <= want }
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"bare 402", http.StatusPaymentRequired, ""},
		{"openai legacy", http.StatusTooManyRequests, openAIQuotaLegacy},
		{"openai credits", http.StatusTooManyRequests, openAICreditsGone},
		{"openai spend limit", http.StatusTooManyRequests, openAISpendLimit},
		{"openai-shaped 402", http.StatusPaymentRequired, openAICreditsGone},
		{"deepseek", http.StatusPaymentRequired, deepSeekNoBalance},
		{"anthropic", http.StatusBadRequest, anthropicTooLow},
		{"zhipu", http.StatusTooManyRequests, zhipuNoBalance},
		{"workbuddy", http.StatusTooManyRequests, wbExhausted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			s := New()
			key := candidate{p: provider.Provider{ID: "openai", Key: "sk-x"}, model: "gpt-x", rest: "openai#k"}
			id := key.restKey()
			refuse := func() Rest { return s.restAfter(key, tc.status, http.Header{}, []byte(tc.body)) }
			for i, want := range creditLadder {
				r := refuse()
				if r.Why != failCredit || r.By != "credit" || r.Failures != i+1 || !near(time.Until(r.Until), want) {
					t.Fatalf("refusal %d: rests %v (%s, by %s, failures %d), want %v", i+1, time.Until(r.Until).Round(time.Second), r.Why, r.By, r.Failures, want)
				}
				// refused again while it rests (the lone key, asked all the
				// same): the same rung
				if r := refuse(); r.Failures != i+1 || !near(time.Until(r.Until), want) {
					t.Fatalf("refusal %d again while resting: %v, failures %d", i+1, time.Until(r.Until).Round(time.Second), r.Failures)
				}
				expire(id)
			}
			// the top rung holds
			if r := refuse(); !near(time.Until(r.Until), creditLadder[len(creditLadder)-1]) {
				t.Fatalf("past the ladder: %v", time.Until(r.Until).Round(time.Second))
			}
			// "Try now": lifted, and refused again it starts over
			if !s.Unrest(id) {
				t.Fatal("Try now lifted nothing")
			}
			if _, ok := restOf(id); ok {
				t.Fatal("still resting after Try now")
			}
			if r := refuse(); r.Failures != 1 || !near(time.Until(r.Until), creditRest) {
				t.Fatalf("after Try now: %v, failures %d", time.Until(r.Until).Round(time.Second), r.Failures)
			}
			expire(id)
			refuse() // 2h
			// an answer clears the count
			servedCandidate(key, 1)
			if r := refuse(); r.Failures != 1 || !near(time.Until(r.Until), creditRest) {
				t.Fatalf("after an answer: %v, failures %d", time.Until(r.Until).Round(time.Second), r.Failures)
			}
		})
	}
}

// A 402 that can pass soon, or for a smaller request, rests creditRest
// each time, as before; one that names when it is back rests till then.
func TestKeyCreditSoonDoesNotClimb(t *testing.T) {
	near := func(d, want time.Duration) bool { return d > want-time.Minute && d <= want }
	for _, tc := range []struct {
		name   string
		status int
		header http.Header
		body   string
	}{
		{"openrouter afford", http.StatusPaymentRequired, http.Header{}, openRouterAfford},
		{"openrouter in-flight", http.StatusPaymentRequired, http.Header{"Retry-After": {"20"}}, openRouterInFlight},
		{"try again in seconds", http.StatusTooManyRequests, http.Header{}, `{"error":{"message":"Insufficient balance. Please try again in 30 seconds"}}`},
		{"一分钟后重试", http.StatusTooManyRequests, http.Header{}, `{"error":{"message":"余额不足，请充值后 1 分钟后重试"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			s := New()
			key := candidate{p: provider.Provider{ID: "or", Key: "k"}, model: "m", rest: "or#k"}
			for i := range 4 {
				r := s.restAfter(key, tc.status, tc.header, []byte(tc.body))
				// creditRest, or the shorter time it names (resetsAt
				// reads "try again in 30 seconds"): never a rung
				if r.Why != failCredit || r.Failures != 0 || time.Until(r.Until) > creditRest {
					t.Fatalf("refusal %d: %v (%s, by %s, failures %d), want %v at most, flat", i+1, time.Until(r.Until).Round(time.Second), r.Why, r.By, r.Failures, creditRest)
				}
				expire(key.restKey())
			}
		})
	}

	// a spend limit that says when it resets is out till then
	fresh(t)
	s := New()
	key := candidate{p: provider.Provider{ID: "k", Key: "k"}, model: "m", rest: "k#k"}
	at := time.Now().Add(10 * 24 * time.Hour).Unix()
	body := `{"error":{"type":"insufficient_quota","code":"project_spend_limit_exceeded","message":"x","resets_at":` + strconv.FormatInt(at, 10) + `}}`
	if r := s.restAfter(key, http.StatusTooManyRequests, http.Header{}, []byte(body)); r.By != "resets" || !near(time.Until(r.Until), 10*24*time.Hour) {
		t.Fatalf("named reset: %v by %s", time.Until(r.Until).Round(time.Second), r.By)
	}
}

// What doesn't climb: a subscription account other than ChatGPT's, and
// another computer's magpie, whose model rests for its own provider there.
func TestCreditLadderIsKeysOnly(t *testing.T) {
	near := func(d, want time.Duration) bool { return d > want-time.Minute && d <= want }
	fresh(t)
	s := New()
	other := candidate{p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "me@example.com"}}, model: "claude-x", rest: "claude@me@example.com"}
	for range 3 {
		if r := s.restAfter(other, http.StatusPaymentRequired, http.Header{}, nil); r.Failures != 0 || !near(time.Until(r.Until), creditRest) {
			t.Fatalf("account: %v failures %d", time.Until(r.Until).Round(time.Second), r.Failures)
		}
		expire(other.restKey())
	}
	h := http.Header{answeredThereHeader: {"openai"}}
	remote := candidate{p: provider.Provider{ID: "lan", Key: "k"}, model: "m", rest: "lan#k"}
	for range 3 {
		r := s.restAfter(remote, http.StatusPaymentRequired, h, []byte(deepSeekNoBalance))
		if r.Failures != 0 || !near(time.Until(r.Until), creditRest) {
			t.Fatalf("remote magpie: %v failures %d", time.Until(r.Until).Round(time.Second), r.Failures)
		}
		if !strings.Contains(r.Key, "/") {
			t.Fatalf("remote magpie rests %q, not its model", r.Key)
		}
		expire(r.Key)
	}
}

// avenger on X: Claude Code through an Anthropic API key was refused
// "Credit balance too low", and the key's row said only that. A key at
// Anthropic's own API rests with Anthropic's words and what they mean
// (provider.NoCreditHint); a relay's key passing the same words on rests
// with its words alone.
func TestAnthropicKeyNoCreditRestSaysWhere(t *testing.T) {
	for _, tc := range []struct {
		name string
		base string
		hint bool
	}{
		{"anthropic", "https://api.anthropic.com", true},
		{"relay", "https://relay.example.com", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			s := New()
			key := candidate{p: provider.Provider{ID: tc.name, Anthropic: tc.base, Key: "sk-ant-api03-x"}, model: "claude-sonnet-4-5", rest: tc.name + "#k"}
			r := s.restAfter(key, http.StatusBadRequest, http.Header{}, []byte(anthropicTooLow))
			if r.Why != failCredit {
				t.Fatalf("rests %s, want credit", r.Why)
			}
			if !strings.HasPrefix(r.Error, "Your credit balance is too low to access the Anthropic API") {
				t.Fatalf("Anthropic's words not first: %q", r.Error)
			}
			if got := strings.HasSuffix(r.Error, " — "+provider.AnthropicNoCreditHint); got != tc.hint {
				t.Fatalf("hint %v, want %v: %q", got, tc.hint, r.Error)
			}
		})
	}
}
