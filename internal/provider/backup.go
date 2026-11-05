package provider

// A backup carries providers.json's entries as they are stored, and puts
// them back on another machine (see internal/backup).

import (
	"context"
	"slices"

	"github.com/yetone/magpie/internal/plugin"
)

// handOver is HandOver: a var so a test can see it asked.
var handOver = HandOver

// Stored is the providers as providers.json keeps them: keys included,
// signed-in accounts only as the model picks the user made for them. A
// file that can't be read is an error, never none: a backup or a sync
// carrying none would take every provider away where it is put back.
func Stored() ([]Provider, error) {
	f, err := read()
	return f.Providers, err
}

// Restore puts providers from a backup in. Each replaces the one here with
// its id; one that came without keys keeps the keys already here, and
// one without an access key or balance token keeps those (KeepSecrets). It
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
			p.KeepSelf = false
			f.Providers = append(f.Providers, p)
			added++
			continue
		}
		keepSelf := f.Providers[i].KeepSelf // this computer's own (Mirror)
		f.Providers[i] = KeepSecrets(p, f.Providers[i], p.Key == "" && len(p.Keys) == 0)
		f.Providers[i].KeepSelf = keepSelf
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
		if g.ID == "" || g.ID != GroupSlug(g.ID) {
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
// keys keeps the keys it has here, and an access key or balance token it
// came without is kept too (KeepSecrets). Which account an agent is kept
// signed in to (KeepLogin, KeepLoginAs, SwitchOnly), whether its app is
// restarted to follow (RestartApp), and whether a Remote magpie that is
// this magpie is used anyway (KeepSelf), is this computer's own:
// one person signs in to a personal account at home and a work account at
// the office, and sync set each computer to the other's every day.
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
		if h, ok := here[p.ID]; ok {
			p = KeepSecrets(p, h, p.Key == "" && len(p.Keys) == 0)
			p.KeepLogin, p.KeepLoginAs, p.SwitchOnly, p.RestartApp = h.KeepLogin, h.KeepLoginAs, h.SwitchOnly, h.RestartApp
			p.KeepSelf = h.KeepSelf
		} else {
			p.KeepLoginAs = "" // an account signed in on the other computer
			p.KeepSelf = false // kept on the other computer, of a magpie that isn't this one
		}
		out = append(out, p)
	}
	f.Providers = out
	f.Groups = slices.DeleteFunc(slices.Clone(gs), func(g Group) bool { return g.ID == "" || g.ID != GroupSlug(g.ID) })
	return store(f)
}

// Carried is the plugins a backup or a sync carries: those added from npm
// or a git repository, on or off as the user left them, with their options:
// without keys, those whose names look like a key are left out, as a
// provider's secret headers are. A plugin added from a file or folder is
// this computer's alone, and a plugin's sign-ins (plugin-auth.json) stay
// here, as a subscription's do.
func Carried(keys bool) []plugin.Entry {
	var out []plugin.Entry
	for _, e := range plugin.Load().Plugins {
		if plugin.IsPath(e.Spec) {
			continue
		}
		if !keys {
			e.Options = withoutSecretOptions(e.Options)
		}
		out = append(out, e)
	}
	return out
}

// withoutSecretOptions is opts without the values whose names look like a
// key, at any depth: nil when nothing is left.
func withoutSecretOptions(opts map[string]any) map[string]any {
	var strip func(v any) any
	strip = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, v := range x {
				if !SecretHeader(k) {
					out[k] = strip(v)
				}
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, v := range x {
				out[i] = strip(v)
			}
			return out
		}
		return v
	}
	out, _ := strip(map[string]any(opts)).(map[string]any)
	if len(out) == 0 {
		return nil
	}
	return out
}

// AddPlugins installs the plugins another computer has (Carried) that
// aren't here, the way Settings → Plugins adds one, and hands each a
// deprecated built-in it serves (HandOver). It never takes a plugin away
// or changes one already here: a plugin removed on one computer stays on
// the others. It returns the specs added and why the others couldn't be.
func AddPlugins(ctx context.Context, es []plugin.Entry) (added []string, failed map[string]error) {
	here := map[string]bool{}
	for _, e := range plugin.Load().Plugins {
		here[plugin.Name(e.Spec)] = true
	}
	for _, e := range es {
		if e.Spec == "" || plugin.IsPath(e.Spec) || here[plugin.Name(e.Spec)] {
			continue
		}
		here[plugin.Name(e.Spec)] = true
		// the package that computer installed, as it has it: never
		// taken for a community plugin's name, or held while the
		// community's list can't be read
		got, err := addPluginWith(ctx, e.Spec, addPluginExact)
		if err == nil && e.Off {
			err = plugin.SetOff(got.Spec, true)
		}
		if err == nil && len(e.Options) > 0 {
			err = plugin.SetOptions(got.Spec, e.Options)
		}
		if err != nil {
			if failed == nil {
				failed = map[string]error{}
			}
			failed[e.Spec] = err
			continue
		}
		added = append(added, e.Spec)
	}
	if len(added) > 0 {
		handOver(ctx, false)
	}
	return added, failed
}

// NotSignedIn is the subscriptions among ps (as a backup or a sync brings
// them: their model picks and settings alone) that no account here is
// signed in to: they are listed once one is, and are to be signed in to on
// this computer. A subscription the user removed from magpie is not one.
func NotSignedIn(ps []Provider) []string {
	have := map[string]bool{}
	for _, p := range All() {
		have[p.ID] = true
	}
	return notSignedIn(ps, have)
}

// notSignedIn is NotSignedIn with the ids of the providers here given.
func notSignedIn(ps []Provider, have map[string]bool) []string {
	var out []string
	for _, p := range ps {
		// the same subscription signed in here under its other id (a
		// built-in moved onto its plugin on one computer and not on the
		// other, #1940) is signed in
		if p.ID == "" || !OnlyPicks(p) || p.Hidden || have[p.ID] || have[samePlace(p.ID)] || slices.Contains(out, p.ID) {
			continue
		}
		out = append(out, p.ID)
	}
	return out
}

// OnlyPicks is whether p, as providers.json keeps it, is a subscription's
// model picks and settings alone, with no endpoint or key of its own: it
// is signed in to, never given a key.
func OnlyPicks(p Provider) bool { return !hasEndpoint(normalize(p)) }
