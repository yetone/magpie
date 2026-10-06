package gateway

import "testing"

// sub2api's refusals, as v0.1.149 sends them on the OpenAI and Responses
// paths: a key out of the 5-hour, day or 7-day limit its owner gave it is
// out of quota till that window starts again, though it says
// rate_limit_exceeded; the relay out of accounts for everyone, or its
// upstream rate limiting, is not the key's quota.
func TestSub2APIKeyLimitIsQuota(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{429, `{"error":{"message":"api key 7天限额已用完","type":"rate_limit_exceeded"}}`, failQuota},
		{429, `{"error":{"message":"api key 日限额已用完","type":"rate_limit_exceeded"}}`, failQuota},
		{429, `{"error":{"message":"api key 5小时限额已用完","type":"rate_limit_exceeded"}}`, failQuota},
		// the key's own total quota (its middleware's words)
		{429, `{"code":"API_KEY_QUOTA_EXHAUSTED","message":"API key 额度已用完"}`, failQuota},
		// the pool: v0.1.149's 503, later versions' 429
		{503, `{"error":{"message":"No available accounts","type":"api_error"}}`, failOther},
		{429, `{"error":{"message":"All available accounts are currently rate-limited. Please retry later.","type":"rate_limit_error"}}`, failRate},
		{429, `{"error":{"message":"Upstream rate limit exceeded, please retry later","type":"rate_limit_error"}}`, failRate},
		// the wallet
		{403, `{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`, failCredit},
	} {
		if got := failure(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%d %s: %s, want %s", c.status, c.body, got, c.want)
		}
	}
}
