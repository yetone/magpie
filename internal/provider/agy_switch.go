package provider

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// Switching agy's own account (#1908), as magpie switches Codex's and
// Claude Code's (Provider.SwitchOnly): with "Only switch agy's sign-in" on
// for Antigravity CLI, picking one of its accounts signs agy, and the
// Antigravity app that reads the same keyring entry, in to it, and magpie
// serves no request on these accounts (KeptToAgent).
//
// The agy-cli plugin keeps agy's own account (its sign-in "agy", read
// from the keyring) and every other one in a home of its own
// (metadata.agyHome, whose agy keeps its sign-in in agyTokenFile). A
// switch to one of those trades the two sign-ins: the account picked goes
// into the keyring, the one it replaces into the picked account's home,
// so both stay signed in and the next switch can bring it back. Step by
// step, and refused at the first that fails:
//
//  1. the keyring is read: a read that fails says nothing of who is in
//     it, so the switch is refused rather than taken for "no one";
//  2. a conversation agy or the Antigravity app wrote to in the last
//     minute is a turn under way: refused, so no turn changes account
//     half-way;
//  3. the picked account's sign-in is renewed through Google with its own
//     refresh token, as the plugin renews it; Google refusing it means
//     the account is signed out, and refused here;
//  4. what the keyring holds is kept in a 0600 file first
//     (agy-keyring-backup.json, in magpie's folder);
//  5. the keyring is written in place and read back: one whose
//     id_token_email or refresh token isn't the account's is put back;
//  6. the replaced account goes into the picked one's home, and
//     plugin-auth.json and logins.json follow (agySwapAuth): a step
//     failing puts the keyring and the home back.
//
// No device id is touched, and nothing of the old Antigravity IDE (its
// state.vscdb) is either. The Antigravity app reads the keyring at start,
// so after a switch its Agents row offers a Restart (AgySwitchedAt); it is
// never restarted unasked.

// agyBusyWindow is how recently a conversation written to counts as a
// turn under way.
var agyBusyWindow = time.Minute

var agyHomeIDRe = lazyre.New(`^[a-z0-9]{8,32}$`)

// agyAccountHome is the home of the agy-cli account whose sign-in names
// id (the plugin's account.mjs homeOf), "" when id isn't one.
func agyAccountHome(id string) string {
	if !agyHomeIDRe.MatchString(id) {
		return ""
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "magpie-agy-bridge", "accounts", id)
}

// agyStored is a sign-in as agy stores it, decoded.
type agyStored struct {
	Token map[string]any `json:"token"`
	Email string         `json:"id_token_email"`
}

func agyParse(raw string) (agyStored, error) {
	var s agyStored
	if err := json.Unmarshal([]byte(raw), &s); err != nil || s.Token == nil {
		return agyStored{}, errors.New("not a sign-in agy stores")
	}
	return s, nil
}

func (s agyStored) refreshToken() string { r, _ := s.Token["refresh_token"].(string); return r }

// expiry is when the access token ends, zero when it doesn't say.
func (s agyStored) expiry() time.Time {
	v, _ := s.Token["expiry"].(string)
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil || t.Year() < 2 {
		return time.Time{}
	}
	return t
}

// agyRenew has Google renew a sign-in's access token with its own
// refresh token, through Antigravity's client as the plugin renews it:
// the sign-in with the new token in it. Google turning the refresh token
// away means the account is signed out; failing to reach Google, the
// sign-in is taken as it is while its access token has a while to run.
func agyRenew(ctx context.Context, raw string) (string, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return "", errors.New("its sign-in can't be read")
	}
	s, err := agyParse(raw)
	if err != nil || s.refreshToken() == "" {
		return "", errors.New("its sign-in can't be renewed: sign in to it again")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.refreshToken()},
		"client_id": {antigravityApp.clientID}, "client_secret": {antigravityApp.clientSecret}}
	var fresh struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	err = postToken(ctx, googleTokenURL, "application/x-www-form-urlencoded", []byte(form.Encode()), &fresh)
	var refused *tokenRefused
	switch {
	case errors.As(err, &refused) && refused.status >= 400 && refused.status < 500:
		return "", fmt.Errorf("Google no longer takes its sign-in (%s): sign in to it again", refused.msg)
	case err != nil || fresh.AccessToken == "":
		if e := s.expiry(); !e.IsZero() && time.Until(e) > 5*time.Minute {
			return raw, nil
		}
		return "", errors.New("magpie couldn't renew its sign-in with Google, and it has run out; try again")
	}
	tok, _ := m["token"].(map[string]any)
	tok["access_token"] = fresh.AccessToken
	if fresh.TokenType != "" {
		tok["token_type"] = fresh.TokenType
	}
	if fresh.ExpiresIn > 0 {
		tok["expires_in"] = fresh.ExpiresIn
		tok["expiry"] = time.Now().Add(time.Duration(fresh.ExpiresIn) * time.Second).Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// agyTurnUnderWay is a conversation agy (in the user's home, or in one
// of homes) or the Antigravity app wrote to within agyBusyWindow, "" for
// none. A folder that can't be read tells nothing either way.
func agyTurnUnderWay(homes ...string) string {
	if h, err := os.UserHomeDir(); err == nil {
		homes = append([]string{h}, homes...)
	}
	since := time.Now().Add(-agyBusyWindow)
	for _, h := range homes {
		for _, app := range []string{"antigravity", "antigravity-insiders", "antigravity-cli"} {
			dir := filepath.Join(h, ".gemini", app, "conversations")
			es, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range es {
				n := e.Name()
				if !strings.HasSuffix(n, ".db") && !strings.HasSuffix(n, ".db-wal") {
					continue
				}
				if fi, err := e.Info(); err == nil && fi.ModTime().After(since) {
					return filepath.Join(dir, n)
				}
			}
		}
	}
	return ""
}

// agyBackupPath keeps what the keyring held before magpie last switched
// agy's account: the JSON as agy stores it, readable by the user alone.
func agyBackupPath() string { return filepath.Join(settings.Dir(), "agy-keyring-backup.json") }

// agySwitchedPath has the time magpie last switched agy's account.
func agySwitchedPath() string { return filepath.Join(settings.Dir(), "agy-switched-at") }

// AgySwitchedAt is when magpie last switched agy's account, zero for
// never: the Antigravity app started before it is still on the account
// it had (agent.AppBehind).
func AgySwitchedAt() time.Time {
	b, err := os.ReadFile(agySwitchedPath())
	if err != nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(b)))
	return t
}

