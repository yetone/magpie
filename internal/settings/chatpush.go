package settings

import (
	"errors"
	"net/url"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

var (
	// a bot's token as @BotFather gives it: the bot's id, a colon, its secret
	telegramToken = lazyre.New(`^[0-9]+:[A-Za-z0-9_-]+$`)
	// a chat's numeric id (a group's and a channel's are negative) or a
	// public chat's @name
	telegramChat = lazyre.New(`^(-?[0-9]+|@[A-Za-z][A-Za-z0-9_]{3,})$`)
)

// CheckTelegram says what is wrong with a Telegram bot's token and chat
// (both "" is off), or nil. The token is never named in the error.
func CheckTelegram(token, chat string) error {
	token, chat = strings.TrimSpace(token), strings.TrimSpace(chat)
	if token == "" && chat == "" {
		return nil
	}
	if token == "" || chat == "" {
		return errors.New("a Telegram alert needs both the bot's token and the chat it writes to")
	}
	if !telegramToken.MatchString(token) {
		return errors.New("a Telegram bot token looks like 123456789:AAH…, as @BotFather gives it")
	}
	if !telegramChat.MatchString(chat) {
		return errors.New("a Telegram chat is its numeric id, like 123456789 or -1001234567890, or a public @name")
	}
	return nil
}

// CheckDiscord says what is wrong with a Discord webhook address ("" is
// off), or nil. The address holds the webhook's token and is never named
// in the error.
func CheckDiscord(hook string) error {
	hook = strings.TrimSpace(hook)
	if hook == "" {
		return nil
	}
	u, err := url.Parse(hook)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" ||
		!strings.Contains(u.Path, "/webhooks/") || strings.ContainsAny(hook, " \t\r\n") {
		return errors.New("a Discord webhook is the address the channel's Integrations › Webhooks copies, like https://discord.com/api/webhooks/…")
	}
	return nil
}
