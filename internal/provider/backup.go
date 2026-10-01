package provider

// A backup carries providers.json's entries as they are stored, and puts
// them back on another machine (see internal/backup).

import "slices"

// Stored is the providers as providers.json keeps them: keys included,
// signed-in accounts only as the model picks the user made for them. A
// file that can't be read is an error, never none: a backup or a sync
// carrying none would take every provider away where it is put back.
func Stored() ([]Provider, error) {
	f, err := read()
	return f.Providers, err
}

// Restore puts providers from a backup in. Each replaces the one here with
// its id; one that came without keys keeps the keys already here. It
// returns how many were new and how many replaced one here.
func Restore(ps []Provider) (added, replaced int, err error) {
	f, err := read()
	if err != nil {
		return 0, 0, err
	}
	for _, p := range ps {
		p.IconURL = ""
		if p.ID == "" || p.ID != Slug(p.ID) || p.ID == "magpie" {
			continue
		}
		i := -1
		for j := range f.Providers {
			if f.Providers[j].ID == p.ID {
				i = j
				break
			}
		}
		if i < 0 {
			f.Providers = append(f.Providers, p)
			added++
			continue
		}
		if p.Key == "" && len(p.Keys) == 0 {
			p.Key, p.KeyName, p.Keys, p.KeyProtocol = f.Providers[i].Key, f.Providers[i].KeyName, f.Providers[i].Keys, f.Providers[i].KeyProtocol
			if p.BalanceToken == "" {
				p.BalanceToken = f.Providers[i].BalanceToken
			}
		}
		f.Providers[i] = p
		replaced++
	}
	if added+replaced == 0 {
		return 0, 0, nil
	}
	return added, replaced, store(f)
}

// StoredGroups is the groups as saved: the user's own, and the found ones
// the user removed. As with Stored, a file that can't be read is an error.
func StoredGroups() ([]Group, error) {
	f, err := read()
	return f.Groups, err
}

// RestoreGroups puts groups from a backup in, each replacing the one here
// with its id.
func RestoreGroups(gs []Group) error {
	if len(gs) == 0 {
		return nil
	}
	f, err := read()
	if err != nil {
		return err
	}
	for _, g := range gs {
		if g.ID == "" || g.ID != Slug(g.ID) {
			continue
		}
		g.Auto = false
		if i := slices.IndexFunc(f.Groups, func(x Group) bool { return x.ID == g.ID }); i >= 0 {
			f.Groups[i] = g
		} else {
			f.Groups = append(f.Groups, g)
		}
	}
	return store(f)
}

// Mirror makes the providers and groups exactly these, as sync brings them
// from another computer: one not among them goes, one that came without
// keys keeps the keys it has here.
func Mirror(ps []Provider, gs []Group) error {
	f, err := read()
	if err != nil {
		return err
	}
	here := map[string]Provider{}
	for _, p := range f.Providers {
		here[p.ID] = p
	}
	out := []Provider{}
	for _, p := range ps {
		p.IconURL = ""
		if p.ID == "" || p.ID != Slug(p.ID) || p.ID == "magpie" {
			continue
		}
		if h, ok := here[p.ID]; ok && p.Key == "" && len(p.Keys) == 0 {
			p.Key, p.KeyName, p.Keys, p.KeyProtocol = h.Key, h.KeyName, h.Keys, h.KeyProtocol
			if p.BalanceToken == "" {
				p.BalanceToken = h.BalanceToken
			}
		}
		out = append(out, p)
	}
	f.Providers = out
	f.Groups = slices.DeleteFunc(slices.Clone(gs), func(g Group) bool { return g.ID == "" || g.ID != Slug(g.ID) })
	return store(f)
}
