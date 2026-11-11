package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// autoHome is a machine of its own with the setup setUp makes, and the
// schedule's memory of an earlier test forgotten.
func autoHome(t *testing.T) {
	t.Helper()
	home(t)
	setUp(t)
	autoMu.Lock()
	autoChecked, autoErr = time.Time{}, ""
	clear(autoSeen)
	autoMu.Unlock()
}

func autoList(t *testing.T) []AutoBackup {
	t.Helper()
	v := AutoStatus()
	if v.Error != "" {
		t.Fatal(v.Error)
	}
	return v.Backups
}

func setTheme(t *testing.T, theme string) {
	t.Helper()
	s := settings.Load()
	s.Theme = theme
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

var t0 = time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)

// On with nothing set, a backup is made at once, then again only once the
// interval has gone by, and only when the setup changed (jwaitq on Discord).
func TestAutoBackupOnSchedule(t *testing.T) {
	autoHome(t)
	tick := func(at time.Time, want bool) {
		t.Helper()
		b, err := AutoTick(at, "test")
		if err != nil {
			t.Fatal(err)
		}
		if (b != nil) != want {
			t.Fatalf("at %v: made %+v, wanted one: %v", at, b, want)
		}
	}
	tick(t0, true)
	if l := autoList(t); len(l) != 1 || l[0].Reason != ReasonAuto || !l[0].Made.Equal(t0) || l[0].Providers != 2 {
		t.Fatalf("after the first: %+v", l)
	}
	setTheme(t, "light")
	tick(t0.Add(time.Hour), false)
	tick(t0.Add(23*time.Hour+59*time.Minute), false)
	tick(t0.Add(24*time.Hour), true)
	// a day with no change makes none, and pushes nothing out
	tick(t0.Add(48*time.Hour), false)
	setTheme(t, "dark")
	tick(t0.Add(49*time.Hour), false) // looked at 48h: the next look is a day after
	tick(t0.Add(72*time.Hour), true)
	if l := autoList(t); len(l) != 3 {
		t.Fatalf("want 3 backups, have %+v", l)
	}
	// the clock set back before the last backup: due, not waited out
	setTheme(t, "light")
	tick(t0.Add(-time.Hour), true)
}

// A magpie that starts again goes by the newest backup in the folder, not
// a fresh start: no backup at every launch.
func TestAutoBackupAfterRestartWaits(t *testing.T) {
	autoHome(t)
	if _, err := AutoTick(t0, "test"); err != nil {
		t.Fatal(err)
	}
	setTheme(t, "light")
	autoMu.Lock()
	autoChecked = time.Time{}
	clear(autoSeen)
	autoMu.Unlock()
	if b, err := AutoTick(t0.Add(2*time.Hour), "test"); err != nil || b != nil {
		t.Fatalf("made %+v, %v two hours after the last", b, err)
	}
}

