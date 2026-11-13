package gui

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/bark"
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

// barkPush sends one alert's words to Bark when it is set, without making
// the alert wait for it. The key is never logged.
func barkPush(title, body string) {
	t := barkTarget(settings.Load())
	if !t.On() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := bark.Push(ctx, t, title, body); err != nil {
			log.Println("bark:", err)
		}
	}()
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

// step pushes the day's summary when it is due at now: Settings has a key
// and a time, the time has come and is not more than barkLate past, and none
// was pushed today. A failed push is asked again after barkRetry, and a
// summary that can't be pushed in barkLate is left. It reports whether it
// pushed.
func (d *barkDaily) step(now time.Time, s settings.Settings, build func() (string, string), send func(title, body string) error) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !barkTarget(s).On() || s.BarkDaily == "" {
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
		log.Println("bark:", err)
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

// watchBarkDaily pushes the daily summary at the time Settings names, until
// ctx ends.
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
				cx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				return bark.Push(cx, barkTarget(settings.Load()), title, body)
			})
		}
	}
}
