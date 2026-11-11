package provider

// One vendor's keys, each saved as a provider of its own (yuluo_yx on X:
// "NVIDIA #001" to "NVIDIA #600" at integrate.api.nvidia.com, "Mistral
// #0001"… at api.mistral.ai, brought in from another app that keeps an
// entry per key) are one provider with that many keys: its Providers row
// is one row, its models are listed once in every agent's picker and in
// the routing groups magpie finds, and requests go over the keys as the
// provider's Routing says. Hundreds of copies made every agent's list
// hundreds of times as long: /api/state was 86 MB for 3,000 keys.
//
// Folded are only providers alike in everything but their key: the same
// address, models and settings, and what the user said of their models.
// The fold keeps what each one was: its id goes on the provider that took
// it in (Was), so its calls are counted with it, per key, and an agent
// still set to a model by the old id reaches it; its name is its key's
// name; one that was off is a key that is off; the keys follow the order
// the providers were in.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/settings"
)

// numbered is a name with a number after it that tells copies apart:
// "NVIDIA #595", "Mistral #0001", or "NVIDIA NIM 2" as freeName names a
// second one alike.
var (
	numberedHash  = lazyre.New(`^(.*\S)\s*#\s*\d+$`)
	numberedSpace = lazyre.New(`^(.*\S) (\d+)$`)
)

// copyBase is the name a numbered copy is a copy of, and whether name is
// numbered at all.
func copyBase(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if m := numberedHash.FindStringSubmatch(name); m != nil {
		return m[1], true
	}
	if m := numberedSpace.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[2]); err == nil && n >= 2 {
			return m[1], true
		}
	}
	return name, false
}

// foldable is whether p can be folded with another: a provider of a key
// at an address of the user's, not a subscription's, a hidden one or one
// with a StepFun sign-in of its own (kept by its id).
func foldable(p Provider, stepfun map[string]stepfunSession) bool {
	if p.Account != nil || p.Hidden || p.Key == "" || !hasEndpoint(p) || p.IsVertex() {
		return false
	}
	if slices.Contains(accountIDs, p.ID) || subscriptionID(p.ID) || p.ID == "magpie" {
		return false
	}
	_, signed := stepfun[p.ID]
	return !signed
}

// foldShape is p as providers.json has it with what tells one key's copy
// from another's left out: its id, name, keys and whether it is on. Two
// providers fold only when their shapes and their model settings (prefs)
// are the same.
func foldShape(p Provider, prefs string) string {
	p.ID, p.Name, p.Was, p.Gave = "", "", nil, nil
	p.Key, p.KeyName, p.Keys, p.KeyProtocol, p.KeyWeight, p.KeyPlan = "", "", nil, "", 0, ""
	p.Off = false
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(b) + "\x00" + prefs
}

// modelPrefs is what the user said of id's models, in every per-model map
// of the settings, by the model's id without the provider's: two copies
// alike have the same.
func modelPrefs(s *settings.Settings, id string) string {
	var out []string
	settings.PerModelKeys(s, func(name string, m reflect.Value) {
		if !m.IsValid() || m.Kind() != reflect.Map {
			return
		}
		for _, k := range m.MapKeys() {
			rest, ok := strings.CutPrefix(k.String(), id+"/")
			if !ok {
				continue
			}
			v, _ := json.Marshal(m.MapIndex(k).Interface())
			out = append(out, name+"\x01"+rest+"\x01"+string(v))
		}
	})
	slices.Sort(out)
	return strings.Join(out, "\x02")
}

