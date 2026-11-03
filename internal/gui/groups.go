package gui

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
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
	// Drawers: the providers' image models (gateway.OfferedDrawers) not
	// among Models, which a group may hold to draw with at the images API
	// (jzw690 on X); agents are never offered them for a conversation
	Drawers []modelRef `json:"drawers"`
	// Found: magpie finds groups on its own (provider.AutoGroupsOn)
	Found bool `json:"found"`
	// Skip: the providers kept out of found groups, by id
	// (provider.AutoGroupSkip); Skippable: those that can be, each a
	// provider ready to serve, switched off or not, and one in Skip that
	// is no more, so it can be let back in
	Skip      []string        `json:"skip"`
	Skippable []skippableJSON `json:"skippable"`
	// Moved: the agents turning found groups off moved off one of them,
	// to its model from one provider (agent.Reseat)
	Moved []agent.Move `json:"moved,omitempty"`
	// Templates: groups for common needs made of the user's own models,
	// which the page adds in a click (provider.Templates)
	Templates []provider.Template `json:"templates"`
	// Clock: this computer's time zone, which the gateway reads a rule's
	// hours on when they name no zone of their own; the page tells a rule's
	// zone beside its hours when the two differ (gaubeebangeel on X: magpie
	// on a server in UTC, its user in Beijing)
	Clock clockJSON `json:"clock"`
	// Shared: share's answer, the group as text to paste into another
	// magpie (provider.ShareGroup)
	Shared string `json:"shared,omitempty"`
	// Imported: import's answer, the group added and what of the text
	// this magpie has none of (provider.ImportGroup)
	Imported *provider.Imported `json:"imported,omitempty"`
	// Accounts: by provider id, the accounts and keys a group may be held
	// to (Group.Accounts), as a gateway key's list has them (#905): each
	// provider with more than one, or one a group names already
	Accounts map[string][]keyModelJSON `json:"accounts"`
}

type skippableJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
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
	// Decides: it is a group of decision models, asked at /v1/systemone
	// and by a group's classifier, never by an agent
	Decides bool `json:"decides,omitempty"`
	// Ref: the id agents pick it by (provider.Group.Ref): auto/<model> for
	// a group magpie found, group/<id> for any other
	Ref string `json:"ref"`
	// ClassifierFailed: its classifier couldn't say the last time this
	// group asked it, and why (gateway.GroupClassifierFailure), which its
	// card says (#1929): otherwise only each request's details did
	ClassifierFailed *classifierFailedJSON `json:"classifierFailed,omitempty"`
}

type classifierFailedJSON struct {
	Error string    `json:"error"`
	At    time.Time `json:"at"`
}

// classifierFailure is gateway.GroupClassifierFailure, which a test stands
// in for
var classifierFailure = gateway.GroupClassifierFailure

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
	// OwnFast: that fast mode is asked of a provider the user added by its
	// address, which may or may not serve it (provider.OwnFast, #1615)
	OwnFast bool `json:"ownFast,omitempty"`
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
	// Free: Free first and Cheapest first count it as costing nothing
	// (provider.FreeModel): its provider is the user's Free for me, or its
	// vendor serves it free. The Routing page tags it (jiabubu on Discord)
	Free bool `json:"free,omitempty"`
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
	// OwnFast: asked of a provider the user added by its address, which may
	// or may not serve it (provider.OwnFast, #1615)
	OwnFast bool `json:"ownFast,omitempty"`
	// Images: agents are told it takes images (provider.Entry.Images); a
	// group's editor says of each member what it says of it (#756).
	// ImagesUnknown: nothing was read of it either way (imagesUnknown),
	// sent only when true
	Images        bool `json:"images,omitempty"`
	ImagesUnknown bool `json:"imagesUnknown,omitempty"`
	// Sub: a subscription account serves it (Provider.Account), so a
	// group's classifier set to it asks that subscription at every turn,
	// which the Routing page says (#1839)
	Sub bool `json:"sub,omitempty"`
	// VendorPicks: an image model whose vendor picks the model itself,
	// whichever is named (a ChatGPT account's, #1865), which Settings ›
	// Images says
	VendorPicks bool `json:"vendorPicks,omitempty"`
}

// servedRef is a served model as the Routing page lists it
func servedRef(e provider.Entry) modelRef {
	return modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon, Context: e.Context, Efforts: e.Efforts, CanFast: provider.CanFast(e.Provider, e.Model), OwnFast: provider.OwnFast(e.Provider, e.Model), Images: seesImages(e), ImagesUnknown: imagesUnknown(e), Sub: e.Provider.Account != nil}
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

