package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// groupsJSON is what the Routing view manages: the routing groups, the
// models they can be made of, and each provider with several keys or
// accounts on — which any of its models routes over already.
type groupsJSON struct {
	Groups []groupJSON `json:"groups"`
	Models []modelRef  `json:"models"`
	Pools  []poolJSON  `json:"pools"`
	// Deciders: the decision providers' models (Jev), which may only be a
	// group's classifier
	Deciders []modelRef `json:"deciders"`
	// Found: magpie finds groups on its own (provider.AutoGroupsOn)
	Found bool `json:"found"`
	// Moved: the agents turning found groups off moved off one of them,
	// to its model from one provider (agent.Reseat)
	Moved []agent.Move `json:"moved,omitempty"`
}

type groupJSON struct {
	provider.Group
	Ready bool         `json:"ready"` // a member is: agents can pick it
	Info  []memberJSON `json:"memberInfo"`
	// Holds: the groups in it, at any depth — none of which can have it in
	// turn
	Holds []string `json:"holds"`
	// Offers: the reasoning levels agents are offered for it; Shared: those
	// its members have in common, which it offers unless it names its own
	Offers []string `json:"offers"`
	Shared []string `json:"shared"`
	// Picked: the member a manual group sends every request to
	Picked string `json:"picked,omitempty"`
	// Patterns: its patterns, each with how many models it matches now
	// (#766), so one that matches nothing is said on its card
	Patterns []provider.PatternHit `json:"patterns"`
}

