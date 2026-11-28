package chatpush

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"
)

// fake is a Telegram or Discord server: it records each request and
// answers with code and say.
type fake struct {
	mu   sync.Mutex
	got  []map[string]any
	reqs []string
	ct   []string
	code int
	say  string
	*httptest.Server
}

func newFake(t *testing.T, code int, say string) *fake {
	f := &fake{code: code, say: say}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		f.mu.Lock()
		f.got, f.reqs, f.ct = append(f.got, m), append(f.reqs, r.Method+" "+r.URL.String()), append(f.ct, r.Header.Get("Content-Type"))
		f.mu.Unlock()
		if f.code == http.StatusNoContent {
			w.WriteHeader(f.code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.code)
		io.WriteString(w, f.say)
	}))
	t.Cleanup(f.Close)
	old, api := client, TelegramAPI
	client = &http.Client{} // the test's own: never the system proxy, never the network
	TelegramAPI = f.URL
	t.Cleanup(func() { client, TelegramAPI = old, api })
	return f
}

const (
	botToken = "7123456789:AAHfakeTokenForTestsOnly_abcdefghij"
	hookPath = "/api/webhooks/1311234567890123456/fakeWebhookTokenForTestsOnly-abcdefghijklmnop"
)

// A message goes to Telegram as the Bot API documents sendMessage: a POST
// of JSON to /bot<token>/sendMessage with chat_id and text, the title over
// the body, in plain text with no link preview.
func TestTelegramSendMessage(t *testing.T) {
	f := newFake(t, 200, `{"ok":true,"result":{"message_id":7,"chat":{"id":123456789,"type":"private"},"date":1764300000,"text":"x"}}`)
	if err := PushTelegram(context.Background(), Telegram{Token: " " + botToken + " ", Chat: " 123456789 "}, "Allowance", "Claude 5h is at 90%"); err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 1 || f.reqs[0] != "POST /bot"+botToken+"/sendMessage" || !strings.HasPrefix(f.ct[0], "application/json") {
		t.Fatalf("requests %v %v", f.reqs, f.ct)
	}
	g := f.got[0]
	if g["chat_id"] != "123456789" || g["text"] != "Allowance\nClaude 5h is at 90%" || g["parse_mode"] != nil {
		t.Errorf("sent %v", g)
	}
	if lp, _ := g["link_preview_options"].(map[string]any); lp["is_disabled"] != true {
		t.Errorf("link preview %v", g["link_preview_options"])
	}
	// a public channel's @name goes as written
	if err := PushTelegram(context.Background(), Telegram{Token: botToken, Chat: "@magpie_alerts"}, "t", "b"); err != nil || f.got[1]["chat_id"] != "@magpie_alerts" {
		t.Errorf("@name: %v %v", err, f.got[1])
	}
}

