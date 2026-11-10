package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// A compaction that keeps failing stops being asked for a while (#2274).
//
// Codex with context_management's experimental mode compacts in the
// background (async_compaction.rs): past a share of the window it starts a
// compaction at every step of a turn whose history has changed since the
// last one it started, and drops one that fails without a word. So a
// conversation over the line whose summaries never come — Antigravity
// cutting each reply short, then its 429s — asked magpie for a summary
// at every step: 1169 compactions in 6 hours, each paying for the whole
// conversation as input, none of them installed, the conversation never
// shorter. A foreground one is asked again twice more by Codex itself on
// a 5xx or a 429.
//
// After compactFailsMost failures in a row of one conversation's
// compaction on one model, each within compactHeldFor of the one before,
// the next is answered at once with a 400 saying so, which Codex doesn't
// ask again, and no model is asked, until compactHeldFor has passed since
// the last failure; then one is asked again. A summary that comes clears
// the count. A conversation is Codex's thread, its prompt_cache_key; a
// request without one isn't counted. Sign-in failures (401, 403) aren't
// counted either: nothing was paid for them, and once the user signs in
// again the next compaction should go.

const (
	compactFailsMost = 3
	compactHeldFor   = 5 * time.Minute
)

type compactFails struct {
	mu sync.Mutex
	m  map[string]compactFail
}

type compactFail struct {
	n    int
	last time.Time
	why  string
}

// compactKey is the conversation and model a summary request is of, "" for
// one that doesn't say its conversation.
func compactKey(body []byte) string {
	var q struct {
		Model string `json:"model"`
		Key   string `json:"prompt_cache_key"`
	}
	if json.Unmarshal(body, &q) != nil || q.Key == "" {
		return ""
	}
	return q.Model + "\x00" + q.Key
}

// held says the conversation's compaction has failed compactFailsMost
// times in a row lately, and what to tell Codex instead of asking again.
func (f *compactFails) held(key, model string) (string, bool) {
	if key == "" {
		return "", false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.m[key]
	if !ok || c.n < compactFailsMost || time.Since(c.last) >= compactHeldFor {
		return "", false
	}
	left := (compactHeldFor - time.Since(c.last)).Round(time.Second)
	return fmt.Sprintf("compaction: %s failed to summarise this conversation %d times in a row (last: %s); magpie doesn't ask it again for %s, so a compaction that can't succeed isn't paid for over and over. Pick another model for this conversation, or a group whose compaction rule sends it to one that answers",
		model, c.n, c.why, left), true
}

// failed counts a failed summary of the conversation, unless it was a
// sign-in's (nothing paid, and fixed by signing in).
func (f *compactFails) failed(key string, status int, why string) {
	if key == "" || status == http.StatusUnauthorized || status == http.StatusForbidden {
		return
	}
	if len(why) > 300 {
		why = why[:300] + "…"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]compactFail{}
	}
	c := f.m[key]
	if time.Since(c.last) >= compactHeldFor {
		c.n = 0 // the ones before are too long ago to say it keeps failing
	}
	c.n, c.last, c.why = c.n+1, time.Now(), why
	f.m[key] = c
	for k, v := range f.m {
		if time.Since(v.last) >= compactHeldFor {
			delete(f.m, k)
		}
	}
}

// done clears the conversation's count: its summary came.
func (f *compactFails) done(key string) {
	if key == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, key)
}

// wholeReplyKey marks a request whose reply is wanted only whole: a
// compaction's summary (codexCompact), whose stream serve holds to its end
// (holdWriter.wantWhole).
type wholeReplyKey struct{}

func wantsWhole(ctx context.Context) bool {
	v, _ := ctx.Value(wholeReplyKey{}).(bool)
	return v
}

// compactionAskKey marks a request that is magpie's own summary ask for a
// compaction (codexCompact). It is not Codex's turn: a provider that
// isn't OpenAI's is given its developer messages as the system ones they
// are (forVendor), while Codex's own turns keep the role the client sent.
type compactionAskKey struct{}

func asksCompaction(ctx context.Context) bool {
	v, _ := ctx.Value(compactionAskKey{}).(bool)
	return v
}

func askCompaction(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), compactionAskKey{}, true))
}

// askAsSent is a summary ask taken back to the roles the client sent,
// after the swap was refused: the ChatGPT backend behind a relay takes
// developer alone, and the summary went to it as system (forVendor,
// #5067). Its turns, developer as they came, are taken, and the summary
// goes the same way.
func askAsSent(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), compactionAskKey{}, false))
}

func askWhole(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), wholeReplyKey{}, true))
}
