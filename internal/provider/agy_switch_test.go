package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// agyFakeKeyring stands in for agy's keyring entry: never the system's.
type agyFakeKeyring struct {
	value   string
	readErr error
	// keeps, when set, is what the keyring holds after a write instead of
	// the value written: a keyring that didn't keep it
	keeps  func(string) string
	writes []string
}

func (k *agyFakeKeyring) Where() string { return "the test's keyring" }

func (k *agyFakeKeyring) Read() (string, error) {
	if k.readErr != nil {
		return "", k.readErr
	}
	if k.value == "" {
		return "", errAgyNoEntry
	}
	return k.value, nil
}

func (k *agyFakeKeyring) Write(v string) error {
	k.writes = append(k.writes, v)
	if k.keeps != nil {
		v = k.keeps(v)
	}
	k.value = v
	return nil
}

func agyValue(email, refresh, access string, expiry time.Time) string {
	b, _ := json.Marshal(map[string]any{
		"token": map[string]any{"access_token": access, "refresh_token": refresh, "expiry": expiry.Format(time.RFC3339Nano),
			"expires_in": 3599, "token_type": "Bearer"},
		"auth_method": "consumer", "id_token_email": email,
	})
	return string(b)
}

// agyWorld is a sandbox HOME with agy-cli's plugin listed, agy signed in
// to a@x.com in the stand-in keyring, b@x.com in a home of its own, and
// Google's token endpoint answered by the test.
type agyWorldT struct {
	kr       *agyFakeKeyring
	bHome    string
	google   *atomic.Int32
	refuse   atomic.Bool
	googleUp atomic.Bool
}

