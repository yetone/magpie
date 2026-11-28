//go:build !nogui

package gui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/chatpush"
	"github.com/yetone/magpie/internal/settings"
)

// fakeChat is a Telegram Bot API or a Discord webhook server that keeps
// what it was sent, and answers code.
type fakeChat struct {
	mu   sync.Mutex
	got  []map[string]any
	path []string
	code int
	*httptest.Server
}

func newFakeChat(t *testing.T, code int) *fakeChat {
	f := &fakeChat{code: code}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		f.mu.Lock()
		f.got, f.path = append(f.got, m), append(f.path, r.URL.Path)
		code := f.code
		f.mu.Unlock()
		switch {
		case code == http.StatusNoContent:
			w.WriteHeader(code)
		case code/100 == 2:
			w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
		default:
			w.WriteHeader(code)
			w.Write([]byte(`{"ok":false,"error_code":500,"description":"down","message":"down"}`))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeChat) sent() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.got...)
}

// fakeTelegram points the Bot API at f for the test.
func fakeTelegram(t *testing.T, f *fakeChat) {
	old := chatpush.TelegramAPI
	chatpush.TelegramAPI = f.URL
	t.Cleanup(func() { chatpush.TelegramAPI = old })
}

const (
	tgToken = "7123456789:AAHguiTokenForTestsOnly_abcdefghij"
	dcHook  = "/api/webhooks/1311234567890123456/guiWebhookTokenForTestsOnly-abcdefghijk"
)

// Settings' Telegram and Discord are saved with their secrets kept away
// from the page, checked before they are, Test sends to the one named, a
// save of the Settings page keeps them, and the daily summary goes with
// the last one taken away.
func TestAlertChannelsSettingAndTest(t *testing.T) {
	barkSandbox(t)
	tg, dc := newFakeChat(t, 200), newFakeChat(t, http.StatusNoContent)
	fakeTelegram(t, tg)
	hook := dc.URL + dcHook
	call := func(method, path, body string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	for _, to := range []string{"telegram", "discord"} {
		if code, _ := call("POST", "/api/alert-channels/test", `{"to":"`+to+`"}`); code == http.StatusOK {
			t.Fatalf("a test to %s with nothing set sent", to)
		}
	}
	if code, _ := call("POST", "/api/alert-channels/test", `{"to":"bark"}`); code == http.StatusOK {
		t.Fatal("a test to no such channel passed")
	}
	// a token with no chat, a bad token, a bad chat and a webhook that isn't one are refused
	for _, bad := range []string{
		`{"telegramToken":"` + tgToken + `"}`,
		`{"telegramToken":"not a token","telegramChat":"1"}`,
		`{"telegramToken":"` + tgToken + `","telegramChat":"my chat"}`,
		`{"discordWebhook":"https://discord.com/channels/1/2"}`,
		`{"discordWebhook":"discord.com/api/webhooks/1/x"}`,
	} {
		code, body := call("POST", "/api/settings/alert-channels", bad)
		if code == http.StatusOK || strings.Contains(body, tgToken) {
			t.Errorf("%s: %d %s", bad, code, body)
		}
	}
	if s := settings.Load(); s.TelegramToken != "" || s.DiscordWebhook != "" {
		t.Fatalf("a refused one was saved: %+v", s)
	}
	code, body := call("POST", "/api/settings/alert-channels", `{"telegramToken":" `+tgToken+` ","telegramChat":" -1001234567890 ","discordWebhook":" `+hook+` "}`)
	s := settings.Load()
	if code != http.StatusOK || s.TelegramToken != tgToken || s.TelegramChat != "-1001234567890" || s.DiscordWebhook != hook {
		t.Fatalf("set: %d %s, saved %+v", code, body, s)
	}
	for _, b := range []string{body, func() string { _, b := call("GET", "/api/settings", ""); return b }()} {
		if strings.Contains(b, tgToken) || strings.Contains(b, "guiWebhookToken") || !strings.Contains(b, `"telegramSet":true`) || !strings.Contains(b, `"discordSet":true`) || !strings.Contains(b, `"telegramChat":"-1001234567890"`) {
			t.Errorf("the page was told %s", b)
		}
	}
	// a Settings save, with the fields blank as the page has them, keeps them
	if code, body = call("POST", "/api/settings", `{"theme":"dark","telegramToken":"","discordWebhook":""}`); code != http.StatusOK || settings.Load().TelegramToken != tgToken || settings.Load().DiscordWebhook != hook {
		t.Fatalf("save: %d %s", code, body)
	}
	for _, to := range []string{"telegram", "discord"} {
		if code, body = call("POST", "/api/alert-channels/test", `{"to":"`+to+`"}`); code != http.StatusOK {
			t.Fatalf("test %s: %d %s", to, code, body)
		}
	}
	if g := tg.sent(); len(g) != 1 || g[0]["chat_id"] != "-1001234567890" || !strings.Contains(g[0]["text"].(string), "Telegram") || tg.path[0] != "/bot"+tgToken+"/sendMessage" {
		t.Errorf("Telegram was sent %v at %v", g, tg.path)
	}
	if g := dc.sent(); len(g) != 1 || !strings.Contains(g[0]["content"].(string), "Discord") || dc.path[0] != dcHook {
		t.Errorf("Discord was sent %v at %v", g, dc.path)
	}
	// a failed test says why, never the token
	tg.mu.Lock()
	tg.code = 500
	tg.mu.Unlock()
	if code, body = call("POST", "/api/alert-channels/test", `{"to":"telegram"}`); code == http.StatusOK || !strings.Contains(body, "down") || strings.Contains(body, tgToken) {
		t.Errorf("failed test: %d %s", code, body)
	}
	// the summary stays while one is set and goes with the last
	if code, _ = call("POST", "/api/settings/bark", `{"daily":"21:00"}`); code != http.StatusOK || settings.Load().BarkDaily != "21:00" {
		t.Fatalf("daily on with Telegram and Discord: %d %+v", code, settings.Load())
	}
	if code, body = call("POST", "/api/settings/alert-channels", `{"telegramToken":"","telegramChat":""}`); code != http.StatusOK || settings.Load().TelegramToken != "" || settings.Load().BarkDaily != "21:00" || strings.Contains(body, `"telegramSet"`) {
		t.Fatalf("remove Telegram: %d %s", code, body)
	}
	if code, _ = call("POST", "/api/settings/alert-channels", `{"discordWebhook":""}`); code != http.StatusOK || settings.Load().DiscordWebhook != "" || settings.Load().BarkDaily != "" {
		t.Fatalf("remove Discord: %d, %+v", code, settings.Load())
	}
}

// Every alert goes to Bark, Telegram and Discord, each that is set, and
// one that fails or hangs never stops the others.
func TestAlertsGoToEveryChannel(t *testing.T) {
	barkSandbox(t)
	bk, tg, dc := newFakeBark(t), newFakeChat(t, 200), newFakeChat(t, http.StatusNoContent)
	fakeTelegram(t, tg)
	n := &notifier{err: errors.New("no notifications here")}
	n.once.Do(func() {})
	wait := func(ok func() bool) {
		for deadline := time.Now().Add(5 * time.Second); !ok() && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
	}
	s := settings.Load()
	s.TelegramToken, s.TelegramChat, s.DiscordWebhook = tgToken, "123", dc.URL+dcHook
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	n.send("Allowance", "Claude 5h is at 90%")
	wait(func() bool { return len(tg.sent()) == 1 && len(dc.sent()) == 1 })
	if g := tg.sent(); len(g) != 1 || g[0]["text"] != "Allowance\nClaude 5h is at 90%" {
		t.Errorf("Telegram was sent %v", g)
	}
	if g := dc.sent(); len(g) != 1 || g[0]["content"] != "Allowance\nClaude 5h is at 90%" {
		t.Errorf("Discord was sent %v", g)
	}
	if len(bk.got()) != 0 {
		t.Errorf("Bark was sent with no key")
	}
	// Telegram refuses and Bark hangs past its answer: Discord still has it
	hang := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(hang) }) // before Close, which waits for the handler
	tg.mu.Lock()
	tg.code = 500
	tg.mu.Unlock()
	s = settings.Load()
	s.BarkServer, s.BarkKey = slow.URL, "k1"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	n.send("Balance", "OpenRouter is down to $4.80")
	wait(func() bool { return len(dc.sent()) == 2 })
	if g := dc.sent(); len(g) != 2 || g[1]["content"] != "Balance\nOpenRouter is down to $4.80" {
		t.Errorf("Discord waited on the others: %v", g)
	}
	wait(func() bool { return len(tg.sent()) == 2 })
	if len(tg.sent()) != 2 {
		t.Errorf("Telegram was not tried")
	}
}