// The user's own choice is kept: off stays off, and a setup that can't be
// read is unknown, so nothing is made.
func TestAutoBackupOffOrUnreadable(t *testing.T) {
	autoHome(t)
	if err := SaveAuto(AutoConfig{Off: true}); err != nil {
		t.Fatal(err)
	}
	if b, err := AutoTick(t0, "test"); err != nil || b != nil {
		t.Fatalf("off made %+v, %v", b, err)
	}
	if v := AutoStatus(); v.On || v.Hours != DefaultHours || v.Keep != DefaultKeep {
		t.Fatalf("off reads %+v", v)
	}
	if err := os.WriteFile(autoPath(), []byte(`{"off":tr`), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := AutoTick(t0, "test"); err == nil || b != nil {
		t.Fatalf("unreadable setup made %+v, %v", b, err)
	}
	if v := AutoStatus(); v.On || v.Error == "" {
		t.Fatalf("unreadable setup reads %+v", v)
	}
	if _, err := os.Stat(AutoDefaultDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a backup folder was made: %v", err)
	}
	if err := SaveAuto(AutoConfig{Hours: 5}); err == nil {
		t.Fatal("an interval Settings doesn't offer was saved")
	}
	if err := SaveAuto(AutoConfig{Dir: "relative/dir"}); err == nil {
		t.Fatal("a relative folder was saved")
	}
}

// Only the newest of each kind are kept, and nothing else in the folder is
// touched: the user may have picked one with their own files in it.
func TestAutoBackupPrunes(t *testing.T) {
	autoHome(t)
	dir := filepath.Join(t.TempDir(), "My backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveAuto(AutoConfig{Keep: 3, Dir: dir, Hours: 1}); err != nil {
		t.Fatal(err)
	}
	mine := []string{"notes.txt", "magpie-2026-11-01.magpie-backup", "magpie-20200101-000000-auto.json.bak"}
	for _, f := range mine {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var made []string
	for i := range 5 {
		if err := profile.Save("work", profile.Profile{Fields: map[string]string{"claude.model": fmt.Sprint("acme/m", i)}}); err != nil {
			t.Fatal(err)
		}
		b, err := AutoTick(t0.Add(time.Duration(i)*time.Hour), "test")
		if err != nil || b == nil {
			t.Fatalf("tick %d: %+v %v", i, b, err)
		}
		made = append(made, b.Name)
	}
	l := autoList(t)
	if len(l) != 3 || l[0].Name != made[4] || l[1].Name != made[3] || l[2].Name != made[2] {
		t.Fatalf("kept %+v, made %v", l, made)
	}
	for _, f := range mine {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("the user's %s: %v", f, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, made[4])); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("a backup others can read: %v", fi.Mode())
	}
}

// What a backup holds is what an export without keys holds, the proxies
// (plugin downloads' too) left out: nothing secret is in the file.
func TestAutoBackupHoldsNoSecrets(t *testing.T) {
	autoHome(t)
	s := settings.Load()
	s.GitHubToken, s.LANKey, s.Proxy = "ghp_SYNTHETIC_PRIVATE_TOKEN", "lan-SYNTHETIC-KEY", "http://user:proxy-SYNTHETIC-pass@10.0.0.1:8080"
	s.PluginProxy = "socks5://user:plugin-SYNTHETIC-pass@10.0.0.2:1080"
	s.UpdateProxy = "socks5://user:update-SYNTHETIC-pass@10.0.0.3:1080"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	b, err := AutoTick(t0, "test")
	if err != nil || b == nil {
		t.Fatal(b, err)
	}
	data, err := os.ReadFile(filepath.Join(AutoDefaultDir(), b.Name))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-acme", "balance-acme", "AK-acme", "SK-acme", "hdr-secret", "SYNTHETIC"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("%q in the backup", secret)
		}
	}
	var f autoFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	var got Bundle
	if err := json.Unmarshal(f.Bundle, &got); err != nil {
		t.Fatal(err)
	}
	// the export's own keyless collect, through JSON as a file carries it
	want, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	ws := *want.Settings
	ws.Proxy, ws.ProxyCustom = "", ""
	ws.PluginProxy, ws.PluginProxyCustom = "", ""
	ws.UpdateProxy, ws.UpdateProxyCustom = "", ""
	want.Settings, want.Created, want.App = &ws, time.Time{}, ""
	raw, _ := json.Marshal(want)
	var wantBack Bundle
	json.Unmarshal(raw, &wantBack)
	if !reflect.DeepEqual(got, wantBack) {
		t.Fatalf("the backup is not the keyless export:\n%s\n%s", f.Bundle, raw)
	}
}

// A restore puts the version back, keeps the keys here, and first backs up
// what was here, so restoring that one undoes it.
func TestAutoRestoreRoundTripAndUndo(t *testing.T) {
	autoHome(t)
	a, err := AutoTick(t0, "test")
	if err != nil || a == nil {
		t.Fatal(a, err)
	}
	// then the setup changes: another address, theme and profile
	p, _ := provider.Find("acme")
	p.Chat = "https://changed.example.com/v1"
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	setTheme(t, "light")
	if err := profile.Save("work", profile.Profile{Fields: map[string]string{"claude.model": "beta/m2"}}); err != nil {
		t.Fatal(err)
	}

	r, before, err := AutoRestore(a.Name, "test")
	if err != nil {
		t.Fatal(err)
	}
	if r.Replaced != 2 || !r.Settings || r.Profiles != 1 || before.Reason != ReasonRestore {
		t.Fatalf("restore: %+v, before %+v", r, before)
	}
	p, _ = provider.Find("acme")
	if p.Chat != "https://acme.example.com/v1" || p.Key != "sk-acme" || p.BalanceToken != "balance-acme" || p.Headers["X-Api-Key"] != "hdr-secret" {
		t.Fatalf("acme after the restore: %+v", p)
	}
	if s := settings.Load(); s.Theme != "dark" {
		t.Fatalf("theme %q", s.Theme)
	}
	if ps, _ := profile.Load(); ps["work"].Fields["claude.model"] != "acme/m1" {
		t.Fatalf("profile %+v", ps["work"])
	}
	l := autoList(t)
	if len(l) != 2 || !slices.ContainsFunc(l, func(b AutoBackup) bool { return b.Name == before.Name }) {
		t.Fatalf("list after the restore: %+v", l)
	}

	// undo: the backup of what was here before it
	if _, _, err := AutoRestore(before.Name, "test"); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("acme")
	if p.Chat != "https://changed.example.com/v1" || p.Key != "sk-acme" {
		t.Fatalf("acme after the undo: %+v", p)
	}
	if s := settings.Load(); s.Theme != "light" {
		t.Fatalf("theme after the undo %q", s.Theme)
	}
	if ps, _ := profile.Load(); ps["work"].Fields["claude.model"] != "beta/m2" {
		t.Fatalf("profile after the undo %+v", ps["work"])
	}
}

