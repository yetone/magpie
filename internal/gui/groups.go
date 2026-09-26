package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

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
}

type groupJSON struct {
	provider.Group
	Ready bool         `json:"ready"` // a member is: agents can pick it
	Info  []memberJSON `json:"memberInfo"`
	// Holds: the groups in it, at any depth — none of which can have it in
	// turn
	Holds []string `json:"holds"`
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
	// what a rule may send it: the tokens it takes, when known, and images
	Context int  `json:"context,omitempty"`
	Images  bool `json:"images,omitempty"`
}

type modelRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	PName    string `json:"providerName"`
	Icon     string `json:"icon,omitempty"`
}

type poolJSON struct {
	Provider string   `json:"provider"`
	Name     string   `json:"name"`
	Icon     string   `json:"icon,omitempty"`
	Kind     string   `json:"kind"` // "account" or "key"
	Who      []string `json:"who"`
	Routing  string   `json:"routing"`
	Affinity string   `json:"affinity"`
	// Protocol: the one its keys are made for, when the provider's keys are
	// made for more than one — each protocol's keys are a pool of their own
	Protocol provider.Protocol `json:"protocol,omitempty"`
}

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
	out := groupsJSON{Groups: []groupJSON{}, Models: []modelRef{}, Pools: []poolJSON{}, Deciders: []modelRef{}}
	for _, e := range provider.Deciders() {
		out.Deciders = append(out.Deciders, modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon})
	}
	served := provider.Served()
	for _, e := range served {
		if e.Group == "" {
			out.Models = append(out.Models, modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon})
		}
	}
	for _, g := range provider.Groups() {
		gj := groupJSON{Group: g, Info: []memberJSON{}, Holds: []string{}}
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
						m.Context, m.Images = e.Context, e.Images && (e.ImageInput == nil || *e.ImageInput)
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
			if p, model, ok := provider.Resolve(id); ok {
				_, who := onOf(p)
				m.Ready, m.Provider, m.Name, m.Icon, m.Model, m.On = true, p.ID, p.Name, p.Icon, model, max(len(who), 1)
				for _, e := range served {
					if e.Group == "" && e.Provider.ID == p.ID && e.Model == model {
						m.Context, m.Images = e.Context, e.Images && (e.ImageInput == nil || *e.ImageInput)
						break
					}
				}
				gj.Ready = true
			}
			gj.Info = append(gj.Info, m)
		}
		out.Groups = append(out.Groups, gj)
	}
	for _, p := range provider.All() {
		if !p.Ready() {
			continue
		}
		if kind, who := onOf(p); kind == "account" && len(who) > 1 {
			out.Pools = append(out.Pools, poolJSON{Provider: p.ID, Name: p.Name, Icon: p.Icon, Kind: kind, Who: who, Routing: p.Routing, Affinity: p.Affinity})
			continue
		}
		pools := keyPools(p)
		for _, kp := range pools {
			if len(kp.Who) > 1 {
				kp.Provider, kp.Name, kp.Icon, kp.Kind, kp.Routing, kp.Affinity = p.ID, p.Name, p.Icon, "key", p.Routing, p.Affinity
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
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(rw, err)
			return
		}
		in := body.Group
		var err error
		switch r.PathValue("action") {
		case "save":
			to := strings.ToLower(strings.TrimSpace(in.ID))
			if body.From == "" || body.From == to {
				err = provider.SaveGroup(in)
				break
			}
			if to == "" || to != provider.Slug(to) {
				err = fmt.Errorf("a group's id must be lowercase letters, digits and dashes, not %q", in.ID)
				break
			}
			in.ID = body.From
			if err = provider.SaveGroup(in); err == nil {
				err = provider.RenameGroup(body.From, to)
			}
		case "delete":
			err = provider.DeleteGroup(in.ID)
		case "show":
			err = provider.ShowGroup(in.ID)
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, groupsState())
	})
}
