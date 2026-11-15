package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// What the ChatGPT backend says of a Codex account as it answers one of
// magpie's turns is kept as the account's usage (provider.NoteCodexLimits),
// so its usage cap holds it from the next turn on rather than once
// /wham/usage is read again (#1295). It says it as Codex 0.160.1 reads it
// (codex-api's rate-limit parsing, RateLimitEvent): headers
// x-codex-primary-used-percent, -window-minutes and -reset-at (and
// secondary-), and a codex.rate_limits event ahead of the reply,
// {"rate_limits":{"primary":{"used_percent","window_minutes","reset_at"},
// "secondary":…}}, with metered_limit_name naming a limit other than the
// account's own.

// noteCodexLimits is provider.NoteCodexLimits, which tests watch.
var noteCodexLimits = provider.NoteCodexLimits

// codexLimitsScan is as far into a reply its codex.rate_limits event is
// looked for: it comes ahead of the reply.
const codexLimitsScan = 64 << 10

// heardCodexLimits keeps what res says of p's Codex account.
func heardCodexLimits(p provider.Provider, res *http.Response) {
	if p.Account == nil || p.Account.Agent != "codex" || res == nil {
		return
	}
	agent, user := p.Account.UsageAgent(), p.Account.User
	now := time.Now()
	noteCodexAnswered(agent, user, now)
	if ls := codexHeaderLimits(res.Header, now); len(ls) > 0 {
		noteCodexLimits(agent, user, ls)
	}
	heardCodexCredits(p, res.Header)
	if res.Body != nil && res.StatusCode < 300 {
		res.Body = &codexLimitsTap{ReadCloser: res.Body, note: func(ls []provider.CodexLimit) {
			noteCodexLimits(agent, user, ls)
		}, credits: func(on bool) {
			noteCodexCredits(agent, user, on)
		}}
	}
}

// noteCodexCredits is provider.NoteCodexCredits, which tests watch.
var noteCodexCredits = provider.NoteCodexCredits

// heardCodexCredits keeps what h, a reply to p's Codex account, says of
// its credits (codexHeaderCredits): ChatGPT says them on the 429 that
// turns a turn away as out of the allowance too, which is then asked
// again on them (creditsAgain) rather than the account benched.
func heardCodexCredits(p provider.Provider, h http.Header) {
	if p.Account == nil || p.Account.Agent != "codex" || p.Account.User == "" {
		return
	}
	if on, ok := codexHeaderCredits(h); ok {
		noteCodexCredits(p.Account.UsageAgent(), p.Account.User, on)
	}
}

// codexHeaderCredits is whether the account goes on past its allowance on
// credits, as the x-codex-credits-* headers tell it, and whether they do:
// as Codex 0.160.1 reads them (codex-api's parse_credits_snapshot), both
// has-credits and unlimited said ("true"/"false", "1"/"0"), the balance
// not needed. None when x-codex-active-limit names the image allowance,
// whose headers they then are (codexHeaderLimits).
func codexHeaderCredits(h http.Header) (on, ok bool) {
	if imageLimitName(h.Get("x-codex-active-limit")) {
		return false, false
	}
	has, ok1 := headerBool(h.Get("x-codex-credits-has-credits"))
	unlimited, ok2 := headerBool(h.Get("x-codex-credits-unlimited"))
	if !ok1 || !ok2 {
		return false, false
	}
	return provider.CodexGoesOnCredits(has, unlimited, h.Get("x-codex-credits-balance")), true
}

// headerBool reads a header's bool as Codex does (parse_header_bool).
func headerBool(v string) (bool, bool) {
	v = strings.TrimSpace(v)
	switch {
	case strings.EqualFold(v, "true") || v == "1":
		return true, true
	case strings.EqualFold(v, "false") || v == "0":
		return false, true
	}
	return false, false
}

// codexEventCredits is what a codex.rate_limits event's data tells of the
// account's credits ({"credits":{"has_credits","unlimited","balance"}}),
// as codexHeaderCredits; none for any other event, or one of a limit
// other than the account's own.
func codexEventCredits(data []byte) (on, ok bool) {
	var ev struct {
		Type    string `json:"type"`
		Metered string `json:"metered_limit_name"`
		Credits *struct {
			Has       *bool   `json:"has_credits"`
			Unlimited *bool   `json:"unlimited"`
			Balance   *string `json:"balance"`
		} `json:"credits"`
	}
	if json.Unmarshal(data, &ev) != nil || ev.Type != "codex.rate_limits" || ev.Credits == nil || ev.Credits.Has == nil || ev.Credits.Unlimited == nil {
		return false, false
	}
	if m := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(ev.Metered)), "-", "_"); m != "" && m != "codex" {
		return false, false
	}
	balance := ""
	if ev.Credits.Balance != nil {
		balance = *ev.Credits.Balance
	}
	return provider.CodexGoesOnCredits(*ev.Credits.Has, *ev.Credits.Unlimited, balance), true
}

// codexLearnAgain is how long a Codex account not known that answered is
// not put first again to be learned (learns): its reply told nothing of
// the week, or the reading still fails.
const codexLearnAgain = time.Hour