// The daily summary goes with any channel set, to each of them; one that
// took it is enough, and only when every one failed is it tried again.
func TestDailySummaryToEveryChannel(t *testing.T) {
	barkSandbox(t)
	tg, dc := newFakeChat(t, 200), newFakeChat(t, 500)
	fakeTelegram(t, tg)
	d := &barkDaily{path: t.TempDir() + "/bark-daily.json"}
	day := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	build := func() (string, string) { return "magpie · today", "$3.50 · 12 requests" }
	s := settings.Settings{TelegramToken: tgToken, TelegramChat: "123", DiscordWebhook: dc.URL + dcHook, BarkDaily: "21:00"}
	send := func(title, body string) error { return pushAll(t.Context(), channels(s), title, body) }
	if !d.step(day.Add(21*time.Hour), s, build, send) {
		t.Fatal("not pushed with Telegram up and Discord down")
	}
	if len(tg.sent()) != 1 || len(dc.sent()) != 1 || tg.sent()[0]["text"] != "magpie · today\n$3.50 · 12 requests" {
		t.Fatalf("sent %v %v", tg.sent(), dc.sent())
	}
	// tomorrow both are down: it is tried again after the pause
	tg.mu.Lock()
	tg.code = 500
	tg.mu.Unlock()
	next := day.Add(24*time.Hour + 21*time.Hour)
	if d.step(next, s, build, send) {
		t.Fatal("counted with every channel down")
	}
	tg.mu.Lock()
	tg.code = 200
	tg.mu.Unlock()
	if !d.step(next.Add(barkRetry+time.Minute), s, build, send) || len(tg.sent()) != 3 {
		t.Fatalf("not retried: %d", len(tg.sent()))
	}
	// with nothing set it is off, the time notwithstanding
	if d.step(day.Add(72*time.Hour+21*time.Hour), settings.Settings{BarkDaily: "21:00"}, build, send) {
		t.Fatal("pushed with nowhere to go")
	}
}
