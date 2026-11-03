package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// The sign-ins go in a backup only when the user asks (qianxunji and
// curtinp118 on Discord: a VPS restored from a backup had to sign in to
// every subscription again), sealed with the passphrase, and come out on
// another computer signed in.

func signInsHere(t *testing.T) string {
	t.Helper()
	var all strings.Builder
	for _, p := range []string{filepath.Join(settings.Dir(), "logins.json"), plugin.AuthPath()} {
		b, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		all.Write(b)
	}
	return all.String()
}

func TestExportCarriesSignInsWhenAskedSealed(t *testing.T) {
	home(t)
	appdir.UseExecutable("")
	subscriptionMachine(t)

	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.SignIns != nil {
		t.Fatal("sign-ins in a backup no one asked them in")
	}
	if err := WithSignIns(&b); err != nil {
		t.Fatal(err)
	}
	for _, s := range signInSecrets() {
		if !strings.Contains(carried(t, b), s) {
			t.Errorf("asked for, %q isn't in the backup", s)
		}
	}
	sealed, err := Seal(b, "pass phrase")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range signInSecrets() {
		if strings.Contains(string(sealed), s) {
			t.Fatalf("%q in the clear in the sealed file", s)
		}
	}

	// a new computer (the VPS): signed in once it is put back
	home(t)
	appdir.UseExecutable("")
	got, err := Open(sealed, "pass phrase")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Restore(got, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.SignIns == nil || r.SignIns.Added != 5 {
		t.Fatalf("result: %+v", r.SignIns)
	}
	here := signInsHere(t)
	for _, s := range signInSecrets() {
		if !strings.Contains(here, s) {
			t.Errorf("%q isn't signed in here after the restore", s)
		}
	}

	// left out of the restore when asked
	home(t)
	appdir.UseExecutable("")
	parts := All
	parts.SignIns = false
	if r, err := Restore(got, parts); err != nil || r.SignIns != nil {
		t.Fatal(r.SignIns, err)
	}
	if here := signInsHere(t); strings.Contains(here, fixtureClaudeRefresh) || strings.Contains(here, fixturePluginKey) {
		t.Fatal("sign-ins put in a restore that left them out")
	}
}

func TestAutoBackupSignInsSealedAndDeduped(t *testing.T) {
	autoHome(t)
	appdir.UseExecutable("")
	signIns(t)
	if err := SaveAuto(AutoConfig{SignIns: true}); err == nil {
		t.Fatal("sign-ins on with no passphrase")
	}
	if err := SaveAuto(AutoConfig{SignIns: true, Passphrase: "pass phrase"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(autoPath()); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("the setup holding the passphrase is readable by others: %v %v", fi.Mode(), err)
	}
	if v := AutoStatus(); !v.SignIns || !v.Passphrase {
		t.Fatalf("view: %+v", v)
	}
	if raw, _ := json.Marshal(AutoStatus()); strings.Contains(string(raw), "pass phrase") {
		t.Fatal("the passphrase is sent to the page")
	}
	b, err := AutoTick(t0, "test")
	if err != nil || b == nil || !b.SignIns {
		t.Fatal(b, err)
	}
	data, err := os.ReadFile(filepath.Join(AutoDefaultDir(), b.Name))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range signInSecrets() {
		if strings.Contains(string(data), s) {
			t.Fatalf("%q in the clear in the automatic backup", s)
		}
	}
	// a day on, the same: none made. A sign-in renewed: one made
	if b, err := AutoTick(t0.Add(24*time.Hour), "test"); err != nil || b != nil {
		t.Fatal("a backup made with nothing changed", b, err)
	}
	if err := plugin.EditAuth(func(m map[string]json.RawMessage) (bool, error) {
		m["opencode-copilot"] = json.RawMessage(`{"type":"api","key":"renewed-key"}`)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	b2, err := AutoTick(t0.Add(48*time.Hour), "test")
	if err != nil || b2 == nil {
		t.Fatal("a renewed sign-in made no backup", b2, err)
	}

	// signed out of everything, then the first one put back
	for _, p := range []string{filepath.Join(settings.Dir(), "logins.json"), plugin.AuthPath()} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	r, _, err := AutoRestore(b.Name, "test")
	if err != nil {
		t.Fatal(err)
	}
	if r.SignIns == nil || r.SignIns.Added == 0 || r.SignInsLocked {
		t.Fatalf("result: %+v locked %v", r.SignIns, r.SignInsLocked)
	}
	if here := signInsHere(t); !strings.Contains(here, fixtureClaudeRefresh) || !strings.Contains(here, fixturePluginKey) {
		t.Fatalf("not signed in after the restore:\n%s", here)
	}

	// another passphrase set since: the backup's sign-ins stay shut, the
	// rest is put back
	if err := SaveAuto(AutoConfig{SignIns: true, Passphrase: "another"}); err != nil {
		t.Fatal(err)
	}
	r, _, err = AutoRestore(b.Name, "test")
	if err != nil || !r.SignInsLocked || r.SignIns != nil {
		t.Fatalf("result: %+v locked %v %v", r.SignIns, r.SignInsLocked, err)
	}
}