// A setup here that can't be read can't be backed up first, so nothing is
// put back over it.
func TestAutoRestoreNeedsTheBackupFirst(t *testing.T) {
	autoHome(t)
	a, err := AutoTick(t0, "test")
	if err != nil || a == nil {
		t.Fatal(a, err)
	}
	p, _ := provider.Find("acme")
	p.Chat = "https://changed.example.com/v1"
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	// settings.json there but not readable (a folder at its name: the same
	// on every system, where a file's mode isn't)
	if err := os.Remove(settings.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(settings.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AutoRestore(a.Name, "test"); err == nil {
		t.Fatal("restored with no backup of what was here")
	}
	if p, _ := provider.Find("acme"); p.Chat != "https://changed.example.com/v1" {
		t.Fatalf("acme put back anyway: %+v", p)
	}
	if _, _, err := AutoRestore("../settings.json", "test"); err == nil {
		t.Fatal("a name outside the backups was read")
	}
}

// A backup cut short, damaged, or still being written is never listed or
// put back, and a write that stops before its rename leaves nothing at a
// backup's name.
func TestAutoHalfWrittenNeverListed(t *testing.T) {
	autoHome(t)
	good, err := AutoTick(t0, "test")
	if err != nil || good == nil {
		t.Fatal(good, err)
	}
	dir := AutoDefaultDir()
	whole, err := os.ReadFile(filepath.Join(dir, good.Name))
	if err != nil {
		t.Fatal(err)
	}
	cut := "magpie-20261101-000000-auto.json"
	if err := os.WriteFile(filepath.Join(dir, cut), whole[:len(whole)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	changed := "magpie-20261101-000001-auto.json"
	if err := os.WriteFile(filepath.Join(dir, changed), []byte(strings.Replace(string(whole), `"theme":"dark"`, `"theme":"pink"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".magpie-backup-123.tmp"), whole, 0o600); err != nil {
		t.Fatal(err)
	}
	if l := autoList(t); len(l) != 1 || l[0].Name != good.Name {
		t.Fatalf("listed %+v", l)
	}
	for _, n := range []string{cut, changed} {
		if _, _, err := AutoRestore(n, "test"); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("restored %s: %v", n, err)
		}
	}

	// the write stops after the temporary file is full, before its rename
	autoRename = func(string, string) error { return errors.New("the machine went off") }
	t.Cleanup(func() { autoRename = os.Rename })
	setTheme(t, "light")
	if b, err := AutoNow("test"); err == nil {
		t.Fatalf("made %+v", b)
	}
	if l := autoList(t); len(l) != 1 || l[0].Name != good.Name {
		t.Fatalf("listed after a stopped write %+v", l)
	}
	tmps, _ := filepath.Glob(filepath.Join(dir, autoTemp))
	if len(tmps) != 1 { // the test's own, not the stopped write's
		t.Fatalf("temporary files left: %v", tmps)
	}
}

// Backups in magpie's own folder, the default, are said to be there: a
// reinstalled system or a wiped disk takes them along with the setup
// (two_2025. on Discord, whose magpie is on C:). One picked elsewhere
// isn't, nor one in a folder whose name only starts like magpie's.
func TestAutoInsideMagpiesFolder(t *testing.T) {
	autoHome(t)
	if v := AutoStatus(); !v.Inside || v.Custom {
		t.Fatalf("default folder %q: inside %v, custom %v; want inside", v.Dir, v.Inside, v.Custom)
	}
	in := filepath.Join(settings.Dir(), "mine")
	if err := SaveAuto(AutoConfig{Dir: in}); err != nil {
		t.Fatal(err)
	}
	if v := AutoStatus(); !v.Inside {
		t.Errorf("picked %q in magpie's folder: not said to be inside", v.Dir)
	}
	for _, out := range []string{t.TempDir(), settings.Dir() + "-backups"} {
		if err := SaveAuto(AutoConfig{Dir: out}); err != nil {
			t.Fatal(err)
		}
		if v := AutoStatus(); v.Inside {
			t.Errorf("picked %q elsewhere: said to be inside %q", v.Dir, settings.Dir())
		}
	}
	if runtime.GOOS != "linux" {
		if err := SaveAuto(AutoConfig{Dir: strings.ToUpper(settings.Dir())}); err != nil {
			t.Fatal(err)
		}
		if v := AutoStatus(); !v.Inside {
			t.Errorf("magpie's folder in other letter case %q: not said to be inside", v.Dir)
		}
	}
}
