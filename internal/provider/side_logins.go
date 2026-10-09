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
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	// moved onto its plugin, the agent's own sign-in is the plugin's: its
	// row, set aside, is kept for going back
	if ownUser != "" && !movedAgent(agent) {
		found := false
		for i := range ls {
			if ls[i].Agent == agent && ls[i].own() {
				found = true
				changed, renamed := false, false
				if agent == "copilot" && copilotRenamed(ls[i].User, ownUser) {
					// the same account, its host now in its name (#1220):
					// kept as it was, hidden or not, its settings with it
					ls[i].renameTo(ownUser)
					renamed = renameDue()
				}
				if !strings.EqualFold(ls[i].User, ownUser) {
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
			ls = append(ls, savedLogin{Agent: agent, User: ownUser, Seen: time.Now().UTC().Truncate(time.Second)})
			_ = writeLogins(ls)
		}
	}
	// a saved account renamed as it was read (nameAlike) is written under
	// its new name, its settings moved to it, the first time it is listed
	if agent == "copilot" && renameDue() && slices.ContainsFunc(ls, func(l savedLogin) bool { return l.was != "" }) {
		_ = writeLogins(slices.Clone(ls)) // sorted there: listed here as added
	}
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
		out = append(out, sideLogin{Login{Agent: agent, User: l.User, Plan: l.Plan, Seen: l.Seen, On: l.On, Own: l.own()}, l})
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

// editSideLogin changes the saved account of user.
func editSideLogin(agent, user string, f func(ls []savedLogin, i int) ([]savedLogin, error)) error {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	for i := range ls {
		if ls[i].Agent == agent && strings.EqualFold(ls[i].User, user) {
			ls, err := f(ls, i)
			if err != nil {
				return err
			}
			return writeLogins(ls)
		}
	}
	return fmt.Errorf("no %s account %q", agent, user)
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
	return editSideLogin(agent, user, func(saved []savedLogin, i int) ([]savedLogin, error) {
		for j := range saved {
			if saved[j].Agent == agent {
				if j != i && strings.EqualFold(saved[j].User, was) {
					saved[j].On = true // the one it replaces is next in line
				}
				saved[j].First = j == i
			}
		}
		return saved, nil
	})
}

func setSideLoginOn(agent, user string, on bool, ls []sideLogin) error {
	if !on && strings.EqualFold(activeOf(ls), user) {
		return fmt.Errorf("magpie uses %s first; put another account first to stop using it", user)
	}
	return editSideLogin(agent, user, func(saved []savedLogin, i int) ([]savedLogin, error) {
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
	err := editSideLogin(agent, user, func(saved []savedLogin, i int) ([]savedLogin, error) {
		for j := range saved {
			if next != "" && j != i && saved[j].Agent == agent {
				saved[j].First = strings.EqualFold(saved[j].User, next)
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
