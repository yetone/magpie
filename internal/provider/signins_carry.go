package provider

// Sign-ins a backup or a sync carries, when the user turned that on
// ("Include sign-ins", off by default): the subscription accounts magpie
// keeps in logins.json (Codex, Claude Code, Gemini CLI, Copilot, …) and
// the plugins' sign-ins in plugin-auth.json. They go only inside a sealed
// backup, and are put back without writing over what is newer here:
//
//   - an account is told by who it is (email and organization or
//     workspace, or its name) and matched against every account here
//     that could be the same one (sameLogin, and for Codex the refresh
//     token too). One that matches more than one is left as it is here;
//   - of two copies of one account, the one renewed later stays (the
//     vendor rotates refresh tokens, so the older one may be spent); when
//     either can't say when it was renewed, the copy here stays;
//   - the agent's own sign-in here (Claude Code's keychain item, Codex's
//     auth.json and the accounts those are signed in to) is never written
//     over. Codex's auth.json is written, temp-and-rename, only when Codex
//     is signed out here and the user hasn't removed its account; Claude
//     Code's keychain is never touched: a saved Claude account is served
//     by the gateway as it is.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/plugin"
)

// SignIns are the sign-ins a backup carries.
type SignIns struct {
	// Logins are the subscription accounts magpie keeps, with their
	// credentials: logins.json's records as written there.
	Logins []savedLogin `json:"logins,omitempty"`
	// Plugins are the plugins' sign-ins by key, plugin-auth.json's.
	Plugins map[string]json.RawMessage `json:"plugins,omitempty"`
	// Codex is the record (SignInID) of the account Codex was signed in to
	// where these were collected: put back where Codex is signed out.
	Codex string `json:"codex,omitempty"`
}

// SignInsResult says what putting sign-ins back did.
type SignInsResult struct {
	Added   int `json:"added,omitempty"`   // accounts that weren't here
	Updated int `json:"updated,omitempty"` // accounts here renewed later where they came from
	// Kept is the accounts left as they are here: renewed later here, the
	// agent's own sign-in, or more than one account here could be it.
	Kept int `json:"kept,omitempty"`
	// Codex: Codex was signed out here, and is signed in now.
	Codex bool `json:"codex,omitempty"`
}

// signInRec is one carried sign-in: an account or a plugin's.
type signInRec struct {
	id    string
	hash  string    // of the credential alone
	at    time.Time // when it was renewed, zero when it doesn't say
	login *savedLogin
	key   string // plugin-auth.json's key
	raw   json.RawMessage
}

// carried is whether an account of logins.json goes with the sign-ins:
// one holding its credential in the file, magpie's own copy — not the
// agent's own sign-in read in place, one kept in a folder of its own
// (Home), one removed in magpie, a plugin's or a retired built-in's.
func carried(l savedLogin) bool {
	return !l.own() && l.Home == "" && l.Hidden == "" &&
		!strings.HasPrefix(l.Agent, "plugin:") && !BuiltinRetired(l.Agent)
}

// loginRecID is who an account is, the same on every computer.
func loginRecID(l savedLogin) string {
	var who, org string
	switch l.Agent {
	case "claude":
		who, org = claudeWho(l.Profile)
	case "codex":
		who, org = codexWho(l.Auth)
	}
	if who != "" && org != "" {
		return l.Agent + "|" + strings.ToLower(who) + "|" + org
	}
	return l.Agent + "|" + accountKey(l.User)
}

func pluginRecID(key string, raw json.RawMessage) string {
	if who := plugin.AuthWho(raw); who != "" {
		return "plugin|" + plugin.ProviderOf(key) + "|" + strings.ToLower(who)
	}
	return "plugin|" + key
}

func credHash(b []byte) string {
	var c bytes.Buffer
	if json.Compact(&c, b) != nil {
		c.Reset()
		c.Write(b)
	}
	s := sha256.Sum256(c.Bytes())
	return hex.EncodeToString(s[:12])
}

// credTime is when a credential was renewed, as it says itself: Codex's
// last_refresh, an access token's expiry (Claude's expiresAt, Google's
// expiry_date, a plugin's expires), else when magpie renewed it. Zero:
// it doesn't say.
func credTime(auth json.RawMessage, renewed time.Time) time.Time {
	var a struct {
		LastRefresh string `json:"last_refresh"`
		Claude      *struct {
			ExpiresAt int64 `json:"expiresAt"`
		} `json:"claudeAiOauth"`
		ExpiresAt  int64 `json:"expiresAt"`
		ExpiryDate int64 `json:"expiry_date"`
		Expires    int64 `json:"expires"`
	}
	if json.Unmarshal(auth, &a) == nil {
		if t, err := time.Parse(time.RFC3339Nano, a.LastRefresh); err == nil {
			return t
		}
		for _, ms := range []int64{func() int64 {
			if a.Claude != nil {
				return a.Claude.ExpiresAt
			}
			return 0
		}(), a.ExpiresAt, a.ExpiryDate, a.Expires} {
			if ms > 0 {
				return time.UnixMilli(ms)
			}
		}
	}
	return renewed
}

