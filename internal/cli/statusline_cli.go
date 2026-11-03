package cli

// magpie statusline: one line for Claude Code's status line (#1997,
// kensenzhao). Claude Code runs the statusLine command on every message,
// hands it the session's JSON on stdin and shows what it prints, however
// Claude Code signs in to the gateway; the anthropic-ratelimit-* headers
// (#1257) reach it only while it is signed in to claude.ai itself, and
// tell only the one account that answered. This asks the running gateway
// for what it already holds — the session's latest route and every
// account's windows as last read (/v1/magpie/quotas?kept=1) — and asks no
// vendor itself, so it never waits on one.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

const statuslineUsage = `usage: magpie statusline [<provider|account>…]
  one line for Claude Code's status line: the model the session's latest turn went
  to through magpie, then each subscription and plan magpie has, as last read: its
  windows' use ("5h 62% 7d 41%"), * on the one that answered last. Accounts that
  answered through magpie in the last 30 days are shown, all of them when none has;
  a provider or an account's email, login or name keeps only those.
  Claude Code runs it on every message with the session's JSON on stdin. Turn it on
  with the status line square on Claude Code's row in the Agents page (or
  magpie claude statusline on), which writes ~/.claude/settings.json's statusLine
  unless a status line of your own (claude-hud, a script) is there; or add it yourself:
    "statusLine": {"type": "command", "command": "magpie statusline"}
  It asks the gateway on this computer (the port in Settings' Gateway address, or MAGPIE_ADDR), never
  a vendor, and prints nothing when the gateway isn't running.`

// statuslineWait bounds each ask of the gateway: Claude Code shows the line
// only once the command ends.
const statuslineWait = time.Second

// statuslineCmd: magpie statusline [<provider|account>…]
func statuslineCmd(args []string) error {
	fmt.Println(statusline(os.Stdin, args[1:], "http://"+settings.GatewayAddr(), time.Now()))
	return nil
}

// statusline is the line: "" when the gateway can't be asked.
func statusline(stdin io.Reader, only []string, base string, now time.Time) string {
	var in struct {
		SessionID string `json:"session_id"`
	}
	if stdin != nil {
		// Claude Code's JSON is small; a terminal's stdin with nothing
		// piped in isn't waited on (read only when it is a pipe or file)
		if f, ok := stdin.(*os.File); !ok || stdinPiped(f) {
			_ = json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&in)
		}
	}
	client := &http.Client{Timeout: statuslineWait}
	var quotas struct {
		Data []provider.Quota `json:"data"`
	}
	if !getJSON(client, base+"/v1/magpie/quotas?kept=1", &quotas) {
		return ""
	}
	var parts []string
	if in.SessionID != "" {
		var route struct {
			Route *struct {
				Asked string `json:"asked"`
				Model string `json:"model"`
			} `json:"route"`
		}
		if getJSON(client, base+"/v1/magpie/route?session="+url.QueryEscape(in.SessionID), &route) && route.Route != nil {
			if m := route.Route.Model; m != "" {
				parts = append(parts, "→ "+m)
			} else if route.Route.Asked != "" {
				parts = append(parts, "→ "+route.Route.Asked)
			}
		}
	}
	for _, q := range statuslineCards(quotas.Data, only) {
		parts = append(parts, statuslineCard(q, now))
	}
	return strings.Join(parts, " | ")
}

// stdinPiped: f is a pipe or a file, not a terminal waiting for typing.
func stdinPiped(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice == 0
}

func getJSON(c *http.Client, u string, v any) bool {
	res, err := c.Get(u)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode == http.StatusOK && json.NewDecoder(res.Body).Decode(v) == nil
}

// statuslineCards are the subscriptions and plans with windows to show:
// those named, else those that answered through magpie lately, else all.
func statuslineCards(qs []provider.Quota, only []string) []provider.Quota {
	var withWindows, served []provider.Quota
	for _, q := range qs {
		if q.Kind == "balance" || len(q.Windows) == 0 {
			continue
		}
		if len(only) > 0 && !statuslineMatches(q, only) {
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

func statuslineMatches(q provider.Quota, only []string) bool {
	for _, o := range only {
		for _, v := range []string{q.Provider, q.Name, q.User, q.Alias, q.Provider + "/" + q.User} {
			if v != "" && strings.EqualFold(v, o) {
				return true
			}
		}
	}
	return false
}

// statuslineCard is one account: "claude a@x.com* 5h 62% 7d 41%".
func statuslineCard(q provider.Quota, now time.Time) string {
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
			cells = append(cells, n+" "+provider.BalanceText(*w.Balance, w.Unit))
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
