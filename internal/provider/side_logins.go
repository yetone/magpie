package provider

// Accounts magpie signs in beside an agent's own. Grok's CLI and Copilot's
// editors and CLI each keep one account, which magpie only ever reads; a
// further account is signed in by magpie and kept by magpie alone (a home
// of its own for Grok, a token for Copilot). logins.json lists them, and
// the agent's own account too, with nothing of its secrets, so it can go
// behind another or be turned off like the rest.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
)

// sideLogin is one of those accounts, with what magpie saved of it.
type sideLogin struct {
	Login
	saved savedLogin
}

// own says a saved account is the agent's own sign-in, not one of magpie's.
func (l savedLogin) own() bool {
	return l.Home == "" && (len(l.Auth) == 0 || string(l.Auth) == "null")
}

// sideLogins lists an agent's accounts that are signed in, the first in
// use first, then the rest as they were added. ownUser is who the agent
// itself is signed in to, "" for no one; usable says a saved one of
// magpie's is still signed in.
func sideLogins(agent, ownUser string, usable func(savedLogin) bool) []sideLogin {
	// read without the lock, which is taken only for a change to write:
	// a list that changes nothing, nearly every one, made and locked
	// logins.json.lock all the same (#1665: every page's read lists a
	// dozen agents' accounts, each a lock). logins.json is only ever
	// replaced whole (writePrivate), so the read is one write's file.
	ls := sharedLogins() // only looked at: a change reads its own copy
	if _, change := keepSideLogins(ls, agent, ownUser, true); change {
		loginsMu.Lock()
		ls, _ = keepSideLogins(readLogins(), agent, ownUser, false)
		loginsMu.Unlock()
	}
	return listSideLogins(ls, agent, ownUser, usable)
}

// keepSideLogins brings the agent's own sign-in into ls, and writes ls
// when that changed it; looking (look), it changes and writes nothing, and
// says whether it would. Written, it is called under loginsMu.
func keepSideLogins(ls []savedLogin, agent, ownUser string, look bool) ([]savedLogin, bool) {
	// moved onto its plugin, the agent's own sign-in is the plugin's: its
	// row, set aside, is kept for going back
	if ownUser != "" && !movedAgent(agent) {
		found := false
		for i := range ls {
			if ls[i].Agent == agent && ls[i].own() {
				found = true
				changed, renamed := false, false
				if agent == "copilot" && copilotRenamed(ls[i].User, ownUser) {
					if look {
						return ls, true
					}
					// the same account, its host now in its name (#1220):
					// kept as it was, hidden or not, its settings with it
					ls[i].renameTo(ownUser)
					renamed = renameDue()
				}
				if !strings.EqualFold(ls[i].User, ownUser) {
					if look {
						return ls, true
					}
					ls[i].User, ls[i].Seen = ownUser, time.Now().UTC().Truncate(time.Second)
					if agent == "copilot" {
						ls[i].Plan, ls[i].AccessSKU = "", ""
					}
					changed = true
				}
				// removed in magpie, it shows again once the agent signs in
				// anew: told by its sign-in's mark where the agent has one,
				// else by another account
				if ls[i].Hidden != "" {
					if m := ownMark(agent); m != "" && m != ls[i].Hidden || m == "" && ls[i].Hidden == hiddenNoMark && changed {
						if look {
							return ls, true
						}
						ls[i].Hidden = ""
						changed = true
					}
				}
				if changed || renamed {
					_ = writeLogins(ls)
				}
			}
		}
		// one of magpie's that is the same account stands for it: it took
		// the agent's own over while that couldn't be read (addSideLogin)
		if !found && !slices.ContainsFunc(ls, func(l savedLogin) bool { return l.Agent == agent && strings.EqualFold(l.User, ownUser) }) {
			if look {
				return ls, true
			}
			ls = append(ls, savedLogin{Agent: agent, User: ownUser, Seen: time.Now().UTC().Truncate(time.Second)})
			_ = writeLogins(ls)
		}
	}
	// a saved account renamed as it was read (nameAlike) is written under
	// its new name, its settings moved to it, the first time it is listed
	if agent == "copilot" && renameDue() && slices.ContainsFunc(ls, func(l savedLogin) bool { return l.was != "" }) {
		if look {
			return ls, true
		}
		_ = writeLogins(slices.Clone(ls)) // sorted there: listed here as added
	}
	return ls, false
}