// agySwitchesKeyring says a switch of agy-cli's accounts is a switch of
// agy's own sign-in: the user ticked Only switch agy's sign-in. Unread,
// it isn't: the keyring is written only on the user's word.
func agySwitchesKeyring() bool {
	p, err := Find(agyCLIID)
	return err == nil && p.SwitchOnly && p.IsPlugin()
}

// agyAuthOf is an agy-cli sign-in in plugin-auth.json: the email it is
// named by and its home's id ("" for agy's own).
func agyAuthOf(raw json.RawMessage) (email, homeID string) {
	var a struct {
		Metadata struct {
			Email   string `json:"email"`
			AgyHome string `json:"agyHome"`
		} `json:"metadata"`
	}
	_ = json.Unmarshal(raw, &a)
	return a.Metadata.Email, a.Metadata.AgyHome
}

func agyCLIKey(k string) bool { return k == agyCLIID || strings.HasPrefix(k, agyCLIID+"#") }

// switchAgyLogin signs agy in to user, one of agy-cli's accounts.
func switchAgyLogin(pp plugin.Provider, user string) error {
	agent := pluginAgent(pp)
	ls := pluginSide(pp)
	i := -1
	for j, l := range ls {
		if strings.EqualFold(l.User, user) {
			i = j
			break
		}
	}
	if i < 0 {
		return fmt.Errorf("no %s account %q", pp.Name, user)
	}
	key := ls[i].saved.Home
	auths, err := plugin.AuthEntries()
	if err != nil {
		return err
	}
	raw, ok := auths[key]
	if !ok {
		return fmt.Errorf("no %s account %q", pp.Name, user)
	}
	email, homeID := agyAuthOf(raw)
	if homeID == "" { // agy's own already: only the list's order moves
		return switchSideLogin(agent, user, ls)
	}
	home := agyAccountHome(homeID)
	if home == "" {
		return fmt.Errorf("%s's sign-in names no home magpie knows", user)
	}
	kr, err := agyKeyringHere()
	if err != nil {
		return err
	}
	live, err := kr.Read()
	switch {
	case errors.Is(err, errAgyNoEntry):
		live = ""
	case err != nil:
		return fmt.Errorf("magpie couldn't read agy's sign-in in %s, so it doesn't know who agy is signed in to; nothing was changed (%v)", kr.Where(), err)
	}
	var old agyStored
	if live != "" {
		if old, err = agyParse(live); err != nil {
			return fmt.Errorf("agy's sign-in in %s isn't one magpie can read; nothing was changed", kr.Where())
		}
	}
	if f := agyTurnUnderWay(home); f != "" {
		return errors.New("agy or the Antigravity app is in the middle of a turn; switch once it has finished")
	}
	// one running keeps its account and writes it back over the switch
	lc, lcancel := context.WithTimeout(context.Background(), 20*time.Second)
	err = agyQuitFirst(lc)
	lcancel()
	if err != nil {
		return err
	}
	file := agyFileStore{path: agyTokenFile(home)}
	cur, err := file.Read()
	if err != nil {
		return fmt.Errorf("%s's sign-in can't be read; sign in to it again", user)
	}
	if s, err := agyParse(cur); err != nil || s.Email != "" && email != "" && !strings.EqualFold(s.Email, email) {
		return fmt.Errorf("%s's home holds another account's sign-in; nothing was changed", user)
	}
	ctx, cancel := context.WithTimeout(ViaLogin(context.Background(), agyCLIID, user), 30*time.Second)
	defer cancel()
	fresh, err := agyRenew(ctx, cur)
	if err != nil {
		return fmt.Errorf("%s: %w", user, err)
	}
	want, _ := agyParse(fresh)
	if want.Email == "" {
		want.Email = email
	}
	if fresh != cur {
		if err := file.Write(fresh); err != nil {
			return err
		}
	}
	if live != "" {
		if err := writePrivateQuiet(agyBackupPath(), []byte(live)); err != nil {
			return fmt.Errorf("magpie couldn't keep a copy of agy's sign-in first; nothing was changed (%v)", err)
		}
	}
	putBack := func() {
		if live == "" {
			return
		}
		if err := kr.Write(live); err != nil {
			log.Printf("agy: putting back the sign-in agy had: %v (a copy is in %s)", err, agyBackupPath())
		}
	}
	if err := kr.Write(fresh); err != nil {
		putBack()
		return fmt.Errorf("%s didn't take %s's sign-in: %w", kr.Where(), user, err)
	}
	back, err := kr.Read()
	got, perr := agyParse(back)
	if err != nil || perr != nil || !strings.EqualFold(got.Email, want.Email) || got.refreshToken() != want.refreshToken() {
		putBack()
		return fmt.Errorf("%s didn't keep %s's sign-in; agy is left on the account it had", kr.Where(), user)
	}
	if live != "" {
		if err := file.Write(live); err != nil {
			putBack()
			return fmt.Errorf("magpie couldn't keep %s's sign-in, so agy is left on it: %w", firstNonEmpty(old.Email, "the account agy had"), err)
		}
	}
	if err := agySwapAuth(agent, key, want.Email, old.Email, homeID); err != nil {
		putBack()
		_ = file.Write(fresh)
		return err
	}
	_ = writePrivateQuiet(agySwitchedPath(), []byte(time.Now().UTC().Format(time.RFC3339Nano)))
	log.Printf("agy: signed in to %s (was %s)", want.Email, firstNonEmpty(old.Email, "no one"))
	return nil
}