// foldRefs is, by provider id, where the providers' list and the settings
// name it, each place with the model it names there and without the id:
// the agents shown it (Visible), the models picked, hidden or ordered for
// an agent, the groups' members and what they say of them, and the
// providers that fall back to it. Two copies named in different places
// are told apart by the user — one key for this agent, another for that —
// and are not folded; named in the same, they fold, and each place names
// the one they went into once.
func foldRefs(f file, s *settings.Settings) map[string][]string {
	out := map[string][]string{}
	note := func(where, ref string) {
		r := strings.TrimPrefix(ref, "magpie/")
		id, model, ok := strings.Cut(r, "/")
		if !ok {
			id, model = r, ""
		}
		if id == "" || id == "group" {
			return
		}
		out[id] = append(out[id], where+"\x01"+model)
	}
	for a, names := range s.Visible {
		for _, n := range names {
			note("visible\x01"+a, n)
		}
	}
	lists := map[string]map[string][]string{"hidden": s.HiddenModels, "picked": s.PickedModels, "ordered": s.OrderedModels, "fast": s.FastPicks, "ultra": s.UltraPicks}
	for name, l := range lists {
		for a, ids := range l {
			for _, id := range ids {
				note(name+"\x01"+a, id)
			}
		}
	}
	for _, g := range f.Groups {
		at := "group\x01" + g.ID + "\x01"
		for _, m := range g.Members {
			note(at+"member", m)
		}
		for _, m := range g.Fast {
			note(at+"fast", m)
		}
		for _, m := range g.Ultra {
			note(at+"ultra", m)
		}
		for _, m := range g.Off {
			note(at+"off", m)
		}
		for m, w := range g.Weights {
			note(at+"weight"+strconv.Itoa(w), m)
		}
		for m, to := range g.EffortMaps {
			pairs := make([]string, 0, len(to))
			for from, l := range to {
				pairs = append(pairs, from+">"+l)
			}
			slices.Sort(pairs)
			note(at+"effortmap"+strings.Join(pairs, ","), m)
		}
		if g.Pick != "" {
			note(at+"pick", g.Pick)
		}
		if g.Classifier != "" {
			note(at+"classifier", g.Classifier)
		}
		for i := range g.Rules {
			r := g.Rules[i]
			r.MapUses(func(u string) string { note(at+"rule"+strconv.Itoa(i), u); return u })
		}
	}
	for _, p := range f.Providers {
		for _, fb := range p.Fallback {
			note("fallback\x01"+p.ID, fb)
		}
	}
	for id := range out {
		slices.Sort(out[id])
	}
	return out
}

// FoldCopies folds the providers that are copies of one, a key each
// (see above), as providers.json has them now. Run at start-up, so a
// user who has them already (from an import before this) finds them
// folded. It answers how many providers were folded into others. A
// providers.json that can't be read is left alone.
func FoldCopies() (int, error) {
	return foldWhere(func(ps []Provider) [][]int { return copiesByName(ps) })
}

// foldAdded folds the providers with ids among added that are alike but
// for their keys, whatever their names: what one import brought in at
// once, an entry a key, as CC Switch keeps them.
func foldAdded(added []string) (int, error) {
	if len(added) < 2 {
		return 0, nil
	}
	in := map[string]bool{}
	for _, id := range added {
		in[id] = true
	}
	return foldWhere(func(ps []Provider) [][]int {
		var at []int
		for i, p := range ps {
			if in[p.ID] {
				at = append(at, i)
			}
		}
		return [][]int{at}
	})
}

// copiesByName is the providers of ps that may be copies of one another,
// by the name they are copies of: those whose name is the same but for a
// number after it, one at least numbered.
func copiesByName(ps []Provider) [][]int {
	byBase := map[string][]int{}
	numbered := map[string]bool{}
	var bases []string
	for i, p := range ps {
		base, num := copyBase(p.Name)
		k := strings.ToLower(base)
		if _, ok := byBase[k]; !ok {
			bases = append(bases, k)
		}
		byBase[k] = append(byBase[k], i)
		numbered[k] = numbered[k] || num
	}
	var out [][]int
	for _, k := range bases {
		if numbered[k] && len(byBase[k]) > 1 {
			out = append(out, byBase[k])
		}
	}
	return out
}

