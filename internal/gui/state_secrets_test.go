package gui

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// /api/state's settings, polled after nearly every click, carry none of
// the credentials /api/settings keeps on disk: a saved proxy's password,
// the old LAN key, the Bark key, the GitHub token, and a Telegram bot's
// token and a Discord webhook (gandli on X). Neither does the
// last state served at start (?last=1), which has today's settings.
func TestStateSendsNoCredentials(t *testing.T) {
	sandboxHome(t)
	const pass, lan, bark, gh = "hunter2-proxy-pass", "sk-magpie-statelanstatelanstatelan0123", "bark-state-key-0123", "ghp_statetokenstatetoken0123"
	const tg, dc = "7123:state-telegram-token", "https://discord.com/api/webhooks/1/state-discord-hook"
	h := Handler(nil, nil)
	call := func(method, path, body string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s %d %s", path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	call("POST", "/api/proxies/save", `{"name":"Office","url":"http://10.0.0.1:8080","user":"me","pass":"`+pass+`"}`)
	s := settings.Load()
	if len(s.Proxies) != 1 || s.Proxies[0].Pass != pass {
		t.Fatalf("the proxy wasn't saved: %+v", s.Proxies)
	}
	s.LANKey, s.BarkKey, s.GitHubToken = lan, bark, gh
	s.TelegramToken, s.TelegramChat, s.DiscordWebhook = tg, "12345", dc
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/state", "/api/state?last=1"} {
		body := call("GET", path, "")
		if path == "/api/state?last=1" && body == "null\n" {
			t.Fatal("no state was kept for the next start")
		}
		for _, secret := range []string{pass, lan, bark, gh, tg, dc, "state-discord-hook"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s carries %q", path, secret)
			}
		}
	}
	if s := settings.Load(); s.Proxies[0].Pass != pass || s.LANKey != lan || s.BarkKey != bark || s.GitHubToken != gh || s.TelegramToken != tg || s.DiscordWebhook != dc {
		t.Fatal("the credentials on disk were changed")
	}
}
