package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/edit"
)

// AccountGroup is a named set of accounts and keys, of any providers
// (LeiWang_2000 on X): a gateway key's list names it as "@<id>" to use
// its accounts, or "!@<id>" to keep them out, instead of each account
// one by one. A key reads the group as it is when a request comes, so
// changing a group's accounts changes every key that names it.
type AccountGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Accounts are "<provider>/<ref>", as a key's list keeps them
	// (CleanAccounts): an account by its stable id, a key by its
	// fingerprint. One signed out since stays, matching nothing.
	Accounts []string `json:"accounts"`
}

// GroupMark starts an entry of a key's account list that names an
// account group by its id: "@<id>", or "!@<id>" (ExcludeMark) to keep
// the group's accounts out. A magpie older than the mark reads it as an
// entry naming no provider, a key that names no model holding it to no
// model (HeldModels): it fails closed.
const GroupMark = "@"

// heldToNothing is the entry a group a key names expands to when the
// group is gone or has no account: AllowsAccount refuses every account
// with it, and HeldModels' "\x00/*" matches no model, so the key is held
// to nothing rather than let off to everything (a failed read means
// unknown).
const heldToNothing = "\x00/\x00"

// groupEntry is the group id an entry names and whether it keeps the
// group's accounts out.
func groupEntry(a string) (id string, out bool, ok bool) {
	a = strings.TrimSpace(a)
	a, out = strings.CutPrefix(a, ExcludeMark)
	id, ok = strings.CutPrefix(a, GroupMark)
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false, false
	}
	return id, out, true
}

// IsGroupEntry says the entry names an account group, "@<id>" or "!@<id>".
func IsGroupEntry(a string) bool {
	_, _, ok := groupEntry(a)
	return ok
}

func groupsPath() string { return filepath.Join(filepath.Dir(Path()), "account-groups.json") }

// GroupsPath is where the account groups are kept.
func GroupsPath() string { return groupsPath() }

// loadGroups reads the groups without the lock: for a caller holding mu,
// or one that only reads. No file is no groups; one that can't be read
// or parsed is an error, never none.
func loadGroups() ([]AccountGroup, error) {
	b, err := os.ReadFile(groupsPath())
	if errors.Is(err, os.ErrNotExist) {
		return []AccountGroup{}, nil
	}
	if err != nil {
		return nil, err
	}
	var gs []AccountGroup
	if err := json.Unmarshal(b, &gs); err != nil {
		return nil, fmt.Errorf("account-groups.json doesn't read: %w", err)
	}
	return gs, nil
}

func saveGroups(gs []AccountGroup) error {
	b, err := json.MarshalIndent(gs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(groupsPath()), 0o755); err != nil {
		return err
	}
	return edit.WriteAtomic(groupsPath(), append(b, '\n'))
}

// AccountGroups are the account groups, in the order they were made.
func AccountGroups() ([]AccountGroup, error) {
	mu.Lock()
	defer mu.Unlock()
	return loadGroups()
}

// ExportGroups are the account groups for a backup; RestoreGroups puts
// a backup's back.
func ExportGroups() ([]AccountGroup, error) { return AccountGroups() }

func RestoreGroups(gs []AccountGroup) error {
	mu.Lock()
	defer mu.Unlock()
	return saveGroups(slices.Clone(gs))
}

// findGroup is the group ref names, by its id or its name, in any case.
func findGroup(gs []AccountGroup, ref string) int {
	ref = strings.TrimSpace(ref)
	if i := slices.IndexFunc(gs, func(g AccountGroup) bool { return strings.EqualFold(g.ID, ref) }); i >= 0 {
		return i
	}
	return slices.IndexFunc(gs, func(g AccountGroup) bool { return strings.EqualFold(g.Name, ref) })
}

// SaveAccountGroup adds g (no ID) or changes the group with g's ID: its
// name and accounts. A name is 1 to 80 characters and no other group's;
// each account is one a provider has now, as a key's (CleanAccounts),
// except one the group had already, kept though signed out since. A group
// holds accounts only, never another group or an account kept out.
func SaveAccountGroup(g AccountGroup) (AccountGroup, error) {
	mu.Lock()
	defer mu.Unlock()
	gs, err := loadGroups()
	if err != nil {
		return AccountGroup{}, err
	}
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" || utf8.RuneCountInString(g.Name) > 80 {
		return AccountGroup{}, errors.New("Use a group name between 1 and 80 characters")
	}
	if strings.HasPrefix(g.Name, GroupMark) || strings.HasPrefix(g.Name, ExcludeMark) || strings.Contains(g.Name, "/") {
		return AccountGroup{}, errors.New("A group name can't start with @ or ! or hold a /")
	}
	at := -1
	if g.ID != "" {
		if at = slices.IndexFunc(gs, func(o AccountGroup) bool { return o.ID == g.ID }); at < 0 {
			return AccountGroup{}, errors.New("Account group not found")
		}
	}
	for i, o := range gs {
		if i != at && strings.EqualFold(o.Name, g.Name) {
			return AccountGroup{}, fmt.Errorf("There is an account group named %s already", o.Name)
		}
	}
	var had []string
	if at >= 0 {
		had = gs[at].Accounts
	}
	accounts := []string{}
	for _, a := range g.Accounts {
		if a = strings.TrimSpace(a); a == "" {
			continue
		}
		if IsGroupEntry(a) || Excluded(a) {
			return AccountGroup{}, errors.New("An account group holds accounts, not another group or an account kept out: " + a)
		}
		kept := a
		if !slices.ContainsFunc(had, func(h string) bool { return strings.EqualFold(h, a) }) {
			cleaned, err := CleanAccounts([]string{a})
			if err != nil {
				return AccountGroup{}, err
			}
			if len(cleaned) == 0 {
				continue
			}
			kept = cleaned[0]
		}
		if !slices.ContainsFunc(accounts, func(o string) bool { return strings.EqualFold(o, kept) }) {
			accounts = append(accounts, kept)
		}
	}
	if len(accounts) > 500 {
		return AccountGroup{}, errors.New("An account group can hold at most 500 accounts")
	}
	g.Accounts = accounts
	if at < 0 {
		if len(gs) >= 200 {
			return AccountGroup{}, errors.New("There can be at most 200 account groups")
		}
		id, err := random(6)
		if err != nil {
			return AccountGroup{}, err
		}
		g.ID = id
		gs = append(gs, g)
	} else {
		gs[at] = g
	}
	return g, saveGroups(gs)
}