// records are s's sign-ins by id. Two of one id (accounts told apart here
// in a way that doesn't carry) are left out both: neither is put over the
// other, nor anything here.
func (s *SignIns) records() map[string]signInRec {
	out := map[string]signInRec{}
	if s == nil {
		return out
	}
	twice := map[string]bool{}
	add := func(r signInRec) {
		if _, ok := out[r.id]; ok || twice[r.id] {
			delete(out, r.id)
			twice[r.id] = true
			return
		}
		out[r.id] = r
	}
	for i := range s.Logins {
		l := s.Logins[i]
		if !carried(l) {
			continue
		}
		add(signInRec{id: loginRecID(l), hash: credHash(l.Auth), at: credTime(l.Auth, l.Renewed), login: &l})
	}
	for k, raw := range s.Plugins {
		var at time.Time
		if ms := plugin.AuthExpires(raw); ms > 0 {
			at = time.UnixMilli(ms)
		}
		add(signInRec{id: pluginRecID(k, raw), hash: credHash(raw), at: at, key: k, raw: raw})
	}
	return out
}

// Hashes are s's sign-ins as their ids and the hashes of their
// credentials: what a sync compares, never the credentials themselves.
func (s *SignIns) Hashes() map[string]string {
	out := map[string]string{}
	for id, r := range s.records() {
		out[id] = r.hash
	}
	return out
}

// readLoginsFile is logins.json's accounts, read now: an error when it
// can't be read, never none.
func readLoginsFile() ([]savedLogin, error) {
	b, err := lastgood.Read(loginsPath(), validLogins)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("logins.json can't be read: %w", err)
	}
	ls, err := parseLogins(b)
	if err != nil {
		return nil, fmt.Errorf("logins.json can't be read: %w", err)
	}
	return ls, nil
}

// CollectSignIns gathers the sign-ins to carry. Codex's own sign-in here
// is taken from its auth.json, fresher than magpie's copy of it. A file
// that can't be read is an error: carried as no sign-ins, a sync would
// take them away elsewhere.
func CollectSignIns() (*SignIns, error) {
	if discoveryOff() {
		return nil, errAccountDiscoveryOff
	}
	ls, err := readLoginsFile()
	if err != nil {
		return nil, err
	}
	pl, err := plugin.AuthEntries()
	if err != nil {
		return nil, fmt.Errorf("the plugins' sign-ins can't be read: %w", err)
	}
	s := &SignIns{Plugins: pl}
	live, _, lerr := readCodexLogin(codexHome(), false)
	for _, l := range ls {
		if !carried(l) {
			continue
		}
		l.Held, l.was = false, ""
		if l.Agent == "codex" && lerr == nil && liveCodex(l, live) {
			l.Auth = append(json.RawMessage(nil), bytes.TrimSpace(live)...)
			s.Codex = loginRecID(l)
		}
		s.Logins = append(s.Logins, l)
	}
	if len(s.Plugins) == 0 {
		s.Plugins = nil
	}
	return s, nil
}

// liveCodex says l is the account Codex is signed in to with auth.
func liveCodex(l savedLogin, auth []byte) bool {
	if l.Agent != "codex" || len(auth) == 0 {
		return false
	}
	in := savedLogin{Agent: "codex", Auth: auth}
	e, w := codexWho(auth)
	in.User = e
	if w == "" {
		in.User = "\x00" // a name alone doesn't match: unknown
	}
	return sameLogin(l, in) || sameRefresh(l.Auth, auth)
}

// SignInPlan is what a sync puts in and takes away here (MergeSignIns),
// made over the sign-ins it read here: a sign-in changed here since is
// left alone.
type SignInPlan struct {
	put    []signInRec
	remove []string
	seen   map[string]string // id → hash read here; absent: not here
	codex  *signInRec        // for Codex where it is signed out
}

// Empty says the plan changes nothing.
func (p SignInPlan) Empty() bool { return len(p.put) == 0 && len(p.remove) == 0 && p.codex == nil }

// newer says a was renewed after b, both saying when.
func newer(a, b signInRec) bool { return !a.at.IsZero() && !b.at.IsZero() && a.at.After(b.at) }

