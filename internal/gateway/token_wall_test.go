package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// plugins#512 (cycocyco): SenseTime Raccoon's 429 for an account's token
// throughput used up, word for word. It has no Retry-After.
const raccoonWall = `{"error":{"code":"user_model_rate_limited","message":"您的模型请求频率超限，请稍后重试","type":"rate_limit_error"}}`

// ZCode's account-wide 429 (#5406, erlinerd): the vendor's own error
// carries the code where it puts it, and the plugin names the same code
// in the message it shows, word for word from the two log shapes —
// `[1302][<the vendor's words>][<its request id>]`. Rested and retried,
// the 429 came back: no moment clears it, so it is a wall.
const zcodeWallCode = `{"error":{"code":"1302","message":"[1302][您的账户已达到速率限制，请您控制请求频率][202611051732017a8c6e62fa7d42f5]"}}`
const zcodeWallMessage = `{"error":{"message":"[1302][您的账户已达到速率限制，请您控制请求频率][20261105173006e812537059cd4a83]"}}`

// ZCode's wall is not asked again in place, by the same-one retries or a
// patience's rounds: the loop the reporter saw ("magpie keeps asking, so
// the 429 comes back in a loop") is these two. A quoted `[1302]` without
// the label's shape, and a code of ZCode's that nothing pins the semantics
// of yet, are a rate limit that passes, as before.
func TestZCodeWallIsNotRetriedInPlace(t *testing.T) {
	for _, body := range []string{zcodeWallCode, zcodeWallMessage} {
		if got := failure(429, []byte(body)); got != failRate {
			t.Fatalf("failure: %s, want %s", got, failRate)
		}
		if _, ok := passing(429, nil, []byte(body), 0); ok {
			t.Errorf("the wall passes: asked again in place: %s", body)
		}
		if busy(429, []byte(body)) {
			t.Errorf("the wall is busy: asked again in a patience's rounds: %s", body)
		}
	}
	for _, body := range []string{
		// the label's words quoted in a prompt's echo are no wall
		`{"error":{"message":"[1302] is the code my request got, why?"}}`,
		// the label's shape with a code of another meaning
		`{"error":{"message":"[1302][请求成功][20261105173006e812537059cd4a83]"}}`,
		// Zhipu's own 1302, a concurrency or minute limit with no
		// ZCode label: a moment clears it, as before
		`{"error":{"code":"1302","message":"您的账户已达到速率限制，请您控制请求频率"}}`,
		`{"error":{"code":"1302","message":"您当前使用该API的并发数过高，请降低并发，或联系客服增加限额。"}}`,
		// a neighbour code nothing pins the semantics of yet
		`{"error":{"code":"1303","message":"[1303][请求过多][20261105173006e812537059cd4a83]"}}`,
	} {
		if _, ok := passing(429, nil, []byte(body), 0); !ok {
			t.Errorf("%s: doesn't pass", body)
		}
		if !busy(429, []byte(body)) {
			t.Errorf("%s: not busy", body)
		}
	}
}

// The wall is a rate limit, rested as one, but no moment clears it: it is
// not asked again in place, by the same-one retries or a patience's
// rounds. A rate limit without its code still is.
func TestTokenWallIsNotRetriedInPlace(t *testing.T) {
	if got := failure(429, []byte(raccoonWall)); got != failRate {
		t.Fatalf("failure: %s, want %s", got, failRate)
	}
	if _, ok := passing(429, nil, []byte(raccoonWall), 0); ok {
		t.Error("the wall passes: asked again in place")
	}
	if busy(429, []byte(raccoonWall)) {
		t.Error("the wall is busy: asked again in a patience's rounds")
	}
	// the same words with another code, or the code in a message, are a
	// rate limit that passes, as before
	for _, body := range []string{
		`{"error":{"code":"rate_limited","message":"您的模型请求频率超限，请稍后重试","type":"rate_limit_error"}}`,
		`{"error":{"message":"Rate limit exceeded (user_model_rate_limited), please try again later"}}`,
		`{"error":{"code":429,"message":"Rate limit exceeded"}}`,
	} {
		if _, ok := passing(429, nil, []byte(body), 0); !ok {
			t.Errorf("%s: doesn't pass", body)
		}
		if !busy(429, []byte(body)) {
			t.Errorf("%s: not busy", body)
		}
	}
}

// The reporter's case: the lone account's wall reaches the agent after one
// ask, not eight in 38s; in a group the next member answers, and the
// walled one rests, so the next request isn't sent to it at all.
func TestTokenWallFailsFast(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{429, "", raccoonWall}}}
	scriptedOn(t, "a", provider.Chat, a)
	start := time.Now()
	code, body := postAs(t, New(), "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 429 || a.n != 1 || !strings.Contains(body, "user_model_rate_limited") && !strings.Contains(body, "频率超限") {
		t.Fatalf("%d after %d tries: %s", code, a.n, body)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the agent had it after %v", d)
	}

	fresh(t)
	a = &scripted{replies: []reply{{429, "", raccoonWall}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for i := range 2 {
		code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != i+1 {
			t.Fatalf("request %d: %d %s (a %d, b %d)", i+1, code, body, a.n, b.n)
		}
	}
}
