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
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	model := ""
	if in.SessionID != "" {
		var route struct {
			Route *struct {
				Asked string `json:"asked"`
				Model string `json:"model"`
			} `json:"route"`
		}
		if getJSON(client, base+"/v1/magpie/route?session="+url.QueryEscape(in.SessionID), &route) && route.Route != nil {
			model = cmp.Or(route.Route.Model, route.Route.Asked)
		}
	}
	return provider.StatuslineText(model, quotas.Data, only, now)
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
