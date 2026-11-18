package provider

// The line Claude Code's status line shows (#1997, kensenzhao): the model a
// session's latest turn went to, then each subscription and plan with the
// windows magpie last read. `magpie statusline` and the gateway's
// GET /v1/magpie/statusline (for a Claude Code on another computer than
// the gateway, steven_4422 running magpie in Docker) both print it from
// here, so the two never differ.

import (
	"fmt"
	"strings"
	"time"
)

// StatuslineText is the whole line: "→ model" when the session has one,
// then the cards of qs that only (or the lately served) keeps.
func StatuslineText(model string, qs []Quota, only []string, now time.Time) string {
	var parts []string
	if model != "" {
		parts = append(parts, "→ "+model)
	}
	for _, q := range StatuslineCards(qs, only) {
		parts = append(parts, StatuslineCard(q, now))
	}
	return strings.Join(parts, " | ")
}

// StatuslineCards are the subscriptions and plans with windows to show:
// those named, else those that answered through magpie lately, else all.
func StatuslineCards(qs []Quota, only []string) []Quota {
	var withWindows, served []Quota
	for _, q := range qs {
		if q.Kind == "balance" || len(q.Windows) == 0 {
			continue
		}
		if len(only) > 0 && !StatuslineMatches(q, only) {
			continue
		}
		withWindows = append(withWindows, q)
		if q.LastServedAt != nil {
			served = append(served, q)
		}
	}
	if len(only) == 0 && len(served) > 0 {
		return served
	}
	return withWindows
}

func StatuslineMatches(q Quota, only []string) bool {
	for _, o := range only {
		for _, v := range []string{q.Provider, q.Name, q.User, q.Alias, q.Provider + "/" + q.User} {
			if v != "" && strings.EqualFold(v, o) {
				return true
			}
		}
	}
	return false
}

// StatuslineCard is one account: "claude a@x.com* 5h 62% 7d 41%".
func StatuslineCard(q Quota, now time.Time) string {
	name := q.Provider
	switch {
	case q.Alias != "":
		name += " " + q.Alias
	case q.User != "":
		name += " " + q.User
	}
	if q.Last {
		name += "*"
	}
	short := strings.NewReplacer(" hours", "h", " hour", "h", " days", "d", " day", "d", " · ", " ")
	cells := []string{name}
	for _, w := range q.Windows {
		n := short.Replace(w.Name)
		switch {
		case w.Unlimited:
			cells = append(cells, n+" ∞")
		case w.Balance != nil:
			// a wallet's figure in its unit, never a share (#3950)
			cells = append(cells, n+" "+BalanceText(*w.Balance, w.Unit))
		case w.Text:
			// a row of text alone (a plugin's balance) has no share
			cells = append(cells, n+" "+w.Display)
		case w.ResetsAt != nil && !w.ResetsAt.After(now):
			// its window has started again since it was read
			cells = append(cells, n+" 0%")
		default:
			cells = append(cells, fmt.Sprintf("%s %.0f%%", n, w.Used))
		}
	}
	return strings.Join(cells, " ")
}

// CodexStatuslineCards are the accounts a Codex thread's footer shows
// (lyj1115 on X: the quota of the account in use, or the relay's balance,
// under Codex's chat box): those of the provider the thread's latest turn
// went to (model is provider/model), the one that answered last of them,
// a key's balance as well as a subscription's windows; with no such
// account, the one that answered last of all.
func CodexStatuslineCards(model string, qs []Quota) []Quota {
	shown := func(q Quota) bool { return len(q.Windows) > 0 || q.Kind == "balance" && q.Balance != "" }
	latest := func(keep func(Quota) bool) []Quota {
		var out []Quota
		for _, q := range qs {
			if !shown(q) || !keep(q) {
				continue
			}
			switch {
			case len(out) == 0:
				out = []Quota{q}
			case q.LastServedAt != nil && (out[0].LastServedAt == nil || q.LastServedAt.After(*out[0].LastServedAt)):
				out[0] = q
			}
		}
		return out
	}
	if p, _, ok := strings.Cut(model, "/"); ok && p != "" {
		if out := latest(func(q Quota) bool { return q.Provider == p }); len(out) > 0 {
			return out
		}
	}
	return latest(func(q Quota) bool { return q.Last })
}

// CodexStatuslineCard is one account as Codex's footer shows it: its
// windows as StatuslineCard has them, or a key's balance.
func CodexStatuslineCard(q Quota, now time.Time) string {
	if len(q.Windows) == 0 && q.Balance != "" {
		name := q.Provider
		if q.Alias != "" {
			name += " " + q.Alias
		}
		return name + " " + q.Balance
	}
	q.Last = false // one account is shown: no star to tell it apart
	return StatuslineCard(q, now)
}