// listSideLogins is sideLogins' list, of ls as kept.
func listSideLogins(ls []savedLogin, agent, ownUser string, usable func(savedLogin) bool) []sideLogin {
	var out []sideLogin
	first := -1
	for _, l := range ls {
		if l.Agent != agent {
			continue
		}
		if l.own() && (ownUser == "" || l.Hidden != "") || !l.own() && !usable(l) {
			continue // signed out there, or removed in magpie
		}
		// the agent's own that is one of magpie's already, as when the
		// agent was signed in to another account (or signed out) when
		// magpie signed this one in, and the agent signed in to it after:
		// that one stands for it, as above, not listed twice (蓝猫 on
		// Discord, Devin)
		if l.own() && slices.ContainsFunc(ls, func(m savedLogin) bool {
			return m.Agent == agent && !m.own() && strings.EqualFold(m.User, l.User) && usable(m)
		}) {
			continue
		}
		if l.First || (first < 0 && l.own()) {
			first = len(out)
		}
		// a sign-in its vendor revoked shows on the account (signin_revoked.go)
		out = append(out, sideLogin{Login{Agent: agent, User: l.User, Plan: l.Plan, Seen: l.Seen, On: l.On, Own: l.own(), Lapsed: revokedLapse(l)}, l})
	}
	if len(out) == 0 {
		return nil
	}
	if first < 0 {
		first = 0
	}
	out[first].Active, out[first].On = true, true
	return append([]sideLogin{out[first]}, append(out[:first:first], out[first+1:]...)...)
}

func loginsOf(ls []sideLogin) []Login {
	var out []Login
	for _, l := range ls {
		out = append(out, l.Login)
	}
	return out
}

// editSideLogin changes the saved account of user: one shown before one
// hidden of the same name, the agent's own removed in magpie, which only
// a slot (Home) tells apart (dev on Discord: Remove on a ZCode account took
// the hidden one beside it).
func editSideLogin(agent, user string, f func(ls []savedLogin, i int) ([]savedLogin, error)) error {
	return editSavedLogin(agent, user, func(l savedLogin) bool {
		return l.Agent == agent && strings.EqualFold(l.User, user)
	}, f)
}

// editListedLogin changes the account of user in ls, the list the user
// picked it from: the very one saved, where ls knows it, not the first of
// its name.
func editListedLogin(agent, user string, ls []sideLogin, f func(ls []savedLogin, i int) ([]savedLogin, error)) error {
	return editSavedLogin(agent, user, listedMatch(agent, user, ls), f)
}

func editSavedLogin(agent, user string, match func(savedLogin) bool, f func(ls []savedLogin, i int) ([]savedLogin, error)) error {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	i := pickSaved(ls, match)
	if i < 0 {
		return fmt.Errorf("no %s account %q", agent, user)
	}
	ls, err := f(ls, i)
	if err != nil {
		return err
	}
	return writeLogins(ls)
}

// pickSaved is the saved account match takes, one shown before one
// hidden; -1 for none.
func pickSaved(ls []savedLogin, match func(savedLogin) bool) int {
	i := -1
	for j := range ls {
		if match(ls[j]) && (i < 0 || ls[i].Hidden != "" && ls[j].Hidden == "") {
			i = j
		}
	}
	return i
}