// codexAnswered is when each Codex account last answered, by
// agent/lower-cased user.
var codexAnswered = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func noteCodexAnswered(agent, user string, now time.Time) {
	codexAnswered.Lock()
	codexAnswered.m[agent+"/"+strings.ToLower(user)] = now
	codexAnswered.Unlock()
}

// codexAnsweredLately: user answered within codexLearnAgain of now.
func codexAnsweredLately(agent, user string, now time.Time) bool {
	codexAnswered.Lock()
	at, ok := codexAnswered.m[agent+"/"+strings.ToLower(user)]
	codexAnswered.Unlock()
	return ok && now.Sub(at) < codexLearnAgain
}

// codexHeaderLimits are the windows the x-codex-primary-* and
// x-codex-secondary-* headers tell; one that doesn't say how long it runs
// is left out, as no window read can be told to be it. None when
// x-codex-active-limit names the image allowance, whose window they then
// are (codexImageHeaderLimit).
func codexHeaderLimits(h http.Header, now time.Time) []provider.CodexLimit {
	if imageLimitName(h.Get("x-codex-active-limit")) {
		// the x-codex-* windows are the image allowance's (#6412)
		return nil
	}
	return headerLimitsOf(h, "x-codex-", now)
}

// headerLimitsOf are the windows of the header family fam (x-codex-,
// x-imagegen-premium-) tells: its primary- and secondary- ones.
func headerLimitsOf(h http.Header, fam string, now time.Time) []provider.CodexLimit {
	var out []provider.CodexLimit
	for _, which := range []string{"primary", "secondary"} {
		pre := fam + which + "-"
		used, err := strconv.ParseFloat(strings.TrimSpace(h.Get(pre+"used-percent")), 64)
		if err != nil {
			continue
		}
		mins, err := strconv.ParseInt(strings.TrimSpace(h.Get(pre+"window-minutes")), 10, 64)
		if err != nil || mins <= 0 {
			continue
		}
		l := provider.CodexLimit{Used: used, Span: time.Duration(mins) * time.Minute}
		if at, err := strconv.ParseInt(strings.TrimSpace(h.Get(pre+"reset-at")), 10, 64); err == nil && at > 0 {
			l.Resets = time.Unix(at, 0)
		} else if after, err := strconv.ParseInt(strings.TrimSpace(h.Get(pre+"reset-after-seconds")), 10, 64); err == nil && after > 0 {
			l.Resets = onWall(now.Add(time.Duration(after) * time.Second))
		}
		out = append(out, l)
	}
	return out
}

// codexEventLimits are the windows a codex.rate_limits event's data tells,
// nil for any other event, and for one of a limit other than the
// account's own (metered_limit_name: a model's own allowance).
func codexEventLimits(data []byte, now time.Time) []provider.CodexLimit {
	type window struct {
		Used       *float64 `json:"used_percent"`
		Minutes    int64    `json:"window_minutes"`
		ResetAt    int64    `json:"reset_at"`
		ResetAfter int64    `json:"reset_after_seconds"`
	}
	var ev struct {
		Type       string `json:"type"`
		Metered    string `json:"metered_limit_name"`
		RateLimits *struct {
			Primary   *window `json:"primary"`
			Secondary *window `json:"secondary"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(data, &ev) != nil || ev.Type != "codex.rate_limits" || ev.RateLimits == nil {
		return nil
	}
	if m := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(ev.Metered)), "-", "_"); m != "" && m != "codex" {
		return nil
	}
	var out []provider.CodexLimit
	for _, w := range []*window{ev.RateLimits.Primary, ev.RateLimits.Secondary} {
		if w == nil || w.Used == nil || w.Minutes <= 0 {
			continue
		}
		l := provider.CodexLimit{Used: *w.Used, Span: time.Duration(w.Minutes) * time.Minute}
		if w.ResetAt > 0 {
			l.Resets = time.Unix(w.ResetAt, 0)
		} else if w.ResetAfter > 0 {
			l.Resets = onWall(now.Add(time.Duration(w.ResetAfter) * time.Second))
		}
		out = append(out, l)
	}
	return out
}

// codexLimitsTap passes a reply on as it comes, looking in its first
// codexLimitsScan bytes for a codex.rate_limits event.
type codexLimitsTap struct {
	io.ReadCloser
	note func([]provider.CodexLimit)
	// credits: what the event said of the account's credits
	// (codexEventCredits), when it said
	credits func(on bool)
	buf     []byte
	done    bool
}

func (t *codexLimitsTap) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if n > 0 && !t.done {
		t.buf = append(t.buf, p[:n]...)
		t.look()
	}
	return n, err
}

// look reads the whole lines seen so far for the event.
func (t *codexLimitsTap) look() {
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(t.buf[:i])
		t.buf = t.buf[i+1:]
		data, ok := bytes.CutPrefix(line, []byte("data:"))
		if !ok || !bodyHas(data, `"codex.rate_limits"`) {
			continue
		}
		if ls := codexEventLimits(bytes.TrimSpace(data), time.Now()); len(ls) > 0 {
			t.note(ls)
		}
		if on, ok := codexEventCredits(bytes.TrimSpace(data)); ok && t.credits != nil {
			t.credits(on)
		}
		t.done, t.buf = true, nil
		return
	}
	if len(t.buf) > codexLimitsScan {
		t.done, t.buf = true, nil
	}
}