// RemoveAccountGroup removes the group id. A group a gateway key names is
// not removed: a key held to it would be held to nothing, and one keeping
// it out would be let onto its accounts; the error names the keys.
func RemoveAccountGroup(id string) error {
	mu.Lock()
	defer mu.Unlock()
	gs, err := loadGroups()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(gs, func(g AccountGroup) bool { return g.ID == id })
	if i < 0 {
		return errors.New("Account group not found")
	}
	keys, err := load()
	if err != nil {
		return err
	}
	var users []string
	for _, k := range keys {
		if slices.ContainsFunc(k.Accounts, func(a string) bool {
			gid, _, ok := groupEntry(a)
			return ok && strings.EqualFold(gid, id)
		}) {
			users = append(users, k.Name)
		}
	}
	if len(users) > 0 {
		return fmt.Errorf("The account group %s is used by the gateway key %s: take it off the key first", gs[i].Name, strings.Join(users, ", "))
	}
	return saveGroups(slices.Delete(gs, i, i+1))
}

// hasGroupEntry says some entry names an account group.
func hasGroupEntry(accounts []string) bool { return slices.ContainsFunc(accounts, IsGroupEntry) }

// expandGroups is a key's account list with each group it names put in
// its place: "@<id>" by the group's accounts, "!@<id>" by each of them
// kept out. A group gone, or one with no account, held to, or a group
// gone kept out, is heldToNothing: the key is held to nothing rather than
// let off to everything. A list naming no group is returned as it is, and
// the groups are read only for one that does; groups that can't be read
// are the error.
func expandGroups(accounts []string, read func() ([]AccountGroup, error)) ([]string, error) {
	if !hasGroupEntry(accounts) {
		return accounts, nil
	}
	gs, err := read()
	if err != nil {
		return nil, err
	}
	var out []string
	add := func(a string) {
		if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, a) }) {
			out = append(out, a)
		}
	}
	for _, a := range accounts {
		id, kept, ok := groupEntry(a)
		if !ok {
			add(a)
			continue
		}
		i := slices.IndexFunc(gs, func(g AccountGroup) bool { return strings.EqualFold(g.ID, id) })
		if i < 0 || (!kept && len(gs[i].Accounts) == 0) {
			add(heldToNothing)
			continue
		}
		for _, m := range gs[i].Accounts {
			if kept {
				p, r, _ := strings.Cut(m, "/")
				add(p + "/" + ExcludeMark + r)
			} else {
				add(m)
			}
		}
	}
	// an account a group uses and another keeps out is kept out
	// (AllowsAccount refuses it first)
	return out, nil
}

// ExpandGroups is accounts with each account group put in its place, for
// what reads a key's list outside a request (the CLI's models, the
// GUI): groups that can't be read hold it to nothing.
func ExpandGroups(accounts []string) []string {
	out, err := expandGroups(accounts, loadGroups)
	if err != nil {
		return []string{heldToNothing}
	}
	return out
}

// cleanGroupEntry is the entry a key keeps for a group ref names, by its
// id or name: "@<id>", or "!@<id>" kept out. A group no one made is an
// error, as an account no provider has is.
func cleanGroupEntry(gs []AccountGroup, a string) (string, error) {
	a = strings.TrimSpace(a)
	rest, out := strings.CutPrefix(a, ExcludeMark)
	ref, _ := strings.CutPrefix(rest, GroupMark)
	i := findGroup(gs, ref)
	if i < 0 {
		return "", fmt.Errorf("There is no account group %q", ref)
	}
	if out {
		return ExcludeMark + GroupMark + gs[i].ID, nil
	}
	return GroupMark + gs[i].ID, nil
}

// GroupNames maps each group entry a key's list may hold, "@<id>", to
// its group's name, for words that say a key's accounts.
func GroupNames() map[string]string {
	gs, err := loadGroups()
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(gs))
	for _, g := range gs {
		out[GroupMark+g.ID] = g.Name
	}
	return out
}

// FindAccountGroup is the group ref names, by its id or name, in any case.
func FindAccountGroup(ref string) (AccountGroup, error) {
	gs, err := AccountGroups()
	if err != nil {
		return AccountGroup{}, err
	}
	i := findGroup(gs, strings.TrimPrefix(strings.TrimSpace(ref), GroupMark))
	if i < 0 {
		return AccountGroup{}, fmt.Errorf("There is no account group %q", ref)
	}
	return gs[i], nil
}
