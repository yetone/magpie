package provider

import (
	"context"
	"fmt"
	"slices"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's provider leaves a record in providers.json once the user picks
// its models or changes a setting on it: its id, and no endpoint or key of
// its own (OnlyPicks). Removing the plugin left that record behind, where
// nothing listed it and `magpie provider rm` couldn't find it, and a sync
// or a backup then asked for a sign-in to it on every run (#2057, dawn1095).
// So removing a plugin sets its records aside, and adding it again puts
// them back.

// LeftRecord is a record set aside when the plugin it was one of was
// removed: Package is the plugin's package name, as plugin.PackageName
// gives it.
type LeftRecord struct {
	Package  string   `json:"package"`
	Provider Provider `json:"provider"`
}

// pluginRecordIDs are the ids the records in providers.json that the
// plugin spec made have: its providers' ids as magpie lists them, apart
// from a built-in's (whose picks are the built-in's own) and one another
// installed plugin serves too. false when the plugins' listing isn't
// known, which sets nothing aside.
func pluginRecordIDs(spec string) ([]string, bool) {
	listed, ok := plugin.Listed()
	if !ok {
		return nil, false
	}
	installed := map[string]bool{}
	for _, e := range plugin.Load().Plugins {
		installed[e.Spec] = true
	}
	others := map[string]bool{}
	for _, pp := range listed {
		if pp.Spec != spec && installed[pp.Spec] {
			others[PluginID(pp.ID)] = true
		}
	}
	var ids []string
	for _, pp := range listed {
		id := PluginID(pp.ID)
		if pp.Spec != spec || id == "" || id == "magpie" || slices.Contains(accountIDs, id) || others[id] || slices.Contains(ids, id) {
			continue
		}
		ids = append(ids, id)
	}
	return ids, true
}

// installedSpec is the spec of the installed plugin named (by its spec or
// its name, as plugin.Remove takes it), "" for none.
func installedSpec(name string) string {
	es := plugin.Load().Plugins
	if i := slices.IndexFunc(es, func(e plugin.Entry) bool { return e.Spec == name }); i >= 0 {
		return es[i].Spec
	}
	if i := slices.IndexFunc(es, func(e plugin.Entry) bool { return plugin.Name(e.Spec) == name }); i >= 0 {
		return es[i].Spec
	}
	return ""
}

// setAside moves the records with the ids given that hold only picks out
// of providers.json's providers into its pluginLeft, under pkg. A record
// with an endpoint or a key is the user's own provider, never the
// plugin's, and stays where it is.
func setAside(pkg string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	f, err := read()
	if err != nil {
		return err
	}
	keep := make([]Provider, 0, len(f.Providers))
	moved := false
	for _, p := range f.Providers {
		if slices.Contains(ids, p.ID) && OnlyPicks(p) {
			// one set aside before under the same id gives way to this
			f.PluginLeft = slices.DeleteFunc(f.PluginLeft, func(l LeftRecord) bool { return l.Provider.ID == p.ID })
			f.PluginLeft = append(f.PluginLeft, LeftRecord{Package: pkg, Provider: p})
			moved = true
			continue
		}
		keep = append(keep, p)
	}
	if !moved {
		return nil
	}
	f.Providers = keep
	return store(f)
}

// takeBackLeft puts the records set aside for the plugin spec back among the
// providers, each one whose id has no record now; one that has (the user
// gave the id to a provider since) stays aside.
func takeBackLeft(spec string) error {
	pkg := plugin.PackageName(spec)
	f, err := read()
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(f.PluginLeft, func(l LeftRecord) bool { return l.Package == pkg }) {
		return nil
	}
	left := f.PluginLeft[:0]
	changed := false
	for _, l := range f.PluginLeft {
		if l.Package == pkg && !slices.ContainsFunc(f.Providers, func(p Provider) bool { return p.ID == l.Provider.ID }) {
			f.Providers = append(f.Providers, l.Provider)
			changed = true
			continue
		}
		left = append(left, l)
	}
	if !changed {
		return nil
	}
	f.PluginLeft = left
	return store(f)
}

// AddPlugin installs the plugin spec, as plugin.Add does, and puts back
// what was set aside of it when it was last removed.
func AddPlugin(ctx context.Context, spec string) (plugin.Entry, error) {
	e, err := addPluginWith(ctx, spec, addPlugin)
	if err == nil {
		// a partner's plugin installed counts for it, as a provider added
		// from a partner does (partner_events.go); one carried over from a
		// backup (AddPlugins) is no new install
		countPluginPartner(plugin.Name(e.Spec))
	}
	return e, err
}

// addPluginWith is AddPlugin, installing with add: plugin.Add for a name
// the user typed, plugin.AddExact for a spec carried from elsewhere.
func addPluginWith(ctx context.Context, spec string, add func(context.Context, string) (plugin.Entry, error)) (plugin.Entry, error) {
	e, err := add(ctx, spec)
	if err != nil {
		return e, err
	}
	if err := takeBackLeft(e.Spec); err != nil {
		return e, fmt.Errorf("%s is added; its model picks from before weren't put back: %w", e.Spec, err)
	}
	return e, nil
}
