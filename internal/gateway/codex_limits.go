package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
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
	if ls := codexHeaderLimits(res.Header, now); len(ls) > 0 {
		noteCodexLimits(agent, user, ls)
	}
	if res.Body != nil && res.StatusCode < 300 {
		res.Body = &codexLimitsTap{ReadCloser: res.Body, note: func(ls []provider.CodexLimit) {
			noteCodexLimits(agent, user, ls)
		}}
	}
}

// codexHeaderLimits are the windows the x-codex-primary-* and
// x-codex-secondary-* headers tell; one that doesn't say how long it runs
// is left out, as no window read can be told to be it.
func codexHeaderLimits(h http.Header, now time.Time) []provider.CodexLimit {
	var out []provider.CodexLimit
	for _, which := range []string{"primary", "secondary"} {
		pre := "x-codex-" + which + "-"
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
			l.Resets = now.Add(time.Duration(after) * time.Second)
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
			l.Resets = now.Add(time.Duration(w.ResetAfter) * time.Second)
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
	buf  []byte
	done bool
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
		if !ok || !bytes.Contains(data, []byte(`"codex.rate_limits"`)) {
			continue
		}
		if ls := codexEventLimits(bytes.TrimSpace(data), time.Now()); len(ls) > 0 {
			t.note(ls)
		}
		t.done, t.buf = true, nil
		return
	}
	if len(t.buf) > codexLimitsScan {
		t.done, t.buf = true, nil
	}
}