// agySwapAuth has plugin-auth.json and logins.json follow a switch: the
// account at key (home homeID) is now agy's own, and the one agy had
// (was, "" for none) lives in homeID. With an own sign-in listed, the two
// trade names, and logins.json's records trade keys, so each account
// keeps what it had (on, its id, its plan); with none, key becomes agy's
// own and the one agy had gets a key of its own, unless it is listed
// already. The account agy is now on goes first.
func agySwapAuth(agent, key, now, was, homeID string) error {
	ownKey := ""
	err := plugin.EditAuth(func(m map[string]json.RawMessage) (bool, error) {
		if _, ok := m[key]; !ok {
			return false, errors.New("the account was signed out meanwhile")
		}
		listed := ""
		for k, raw := range m {
			if !agyCLIKey(k) || k == key {
				continue
			}
			e, h := agyAuthOf(raw)
			if h == "" && ownKey == "" {
				ownKey = k
			} else if was != "" && strings.EqualFold(e, was) {
				listed = k
			}
		}
		set := func(k string, f func(a map[string]any, md map[string]any)) error {
			var a map[string]any
			if err := json.Unmarshal(m[k], &a); err != nil {
				return err
			}
			md, _ := a["metadata"].(map[string]any)
			if md == nil {
				md = map[string]any{}
			}
			f(a, md)
			a["metadata"] = md
			b, err := json.Marshal(a)
			m[k] = b
			return err
		}
		if ownKey != "" {
			if err := set(ownKey, func(_, md map[string]any) { md["email"] = now }); err != nil {
				return false, err
			}
			if was == "" || listed != "" {
				delete(m, key) // its home holds no other account
				return true, nil
			}
			return true, set(key, func(_, md map[string]any) { md["email"] = was })
		}
		ownKey = key
		home := map[string]any{}
		_ = json.Unmarshal(m[key], &home)
		if err := set(key, func(a, md map[string]any) {
			a["key"] = "agy"
			md["email"] = now
			delete(md, "agyHome")
		}); err != nil {
			return false, err
		}
		if was != "" && listed == "" {
			hk, _ := home["key"].(string)
			b, _ := json.Marshal(map[string]any{"type": firstNonEmpty(str(home["type"]), "api"), "key": firstNonEmpty(hk, "agy:"+homeID),
				"metadata": map[string]any{"email": was, "agyHome": homeID}})
			m[agyCLIID+"#"+agySlot()] = b
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	o, t := -1, -1
	for j, l := range ls {
		if l.Agent != agent {
			continue
		}
		switch l.Home {
		case ownKey:
			o = j
		case key:
			t = j
		}
	}
	first := t
	if o >= 0 && t >= 0 && ownKey != key {
		ls[o].Home, ls[t].Home = key, ownKey
	}
	for j := range ls {
		if ls[j].Agent == agent {
			if ls[j].First && j != first {
				ls[j].On = true // the one it replaces is next in line
			}
			ls[j].First = j == first
		}
	}
	return writeLogins(ls)
}

// agySlot is a new sign-in's slot, as the plugin host names one: six
// letters or digits.
func agySlot() string {
	const abc = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(abc))))
		b[i] = abc[n.Int64()]
	}
	return string(b)
}