// listedMatch tells the saved account of user in ls: by its slot and
// whether it is the agent's own where ls has what was saved of it (a
// plugin's account by its key), else by its name.
func listedMatch(agent, user string, ls []sideLogin) func(savedLogin) bool {
	i := slices.IndexFunc(ls, func(l sideLogin) bool { return strings.EqualFold(l.User, user) })
	if i < 0 || ls[i].saved.Agent == "" {
		return func(l savedLogin) bool { return l.Agent == agent && strings.EqualFold(l.User, user) }
	}
	want := ls[i].saved
	return func(l savedLogin) bool { return sameSaved(l, want) }
}

// sameSaved says two saved accounts are one: a plugin's by its key, an
// agent's own or one of magpie's with no home of its own by its name.
func sameSaved(a, b savedLogin) bool {
	if a.Agent != b.Agent || a.Home != b.Home || a.own() != b.own() {
		return false
	}
	return a.Home != "" || strings.EqualFold(a.User, b.User)
}

func activeOf(ls []sideLogin) string {
	for _, l := range ls {
		if l.Active {
			return l.User
		}
	}
	return ""
}

// switchSideLogin puts an account first. The agent's own sign-in stays as
// it is: magpie only changes which account its gateway uses first.
func switchSideLogin(agent, user string, ls []sideLogin) error {
	was := activeOf(ls)
	isWas := listedMatch(agent, was, ls)
	return editListedLogin(agent, user, ls, func(saved []savedLogin, i int) ([]savedLogin, error) {
		w := -1
		if was != "" {
			w = pickSaved(saved, isWas)
		}
		for j := range saved {
			if saved[j].Agent == agent {
				if j != i && j == w {
					saved[j].On = true // the one it replaces is next in line
				}
				saved[j].First = j == i
			}
		}
		return saved, nil
	})
}

// setSideLoginOn ticks an account on or off. First is only the gateway's
// order here, so the first ticked off hands first to the next ticked
// account (Rime_Nan on X: it could be ticked off only after dragging it
// down the list); with no other ticked, it stays, as one account is used.
func setSideLoginOn(agent, user string, on bool, ls []sideLogin) error {
	if !on && strings.EqualFold(activeOf(ls), user) {
		next := ""
		for _, l := range ls {
			if !l.Active && l.On && l.Lapsed == "" {
				next = l.User
				break
			}
		}
		if next == "" {
			return fmt.Errorf("%s is the only account in use; tick another to stop using it", user)
		}
		isNext := listedMatch(agent, next, ls)
		return editListedLogin(agent, user, ls, func(saved []savedLogin, i int) ([]savedLogin, error) {
			n := pickSaved(saved, isNext)
			if n < 0 {
				return nil, fmt.Errorf("no %s account %q", agent, next)
			}
			for j := range saved {
				if saved[j].Agent == agent {
					saved[j].First = j == n
				}
			}
			saved[i].On = false
			return saved, nil
		})
	}
	return editListedLogin(agent, user, ls, func(saved []savedLogin, i int) ([]savedLogin, error) {
		saved[i].On = on
		return saved, nil
	})
}

// forgetSideLogin drops an account magpie signed in; gone is told what it
// kept. The agent's own sign-in is only hidden: its files stay as they
// are, and it shows again once the agent signs in anew (ownMark). Either
// one, removed while first, puts the next account first; the only one
// leaves the subscription with no account (#874: a removed subscription's
// Sign out… could never take its one account; 歧路亡羊 on Discord: one of
// magpie's that was first was refused, "put another account first", even
// by the subscription's Sign out…, which takes every account).
func forgetSideLogin(agent, user string, ls []sideLogin, gone func(savedLogin)) error {
	own := slices.ContainsFunc(ls, func(l sideLogin) bool { return l.Own && strings.EqualFold(l.User, user) })
	first := strings.EqualFold(activeOf(ls), user)
	next := ""
	if first {
		for _, l := range ls {
			if !strings.EqualFold(l.User, user) {
				next = l.User
				break
			}
		}
	}
	mark := hiddenNoMark
	if own {
		mark = firstNonEmpty(ownMark(agent), hiddenNoMark)
	}
	var old savedLogin
	isNext := listedMatch(agent, next, ls)
	err := editListedLogin(agent, user, ls, func(saved []savedLogin, i int) ([]savedLogin, error) {
		n := -1
		if next != "" {
			n = pickSaved(saved, isNext)
		}
		for j := range saved {
			if next != "" && j != i && saved[j].Agent == agent {
				saved[j].First = j == n
				saved[j].On = saved[j].On || saved[j].First
			}
		}
		if saved[i].own() {
			saved[i].Hidden, saved[i].First = mark, false
			return saved, nil
		}
		old = saved[i]
		return append(saved[:i], saved[i+1:]...), nil
	})
	if err == nil && gone != nil && old.Agent != "" {
		gone(old)
	}
	return err
}