// foldWhere folds, among each set of providers candidates names (indexes
// into providers.json's list in the order the user put them), those alike
// but for their keys into the first of them that is on.
func foldWhere(candidates func([]Provider) [][]int) (int, error) {
	// read from its last good generation, the file is the user's to mend:
	// a fold would write that generation over it
	if b, err := os.ReadFile(Path()); err == nil && !validProviders(b) {
		return 0, &unreadableError{Path(), errors.New("it doesn't parse")}
	}
	f, err := read()
	if err != nil {
		return 0, err
	}
	if len(f.Providers) < 2 {
		return 0, nil
	}
	ps := ordered(slices.Clone(f.Providers), f.Order)
	stepfunMu.Lock()
	stepfun := loadStepFun()
	stepfunMu.Unlock()
	s := settings.Load()
	refs := foldRefs(f, &s)

	// each set of copies alike, by its shape, in the order of ps
	var folds [][]int
	for _, set := range candidates(ps) {
		byShape := map[string][]int{}
		var shapes []string
		for _, i := range set {
			p := ps[i]
			if !foldable(p, stepfun) {
				continue
			}
			sh := foldShape(p, modelPrefs(&s, p.ID)+"\x00"+strings.Join(refs[p.ID], "\x02"))
			if sh == "" {
				continue
			}
			if _, ok := byShape[sh]; !ok {
				shapes = append(shapes, sh)
			}
			byShape[sh] = append(byShape[sh], i)
		}
		for _, sh := range shapes {
			if len(byShape[sh]) > 1 {
				folds = append(folds, byShape[sh])
			}
		}
	}
	if len(folds) == 0 {
		return 0, nil
	}

	now := time.Now()
	gone := map[string]string{} // a folded provider's id → the one it went into
	into := map[string]Provider{}
	for _, set := range folds {
		p, ids := foldSet(ps, set)
		into[p.ID] = p
		for _, id := range ids {
			gone[id] = p.ID
		}
	}
	// providers.json's list as it was, the folded ones out and each that
	// took them in where it was
	kept := f.Providers[:0:0]
	for _, p := range f.Providers {
		if _, ok := gone[p.ID]; ok {
			continue
		}
		if q, ok := into[p.ID]; ok {
			p = q
		}
		kept = append(kept, p)
	}
	f.Providers = kept
	// a name another provider has already stays the copy's own
	names := map[string]int{}
	for _, p := range f.Providers {
		names[strings.ToLower(p.Name)]++
	}
	for j, p := range f.Providers {
		if _, ok := into[p.ID]; ok && names[strings.ToLower(p.Name)] > 1 && p.KeyName != "" {
			if i := slices.IndexFunc(ps, func(q Provider) bool { return q.ID == p.ID }); i >= 0 && ps[i].Name != p.Name {
				f.Providers[j].Name = ps[i].Name
			}
		}
	}
	// an id one of them gave away before is the one's that has it now
	for id := range gone {
		giveID(f.Providers, slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == gone[id] }), id, now)
	}
	to := func(id string) string {
		if t, ok := gone[id]; ok {
			return t
		}
		return id
	}
	ref := func(m string) string { return foldedRef(m, to) }
	f.Order = compact(mapEach(f.Order, to))
	for j := range f.Providers {
		f.Providers[j].Fallback = compact(mapEach(f.Providers[j].Fallback, ref))
	}
	for j := range f.Groups {
		g := &f.Groups[j]
		g.Members = compact(mapEach(g.Members, ref))
		for k := range g.Rules {
			g.Rules[k].MapUses(ref)
		}
		g.Fast = compact(mapEach(g.Fast, ref))
		g.Ultra = compact(mapEach(g.Ultra, ref))
		g.Placed = compact(mapEach(g.Placed, ref))
		g.Seen = compact(mapEach(g.Seen, ref))
		g.Off = compact(mapEach(g.Off, ref))
		g.Weights = renamedWeights(g.Weights, ref)
		g.EffortMaps = renamedEffortMaps(g.EffortMaps, ref)
		g.Classifier = ref(g.Classifier)
		g.Pick = ref(g.Pick)
	}
	if err := store(f); err != nil {
		return 0, err
	}
	for id := range gone {
		// the list last fetched for it is the one it went into's
		_ = os.Remove(catalog.LivePath(id))
		_ = os.Remove(catalog.LivePath(decisionsID(id)))
	}
	foldSettings(&s, gone, ref)
	foldHistories(folds, ps)
	return len(gone), nil
}

// foldSet is the provider the copies ps[set…] fold into — the first of
// them that is on, or the first when none is — with every key of the
// others after its own in their order, and the ids of the others.
func foldSet(ps []Provider, set []int) (Provider, []string) {
	head := set[0]
	for _, i := range set {
		if !ps[i].Off {
			head = i
			break
		}
	}
	allOff := ps[head].Off
	p := ps[head].clone()
	// "NVIDIA #001"'s key is named so, and the provider is NVIDIA, when
	// every copy's name is NVIDIA's
	base, _ := copyBase(p.Name)
	same := base != ""
	for _, i := range set {
		b, _ := copyBase(ps[i].Name)
		same = same && strings.EqualFold(b, base)
	}
	if same && base != p.Name {
		if p.KeyName == "" {
			p.KeyName = p.Name
		}
		p.Name = base
	}
	have := map[string]bool{p.Key: true}
	for _, k := range p.Keys {
		have[k.Key] = true
	}
	var ids []string
	for _, i := range set {
		if i == head {
			continue
		}
		q := ps[i]
		ids = append(ids, q.ID)
		p.Was = append(p.Was, q.ID)
		p.Was = append(p.Was, q.Was...)
		p.Gave = append(p.Gave, q.Gave...)
		off := q.Off && !allOff
		first := KeyAccount{Name: cmpOr(q.KeyName, q.Name), Key: q.Key, Off: off, Protocol: q.KeyProtocol, Weight: q.KeyWeight, Plan: q.KeyPlan}
		for _, k := range append([]KeyAccount{first}, q.Keys...) {
			if k.Key == "" || have[k.Key] {
				continue
			}
			have[k.Key] = true
			k.Off = k.Off || off
			p.Keys = append(p.Keys, k)
		}
	}
	p.Was = compact(p.Was)
	return p, ids
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// foldedRef is a model id ("provider/model", "magpie/provider/model")
// with its provider as to names it now.
func foldedRef(ref string, to func(string) string) string {
	pre, rest := "", ref
	if r, ok := strings.CutPrefix(ref, "magpie/"); ok {
		pre, rest = "magpie/", r
	}
	id, m, ok := strings.Cut(rest, "/")
	if !ok {
		return ref
	}
	if t := to(id); t != id {
		return pre + t + "/" + m
	}
	return ref
}

func mapEach(xs []string, f func(string) string) []string {
	if xs == nil {
		return nil
	}
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = f(x)
	}
	return out
}

