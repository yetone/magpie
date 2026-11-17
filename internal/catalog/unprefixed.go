package catalog

import (
	"strings"
	"sync"
)

// Unprefixed is the model a provider's id is when the provider lists it
// without its maker's name in front, by what the provider's own catalogs
// (models.dev ids, first match wins) say of that very id: the spelling
// models.dev gives the model (Spelling), "" when nothing says so.
//
// Kimi Code lists Kimi K3.1 Pro as k3.1-pro and K3 as k3, whose entries
// models.dev gives the canonical_model_id moonshotai/kimi-k3.1-pro and
// moonshotai/kimi-k3; OpenCode Go and Devin serve them as kimi-k3.1-pro,
// so the group magpie found for the model left Kimi Code out (tmr11235 on
// X). A relay's minimax/m3.1-flash is MiniMax-M3.1-Flash the same way.
//
// Only a dropped prefix counts, never the canonical id alone: models.dev
// gives Kimi Code's k3.1-pro-highspeed the canonical kimi-k3.1-pro and
// k3-256k kimi-k3, which are other models (another speed, another
// window). So the id is the canonical's with a maker's name taken off the
// front — words of letters, "kimi-", "minimax-" — and nothing else: k3.1-pro
// is kimi-k3.1-pro, never kimi-k3.1 nor kimi-k3. An id that goes on past
// the canonical (k3.1-pro-highspeed past kimi-k3.1-pro) is that maker's
// name in front of the whole id, kimi-k3.1-pro-highspeed, only where
// models.dev knows a model by it; k3-256k, whose kimi-k3-256k it doesn't,
// stays its own.
func Unprefixed(catalogs []string, id string) string {
	if len(catalogs) == 0 || id == "" {
		return ""
	}
	all, at := loadGen()
	k := SameModel(id)
	for _, c := range catalogs {
		if s, ok := unprefixedOf(all, at, c)[k]; ok {
			return s
		}
	}
	return ""
}

var unprefixedIndexes struct {
	sync.Mutex
	gen int
	by  map[string]map[string]string
}

// unprefixedOf is, for the models.dev provider pid, the spelling each of
// its models listed without a maker's prefix is (Unprefixed), by the
// model's SameModel id, built once a load.
func unprefixedOf(all map[string]mdProvider, at int, pid string) map[string]string {
	unprefixedIndexes.Lock()
	defer unprefixedIndexes.Unlock()
	if unprefixedIndexes.by == nil || unprefixedIndexes.gen != at {
		unprefixedIndexes.gen, unprefixedIndexes.by = at, map[string]map[string]string{}
	}
	if x, ok := unprefixedIndexes.by[pid]; ok {
		return x
	}
	x := map[string]string{}
	for key, m := range all[pid].Models {
		if _, _, ok := strings.Cut(m.Canonical, "/"); !ok {
			continue
		}
		b, c := SameModel(key), SameModel(m.Canonical)
		if b == "" || b == c {
			continue
		}
		if s := unprefixedAs(b, c); s != "" {
			if was, ok := x[b]; !ok || s < was {
				x[b] = s
			}
		}
	}
	unprefixedIndexes.by[pid] = x
	return x
}

// unprefixedAs is the spelling of the model b (an id as SameModel gives
// it) is, given the canonical c it is listed under (also so given): c
// itself when b is c with a maker's name taken off the front, that name
// before b when b goes on past such a c and models.dev knows a model by
// it, else "".
func unprefixedAs(b, c string) string {
	for i := 0; i < len(c); i++ {
		if c[i] != '-' {
			continue
		}
		maker, rest := c[:i], c[i+1:]
		if !lettersOnly(maker) {
			return "" // a version or a size is no maker's name, nor after one
		}
		switch {
		case b == rest:
			return spellingOr(c)
		case strings.HasPrefix(b, rest+"-"):
			if s := normal[maker+"-"+b]; s != "" {
				return s
			}
		}
	}
	return ""
}

// spellingOr is models.dev's spelling of the model k (a SameModel id),
// else k.
func spellingOr(k string) string {
	if s := normal[k]; s != "" {
		return s
	}
	return k
}

// lettersOnly is whether s is a maker's name: words of letters a-z
// joined by "-" ("kimi", "moonshot-kimi").
func lettersOnly(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && c != '-' {
			return false
		}
	}
	return true
}
