package catalog

import (
	"slices"
	"strconv"
	"strings"
)

// shareCatalog has what the loaded catalog's models repeat held once: the
// JSON gives each of ~9k models its own copy of "text", "image", its
// family, its release date, its levels and its SDK's name, and the catalog
// is kept while anything reads it (#2278). A list is shared clipped, so an
// append to one copies it rather than writing into another model's.
func shareCatalog(m map[string]mdProvider) {
	strs := map[string]string{}
	str := func(s string) string {
		if s == "" {
			return ""
		}
		if k, ok := strs[s]; ok {
			return k
		}
		strs[s] = s
		return s
	}
	lists := map[string][]string{}
	list := func(l []string) []string {
		if l == nil {
			return nil
		}
		k := strconv.Itoa(len(l)) + "\x00" + strings.Join(l, "\x00")
		if v, ok := lists[k]; ok {
			return v
		}
		for i := range l {
			l[i] = str(l[i])
		}
		l = slices.Clip(l)
		lists[k] = l
		return l
	}
	sdks := map[string]any{}
	for pid, p := range m {
		p.Env = list(p.Env)
		for id, x := range p.Models {
			if x.ID == id {
				x.ID = id // the map's key already holds it
			}
			x.Name, x.ReleaseDate, x.Family, x.Canonical = str(x.Name), str(x.ReleaseDate), str(x.Family), str(x.Canonical)
			x.Modalities.Input, x.Modalities.Output = list(x.Modalities.Input), list(x.Modalities.Output)
			for i := range x.Reasoning {
				r := &x.Reasoning[i]
				r.Type, r.Values = str(r.Type), list(r.Values)
			}
			if x.Provider != nil {
				x.Provider = sharedOf(sdks, x.Provider.NPM, x.Provider)
			}
			p.Models[id] = x
		}
		m[pid] = p
	}
}

// sharedOf is the value first kept under k, keeping v when there is none.
func sharedOf[T any](kept map[string]any, k string, v T) T {
	if was, ok := kept[k]; ok {
		return was.(T)
	}
	kept[k] = v
	return v
}