func agyWorld(t *testing.T, ownListed bool) *agyWorldT {
	t.Helper()
	home := signIn(t)
	t.Setenv("MAGPIE_BUN", "") // nothing here runs a plugin
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	for _, k := range agyEnvSignIns {
		t.Setenv(k, "")
	}
	w := &agyWorldT{kr: &agyFakeKeyring{value: agyValue("a@x.com", "ra", "acc-a", time.Now().Add(time.Hour))}, google: &atomic.Int32{}}
	w.googleUp.Store(true)
	old := agyKeyringHere
	agyKeyringHere = func() (agyKeyring, error) { return w.kr, nil }
	t.Cleanup(func() {
		agyKeyringHere = old
		agyPending.Lock()
		agyPending.from, agyPending.to, agyPending.said, agyPending.running = "", "", false, AgyRunning{}
		agyPending.Unlock()
	})
	agyRunning(t) // nothing runs on agy's sign-in, whatever runs here
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.google.Add(1)
		_ = r.ParseForm()
		if !w.googleUp.Load() {
			rw.WriteHeader(http.StatusBadGateway)
			return
		}
		if w.refuse.Load() || r.Form.Get("client_id") != antigravityApp.clientID || r.Form.Get("refresh_token") != "rb" {
			rw.WriteHeader(http.StatusBadRequest)
			_, _ = rw.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
			return
		}
		_, _ = rw.Write([]byte(`{"access_token":"fresh-b","expires_in":3599,"token_type":"Bearer"}`))
	}))
	t.Cleanup(srv.Close)
	oldURL := googleTokenURL
	googleTokenURL = srv.URL
	t.Cleanup(func() { googleTokenURL = oldURL })

	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"plugins": []map[string]any{{"spec": agyCLIPackage}}})
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	plugin.UseCached([]plugin.Provider{{ID: agyCLIID, Name: "Antigravity CLI", Spec: agyCLIPackage}})
	t.Cleanup(func() { plugin.UseCached(nil) })
	auth := map[string]any{
		"agy-cli#bbbbbb": map[string]any{"type": "api", "key": "agy:homeb0001", "metadata": map[string]any{"email": "b@x.com", "agyHome": "homeb0001"}},
	}
	if ownListed {
		auth["agy-cli"] = map[string]any{"type": "api", "key": "agy", "metadata": map[string]any{"email": "a@x.com"}}
	}
	writePluginAuth(t, auth)
	w.bHome = agyAccountHome("homeb0001")
	if err := os.MkdirAll(filepath.Dir(agyTokenFile(w.bHome)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agyTokenFile(w.bHome), []byte(agyValue("b@x.com", "rb", "acc-b", time.Now().Add(-time.Minute))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetSwitchOnly(agyCLIID, true); err != nil {
		t.Fatal(err)
	}
	return w
}

func agyAuthEmails(t *testing.T) map[string]string {
	t.Helper()
	m, err := plugin.AuthEntries()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for k, raw := range m {
		e, h := agyAuthOf(raw)
		out[k] = e + "@" + h
	}
	return out
}

func agyFirst(t *testing.T) string {
	t.Helper()
	for _, l := range Logins(agyCLIID) {
		if l.Active {
			return l.User
		}
	}
	return ""
}

// With Only switch agy's sign-in on, picking one of Antigravity CLI's
// accounts signs agy in to it: the keyring holds its renewed sign-in, the
// account agy had moves into the picked account's home, plugin-auth.json
// and the accounts list follow, what the keyring held is kept in a 0600
// copy, and the switch is recorded for the Antigravity app's Restart.
// Switching back trades them again.
func TestAgySwitchTradesSignIns(t *testing.T) {
	w := agyWorld(t, true)
	if p, err := Find(agyCLIID); err != nil || !p.SwitchOnly || !p.KeptToAgent() || p.On() {
		t.Fatalf("Find = %+v, %v: switch-only isn't kept to agy", p, err)
	}
	if got := agyFirst(t); got != "a@x.com" {
		t.Fatalf("first is %q", got)
	}
	was := w.kr.value
	if err := SwitchLogin(agyCLIID, "b@x.com"); err != nil {
		t.Fatal(err)
	}
	s, err := agyParse(w.kr.value)
	if err != nil || s.Email != "b@x.com" || s.refreshToken() != "rb" || s.Token["access_token"] != "fresh-b" {
		t.Fatalf("the keyring holds %s", w.kr.value)
	}
	if !s.expiry().After(time.Now().Add(50 * time.Minute)) {
		t.Fatalf("the renewed sign-in's expiry is %v", s.expiry())
	}
	if w.google.Load() != 1 {
		t.Fatalf("Google was asked %d times", w.google.Load())
	}
	if b, _ := os.ReadFile(agyTokenFile(w.bHome)); string(b) != was {
		t.Fatalf("b's home holds %s, not the sign-in agy had", b)
	}
	if got := agyAuthEmails(t); got["agy-cli"] != "b@x.com@" || got["agy-cli#bbbbbb"] != "a@x.com@homeb0001" || len(got) != 2 {
		t.Fatalf("plugin-auth.json: %v", got)
	}
	if got := agyFirst(t); got != "b@x.com" {
		t.Fatalf("first is %q after the switch", got)
	}
	if fi, err := os.Stat(agyBackupPath()); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("backup: %v, %v", fi, err)
	}
	if b, _ := os.ReadFile(agyBackupPath()); string(b) != was {
		t.Fatalf("the backup holds %s", b)
	}
	if at := AgySwitchedAt(); time.Since(at) > time.Minute {
		t.Fatalf("AgySwitchedAt = %v", at)
	}

	// and back: a's refresh token is its own, which this Google refuses,
	// but its access token has an hour to run
	w.googleUp.Store(false)
	if err := SwitchLogin(agyCLIID, "a@x.com"); err != nil {
		t.Fatal(err)
	}
	if s, _ := agyParse(w.kr.value); s.Email != "a@x.com" || s.refreshToken() != "ra" {
		t.Fatalf("the keyring holds %s after switching back", w.kr.value)
	}
	if b, _ := os.ReadFile(agyTokenFile(w.bHome)); !strings.Contains(string(b), `"rb"`) {
		t.Fatalf("b's home holds %s", b)
	}
	if got := agyAuthEmails(t); got["agy-cli"] != "a@x.com@" || got["agy-cli#bbbbbb"] != "b@x.com@homeb0001" {
		t.Fatalf("plugin-auth.json: %v", got)
	}
	if got := agyFirst(t); got != "a@x.com" {
		t.Fatalf("first is %q after switching back", got)
	}
}

// Each account keeps what magpie keeps of it in logins.json (ticked off
// here) across a switch: the record goes with the account, not the key.
func TestAgySwitchKeepsEachAccountsRecord(t *testing.T) {
	agyWorld(t, true)
	Logins(agyCLIID) // listed, as the page lists them
	loginsMu.Lock()
	ls := readLogins()
	for i := range ls {
		if ls[i].Home == "agy-cli#bbbbbb" {
			ls[i].Plan = "b-plan"
		}
	}
	if err := writeLogins(ls); err != nil {
		loginsMu.Unlock()
		t.Fatal(err)
	}
	loginsMu.Unlock()
	if err := SwitchLogin(agyCLIID, "b@x.com"); err != nil {
		t.Fatal(err)
	}
	for _, l := range readLogins() {
		if l.Agent != pluginAgent(plugin.Provider{ID: agyCLIID}) {
			continue
		}
		if (l.Plan == "b-plan") != (l.Home == "agy-cli") {
			t.Fatalf("b's record is at %s (%+v)", l.Home, l)
		}
	}
}

// With no own sign-in listed, the picked account becomes agy's own and
// the one agy had gets a sign-in of its own in the picked one's home.
func TestAgySwitchWithNoOwnSignInListed(t *testing.T) {
	w := agyWorld(t, false)
	if err := SwitchLogin(agyCLIID, "b@x.com"); err != nil {
		t.Fatal(err)
	}
	if s, _ := agyParse(w.kr.value); s.Email != "b@x.com" {
		t.Fatalf("the keyring holds %s", w.kr.value)
	}
	got := agyAuthEmails(t)
	if got["agy-cli#bbbbbb"] != "b@x.com@" || len(got) != 2 {
		t.Fatalf("plugin-auth.json: %v", got)
	}
	var a string
	for k, v := range got {
		if k != "agy-cli#bbbbbb" {
			a = v
		}
	}
	if a != "a@x.com@homeb0001" {
		t.Fatalf("a's sign-in: %q (%v)", a, got)
	}
}

// Every refusal leaves agy as it was: the keyring unwritten (or written
// back), b's home as it was.
func TestAgySwitchRefusals(t *testing.T) {
	cases := []struct {
		name string
		set  func(t *testing.T, w *agyWorldT)
		want string
	}{
		{"keyring unreadable", func(t *testing.T, w *agyWorldT) { w.kr.readErr = errors.New("the keychain asked for a password") }, "doesn't know who agy is signed in to"},
		{"signed out at Google", func(t *testing.T, w *agyWorldT) { w.refuse.Store(true) }, "sign in to it again"},
		{"Google down, token run out", func(t *testing.T, w *agyWorldT) { w.googleUp.Store(false) }, "has run out"},
		{"a turn under way", func(t *testing.T, w *agyWorldT) {
			home, _ := os.UserHomeDir()
			f := filepath.Join(home, ".gemini", "antigravity", "conversations", "c1.db-wal")
			if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "middle of a turn"},
		{"the keyring doesn't keep it", func(t *testing.T, w *agyWorldT) {
			w.kr.keeps = func(v string) string {
				if strings.Contains(v, "b@x.com") {
					return agyValue("someone@else", "rz", "z", time.Now())
				}
				return v
			}
		}, "didn't keep"},
		{"go-keyring's encoding read back", func(t *testing.T, w *agyWorldT) {
			w.kr.value = "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte("not json"))
		}, "isn't one magpie can read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := agyWorld(t, true)
			c.set(t, w)
			was, bWas := w.kr.value, ""
			if b, err := os.ReadFile(agyTokenFile(w.bHome)); err == nil {
				bWas = string(b)
			}
			err := SwitchLogin(agyCLIID, "b@x.com")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("SwitchLogin = %v, want %q", err, c.want)
			}
			if w.kr.value != was {
				t.Fatalf("the keyring holds %s, not %s", w.kr.value, was)
			}
			// b's home holds b's sign-in still: as it was, or renewed
			if b, _ := os.ReadFile(agyTokenFile(w.bHome)); string(b) != bWas {
				if s, err := agyParse(string(b)); err != nil || s.Email != "b@x.com" || s.refreshToken() != "rb" {
					t.Fatalf("b's home holds %s", b)
				}
			}
			if got := agyAuthEmails(t); got["agy-cli"] != "a@x.com@" {
				t.Fatalf("plugin-auth.json: %v", got)
			}
			if got := agyFirst(t); got != "a@x.com" {
				t.Fatalf("first is %q", got)
			}
			if !AgySwitchedAt().IsZero() {
				t.Fatal("a refused switch was recorded")
			}
		})
	}
}

