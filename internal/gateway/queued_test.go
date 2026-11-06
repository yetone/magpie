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

// askQueuedGroup sends one request to a provider of keys accounts, in order,
// whose upstream answers every try with code, Retry-After and body, and
// says how many times the upstream was asked and what the agent was told.
func askQueuedGroup(t *testing.T, keys, code int, body string) (asked, told int) {
	t.Helper()
	u := &busyUpstream{replies: []busyReply{{code: code, retryAfter: "0.04", body: body}}}
	fresh(t)
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "a", Name: "A", Key: "k0", Models: []string{"m"}, Chat: up.URL + "/v1", Routing: "order", Patience: provider.PatienceDefault}
	for i := 1; i < keys; i++ {
		p.Keys = append(p.Keys, provider.KeyAccount{Key: string(rune('0' + i))})
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	scaled(t, 3*time.Second)
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(singleAsk(false)))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return u.asked(), res.StatusCode
}

// The Qoder plugin (0.2.40, #4049) answers a request Qoder queued at once
// with a 503 + Retry-After that names the queue, when magpie has another
// candidate to ask. Asked again as a group's patience asks, every member
// only meets the queue again, the body sent again on every account, one
// whole round after another (#5346: 217 accounts, tries up to 656 for one
// request, 20 GB of re-uploaded bodies a day). Each member is asked once,
// and the last one left only gets its same-one retries.
func TestQueuedGroupAsksEachMemberOnce(t *testing.T) {
	// the plugin's errorResponse, with the words it says for code 10605
	// (packages/qoder/index.mjs), isQueued true and the service out
	for name, body := range map[string]string{
		"queued": `{"error":{"message":"Qoder queued the request for efficient (this plan's queue: p3, 3 ahead); try again in 30s","type":"qoder_error","code":503}}`,
		"busy":   `{"error":{"message":"Qoder is busy for efficient (this plan's queue: p3); try again in 30s","type":"qoder_error","code":503}}`,
	} {
		t.Run(name, func(t *testing.T) {
			const keys = 3
			n, told := askQueuedGroup(t, keys, 503, body)
			if told != 503 {
				t.Fatalf("the agent was told %d, want the 503", told)
			}
			if n > keys+lastRetries {
				t.Fatalf("a queued group asked %d times, want %d at most (each key once, the last one's same-one retries)", n, keys+lastRetries)
			}
		})
	}
}

// A 5xx that is not the plugin's queue answer is still one a group's
// patience waits out, the members asked again in turn (#1418), a relay's
// own words for its queue included: the plugin's words, not the 503 nor
// the word "queue", are what end the rounds.
func TestOverloadedGroupStillReplans(t *testing.T) {
	for name, body := range map[string]string{
		"overloaded": `{"error":{"message":"upstream overloaded, please try again","type":"api_error"}}`,
		"queue full": `{"error":{"message":"Request queue is full, please retry later","type":"server_error"}}`,
		"排队":         `{"error":{"message":"当前排队人数较多，请稍后重试","type":"new_api_error"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			const keys = 3
			if n, _ := askQueuedGroup(t, keys, 503, body); n <= keys+lastRetries {
				t.Fatalf("an overloaded group asked %d times, want more than %d: its patience asks the members again", n, keys+lastRetries)
			}
		})
	}
}
