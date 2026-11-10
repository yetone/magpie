//go:build !nogui

package gui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

func barkSandbox(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local", "share"))
}

// fakeBark is a Bark server that keeps what it was sent.
type fakeBark struct {
	mu     sync.Mutex
	pushes []map[string]string
	*httptest.Server
}

func newFakeBark(t *testing.T) *fakeBark {
	f := &fakeBark{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]string
		json.Unmarshal(b, &m)
		f.mu.Lock()
		f.pushes = append(f.pushes, m)
		f.mu.Unlock()
		w.Write([]byte(`{"code":200,"message":"success"}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeBark) got() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]string(nil), f.pushes...)
}

// Settings' Bark target is saved with its key kept away from the page, the
// test button pushes to it, and a save of the Settings page keeps it.
func TestBarkSettingAndTest(t *testing.T) {
	barkSandbox(t)
	fake := newFakeBark(t)
	const key = "AbCdEf123456secretkey"
	call := func(method, path, body string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	if code, _ := call("POST", "/api/bark/test", ""); code == http.StatusOK {
		t.Fatal("a test with no key pushed")
	}
	code, body := call("POST", "/api/settings/bark", `{"server":"`+fake.URL+`/","key":" `+key+` ","daily":"21:30"}`)
	s := settings.Load()
	if code != http.StatusOK || s.BarkKey != key || s.BarkServer != fake.URL || s.BarkDaily != "21:30" {
		t.Fatalf("set: %d %s, saved %+v", code, body, s)
	}
	if strings.Contains(body, key) || !strings.Contains(body, `"barkKeySet":true`) {
		t.Errorf("the page was told %s", body)
	}
	if code, body = call("GET", "/api/settings", ""); strings.Contains(body, key) || !strings.Contains(body, `"barkKeySet":true`) {
		t.Errorf("GET /api/settings: %d %s", code, body)
	}
	// a Settings save and a server-only change keep the key
	if code, body = call("POST", "/api/settings", `{"theme":"dark","barkKey":""}`); code != http.StatusOK || settings.Load().BarkKey != key {
		t.Fatalf("save: %d %s, key %q", code, body, settings.Load().BarkKey)
	}
	if code, _ = call("POST", "/api/settings/bark", `{"daily":""}`); code != http.StatusOK || settings.Load().BarkKey != key || settings.Load().BarkDaily != "" {
		t.Fatalf("daily off: %d, %+v", code, settings.Load())
	}
	if code, _ = call("POST", "/api/settings/bark", `{"key":"two words"}`); code == http.StatusOK || settings.Load().BarkKey != key {
		t.Errorf("a key with a space was taken: %d", code)
	}
	code, body = call("POST", "/api/bark/test", "")
	if code != http.StatusOK {
		t.Fatalf("test: %d %s", code, body)
	}
	p := fake.got()
	if len(p) != 1 || p[0]["device_key"] != key || p[0]["body"] == "" {
		t.Errorf("the fake was sent %v", p)
	}
	if code, body = call("POST", "/api/settings/bark", `{"key":""}`); code != http.StatusOK || settings.Load().BarkKey != "" || strings.Contains(body, `"barkKeySet"`) {
		t.Errorf("remove: %d %s", code, body)
	}
}

// Every alert goes to Bark when it is set, and to nothing when it is not.
func TestAlertsGoToBark(t *testing.T) {
	barkSandbox(t)
	fake := newFakeBark(t)
	// the system side is out of reach, as it is where notifications are off
	n := &notifier{err: errors.New("no notifications here")}
	n.once.Do(func() {})
	n.send("title", "body")
	time.Sleep(50 * time.Millisecond)
	if p := fake.got(); len(p) != 0 {
		t.Fatalf("pushed with no Bark set: %v", p)
	}
	s := settings.Load()
	s.BarkServer, s.BarkKey = fake.URL, "k1"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	n.send("Allowance", "Claude 5h is at 90%")
	deadline := time.Now().Add(5 * time.Second)
	for len(fake.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	p := fake.got()
	if len(p) != 1 || p[0]["title"] != "Allowance" || p[0]["body"] != "Claude 5h is at 90%" {
		t.Errorf("the fake was sent %v", p)
	}
}

// The daily summary is pushed once, at its time and not before, not again
// after a restart, retried after a failure, and left when it is too late.
func TestBarkDailyStep(t *testing.T) {
	barkSandbox(t)
	d := &barkDaily{path: filepath.Join(t.TempDir(), "bark-daily.json")}
	s := settings.Settings{BarkKey: "k", BarkDaily: "21:00"}
	day := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	var sent int
	build := func() (string, string) { return "t", "b" }
	ok := func(string, string) error { sent++; return nil }
	step := func(at time.Time, s settings.Settings, send func(string, string) error) bool {
		return d.step(at, s, build, send)
	}
	if step(day.Add(20*time.Hour), s, ok) || sent != 0 {
		t.Fatal("pushed before its time")
	}
	if !step(day.Add(21*time.Hour+time.Minute), s, ok) || sent != 1 {
		t.Fatal("not pushed at its time")
	}
	if step(day.Add(22*time.Hour), s, ok) {
		t.Fatal("pushed twice in a day")
	}
	// a restart reads the day from the file
	d2 := &barkDaily{path: d.path}
	if d2.step(day.Add(22*time.Hour), s, build, ok) || sent != 1 {
		t.Fatal("pushed again after a restart")
	}
	// tomorrow: a failure waits, then retries
	next := day.Add(24 * time.Hour)
	bad := func(string, string) error { return errors.New("down") }
	if step(next.Add(21*time.Hour), s, bad) {
		t.Fatal("a failed push counted")
	}
	if step(next.Add(21*time.Hour+5*time.Minute), s, ok) || sent != 1 {
		t.Fatal("retried before the pause")
	}
	if !step(next.Add(21*time.Hour+20*time.Minute), s, ok) || sent != 2 {
		t.Fatal("not retried after the pause")
	}
	// too late, off, no key
	late := next.Add(24 * time.Hour)
	if step(late.Add(21*time.Hour+barkLate+time.Minute), s, ok) {
		t.Fatal("pushed a summary that was a day late")
	}
	if step(late.Add(21*time.Hour), settings.Settings{BarkKey: "k"}, ok) || step(late.Add(21*time.Hour), settings.Settings{BarkDaily: "21:00"}, ok) {
		t.Fatal("pushed while off")
	}
}

// The summary says what today cost and the allowances near their end, in
// the page's language, and caps the list.
func TestBarkDailyText(t *testing.T) {
	now := time.Date(2026, 11, 11, 21, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	var near []provider.QuotaAlert
	for i := 0; i < 10; i++ {
		near = append(near, provider.QuotaAlert{Provider: "claude", Name: "Claude", Window: "5h", Used: 91, ResetsAt: &reset})
	}
	title, body := barkDailyText("en", usage.Totals{Calls: 12, Cost: 3.5}, "usd", 0, near[:2], 0, false, now)
	if title == "" || !strings.Contains(body, "$3.50") || !strings.Contains(body, "12 requests") || strings.Count(body, "\n· ") != 2 {
		t.Errorf("en: %q %q", title, body)
	}
	_, body = barkDailyText("en", usage.Totals{Calls: 12, Cost: 3.5, Unpriced: 1}, "usd", 0, nil, 0, false, now)
	if !strings.Contains(body, "$3.50+") || !strings.Contains(body, "No allowance is near") {
		t.Errorf("unpriced: %q", body)
	}
	_, body = barkDailyText("zh", usage.Totals{}, "usd", 0, near, 0, false, now)
	if !strings.Contains(body, "今天还没有请求") || !strings.Contains(body, "另有 2 项") || strings.Count(body, "\n· ") != barkNearMax {
		t.Errorf("zh: %q", body)
	}
	for lang, w := range barkWords {
		for k := range barkWords["en"] {
			if w[k] == "" {
				t.Errorf("%s misses %s", lang, k)
			}
		}
	}
}
