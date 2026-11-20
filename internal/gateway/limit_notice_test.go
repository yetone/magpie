package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// restartNotices is a magpie started again: the notices in memory are
// gone, and the file is read as ListenAndServe reads it.
func restartNotices() {
	limitNotices.Lock()
	limitNotices.m = map[string]LimitNotice{}
	limitNotices.Unlock()
	noticesSaving.Lock()
	noticesSaving.path, noticesSaving.last, noticesSaving.loaded = "", "", nil
	noticesSaving.Unlock()
	loadNotices()
}

// limitedAccount is a subscription account as a candidate, its windows
// unread.
func limitedAccount(t *testing.T) candidate {
	t.Helper()
	fresh(t)
	stale := staleAllowance
	staleAllowance = func(string, string) {}
	t.Cleanup(func() { staleAllowance = stale })
	p := provider.Provider{ID: "workbuddy-ai", Account: &provider.Account{Agent: "workbuddy-ai", User: "Me@Example.com"}}
	return candidate{p: p, model: "glm-5.3", rest: "workbuddy-ai#me@example.com"}
}

func noticeOf(t *testing.T, key string) (LimitNotice, bool) {
	t.Helper()
	ns := LimitNoticesUnder(key)
	for _, n := range ns {
		if n.Key == key {
			return n, true
		}
	}
	return LimitNotice{}, false
}

// @eachg0 on X: an account rate limited showed its red line a while, not
// as long as the limit: the vendor said "try again in 6 hours", the
// gateway trusts a Retry-After an hour at most, and the line went with
// the rest; a restart lost it too. The limit is now shown till the time
// the vendor named, past the rest and over a restart, and then goes.
func TestLimitNoticeOutlivesItsRest(t *testing.T) {
	c := limitedAccount(t)
	s := New()
	id := c.restKey()
	said := []byte(`{"error":{"message":"Rate limit reached, please try again in 6 hours"}}`)
	r := s.restAfter(c, http.StatusTooManyRequests, http.Header{"Retry-After": {"21600"}}, said)
	if r.Why != failRate || r.Key != id {
		t.Fatalf("rested %s by %q, want a rate limit by %q", r.Why, r.Key, id)
	}
	if left := time.Until(r.Until); left > longestWait+time.Minute {
		t.Fatalf("the rest is %v; this test is about a rest shorter than the limit", left)
	}
	n, ok := noticeOf(t, id)
	if !ok {
		t.Fatal("no notice of the rate limit")
	}
	if left := time.Until(n.Until); left < 6*time.Hour-time.Minute || left > 6*time.Hour+time.Minute {
		t.Fatalf("the notice ends in %v, want the 6 hours the vendor said", left.Round(time.Second))
	}
	if n.Why != failRate || n.Status != http.StatusTooManyRequests || n.Error == "" {
		t.Fatalf("the notice is %+v, want the 429 with its words", n)
	}

	// its rest over, the limit still is
	expire(id)
	if rs := RestsUnder("workbuddy-ai@"); len(rs) != 0 {
		t.Fatalf("still resting: %+v", rs)
	}
	if _, ok := noticeOf(t, id); !ok {
		t.Fatal("the notice went with the rest")
	}
	// and a restart keeps it
	restartNotices()
	got, ok := noticeOf(t, id)
	if !ok || !got.Until.Equal(n.Until) || got.Error != n.Error {
		t.Fatalf("after a restart: %+v (%v), want %+v", got, ok, n)
	}
	// till the time the vendor named
	limitNotices.Lock()
	got.Until = time.Now().Add(-time.Second)
	limitNotices.m[id] = got
	limitNotices.Unlock()
	if _, ok := noticeOf(t, id); ok {
		t.Fatal("a notice past its time is still shown")
	}
	saveNotices()
	restartNotices()
	if _, ok := noticeOf(t, id); ok {
		t.Fatal("a notice past its time came back after a restart")
	}
}

// A limit that names no time is shown till the account answers again,
// not for the minute it rests; an answer lets it go, over a restart too.
func TestLimitNoticeWithNoTimeLastsTillAnAnswer(t *testing.T) {
	c := limitedAccount(t)
	s := New()
	id := c.restKey()
	s.restAfter(c, http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"message":"Too many requests"}}`))
	n, ok := noticeOf(t, id)
	if !ok || !n.Until.IsZero() {
		t.Fatalf("notice %+v (%v), want one with no end", n, ok)
	}
	expire(id)
	restartNotices()
	if _, ok := noticeOf(t, id); !ok {
		t.Fatal("a limit with no time went with its rest, or a restart")
	}
	// refused again naming a time: that time; again naming none: kept
	s.restAfter(c, http.StatusTooManyRequests, http.Header{"Retry-After": {"7200"}}, []byte(`{"error":{"message":"slow down"}}`))
	expire(id)
	s.restAfter(c, http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"message":"slow down"}}`))
	if n, _ := noticeOf(t, id); time.Until(n.Until) < 2*time.Hour-time.Minute {
		t.Fatalf("a refusal naming no time dropped the 2 hours an earlier one named: %+v", n)
	}

	servedCandidate(c, 1)
	if _, ok := noticeOf(t, id); ok {
		t.Fatal("an answer left the notice")
	}
	restartNotices()
	if _, ok := noticeOf(t, id); ok {
		t.Fatal("an answered account's notice came back after a restart")
	}
}

// Try now lets a notice go with the rest, over a restart too; a failure
// that is no limit leaves none; a model's notice is the model's, and an
// answer of the account's other model leaves it.
func TestLimitNoticeLiftedAndScoped(t *testing.T) {
	c := limitedAccount(t)
	s := New()
	id := c.restKey()
	s.restAfter(c, http.StatusTooManyRequests, http.Header{"Retry-After": {"21600"}}, []byte(`{"error":{"message":"rate limited"}}`))
	s.Unrest(id)
	restartNotices()
	if _, ok := noticeOf(t, id); ok {
		t.Fatal("Try now left the notice, or a restart brought it back")
	}

	s.restAfter(c, http.StatusInternalServerError, http.Header{}, []byte(`{"error":{"message":"boom"}}`))
	if ns := LimitNoticesUnder("workbuddy-ai@"); len(ns) != 0 {
		t.Fatalf("a plain 500 left a notice: %+v", ns)
	}
	clearRest(id)

	// a model's own quota (WorkBuddy's "您也可以切换其他模型继续使用")
	at := time.Now().Add(3 * time.Hour).In(time.FixedZone("", 8*3600)).Format("2006-01-02 15:04:05")
	said := []byte(`{"error":{"message":"您的使用量已超出频率限制，将在 ` + at + ` UTC+8 重置，您也可以切换其他模型继续使用。"}}`)
	r := s.restAfter(c, http.StatusTooManyRequests, http.Header{}, said)
	if r.Key != c.restID() {
		t.Fatalf("rested %q, want the model's %q", r.Key, c.restID())
	}
	if n, _ := noticeOf(t, c.restID()); time.Until(n.Until) < 3*time.Hour-2*time.Minute {
		t.Fatalf("the model's notice is %+v, want it till the time named", n)
	}
	other := c
	other.model = "kimi-k3"
	servedCandidate(other, 1)
	if _, ok := noticeOf(t, c.restID()); !ok {
		t.Fatal("another model's answer let the model's notice go")
	}
	servedCandidate(c, 1)
	if _, ok := noticeOf(t, c.restID()); ok {
		t.Fatal("the model's own answer left its notice")
	}
}