// onOf is who a provider's requests spread over: its accounts, by the
// name the user gave each (provider.AccountShown), or keys.
func onOf(p provider.Provider) (kind string, who []string) {
	if p.Account != nil {
		who = append(who, p.AccountShown(p.Account.User))
		for _, q := range p.AlsoOn() {
			who = append(who, p.AccountShown(q.Account.User))
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

// clockJSON is a computer's time zone: its IANA name when it has one,
// and how far ahead of UTC it is now, in seconds.
type clockJSON struct {
	Zone   string `json:"zone,omitempty"`
	Offset int    `json:"offset"`
}

// groupAccounts is groupsJSON's Accounts: the accounts and keys of each
// provider with more than one, which a group may be held to some of, and
// of each one a group in gs names already, so its choice can be undone.
func groupAccounts(gs []provider.Group) map[string][]keyModelJSON {
	named := map[string]bool{}
	for _, g := range gs {
		for _, a := range g.Accounts {
			if pid, _, ok := strings.Cut(a, "/"); ok {
				named[strings.ToLower(pid)] = true
			}
		}
	}
	out := map[string][]keyModelJSON{}
	for _, p := range provider.All() {
		refs := p.AccountIDs()
		if len(refs) < 2 && !named[strings.ToLower(p.ID)] {
			continue
		}
		name := cmp.Or(p.Name, p.ID)
		for _, a := range refs {
			out[p.ID] = append(out[p.ID], keyModelJSON{ID: p.ID + "/" + a.ID, Name: a.Shown(p), Provider: p.ID, ProviderName: name, Account: !a.Key, Key: a.Key, Plan: a.Plan})
		}
	}
	return out
}

func groupsState() groupsJSON {
	_, offset := time.Now().Zone()
	out := groupsJSON{Groups: []groupJSON{}, Models: []modelRef{}, Pools: []poolJSON{}, Deciders: []modelRef{}, Drawers: []modelRef{}, Found: provider.AutoGroupsOn(), Templates: provider.Templates(),
		Clock: clockJSON{Zone: provider.LocalZone(), Offset: offset}}
	for i := range out.Templates {
		if out.Templates[i].Group.Members == nil {
			out.Templates[i].Group.Members = []string{} // one that can't be made has none
		}
	}
	// the decision models a group's pickers offer: a provider's picks, and
	// any a group names already, whatever the picks, so its card still
	// reads it as a decision model
	named, all := map[string]bool{}, provider.Groups()
	for _, g := range all {
		for _, m := range g.Members {
			named[m] = true
		}
		named[g.Classifier] = true
	}
	for _, e := range provider.OfferedDeciders(func(id string) bool { return named[id] }) {
		out.Deciders = append(out.Deciders, modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon, Sub: e.Provider.Account != nil})
	}
	out.Accounts = groupAccounts(all)
	served := provider.Served()
	chat := map[string]bool{}
	for _, e := range served {
		if e.Group == "" {
			chat[e.ID] = true
			out.Models = append(out.Models, servedRef(e))
		}
	}
	// the image models, as Settings › Images lists them: each provider's
	// Drawers less those unticked in its editor, but one a group names
	// already, so its card still reads it. draws: every one, an unticked
	// one included, which draws all the same
	draws := map[string]provider.Provider{}
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		for _, m := range gateway.Drawers(p) {
			ref := p.ID + "/" + m.ID
			draws[ref] = p
			if chat[ref] || p.DrawerHidden(m.ID) && !named[ref] {
				continue
			}
			out.Drawers = append(out.Drawers, modelRef{ID: ref, Name: cmp.Or(m.Name, m.ID), Provider: p.ID, PName: p.Name, Icon: p.Icon, VendorPicks: gateway.VendorPicksDrawer(p)})
		}
	}
	// every provider's models read once for the groups, not once a group
	// (#1762)
	find := provider.GroupFinder()
	for _, g := range all {
		gj := groupJSON{Group: g, Info: []memberJSON{}, Holds: []string{}, Offers: []string{}, Shared: []string{}, Patterns: []provider.PatternHit{}}
		if gj.Members == nil {
			// a group of patterns that match nothing now — a plugin's
			// models not listed — has none, said as [] rather than null,
			// which the Routing page couldn't draw (#1585)
			gj.Members = []string{}
		}
		gj.Patterns = append(gj.Patterns, provider.PatternHits(g)...)
		gj.Decides = g.Decides()
		gj.Ref = g.Ref()
		if g.Classifier != "" {
			if why, at, ok := classifierFailure(g.ID, g.Classifier); ok {
				gj.ClassifierFailed = &classifierFailedJSON{Error: why, At: at}
			}
		}
		for _, e := range served {
			if e.Group == g.ID {
				gj.Offers, gj.Shared = append(gj.Offers, e.Efforts...), append(gj.Shared, e.Shared...)
				break
			}
		}
		if _, ms, ok := find(provider.GroupPrefix + g.ID); ok {
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
					if e.Group == gid {
						_, ms, _ := find(id)
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
			if p, model, ok := provider.ResolveRoute(of); ok {
				_, who := onOf(p)
				m.Ready, m.Provider, m.Name, m.Icon, m.Model, m.On = true, p.ID, p.Name, p.Icon, model, max(len(who), 1)
				m.CanFast = provider.CanFast(p, model)
				m.OwnFast = m.CanFast && provider.OwnFast(p, model)
				m.Fast = m.CanFast && g.IsFast(id)
				m.Free = p.FreeModel(model)
				for _, e := range served {
					if e.Group == "" && e.Provider.ID == p.ID && e.Model == model {
						m.Context, m.Images, m.ImagesUnknown = e.Context, seesImages(e), imagesUnknown(e)
						m.Efforts = e.Efforts
						break
					}
				}
				gj.Ready = true
			} else if p, ok := draws[of]; ok && effort == "" {
				// an image model its provider's picks leave out: the group
				// draws on it (gateway.drawingMembers)
				_, who := onOf(p)
				_, model, _ := strings.Cut(of, "/")
				m.Ready, m.Provider, m.Name, m.Icon, m.Model, m.On = true, p.ID, p.Name, p.Icon, model, max(len(who), 1)
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
	out.Skip = provider.AutoGroupSkip()
	if out.Skip == nil {
		out.Skip = []string{}
	}
	out.Skippable = []skippableJSON{}
	for _, p := range provider.All() {
		if p.Ready() || slices.Contains(out.Skip, p.ID) {
			out.Skippable = append(out.Skippable, skippableJSON{ID: p.ID, Name: p.Name})
		}
	}
	for _, id := range out.Skip {
		if !slices.ContainsFunc(out.Skippable, func(k skippableJSON) bool { return k.ID == id }) {
			out.Skippable = append(out.Skippable, skippableJSON{ID: id, Name: id})
		}
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
			// skip: the providers kept out of found groups
			// (provider.SetAutoGroupSkip)
			Skip []string `json:"skip"`
			// arrange: the groups by id, in the order the Routing page
			// lists them (#779), which /v1/models follows too
			Order []string `json:"order"`
			// delete: several groups at once, all or none
			IDs []string `json:"ids"`
			// template: which one, saved under the group's name
			// (provider.AddTemplate)
			Kind string `json:"kind"`
			// import: a group shared as text (provider.ShareGroup)
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(rw, err)
			return
		}
		in := body.Group
		var err error
		var moved []agent.Move
		var shared string
		var imported *provider.Imported
		switch r.PathValue("action") {
		case "found":
			// off, an agent set to a found group is moved to its model
			// from one provider; a request still naming one goes there too
			// (provider.AutoStandIn)
			moved, err = agent.Reseat(func() error { return provider.SetAutoGroups(body.On) })
		case "skip":
			// an agent set to a found group skipping took away is moved to
			// its model from one provider, as turning found groups off does
			moved, err = agent.Reseat(func() error { return provider.SetAutoGroupSkip(body.Skip) })
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
				// the agents on group/<from> move to group/<to>
				_, err = agent.RenameGroup(body.From, to)
			}
		case "delete":
			if len(body.IDs) > 0 {
				err = provider.DeleteGroups(body.IDs)
			} else {
				err = provider.DeleteGroup(in.ID)
			}
		case "show":
			err = provider.ShowGroup(in.ID)
		case "copy":
			// a group as it is, under name (provider.CopyGroup)
			_, err = provider.CopyGroup(in.ID, in.Name)
		case "share":
			// the group as text for another magpie: no key goes with it
			shared, err = provider.ShareGroup(in.ID)
		case "import":
			var res provider.Imported
			if res, err = provider.ImportGroup(body.Text); err == nil {
				imported = &res
			}
		case "template":
			_, err = provider.AddTemplate(body.Kind, in.Name)
		case "switch":
			// a group of the user's on or off, kept as it is (PAMI on Discord)
			err = provider.SwitchGroup(in.ID, body.On)
		case "arrange":
			err = provider.SetGroupOrder(body.Order)
		case "pick":
			// a Manual group's member every request goes to, and nothing
			// else of it (#2489: the tray panel switches it)
			err = provider.PickGroup(in.ID, in.Pick)
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		st := groupsState()
		st.Moved, st.Shared, st.Imported = moved, shared, imported
		writeJSON(rw, st)
	})
}
