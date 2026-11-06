package gateway

import "testing"

// A 429 that is a per-minute rate limit rests a minute, not the quarter
// hour of a used-up plan — however magpie or the vendor words it (#153).
func TestRateLimitIsNotQuota(t *testing.T) {
	for body, want := range map[string]string{
		// as magpie words an upstream 429 for the agent
		`{"error":{"code":null,"message":"ORF: Rate limit exceeded: free-models-per-min","param":null,"type":"rate_limit_error"}}`:                                    failRate,
		`{"error":{"message":"Rate limit reached for gpt-4o in organization org-x on tokens per min (TPM): Limit 30000, Used 29000"}}`:                                failRate,
		`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your organization's rate limit of 50,000 input tokens per minute"}}`: failRate,
		`{"error":{"message":"Too Many Requests"}}`: failRate,
		// still a used-up plan
		`{"error":{"message":"Rate limit exceeded: free-models-per-day. Add 10 credits to unlock 1000 free model requests per day"}}`: failQuota,
		`{"type":"error","error":{"type":"rate_limit_error","message":"Claude AI usage limit reached|1790000000"}}`:                   failQuota,
		`{"error":{"type":"usage_limit_reached","message":"You've hit your usage limit."}}`:                                           failQuota,
		`{"error":{"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`:                       failQuota,
		`{"error":{"message":"You exceeded your current quota","type":"insufficient_quota"}}`:                                         failCredit,
		// Zhipu's pay-as-you-go endpoint answering a Coding Plan's key
		// (kkgg on Discord: shown as "429 · rate limited")
		`{"error":{"code":"1113","message":"余额不足或无可用资源包,请充值。"}}`:                                              failCredit,
		`{"error":{"message":"Zhipu GLM: 余额不足或无可用资源包,请充值。","type":"rate_limit_error"}}`:                       failCredit,
		`{"error":{"code":"1113","message":"Insufficient balance or no resource package. Please recharge."}}`: failCredit,
		// SenseNova's token-plan gateway answers its throttle in OpenAI's
		// shape, the code insufficient_quota beside the type
		// rate_limit_error and a per-minute message, over a pool half up
		// (hzyqn: the same text read credit from the body and rate from
		// the words magpie gave the agent, #5006)
		`{"error":{"code":"insufficient_quota","message":"inference exceeds tpm/rpm limit","type":"rate_limit_error"}}`: failRate,
		`SenseNova: inference exceeds tpm/rpm limit`: failRate,
	} {
		if got := failure(429, []byte(body)); got != want {
			t.Errorf("%s: %s, want %s", body, got, want)
		}
	}
	// not a 429: words alone don't make it a rate limit
	if got := failure(403, []byte(`{"error":{"message":"rate limit exceeded for plan"}}`)); got != failQuota {
		t.Errorf("403: %s, want %s", got, failQuota)
	}
}