type memberJSON struct {
	ID       string `json:"id"`
	Ready    bool   `json:"ready"`
	Provider string `json:"provider,omitempty"`
	Name     string `json:"name,omitempty"` // the provider's
	Icon     string `json:"icon,omitempty"`
	Model    string `json:"model,omitempty"` // what the vendor is asked for
	On       int    `json:"on"`              // its keys or accounts on
	Group    bool   `json:"group,omitempty"` // a routing group in the group; Name is its
	// ProviderOff: its provider is switched off, so the group skips it
	// until it is on again; Provider, Name, Icon and Model are still said
	ProviderOff bool `json:"providerOff,omitempty"`
	// Of is the model's id without the effort the member is fixed at, and
	// Effort that effort ("provider/model:low"); "" for one without
	Of     string `json:"of,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Fast: the group sends it in its vendor's fast mode; CanFast: its
	// model has one (provider.CanFast)
	Fast    bool `json:"fast,omitempty"`
	CanFast bool `json:"canFast,omitempty"`
	// Efforts: the reasoning levels this member is offered. A model's own,
	// or — a member that is itself a group — the levels it passes on to
	// agents, which are the ones every model in it has (provider.groupEntries).
	// The editor says them in the same chips a model's row does (modelInfo).
	Efforts []string `json:"efforts,omitempty"`
	// what a rule may send it: the tokens it takes, when known, and images
	Context int  `json:"context,omitempty"`
	Images  bool `json:"images,omitempty"`
	// ImagesUnknown: nothing was read of its images either way, so it counts
	// text-only for a describer (gateway.blindTo) without its list having
	// said it takes none. Sent only when true: a model magpie has an answer
	// for carries no such key, which is what the page reads as known (its
	// own `images` decides then). Named apart from the Gateway page's
	// `imageSet`, which means the user answered for the model themselves
	ImagesUnknown bool `json:"imagesUnknown,omitempty"`
}

type modelRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	PName    string `json:"providerName"`
	Icon     string `json:"icon,omitempty"`
	Context  int    `json:"context,omitempty"` // the tokens it takes, when known
	// Efforts: the model's reasoning levels, when known — those a group's
	// member of it may be fixed at
	Efforts []string `json:"efforts,omitempty"`
	// CanFast: a group's member of it may be sent in its vendor's fast
	// mode (provider.CanFast)
	CanFast bool `json:"canFast,omitempty"`
	// Images: agents are told it takes images (provider.Entry.Images); a
	// group's editor says of each member what it says of it (#756).
	// ImagesUnknown: nothing was read of it either way (imagesUnknown),
	// sent only when true
	Images        bool `json:"images,omitempty"`
	ImagesUnknown bool `json:"imagesUnknown,omitempty"`
}

type poolJSON struct {
	Provider string   `json:"provider"`
	Name     string   `json:"name"`
	Icon     string   `json:"icon,omitempty"`
	Kind     string   `json:"kind"` // "account" or "key"
	Who      []string `json:"who"`
	Routing  string   `json:"routing"`
	Affinity string   `json:"affinity"`
	Sink     bool     `json:"sink,omitempty"` // provider.Provider.Sink
	// Protocol: the one its keys are made for, when the provider's keys are
	// made for more than one — each protocol's keys are a pool of their own
	Protocol provider.Protocol `json:"protocol,omitempty"`
}

// seesImages is whether agents are told the model takes images: its list
// says so, and nothing said otherwise (provider.Entry).
func seesImages(e provider.Entry) bool { return e.Images && (e.ImageInput == nil || *e.ImageInput) }

// imagesUnknown is whether nothing was read of the model's images either way:
// no list said so, and what magpie reads of it doesn't say it sees. Such a
// model counts text-only for a describer (gateway.blindTo), which is not its
// list saying it takes none, and the page says unknown rather than text-only.
func imagesUnknown(e provider.Entry) bool { return e.ImageInput == nil && !e.Images }

// onOf is who a provider's requests spread over: its accounts, or keys.
func onOf(p provider.Provider) (kind string, who []string) {
	if p.Account != nil {
		who = append(who, p.Account.User)
		for _, q := range p.AlsoOn() {
			who = append(who, q.Account.User)
		}
		return "account", who
	}
	for _, k := range p.KeysOn() {
		n := k.Name
		if n == "" {
			n = provider.Mask(k.Key)
		}
		who = append(who, n)
	}
	return "key", who
}

// keyPools is a provider's keys by the protocol they're made for, as the
// gateway routes over them: keys made for different protocols are not one
// pool (gateway.perKeyOf).
func keyPools(p provider.Provider) []poolJSON {
	var out []poolJSON
	at := map[provider.Protocol]int{}
	for _, k := range p.KeysOn() {
		if len(p.WithKey(k).Speaks()) == 0 {
			continue // made for a protocol this provider has no endpoint for
		}
		i, ok := at[k.Protocol]
		if !ok {
			i = len(out)
			at[k.Protocol] = i
			out = append(out, poolJSON{Protocol: k.Protocol})
		}
		n := k.Name
		if n == "" {
			n = provider.Mask(k.Key)
		}
		out[i].Who = append(out[i].Who, n)
	}
	return out
}

func groupsState() groupsJSON {
	out := groupsJSON{Groups: []groupJSON{}, Models: []modelRef{}, Pools: []poolJSON{}, Deciders: []modelRef{}, Found: provider.AutoGroupsOn()}
	for _, e := range provider.Deciders() {
		out.Deciders = append(out.Deciders, modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon})
	}
	served := provider.Served()
	for _, e := range served {
		if e.Group == "" {
			out.Models = append(out.Models, modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon, Context: e.Context, Efforts: e.Efforts, CanFast: provider.CanFast(e.Provider, e.Model), Images: seesImages(e), ImagesUnknown: imagesUnknown(e)})
		}
	}
	for _, g := range provider.Groups() {
		gj := groupJSON{Group: g, Info: []memberJSON{}, Holds: []string{}, Offers: []string{}, Shared: []string{}, Patterns: []provider.PatternHit{}}
		gj.Patterns = append(gj.Patterns, provider.PatternHits(g)...)
		for _, e := range served {
			if e.ID == provider.GroupPrefix+g.ID {
				gj.Offers, gj.Shared = append(gj.Offers, e.Efforts...), append(gj.Shared, e.Shared...)
				break
			}
		}
		if _, ms, ok := provider.FindGroup(provider.GroupPrefix + g.ID); ok {
			for _, m := range ms {
				for _, v := range m.Groups() {
					if !slices.Contains(gj.Holds, v) {
						gj.Holds = append(gj.Holds, v)
					}
				}
			}
		}
		for _, id := range g.Members {
			m := memberJSON{ID: id}
			if gid, ok := strings.CutPrefix(id, provider.GroupPrefix); ok {
				// a group in the group: what agents see of it
				m.Group = true
				for _, e := range served {
					if e.ID == id {
						_, ms, _ := provider.FindGroup(id)
						m.Ready, m.Name, m.Icon, m.On = true, e.Name, e.Provider.Icon, len(ms)
						m.Context, m.Images, m.ImagesUnknown = e.Context, seesImages(e), imagesUnknown(e)
						// the levels it offers agents: those every model in
						// it has, not one of them its own
						m.Efforts = e.Efforts
						gj.Ready = true
						break
					}
				}
				if m.Name == "" {
					m.Name = gid
				}
				gj.Info = append(gj.Info, m)
				continue
			}
			of, effort := provider.MemberEffort(id)
			m.Of, m.Effort = of, effort
			if p, model, ok := provider.Resolve(of); ok {
				_, who := onOf(p)
				m.Ready, m.Provider, m.Name, m.Icon, m.Model, m.On = true, p.ID, p.Name, p.Icon, model, max(len(who), 1)
				m.CanFast = provider.CanFast(p, model)
				m.Fast = m.CanFast && g.IsFast(id)
				for _, e := range served {
					if e.Group == "" && e.Provider.ID == p.ID && e.Model == model {
						m.Context, m.Images, m.ImagesUnknown = e.Context, seesImages(e), imagesUnknown(e)
						m.Efforts = e.Efforts
						break
					}
				}
				gj.Ready = true
			} else if pid, model, ok := strings.Cut(of, "/"); ok {
				if p, err := provider.Find(pid); err == nil && !p.On() {
					m.ProviderOff, m.Provider, m.Name, m.Icon, m.Model = true, p.ID, p.Name, p.Icon, model
				}
			}
			gj.Info = append(gj.Info, m)
		}
		if g.Routing == provider.Manual {
			// agents can pick it while the member picked can answer
			gj.Picked = g.Picked()
			i := slices.IndexFunc(gj.Info, func(m memberJSON) bool { return m.ID == gj.Picked })
			gj.Ready = i >= 0 && gj.Info[i].Ready
		}
		out.Groups = append(out.Groups, gj)
	}
	for _, p := range provider.All() {
		if !p.On() {
			continue
		}
		if kind, who := onOf(p); kind == "account" && len(who) > 1 {
			out.Pools = append(out.Pools, poolJSON{Provider: p.ID, Name: p.Name, Icon: p.Icon, Kind: kind, Who: who, Routing: p.Routing, Affinity: p.Affinity, Sink: p.Sink})
			continue
		}
		pools := keyPools(p)
		for _, kp := range pools {
			if len(kp.Who) > 1 {
				kp.Provider, kp.Name, kp.Icon, kp.Kind, kp.Routing, kp.Affinity, kp.Sink = p.ID, p.Name, p.Icon, "key", p.Routing, p.Affinity, p.Sink
				if len(pools) == 1 {
					kp.Protocol = ""
				}
				out.Pools = append(out.Pools, kp)
			}
		}
	}
	return out
}

func groupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/groups", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, groupsState())
	})
	mux.HandleFunc("POST /api/groups/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			provider.Group
			From string `json:"from"` // the id the group had: another is a rename
			On   bool   `json:"on"`   // found: magpie finds groups on its own
			// arrange: the groups by id, in the order the Routing page
			// lists them (#779), which /v1/models follows too
			Order []string `json:"order"`
			// delete: several groups at once, all or none
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(rw, err)
			return
		}
		in := body.Group
		var err error
		var moved []agent.Move
		switch r.PathValue("action") {
		case "found":
			// off, an agent set to a found group is moved to its model
			// from one provider; a request still naming one goes there too
			// (provider.AutoStandIn)
			moved, err = agent.Reseat(func() error { return provider.SetAutoGroups(body.On) })
		case "save":
			to := strings.ToLower(strings.TrimSpace(in.ID))
			if body.From == "" || body.From == to {
				err = provider.SaveGroup(in)
				break
			}
			if to == "" || to != provider.GroupSlug(to) {
				err = fmt.Errorf("a group's id must be lowercase letters, digits, dots and dashes, not %q", in.ID)
				break
			}
			in.ID = body.From
			if err = provider.SaveGroup(in); err == nil {
				err = provider.RenameGroup(body.From, to)
			}
		case "delete":
			if len(body.IDs) > 0 {
				err = provider.DeleteGroups(body.IDs)
			} else {
				err = provider.DeleteGroup(in.ID)
			}
		case "show":
			err = provider.ShowGroup(in.ID)
		case "switch":
			// a group of the user's on or off, kept as it is (PAMI on Discord)
			err = provider.SwitchGroup(in.ID, body.On)
		case "arrange":
			err = provider.SetGroupOrder(body.Order)
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		st := groupsState()
		st.Moved = moved
		writeJSON(rw, st)
	})
}
