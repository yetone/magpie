// Package chatpush sends an alert's words to a chat app beside Bark (gandli
// on X): a Telegram bot's message to one chat (Bot API sendMessage) and a
// Discord channel's webhook. Each is one POST of JSON. The bot token and the
// webhook address are secrets: an error names the server's answer, never
// them, as the token sits in Telegram's address and the webhook's token in
// its own.
package chatpush

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/netproxy"
)

// client follows the app's proxy, as its other requests to the Internet do;
// a var for tests.
var client = &http.Client{Timeout: 15 * time.Second, Transport: proxied()}

func proxied() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = netproxy.Func
	return netproxy.Dispatch(t)
}

// TelegramAPI is the Bot API's server; a var for tests, here and in the
// packages that send alerts.
var TelegramAPI = "https://api.telegram.org"

// The longest text each takes: Telegram's sendMessage 4096 characters,
// Discord's content 2000.
const (
	telegramMax = 4096
	discordMax  = 2000
)

// icon is magpie's own icon, the one Bark's pushes show, for the webhook's
// messages.
const icon = "https://usemagpie.ai/apple-touch-icon.png"

// Telegram is a bot's token, from @BotFather, and the chat it writes to: a
// user's, group's or channel's numeric id, or a public @name.
type Telegram struct {
	Token, Chat string
}

// On is whether there is a token and a chat to send to.
func (t Telegram) On() bool {
	return strings.TrimSpace(t.Token) != "" && strings.TrimSpace(t.Chat) != ""
}

// Discord is a channel's webhook address, as its Integrations page copies it.
type Discord struct {
	Webhook string
}

// On is whether there is a webhook to send to.
func (d Discord) On() bool { return strings.TrimSpace(d.Webhook) != "" }

// text is the title over the body, cut to max characters (counted in UTF-16
// units, which is no more than either app counts) with an ellipsis.
func text(title, body string, max int) string {
	s := strings.TrimSpace(title)
	if b := strings.TrimSpace(body); b != "" {
		if s != "" {
			s += "\n"
		}
		s += b
	}
	if len(utf16.Encode([]rune(s))) <= max {
		return s
	}
	n := 0
	for i, r := range s {
		w := utf16.RuneLen(r)
		if n+w > max-1 {
			return strings.TrimRight(s[:i], " \n") + "…"
		}
		n += w
	}
	return s
}

// hide takes each secret out of an error's text.
func hide(s string, secrets ...string) string {
	for _, k := range secrets {
		if k = strings.TrimSpace(k); k != "" {
			s = strings.ReplaceAll(s, k, "…")
		}
	}
	return s
}

// reach is a request's failure to reach the server, said without its
// address, which holds the secret.
func reach(who string, err error, secrets ...string) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("%s: %s", who, hide(err.Error(), secrets...))
}

func post(ctx context.Context, addr string, payload any) (*http.Response, []byte, error) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", addr, bytes.NewReader(b))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp, raw, nil
}

// PushTelegram sends title and body to t's chat as one plain-text message.
func PushTelegram(ctx context.Context, t Telegram, title, body string) error {
	if !t.On() {
		return errors.New("no Telegram bot token and chat are set")
	}
	token, chat := strings.TrimSpace(t.Token), strings.TrimSpace(t.Chat)
	payload := map[string]any{
		"chat_id":              chat,
		"text":                 text(title, body, telegramMax),
		"link_preview_options": map[string]bool{"is_disabled": true},
	}
	resp, raw, err := post(ctx, strings.TrimRight(TelegramAPI, "/")+"/bot"+token+"/sendMessage", payload)
	if err != nil {
		return reach("Telegram", err, token)
	}
	var reply struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.Unmarshal(raw, &reply)
	if resp.StatusCode/100 == 2 && reply.OK {
		return nil
	}
	say := strings.TrimSpace(reply.Description)
	if say == "" {
		say = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("Telegram answered %d: %s", resp.StatusCode, hide(say, token))
}

// PushDiscord posts title and body to d's channel, as magpie, mentioning no
// one whatever the words hold.
func PushDiscord(ctx context.Context, d Discord, title, body string) error {
	if !d.On() {
		return errors.New("no Discord webhook is set")
	}
	hook := strings.TrimSpace(d.Webhook)
	payload := map[string]any{
		"content":          text(title, body, discordMax),
		"username":         "magpie",
		"avatar_url":       icon,
		"allowed_mentions": map[string]any{"parse": []string{}},
	}
	// the address, its path and its last part (the webhook's token), as a
	// server or a proxy may say any of them back
	secrets := []string{hook}
	if u, err := url.Parse(hook); err == nil {
		last := u.Path[strings.LastIndexByte(u.Path, '/')+1:]
		for _, p := range []string{u.String(), u.Path, last} {
			if len(p) >= 8 {
				secrets = append(secrets, p)
			}
		}
	}
	resp, raw, err := post(ctx, hook, payload)
	if err != nil {
		return reach("Discord webhook", err, secrets...)
	}
	if resp.StatusCode/100 == 2 {
		return nil
	}
	var reply struct {
		Message string `json:"message"`
	}
	json.Unmarshal(raw, &reply)
	say := strings.TrimSpace(reply.Message)
	if say == "" {
		say = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("Discord answered %d: %s", resp.StatusCode, hide(say, secrets...))
}