// MergeSignIns merges this computer's sign-ins with the server's. prev is
// the sign-ins (id → hash) the last sync left on the server and here, nil
// for none known. Of an account on both sides, the one renewed later goes
// on (when neither says, the side that changed since prev); one gone from
// a side since prev was removed there, and goes from the other, unless it
// changed there since. It gives what goes up, what to do here, and what
// goes up's hashes.
func MergeSignIns(local, remote *SignIns, prev map[string]string) (*SignIns, SignInPlan, map[string]string) {
	lr, rr := local.records(), remote.records()
	plan := SignInPlan{seen: map[string]string{}}
	for id, r := range lr {
		plan.seen[id] = r.hash
	}
	shared := map[string]signInRec{}
	for id, l := range lr {
		r, ok := rr[id]
		p, was := prev[id]
		switch {
		case ok && r.hash == l.hash:
			shared[id] = l
		case ok && newer(l, r):
			shared[id] = l
		case ok && newer(r, l):
			shared[id] = r
			plan.put = append(plan.put, r)
		case ok:
			// neither says it was renewed later: the side changed since
			// the last sync goes on, and when both did, or there was no
			// last sync, the one here stays here
			if lc, rc := !was || p != l.hash, !was || p != r.hash; rc && !lc {
				shared[id] = r
				plan.put = append(plan.put, r)
			} else {
				shared[id] = l
			}
		case remote != nil && was && p == l.hash:
			// removed on another computer, unchanged here since
			plan.remove = append(plan.remove, id)
		default:
			shared[id] = l
		}
	}
	for id, r := range rr {
		if _, ok := lr[id]; ok {
			continue
		}
		if p, was := prev[id]; was && p == r.hash {
			continue // removed here since the last sync
		}
		shared[id] = r
		plan.put = append(plan.put, r)
	}
	out := &SignIns{}
	hashes := map[string]string{}
	for _, id := range slices.Sorted(maps.Keys(shared)) {
		r := shared[id]
		hashes[id] = r.hash
		if r.login != nil {
			out.Logins = append(out.Logins, *r.login)
			continue
		}
		if out.Plugins == nil {
			out.Plugins = map[string]json.RawMessage{}
		}
		out.Plugins[r.key] = r.raw
	}
	switch {
	case local != nil && local.Codex != "":
		out.Codex = local.Codex
	case remote != nil:
		out.Codex = remote.Codex
	}
	// a computer joining: Codex, signed out here, is signed in to the
	// account it was on elsewhere. Only then: one signed out of it here
	// later stays signed out
	if c, ok := shared[out.Codex]; ok && c.login != nil && prev == nil {
		plan.codex = &c
	}
	sort.Slice(plan.put, func(i, j int) bool { return plan.put[i].id < plan.put[j].id })
	return out, plan, hashes
}

// RestoreSignIns puts a backup's sign-ins in: those not here are added,
// those here renewed later where the backup was made are updated, nothing
// is taken away, and Codex is signed in to its account when it is signed
// out here.
func RestoreSignIns(s *SignIns) (SignInsResult, error) {
	if s == nil {
		return SignInsResult{}, nil
	}
	here, err := CollectSignIns()
	if err != nil {
		return SignInsResult{}, err
	}
	lr := here.records()
	plan := SignInPlan{seen: here.Hashes()}
	var kept int
	for _, r := range s.records() {
		l, ok := lr[r.id]
		switch {
		case !ok, newer(r, l):
			plan.put = append(plan.put, r)
		case l.hash != r.hash:
			kept++
		}
	}
	if c, ok := s.records()[s.Codex]; ok && c.login != nil {
		plan.codex = &c
	}
	res, err := ApplySignIns(plan)
	res.Kept += kept
	return res, err
}

// ApplySignIns does a plan here: under logins.json's lock, and
// plugin-auth.json's, each read afresh.
func ApplySignIns(p SignInPlan) (SignInsResult, error) {
	var res SignInsResult
	if p.Empty() {
		return res, nil
	}
	if discoveryOff() {
		return res, errAccountDiscoveryOff
	}
	gone := map[string]bool{}
	for _, id := range p.remove {
		gone[id] = true
	}
	var logins, plugins []signInRec
	for _, r := range p.put {
		if r.login != nil {
			logins = append(logins, r)
		} else {
			plugins = append(plugins, r)
		}
	}
	if err := applyLogins(logins, gone, p.seen, &res); err != nil {
		return res, err
	}
	if err := applyPlugins(plugins, gone, p.seen, &res); err != nil {
		return res, err
	}
	if p.codex != nil {
		ok, err := signCodexIn(*p.codex.login)
		if err != nil {
			return res, err
		}
		res.Codex = ok
	}
	return res, nil
}

