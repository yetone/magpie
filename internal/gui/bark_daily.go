package gui

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/bark"
	"github.com/yetone/magpie/internal/chatpush"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// barkLate is how long after its time of day a summary is still pushed: a
// computer asleep then sends it on waking, and one woken the next evening
// leaves the day be.
const barkLate = 6 * time.Hour

// barkRetry is how long a push that failed waits before it is tried again.
const barkRetry = 15 * time.Minute

// barkTarget is where Settings sends pushes, and whether anywhere.
func barkTarget(s settings.Settings) bark.Target {
	return bark.Target{Server: s.BarkServer, Key: s.BarkKey}
}

// channel is one place alerts go beside the system's notifications: Bark,
// a Telegram bot or a Discord webhook (gandli on X).
type channel struct {
	name string
	send func(ctx context.Context, title, body string) error
}

// channels are the ones s has set, each with what it needs to send.
func channels(s settings.Settings) []channel {
	var out []channel
	if t := barkTarget(s); t.On() {
		out = append(out, channel{"bark", func(ctx context.Context, title, body string) error { return bark.Push(ctx, t, title, body) }})
	}
	if t := (chatpush.Telegram{Token: s.TelegramToken, Chat: s.TelegramChat}); t.On() {
		out = append(out, channel{"telegram", func(ctx context.Context, title, body string) error { return chatpush.PushTelegram(ctx, t, title, body) }})
	}
	if d := (chatpush.Discord{Webhook: s.DiscordWebhook}); d.On() {
		out = append(out, channel{"discord", func(ctx context.Context, title, body string) error { return chatpush.PushDiscord(ctx, d, title, body) }})
	}
	return out
}

// pushAll sends title and body to each of cs at once, each on its own, so
// one that fails or hangs never holds the others back, and waits for them.
// It fails only when every one did; each failure is logged, never a secret.
func pushAll(ctx context.Context, cs []channel, title, body string) error {
	errs := make([]error, len(cs))
	var wg sync.WaitGroup
	for i, c := range cs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			if errs[i] = c.send(cx, title, body); errs[i] != nil {
				log.Println(c.name+":", errs[i])
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err == nil {
			return nil
		}
	}
	return errors.Join(errs...)
}

// summaryNeedsChannel turns the daily summary off once the last channel
// it went to is taken away, so one added later starts without it.
func summaryNeedsChannel(s *settings.Settings) {
	if len(channels(*s)) == 0 {
		s.BarkDaily = ""
	}
}

// alertPush sends one alert's words to every channel Settings has, without
// making the alert wait for them.
func alertPush(title, body string) {
	cs := channels(settings.Load())
	if len(cs) == 0 {
		return
	}
	go pushAll(context.Background(), cs, title, body)
}

// barkDaily keeps the day the summary was last pushed, in memory and in a
// file beside the alerts' marks, so a restart doesn't push it again.
type barkDaily struct {
	path    string
	mu      sync.Mutex
	sent    string
	retryAt time.Time
}

func (d *barkDaily) lastDay() string {
	if d.sent != "" {
		return d.sent
	}
	var m struct {
		Day string `json:"day"`
	}
	if b, err := os.ReadFile(d.path); err == nil {
		json.Unmarshal(b, &m)
	}
	return m.Day
}

// step pushes the day's summary when it is due at now: Settings has a
// channel and a time, the time has come and is not more than barkLate past, and none
// was pushed today. A failed push is asked again after barkRetry, and a
// summary that can't be pushed in barkLate is left. It reports whether it
// pushed.
func (d *barkDaily) step(now time.Time, s settings.Settings, build func() (string, string), send func(title, body string) error) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(channels(s)) == 0 || s.BarkDaily == "" {
		return false
	}
	h, m, ok := settings.Clock(s.BarkDaily)
	if !ok {
		return false
	}
	y, mo, dd := now.Date()
	at := time.Date(y, mo, dd, h, m, 0, 0, now.Location())
	day := now.Format("2006-01-02")
	if now.Before(at) || now.Sub(at) > barkLate || d.lastDay() == day || now.Before(d.retryAt) {
		return false
	}
	title, body := build()
	if err := send(title, body); err != nil {
		log.Println("daily summary:", err)
		d.retryAt = now.Add(barkRetry)
		return false
	}
	d.sent = day
	if b, err := json.Marshal(map[string]string{"day": day}); err == nil {
		os.MkdirAll(filepath.Dir(d.path), 0o755)
		os.WriteFile(d.path, append(b, '\n'), 0o600)
	}
	return true
}

// barkSummary reads today's spend and the allowances now, and says them.
func barkSummary(ctx context.Context, now time.Time) (title, body string) {
	s := settings.Load()
	cx, cancel := context.WithTimeout(ctx, time.Minute)
	qs := provider.Quotas(cx)
	cancel()
	pct := s.UsageAlert
	if pct <= 0 {
		pct = barkNearPct
	}
	currency, rate := totalsCurrency()
	return barkDailyText(trayLang(s.Lang, systemLang), trayTotalSum(usage.Today), currency, rate,
		provider.NearLimits(qs, pct, s.BalanceAlert, now), s.BalanceAlert, s.QuotaLeft, now)
}

// watchBarkDaily pushes the daily summary at the time Settings names, to
// every channel it has, until ctx ends. A summary one channel took is not
// sent again for another that failed.
func watchBarkDaily(ctx context.Context) {
	d := &barkDaily{path: filepath.Join(settings.Dir(), "bark-daily.json")}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			d.step(now, settings.Load(), func() (string, string) { return barkSummary(ctx, now) }, func(title, body string) error {
				return pushAll(ctx, channels(settings.Load()), title, body)
			})
		}
	}
}
