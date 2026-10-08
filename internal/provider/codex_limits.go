package provider

import (
	"strings"
	"time"
)

// The ChatGPT backend tells what a Codex account has used as it answers:
// its x-codex-primary-* and x-codex-secondary-* headers, and a
// codex.rate_limits event ahead of the reply, which Codex itself reads for
// its /status. Read only from /wham/usage, once a minute, an account near
// its usage cap was sent turn after turn on a reading that said 96% while
// the account went to 100% (#1295): what each reply says is kept at once,
// so the cap holds the account from the next turn on.

// CodexLimit is one of an account's windows as a reply tells it: the
// share used (0–100), how long the window runs, and when it renews (zero
// when not said).
type CodexLimit struct {
	Used   float64
	Span   time.Duration
	Resets time.Time
}

// NoteCodexLimits keeps what a reply said of agent's account user (agent
// as its usage is read, Account.UsageAgent) in place of the windows of the
// same span last read, for routing (Allowances) and the Usage page alike.
// An account not read yet is left to its reading: a reply names only the
// windows it counts, which isn't the whole of the account.
func NoteCodexLimits(agent, user string, heard []CodexLimit) {
	if len(heard) == 0 || user == "" {
		return
	}
	key := agent + "/" + strings.ToLower(user)
	c := &loginUsageCache
	c.Lock()
	if e, ok := c.m[key]; ok {
		e.q.Windows = heardWindows(e.q.Windows, heard)
		if e.read.Error == "" {
			e.read.Windows = heardWindows(e.read.Windows, heard)
		}
		c.m[key] = e
	}
	c.Unlock()
	u := &usedCache
	u.Lock()
	defer u.Unlock()
	m := u.m[agent]
	for name, a := range m {
		if !strings.EqualFold(name, user) {
			continue
		}
		// new ones: what was handed out is read without the lock
		kept := make(map[string]Allowance, len(m))
		for k, v := range m {
			kept[k] = v
		}
		kept[name] = heardAllowance(a, heard)
		u.m[agent] = kept
		return
	}
}

// heardWindows is ws, copied, with each window that counts every model
// and runs as long as one heard taking what was heard.
func heardWindows(ws []QuotaWindow, heard []CodexLimit) []QuotaWindow {
	out := make([]QuotaWindow, len(ws))
	copy(out, ws)
	for i, w := range out {
		if w.Aside || w.Model != "" || w.Span == 0 {
			continue
		}
		for _, h := range heard {
			if h.Span != w.Span {
				continue
			}
			w.Used = h.Used
			if !h.Resets.IsZero() {
				t := h.Resets
				w.ResetsAt, w.ResetSecs = &t, 0
			}
			out[i] = w
		}
	}
	return out
}

// heardAllowance is heardWindows for an allowance as routing keeps it.
func heardAllowance(a Allowance, heard []CodexLimit) Allowance {
	out := make(Allowance, len(a))
	copy(out, a)
	for i, l := range out {
		if l.Model != "" || l.matches != nil || l.Span == 0 {
			continue
		}
		for _, h := range heard {
			if h.Span != l.Span {
				continue
			}
			l.Used = h.Used
			if !h.Resets.IsZero() {
				l.Resets = h.Resets
			}
			out[i] = l
		}
	}
	return out
}