func applyLogins(put []signInRec, gone map[string]bool, seen map[string]string, res *SignInsResult) error {
	if len(put) == 0 && !hasPrefixed(gone, "plugin|", false) {
		return nil
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls, err := readLoginsFile()
	if err != nil {
		return err
	}
	live, _, lerr := readCodexLogin(codexHome(), false)
	// the agent's own sign-in, and the accounts it is signed in to: never
	// written over nor taken away from here
	protected := func(l savedLogin) bool {
		return l.Held || (lerr == nil && liveCodex(l, live))
	}
	now := map[string]string{} // as it is here now
	for _, l := range ls {
		if carried(l) {
			now[loginRecID(l)] = credHash(l.Auth)
		}
	}
	changed := false
	for _, r := range put {
		in := *r.login
		if now[r.id] != seen[r.id] { // changed here since it was read
			res.Kept++
			continue
		}
		var at []int
		for i, l := range ls {
			if l.Agent == in.Agent && (sameLogin(l, in) || in.Agent == "codex" && sameRefresh(l.Auth, in.Auth) || loginRecID(l) == r.id) {
				at = append(at, i)
			}
		}
		switch {
		case len(at) == 0:
			in.Held, in.First, in.Order, in.was = false, false, 0, ""
			if slices.ContainsFunc(ls, func(l savedLogin) bool { return l.ID != "" && l.ID == in.ID }) {
				in.ID = ""
			}
			if in.Agent == "codex" && accountRemoved("codex") || in.Agent == "claude" && accountRemoved("claude") {
				res.Kept++
				continue
			}
			ls = append(ls, in)
			res.Added++
			changed = true
		case len(at) > 1:
			res.Kept++ // which of them it is isn't known
		default:
			l := &ls[at[0]]
			if !carried(*l) || protected(*l) || loginRecID(*l) != r.id {
				res.Kept++
				continue
			}
			if credHash(l.Auth) == r.hash {
				continue
			}
			l.Auth, l.Profile, l.Plan, l.AccessSKU = in.Auth, in.Profile, in.Plan, in.AccessSKU
			l.Renewed, l.Lapsed, l.Refused, l.Seen = in.Renewed, in.Lapsed, in.Refused, in.Seen
			res.Updated++
			changed = true
		}
	}
	for i := 0; i < len(ls); i++ {
		l := ls[i]
		id := loginRecID(l)
		if !carried(l) || !gone[id] || protected(l) || credHash(l.Auth) != seen[id] {
			continue
		}
		ls = slices.Delete(ls, i, i+1)
		i--
		changed = true
	}
	if !changed {
		return nil
	}
	return writeLogins(ls)
}

// hasPrefixed says some key of m has (or, want false, lacks) prefix.
func hasPrefixed(m map[string]bool, prefix string, want bool) bool {
	for k := range m {
		if strings.HasPrefix(k, prefix) == want {
			return true
		}
	}
	return false
}

func applyPlugins(put []signInRec, gone map[string]bool, seen map[string]string, res *SignInsResult) error {
	if len(put) == 0 && !hasPrefixed(gone, "plugin|", true) {
		return nil
	}
	return plugin.EditAuth(func(m map[string]json.RawMessage) (bool, error) {
		changed := false
		ids := map[string][]string{} // id → keys here
		for k, raw := range m {
			id := pluginRecID(k, raw)
			ids[id] = append(ids[id], k)
		}
		hashOf := func(id string) string {
			if ks := ids[id]; len(ks) == 1 {
				return credHash(m[ks[0]])
			}
			return ""
		}
		for _, r := range put {
			if hashOf(r.id) != seen[r.id] {
				res.Kept++
				continue
			}
			ks := ids[r.id]
			if raw, ok := m[r.key]; ok && !slices.Contains(ks, r.key) && plugin.AuthWho(raw) == "" {
				ks = append(ks, r.key) // the same key, whose sign-in doesn't say who
			}
			switch len(ks) {
			case 0:
				key := r.key
				if _, taken := m[key]; taken {
					key = plugin.ProviderOf(r.key) + "#" + r.hash[:8]
				}
				m[key] = r.raw
				res.Added++
				changed = true
			case 1:
				if credHash(m[ks[0]]) != r.hash {
					m[ks[0]] = r.raw
					res.Updated++
					changed = true
				}
			default:
				res.Kept++
			}
		}
		for id := range gone {
			if ks := ids[id]; strings.HasPrefix(id, "plugin|") && len(ks) == 1 && credHash(m[ks[0]]) == seen[id] {
				delete(m, ks[0])
				changed = true
			}
		}
		return changed, nil
	})
}

// signCodexIn signs Codex in to l where it is signed out: auth.json (or
// the store its config.toml names) gets l's sign-in, written beside the
// old file and renamed in. Codex signed in, its sign-in unreadable, or
// its account removed in magpie: nothing is written.
func signCodexIn(l savedLogin) (bool, error) {
	if l.Agent != "codex" || len(l.Auth) == 0 || accountRemoved("codex") {
		return false, nil
	}
	home := codexHome()
	if _, _, err := readCodexLogin(home, false); !errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err := saveCodexLogin(home, l.Auth); err != nil {
		return false, err
	}
	return true, nil
}