// Telegram's refusal is said with its description, never the token, and
// nothing is sent without a token and a chat.
func TestTelegramRefusalHidesToken(t *testing.T) {
	f := newFake(t, 400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	err := PushTelegram(context.Background(), Telegram{Token: botToken, Chat: "1"}, "t", "b")
	if err == nil || !strings.Contains(err.Error(), "chat not found") || strings.Contains(err.Error(), botToken) {
		t.Fatalf("error %v", err)
	}
	// a 200 that says ok:false is a refusal too
	f.code, f.say = 200, `{"ok":false,"description":"Unauthorized `+botToken+`"}`
	if err := PushTelegram(context.Background(), Telegram{Token: botToken, Chat: "1"}, "t", "b"); err == nil || strings.Contains(err.Error(), botToken) {
		t.Fatalf("ok:false: %v", err)
	}
	n := len(f.reqs)
	for _, tg := range []Telegram{{Token: botToken}, {Chat: "1"}, {Token: " ", Chat: " "}} {
		if PushTelegram(context.Background(), tg, "t", "b") == nil {
			t.Errorf("%+v sent", tg)
		}
	}
	if len(f.reqs) != n {
		t.Errorf("a request went with no token or chat")
	}
	// a server that can't be reached is said without the address, which holds the token
	f.Close()
	if err := PushTelegram(context.Background(), Telegram{Token: botToken, Chat: "1"}, "t", "b"); err == nil || strings.Contains(err.Error(), botToken) || strings.Contains(err.Error(), "AAHfake") {
		t.Fatalf("unreachable: %v", err)
	}
}

// A message goes to Discord as Execute Webhook documents it: a POST of JSON
// with content, as magpie, mentioning no one; 204 No Content is sent.
func TestDiscordExecuteWebhook(t *testing.T) {
	f := newFake(t, http.StatusNoContent, "")
	if err := PushDiscord(context.Background(), Discord{Webhook: " " + f.URL + hookPath + " "}, "Balance", "@everyone OpenRouter is down to $4.80"); err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 1 || f.reqs[0] != "POST "+hookPath || !strings.HasPrefix(f.ct[0], "application/json") {
		t.Fatalf("requests %v %v", f.reqs, f.ct)
	}
	g := f.got[0]
	if g["content"] != "Balance\n@everyone OpenRouter is down to $4.80" || g["username"] != "magpie" {
		t.Errorf("sent %v", g)
	}
	am, _ := g["allowed_mentions"].(map[string]any)
	if p, ok := am["parse"].([]any); !ok || len(p) != 0 {
		t.Errorf("allowed_mentions %v: an @everyone in the words would ping", g["allowed_mentions"])
	}
	// with ?wait=true Discord answers 200 with the message
	f.code, f.say = 200, `{"id":"1","content":"x"}`
	if err := PushDiscord(context.Background(), Discord{Webhook: f.URL + hookPath + "?wait=true"}, "t", "b"); err != nil {
		t.Errorf("wait=true: %v", err)
	}
}

// Discord's refusal is said with its message, never the webhook's address
// or token.
func TestDiscordRefusalHidesWebhook(t *testing.T) {
	f := newFake(t, 404, `{"message": "Unknown Webhook", "code": 10015}`)
	hook := f.URL + hookPath
	err := PushDiscord(context.Background(), Discord{Webhook: hook}, "t", "b")
	if err == nil || !strings.Contains(err.Error(), "Unknown Webhook") || strings.Contains(err.Error(), "fakeWebhookToken") {
		t.Fatalf("error %v", err)
	}
	if PushDiscord(context.Background(), Discord{Webhook: "  "}, "t", "b") == nil || len(f.reqs) != 1 {
		t.Errorf("sent with no webhook")
	}
	f.Close()
	if err := PushDiscord(context.Background(), Discord{Webhook: hook}, "t", "b"); err == nil || strings.Contains(err.Error(), "fakeWebhookToken") || strings.Contains(err.Error(), "1311234567890123456") {
		t.Fatalf("unreachable: %v", err)
	}
}

// A long message is cut within each app's limit, Telegram's 4096
// characters and Discord's 2000, ending in an ellipsis, and a character
// outside the BMP is never cut in half.
func TestMessageCutToLimit(t *testing.T) {
	long := strings.Repeat("额度 near its end · 🐦 ", 400)
	f := newFake(t, 200, `{"ok":true,"result":{}}`)
	if err := PushTelegram(context.Background(), Telegram{Token: botToken, Chat: "1"}, "magpie · today", long); err != nil {
		t.Fatal(err)
	}
	tg, _ := f.got[0]["text"].(string)
	if n := len(utf16.Encode([]rune(tg))); n > telegramMax || n < telegramMax-4 || !strings.HasSuffix(tg, "…") || !strings.HasPrefix(tg, "magpie · today\n") || strings.ContainsRune(tg, '�') {
		t.Errorf("Telegram text is %d UTF-16 units, ends %q", n, tg[len(tg)-8:])
	}
	d := newFake(t, http.StatusNoContent, "")
	if err := PushDiscord(context.Background(), Discord{Webhook: d.URL + hookPath}, "magpie · today", long); err != nil {
		t.Fatal(err)
	}
	dc, _ := d.got[0]["content"].(string)
	if n := len(utf16.Encode([]rune(dc))); n > discordMax || n < discordMax-4 || !strings.HasSuffix(dc, "…") || strings.ContainsRune(dc, '�') {
		t.Errorf("Discord content is %d UTF-16 units", n)
	}
	// a short one goes whole
	if got := text("t", "b", 10); got != "t\nb" {
		t.Errorf("short: %q", got)
	}
}
