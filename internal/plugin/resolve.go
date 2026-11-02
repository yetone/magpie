package plugin

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// SubscriptionPlugin is the community package that serves a retired
// built-in subscription, by its id (commandcode-plan, qoder-cn), "" for
// none: provider's movers, which plugin can't import, set it.
var SubscriptionPlugin = func(id string) string { return "" }

// npmPrefix asks for npm's own package of a name, as written, where the
// name is also one a community plugin is called by: npm:think-tags is
// npm's think-tags, not @magpie-community/middleware-think-tags.
const npmPrefix = "npm:"

// callNames are the names a listing goes by, lower-cased: its package,
// that without the scope and its short name (think-tags, workbuddy), its
// name as shown (WorkBuddy, Command Code, with and without the space),
// and the provider ids it serves or the subscription it replaces.
func callNames(l Listing) []string {
	n := strings.ToLower(strings.TrimSpace(l.Name))
	out := []string{l.Package, bareName(l.Package), ShortName(l.Package), n, strings.ReplaceAll(n, " ", ""), strings.ReplaceAll(n, " ", "-"), l.Replaces}
	out = append(out, l.Providers...)
	for i := range out {
		out[i] = strings.ToLower(out[i])
	}
	return out
}

// resolve is the npm spec a user's name for a plugin stands for: a short
// or shown name (workbuddy, WorkBuddy, think-tags, "Qoder", qoder-cn)
// that names one of magpie's community plugins, case aside, is that
// package, at the version given. A scoped package, a path or a git spec is
// kept as it is, and so is a name no community plugin goes by, which is
// npm's own (opencode-gemini-auth) once the list was read just now: a
// short name never reaches npm's unscoped package of that name while a
// community plugin is called by it, or may be, as far as magpie knows.
// npm:name is npm's name, as written.
func resolve(ctx context.Context, spec string) (string, error) {
	if after, ok := strings.CutPrefix(spec, npmPrefix); ok {
		after = strings.TrimSpace(after)
		if !pkgName.MatchString(Name(after)) {
			return "", fmt.Errorf("%q isn't an npm package name", after)
		}
		return after, nil
	}
	if IsPath(spec) || IsGit(spec) || strings.HasPrefix(spec, "@") {
		return spec, nil
	}
	name, version := spec, ""
	if at := strings.LastIndex(spec, "@"); at > 0 {
		name, version = spec[:at], spec[at:]
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return spec, nil
	}
	// the community's alone: an author's own listing is npm's name, as
	// written
	list, lerr := market(ctx)
	list = Community(list)
	var found []string
	for _, l := range list {
		if slices.Contains(callNames(l), key) && !slices.Contains(found, l.Package) {
			found = append(found, l.Package)
		}
	}
	if p := SubscriptionPlugin(key); p != "" && !slices.Contains(found, p) {
		found = append(found, p)
	}
	switch {
	case len(found) == 1:
		return found[0] + version, nil
	case len(found) > 1:
		sort.Strings(found)
		return "", fmt.Errorf("%q names more than one of magpie's community plugins: %s; give the one you mean", name, strings.Join(found, ", "))
	}
	valid := pkgName.MatchString(name)
	if valid && lerr == nil {
		// npm's own package: no community plugin goes by the name
		return spec, nil
	}
	var b strings.Builder
	if lerr != nil {
		fmt.Fprintf(&b, "couldn't read the community plugin list (%v), so there is no telling whether %q is one of them", lerr, name)
	} else {
		fmt.Fprintf(&b, "no plugin is called %q", name)
	}
	if names := listNames(list); names != "" {
		b.WriteString(". The community plugins")
		if lerr != nil {
			b.WriteString(" magpie knows of")
		}
		b.WriteString(": " + names)
	}
	if valid {
		fmt.Fprintf(&b, ". Try again once the list can be read, give the plugin's full package name (@magpie-community/…), or npm:%s for npm's package %s", name, name)
	} else {
		b.WriteString(". Or give an npm package, a GitHub repo (github:owner/repo) or a folder")
	}
	return "", fmt.Errorf("%s", b.String())
}

// listNames are the listings as a user writes them: the short name, and
// the name shown where it differs.
func listNames(list []Listing) string {
	var out []string
	for _, l := range list {
		s := ShortName(l.Package)
		if l.Name != "" && !strings.EqualFold(l.Name, s) {
			s += " (" + l.Name + ")"
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