// compact is xs with each repeat after the first left out.
func compact(xs []string) []string {
	if len(xs) < 2 {
		return xs
	}
	seen := make(map[string]bool, len(xs))
	out := xs[:0:0]
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// foldSettings moves what the settings say by a folded provider's id to
// the one it went into. Its per-model preferences were the same as that
// one's (foldShape), so they go; a picker's lists of models and an agent's
// providers name the one it went into, once.
func foldSettings(s *settings.Settings, gone map[string]string, ref func(string) string) {
	before, _ := json.Marshal(s)
	settings.PerModelKeys(s, func(_ string, m reflect.Value) {
		if !m.IsValid() || m.Kind() != reflect.Map {
			return
		}
		for _, k := range m.MapKeys() {
			id, _, ok := strings.Cut(k.String(), "/")
			if _, folded := gone[id]; ok && folded {
				m.SetMapIndex(k, reflect.Value{})
			}
		}
	})
	for _, lists := range []map[string][]string{s.HiddenModels, s.PickedModels, s.OrderedModels, s.FastPicks, s.UltraPicks} {
		for k, ids := range lists {
			lists[k] = compact(mapEach(ids, ref))
		}
	}
	for a, names := range s.Visible {
		s.Visible[a] = compact(mapEach(names, func(n string) string {
			if t, ok := gone[strings.ToLower(n)]; ok {
				return t
			}
			return ref(n)
		}))
	}
	if after, _ := json.Marshal(s); !bytes.Equal(before, after) {
		_ = settings.Save(*s)
	}
}

// foldHistories moves the balances and windows kept for each folded
// provider's key, kept by the provider's id while it had one key, to the
// key's name on the one it went into, where they are read now.
func foldHistories(folds [][]int, ps []Provider) {
	moves := map[string]string{} // a folded key's provider id → "into|key name"
	for _, set := range folds {
		p, _ := foldSet(ps, set)
		if src, ok := balanceSourceOf(p); ok && src.token != "" {
			// a balance asked with a token of the account's is the
			// provider's, kept by its id alone, whichever key asks
			continue
		}
		for _, i := range set {
			q := ps[i]
			name := cmpOr(q.KeyName, q.Name)
			if q.ID == p.ID {
				name = cmpOr(p.KeyName, Mask(p.Key))
			}
			moves[q.ID] = quotaHistKey(p.ID, name)
		}
	}
	// "id|" is a provider's with one key, "id||sign" one of its lines of a
	// balance in several currencies
	moved := func(k string) (string, bool) {
		id, rest, ok := strings.Cut(k, "|")
		if !ok {
			return "", false
		}
		user, sign, signed := strings.Cut(rest, "|")
		to, ok := moves[id]
		if !ok || user != "" {
			return "", false
		}
		if signed {
			return to + "|" + sign, true
		}
		return to, true
	}
	balanceHistMu.Lock()
	if _, err := os.Stat(balanceHistPath()); err == nil {
		h := readBalanceHist()
		changed := false
		for k, pts := range h {
			if to, ok := moved(k); ok {
				delete(h, k)
				h[to] = append(h[to], pts...)
				changed = true
			}
		}
		if changed {
			writeBalanceHist(h)
		}
	}
	balanceHistMu.Unlock()
	quotaHistMu.Lock()
	if _, err := os.Stat(quotaHistPath()); err == nil {
		h := heldQuotaHist()
		changed := false
		for k, lines := range h {
			if to, ok := moved(k); ok {
				delete(h, k)
				if h[to] == nil {
					h[to] = lines
				} else {
					for w, pts := range lines {
						h[to][w] = append(h[to][w], pts...)
					}
				}
				changed = true
			}
		}
		if changed {
			writeQuotaHist(h)
		}
	}
	quotaHistMu.Unlock()
}
