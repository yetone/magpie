package provider

// A vendor may sell its plans and its pay as you go on keys of their own,
// each taken only at its own endpoints: Volcengine's Coding Plan at
// /api/coding, its Agent Plan at /api/plan, its pay as you go at /api/v3
// (yckonnnn on X, with one key of each in one provider). A key of such a
// preset (PresetDef.KeyPlans) may say which of its Regions it is for
// (KeyAccount.Plan, Provider.KeyPlan for the first): its requests go to
// that plan's endpoints, it is asked only for that plan's models, its
// model list is that plan's, and its windows and balance are read where
// that plan's are. A key that says none is on the provider's own plan, as
// every key was before.

import (
	"net/url"
	"strings"
)

// keyRegion is the region of p's preset a key's plan names, and the
// region p sits at; nil for a key on p's own plan, or a preset whose keys
// are all on one.
func (p Provider) keyRegion(plan string) (pr *PresetDef, r, cur *Region) {
	if plan == "" {
		return nil, nil, nil
	}
	pr = Preset(p.Preset)
	if pr == nil || !pr.KeyPlans {
		return nil, nil, nil
	}
	for i := range pr.Regions {
		if pr.Regions[i].ID == plan {
			r = &pr.Regions[i]
		}
	}
	if r == nil {
		return nil, nil, nil // a plan the preset no longer has: the provider's
	}
	cur = p.regionOf(pr)
	if cur != nil && cur.ID == r.ID {
		return nil, nil, nil
	}
	return pr, r, cur
}

// atKeyPlan is p at the endpoints of the plan a key is for (its Region),
// with that plan's key page, docs and catalog; p as it is for a key on its
// own plan. A provider asked at another host than its preset's (a mirror,
// a test's fake vendor) is asked at that host on the other plan too.
func (p Provider) atKeyPlan(plan string) Provider {
	pr, r, cur := p.keyRegion(plan)
	if r == nil {
		return p
	}
	host := p.ownHost(cur)
	p.atRegionOf(pr, *r)
	if host != "" {
		for _, u := range []*string{&p.Chat, &p.Responses, &p.Anthropic} {
			*u = rehost(*u, host)
		}
	}
	return p
}

// ownHost is the scheme and host p is asked at when they aren't its
// region's own, "" when they are.
func (p Provider) ownHost(cur *Region) string {
	if cur == nil {
		return ""
	}
	for _, e := range [][2]string{{p.Chat, cur.Chat}, {p.Responses, cur.Responses}, {p.Anthropic, cur.Anthropic}} {
		if e[0] == "" || e[1] == "" {
			continue
		}
		a, err1 := url.Parse(e[0])
		b, err2 := url.Parse(e[1])
		if err1 != nil || err2 != nil || strings.EqualFold(a.Host, b.Host) {
			return ""
		}
		return a.Scheme + "://" + a.Host
	}
	return ""
}

// rehost is raw at host (scheme://host) instead of its own.
func rehost(raw, host string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	h, err := url.Parse(host)
	if err != nil {
		return raw
	}
	u.Scheme, u.Host = h.Scheme, h.Host
	return u.String()
}

// PlansMixed reports whether p's keys in use are on more than one of its
// preset's plans (KeyAccount.Plan).
func (p Provider) PlansMixed() bool { return p.plansMixed() }

// plansMixed reports whether a key of p in use is for another plan than
// p's own: only then do its keys differ in endpoints and models. Read
// from the keys themselves, not KeysOn, which a test counts per request.
func (p Provider) plansMixed() bool {
	if p.Key == "" || p.keyView {
		return false
	}
	pr := Preset(p.Preset)
	if pr == nil || !pr.KeyPlans {
		return false
	}
	if _, r, _ := p.keyRegion(p.KeyPlan); r != nil {
		return true
	}
	for _, k := range p.Keys {
		if k.Off || k.Key == "" || k.Key == p.Key {
			continue
		}
		if _, r, _ := p.keyRegion(k.Plan); r != nil {
			return true
		}
	}
	return false
}

// planList is the models a plan has as the preset says, for a key at it:
// its region's own, or the preset's for a preset that lists nothing at a
// region that doesn't list either; with its embedding, drawing and video
// models. nil when the plan's models are its vendor's list's to say.
func (p Provider) planList() []string {
	pr := Preset(p.Preset)
	if pr == nil {
		return nil
	}
	r := p.regionOf(pr)
	if r == nil {
		return nil
	}
	var out []string
	switch {
	case r.Models != nil:
		out = append(out, r.Models...)
	case pr.NoList && !r.Lists:
		out = append(out, pr.Models...)
	}
	for _, l := range [][]string{r.Embeddings, r.Rerankers, r.Drawers, r.Videos} {
		out = append(out, l...)
	}
	return out
}

// PlanServing is, for each of p's keys, whether the plan that key is for
// can serve model: false only when p's keys are on different plans
// (KeyAccount.Plan) and the key's plan names its models and leaves this
// one out — a pay-as-you-go model id sent to the Agent Plan's endpoint
// only answers 404. A model the user set the key to serve
// (AccountModels) it serves. Every key serves when p's keys are on one
// plan, as before.
func (p Provider) PlanServing(model string) func(KeyAccount) bool {
	if !p.plansMixed() {
		return func(KeyAccount) bool { return true }
	}
	lists := map[string][]string{} // each plan's, found once
	return func(k KeyAccount) bool {
		if m, ok := p.AccountModels[keyID(k.Key)]; ok && containsFold(m, model) {
			return true
		}
		l, ok := lists[k.Plan]
		if !ok {
			l = p.atKeyPlan(k.Plan).planList()
			lists[k.Plan] = l
		}
		return l == nil || containsFold(l, model)
	}
}

func containsFold(l []string, s string) bool {
	for _, x := range l {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// planViews is each provider of ps once for each plan its keys in use are
// on, at that plan's endpoints, with those keys alone: a plan's windows
// read with an access key (Volcengine's, Tencent Cloud's) are read for
// each plan a provider has a key of.
func planViews(ps []Provider) []Provider {
	var out []Provider
	for _, p := range ps {
		if !p.plansMixed() {
			out = append(out, p)
			continue
		}
		at := map[string]int{}
		for _, k := range p.KeysOn() {
			q := p.atKeyPlan(k.Plan)
			if i, ok := at[q.Chat+" "+q.Anthropic]; ok {
				out[i].Keys = append(out[i].Keys, k)
				continue
			}
			at[q.Chat+" "+q.Anthropic] = len(out)
			q.setFirst(k)
			q.Keys = nil
			out = append(out, q)
		}
	}
	return out
}