// Only switch off, a pick moves magpie's order alone: agy's keyring is
// never touched, as before.
func TestAgySwitchOffLeavesKeyring(t *testing.T) {
	w := agyWorld(t, true)
	if err := SetSwitchOnly(agyCLIID, false); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find(agyCLIID); p.KeptToAgent() {
		t.Fatal("kept to agy with Only switch off")
	}
	was := w.kr.value
	if err := SwitchLogin(agyCLIID, "b@x.com"); err != nil {
		t.Fatal(err)
	}
	if len(w.kr.writes) != 0 || w.kr.value != was || w.google.Load() != 0 {
		t.Fatalf("the keyring was written: %v", w.kr.writes)
	}
	if got := agyFirst(t); got != "b@x.com" {
		t.Fatalf("first is %q", got)
	}
}

// agy's file sign-in (WSL, a Linux with no D-Bus session) is the one the
// switch writes there: replaced whole, readable by the user alone.
func TestAgyFileKeyring(t *testing.T) {
	home := t.TempDir()
	f := agyFileStore{path: agyTokenFile(home)}
	if _, err := f.Read(); !errors.Is(err, errAgyNoEntry) {
		t.Fatalf("a missing file read as %v", err)
	}
	v := agyValue("a@x.com", "ra", "x", time.Now())
	if err := f.Write(v); err != nil {
		t.Fatal(err)
	}
	if got, err := f.Read(); err != nil || got != v {
		t.Fatalf("Read = %q, %v", got, err)
	}
	if fi, _ := os.Stat(f.path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(f.path, 0); err != nil {
			t.Fatal(err)
		}
		if os.Getuid() != 0 {
			if _, err := f.Read(); err == nil || errors.Is(err, errAgyNoEntry) {
				t.Fatalf("an unreadable file read as %v", err)
			}
		}
	}
}

// go-keyring's encodings read as the JSON they hold.
func TestAgyDecode(t *testing.T) {
	v := `{"token":{}}`
	for _, raw := range []string{v, "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(v)), "go-keyring-encoded:7b22746f6b656e223a7b7d7d", v + "\n"} {
		if got := agyDecode(raw); got != v {
			t.Fatalf("agyDecode(%q) = %q", raw, got)
		}
	}
}

// A sign-in agy takes from the environment is what agy runs on: the
// keyring isn't switched.
func TestAgyEnvSignInRefused(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "k")
	if _, err := agySystemKeyring(); err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Fatalf("err = %v", err)
	}
}