// hiddenNoMark hides an agent's own sign-in that has no mark to tell a
// fresh sign-in by: it shows again when another account signs in.
const hiddenNoMark = "hidden"

// ownMark tells one sign-in of the agent's own from the next, "" where
// the agent has no way to: a new sign-in, even to the same account, gets a
// new refresh token and so a new mark. Only a digest is kept, never the
// secret.
func ownMark(agent string) string {
	var secret string
	switch agent {
	case "kiro":
		c, ok := readKiroAt("", "")
		if !ok {
			return ""
		}
		secret = c.dbKey + c.idePath + "\x00" + firstNonEmpty(c.refresh, c.access)
	default:
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:12])
}

// addSideLogin keeps an account magpie just signed in, in use beside the
// others. Signed in again, an account keeps the newer sign-in; one that is
// the agent's own already is not kept twice (dup is told of what is let
// go either way). ownUser is who the agent is signed in to now, "" for no
// one it can be read as: the agent's own account remembered from before,
// while the agent is signed out or keeps its tokens encrypted, takes the
// new sign-in rather than letting it go — let go, it was listed nowhere
// (#155). The account's kept usage reading goes (StaleAllowance): it
// was read under the old sign-in.
func addSideLogin(l savedLogin, ownUser string, dup func(savedLogin)) (err error) {
	defer func() { // after loginsMu is let go: Grok's is read by its accounts
		if err == nil {
			StaleAllowance(l.Agent, l.User)
		}
	}()
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	l.Seen = time.Now().UTC().Truncate(time.Second)
	for i := range ls {
		if ls[i].Agent == l.Agent && strings.EqualFold(ls[i].User, l.User) {
			if ls[i].own() && strings.EqualFold(ownUser, l.User) {
				dup(l)
				// removed in magpie, the agent's own is brought back by
				// signing in to it again: kept hidden, the sign-in said
				// done and the account was listed nowhere (#320)
				if ls[i].Hidden == "" {
					return nil
				}
				ls[i].Hidden = ""
				return writeLogins(ls)
			}
			old := ls[i]
			ls[i].Auth, ls[i].Home, ls[i].Plan, ls[i].Seen = l.Auth, l.Home, l.Plan, l.Seen
			if l.Agent == "copilot" {
				ls[i].AccessSKU = l.AccessSKU
			}
			// a new sign-in: the token its vendor refused is gone
			// (copilot_lapsed.go, signin_revoked.go)
			if l.Agent == "copilot" || strings.HasPrefix(ls[i].Refused, revokedPrefix) {
				ls[i].Lapsed, ls[i].Refused = "", ""
			}
			if old.own() {
				ls[i].On = true
			} else if old.Home != l.Home {
				dup(old)
			}
			return writeLogins(ls)
		}
	}
	l.On = true
	return writeLogins(append(ls, l))
}

// sideAgent says an agent's accounts are kept this way, not in the
// agent's own store as Claude Code's and Codex's are.
func sideAgent(agent string) bool {
	switch agent {
	case "grok", "copilot", "zcode", "kiro", "devin", "workbuddy", WorkBuddyAIID, CommandCodePlanID, "gemini", "antigravity", "qoder", QoderCNID, "zed", "factory", MiMoID, ChatGPTAPIID:
		return true
	}
	return false
}
