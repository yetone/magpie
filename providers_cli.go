package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/stats"
)

var amber = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#F2B544"})

const providerUsage = `usage:
  magpie providers                        list providers, keys and who uses them
  magpie presets                          list the vendors magpie knows out of the box
  magpie provider <id>                    show one provider and its models
  magpie provider add <preset> <key>      add a preset vendor   e.g. magpie provider add deepseek sk-…
                                          again, it adds another (deepseek-2); k=v pairs too: id, name, header.X-Foo
  magpie provider add <name> k=v…         add a custom vendor   k: url, anthropic, responses, gemini, decide, key, models, catalog, icon, header.X-Foo, balance, balance.path, balance.token, models.url, search,
                                          access.key, access.secret (a Volcengine account's access key, for its plan's windows)
  magpie provider set <id> k=v…           change a provider's settings, with the same k=v pairs as add
  magpie provider key <id> <key>          change the API key
  magpie provider icon <id> <file|name>   give a custom provider a picture (PNG, JPEG, SVG…) or a built-in icon
  magpie provider fallback <id> <provider/model>…   where requests go when it's out of quota or down (none clears)
  magpie provider models <id> [ids…]      fetch the vendor's model list, or choose which models to expose:
                                          ids… replace the list, +id adds one, -id takes one out, all: the default
  magpie provider refresh <id>            fetch the vendor's model list again (as the app's Refresh)
  magpie provider account-models <id> [account|key [ids…|all]]
                                          the models one account or key alone serves; all: every model the provider has
  magpie provider account-cap <id> [account [percent|off]]
                                          use a subscription account up to a share of each usage window (e.g. 70):
                                          at it, routing takes the account for used up until the window renews
  magpie provider account-cap <id> <account> --window <name> [percent|none|default]
                                          a share for one window alone (e.g. "5 hours" 50): none is no cap on it,
                                          default has it follow the account's cap
  magpie provider account-concurrency <id> [account|key [n|off|default]]
                                          how many requests one account or key has out at once, over every model,
                                          routing group and agent: its own, off for none, default for the provider's
  magpie provider queue <id> [length [seconds]]
                                          how many may wait for each account or key past its limit, and how long;
                                          past either a request is turned away with a 429 (0: no bound)
  magpie provider rpm <id> [n|off]        how many requests each account or key sends the vendor in any minute;
                                          one more waits for room, up to 2 minutes, then is turned away with a 429
  magpie provider listed <id> yes|no      no: its models serve only through routing groups, not in the list
  magpie provider off|on <id>             switch it off (kept, but no agent or request uses it), or on again
  magpie provider test <id> [model…]      send a tiny request through each endpoint, or to each model
  magpie provider rm <id>                 remove a provider

  e.g. magpie provider add "My Relay" url=https://relay.example.com/v1 key=sk-…
       magpie provider add "Own Claude" anthropic=https://gw.example.com key=sk-… catalog=anthropic
       magpie provider add "My Relay" url=https://relay.example.com/v1 key=sk-… header.X-Org-Id=acme
       magpie provider add remote-magpie sk-magpie-… url=http://192.168.1.20:3425 id=office
                                   (another computer's magpie, shared on its network: its models and routing
                                    groups as office/…, each request sent on in the API the agent spoke)
       magpie provider add bailian-decision sk-… workspace=<workspace id>   (or region=ap-southeast-1, or region=token-plan with an sk-sp- key)
       magpie provider add "My Decider" decide=https://decide.example.com/v1 key=sk-… models=my-decision-model
                                   (a System One API, POST …/systemone: it routes groups, its models are never an agent's)
       magpie provider add anthropic sk-… id=anthropic-ws2 name="Anthropic WS2" header.anthropic-workspace-id=wrkspc_…
       magpie provider set my-relay models.url=https://relay.example.com/api/models catalog=
       magpie provider set my-relay search=yes
                                   (the relay answers Claude Code's WebSearch and Codex's web_search itself:
                                    those go to it as sent, not through magpie's own search)
       magpie provider set ollama unmasked=yes
                                   (a model on this computer or the local network: Settings' redaction leaves its
                                    requests as written; not for a local relay that passes them on to a vendor)
       magpie provider add "My Relay" url=https://relay.example.com/v1 key=sk-… balance=https://relay.example.com/api/usage/token balance.path='$data.total_available / 500000'
       magpie provider set my-relay balance.path='(1 - credits.monthlyCredits / 70) %'
       magpie provider set my-relay balance=https://relay.example.com/api/user/self balance.path='$data.quota / 500000' balance.token=<access token> header.New-Api-User=<user id>
                                   (a new-api relay's whole account: its access token and user id, from its personal settings)
                                   (balance.path: where the amount is in the reply, or a sum of those with + - * / and
                                    brackets; $ or ¥ in front adds the sign, % after it shows a percent of 1;
                                    several go apart by ; each with a label: '5h: a.used / a.cap %; $credits.left')
       magpie provider set my-relay context=272k context.gpt-6=1m
                                   (context: how long a request agents are told the models take, over what the
                                    vendor or models.dev says; context.<model> for one of them; empty clears)
       magpie provider set opencode-go family=ocgo
       magpie provider set my-relay id=relay   (renames it: groups and agents on my-relay/… move to relay/…)
                                   (family: a tag for which agents are shown its models, see magpie visible)`

// providers: `magpie providers`
func providers() error {
	all := provider.All()
	// a providers.json that can't be read is not "no providers yet": what
	// is listed is then the signed-in accounts alone, and it ends in why
	bad := provider.FileError()
	if len(all) == 0 && len(provider.Excluded()) == 0 {
		if bad != nil {
			return bad
		}
		fmt.Println(muted.Render("no providers yet ·"), "magpie provider add deepseek sk-…", muted.Render("· magpie presets lists the vendors"))
		return nil
	}
	uses := usesByProvider()
	type row struct{ name, id, host, key, models, uses string }
	var rows []row
	w := [5]int{}
	for _, p := range all {
		r := row{name: bold.Render(p.Name), id: muted.Render(p.ID), host: p.Host()}
		if p.Preset == "" && p.Account == nil {
			r.name += " " + faint.Render("custom")
		}
		switch {
		case p.Off:
			r.key = faint.Render("○ switched off")
		case p.Account != nil:
			r.key = green.Render("●") + " " + muted.Render("signed in as "+p.Account.User)
		case p.Key != "":
			r.key = green.Render("●") + " " + muted.Render(provider.Mask(p.Key))
		case p.Ready():
			r.key = green.Render("●") + " " + muted.Render("no key needed")
		default:
			r.key = amber.Render("○ no key")
		}
		n := len(p.Exposed())
		if t, ok := p.Listed(); ok {
			r.models = fmt.Sprintf("%d of %d models", n, len(p.Available())) + muted.Render(" · fetched "+ago(t))
		} else {
			r.models = fmt.Sprintf("%d models", n)
		}
		if u := uses[p.ID]; len(u) > 0 {
			r.uses = green.Render("← " + strings.Join(u, ", "))
		}
		if e := p.ListError(); e != "" {
			r.uses += amber.Render("  ! couldn't list its models: " + e)
		}
		if len(p.Fallback) > 0 {
			r.uses += muted.Render("  ⤷ " + strings.Join(p.Fallback, " → "))
		}
		for i, s := range []string{r.name, r.id, r.host, r.key, r.models} {
			w[i] = max(w[i], lipgloss.Width(s))
		}
		rows = append(rows, r)
	}
	for _, r := range rows {
		fmt.Printf("  %s  %s  %s  %s  %s  %s\n", pad(r.name, w[0]), pad(r.id, w[1]), pad(r.host, w[2]), pad(r.key, w[3]), pad(r.models, w[4]), r.uses)
	}
	for _, x := range provider.Excluded() {
		name := x.Agent
		if x.Name != "" {
			name = x.Name
		} else if a, err := agent.Find(x.Agent); err == nil {
			name = a.Name
		}
		fmt.Println()
		if x.SignedOut {
			fmt.Println(" ", amber.Render("!"), name+"'s saved accounts are not offered: "+x.Why)
			continue
		}
		back := ""
		if x.Provider != "" {
			back = " · magpie provider add " + x.Provider + " brings it back"
		}
		fmt.Println(" ", muted.Render(name+" is signed in but not offered: "+x.Why+back))
	}
	return bad
}

// usesByProvider maps provider ids to the agents currently routed to them.
func usesByProvider() map[string][]string {
	out := map[string][]string{}
	for _, a := range agent.Detected() {
		if len(a.Fields) == 0 {
			continue
		}
		v := a.Fields[0].Get()
		v = strings.TrimPrefix(v, "magpie/")
		if strings.HasPrefix(v, provider.GroupPrefix) {
			continue // a routing group, of no one provider
		}
		if pid, _, ok := strings.Cut(v, "/"); ok {
			out[pid] = append(out[pid], a.Name)
		}
	}
	return out
}

// presets: `magpie presets`
func presets() error {
	have := map[string]bool{}
	for _, p := range provider.All() {
		have[p.ID], have[p.Preset] = true, true
	}
	kind := provider.Kind("")
	// partners first, as the app lists them: their heading says they pay
	all := []provider.PresetDef{}
	var shown []string
	for _, pa := range provider.PartnersNow(3 * time.Second) {
		all = append(all, pa.PresetDef)
		shown = append(shown, pa.ID)
	}
	provider.CountPartner(provider.PartnerShown, shown...)
	provider.NoticePartners(shown...)
	for _, pr := range append(all, provider.Presets()...) {
		if pr.Kind != kind {
			kind = pr.Kind
			fmt.Println(faint.Render("  " + map[provider.Kind]string{provider.KindPartner: "partners (sponsors)", provider.KindVendor: "vendors", provider.KindRelay: "relays", provider.KindLocal: "local"}[kind]))
		}
		name := bold.Render(pr.Name)
		if pr.Sponsored && pr.Kind != provider.KindPartner {
			name += " " + faint.Render("sponsored")
		}
		state := muted.Render("magpie provider add " + pr.ID + " <key>")
		if pr.NoKey {
			state = muted.Render("magpie provider add " + pr.ID)
		}
		if have[pr.ID] {
			state = green.Render("✓ added")
		}
		fmt.Printf("  %s  %s  %s\n", pad(name, 28), pad(muted.Render(pr.ID), 14), state)
	}
	return nil
}

// models: `magpie models [<agent>]` — the catalog every agent sees, or the
// one agent is shown and what is kept from it
func models(args []string) error {
	entries := provider.Catalog()
	bad := provider.FileError() // as in providers: said, not "no models yet"
	var hidden []provider.Entry
	agentID := ""
	if len(args) > 0 {
		agentID = strings.ToLower(strings.TrimPrefix(args[0], "--agent="))
		if agentID == "--agent" && len(args) > 1 {
			agentID = strings.ToLower(args[1])
		}
		if agentID = agentOf(agentID); !knownAgent(agentID) {
			return fmt.Errorf("no agent %q (%s)", agentID, strings.Join(agentIDs(), ", "))
		}
		entries, hidden = provider.CatalogFor(agentID)
	}
	if _, only := provider.PickedModels(agentID); len(entries) == 0 && agentID != "" && only {
		fmt.Println(amber.Render("!"), agentID, "is shown none of them: it is shown only the models picked for it, and none is", muted.Render("· tick some in its list on the Agents page, or magpie visible "+agentID+" --show-new"))
	} else if len(entries) == 0 && agentID != "" {
		names, _ := provider.VisibleTo(agentID)
		fmt.Println(amber.Render("!"), agentID, "is shown none of them: nothing is in", strings.Join(names, ", "), muted.Render("· magpie visible "+agentID+" all shows it every model"))
	} else if len(entries) == 0 && bad != nil {
		return bad
	} else if len(entries) == 0 {
		fmt.Println(muted.Render("no models yet · add a provider first:"), "magpie provider add deepseek sk-…")
		return nil
	}
	w := 0
	for _, e := range entries {
		w = max(w, len(e.ID))
	}
	last := ""
	for _, e := range entries {
		head, name := e.Provider.ID, e.Provider.Name
		if e.Group != "" {
			head, name = provider.GroupPrefix, "Routing groups"
		}
		if head != last {
			last = head
			fmt.Println(faint.Render("  " + name))
		}
		line := "  " + pad(e.ID, w)
		if e.Name != "" && e.Name != e.Model {
			line += "  " + muted.Render(e.Name)
		}
		if len(e.Efforts) > 0 {
			line += faint.Render("  " + strings.Join(e.Efforts, "/"))
		}
		fmt.Println(line)
	}
	if agentID != "" {
		explainHidden(agentID, hidden)
	}
	// the models a provider's list has that it doesn't expose, which a
	// group naming one finds "not served" (MOMO on Discord: 35 of 37)
	for _, p := range provider.All() {
		if !p.On() || p.Unlisted {
			continue
		}
		if ids, why := notExposed(p); len(ids) > 0 {
			fmt.Println(faint.Render("  "+p.Name+": "+plural(len(ids), "more model")+" in its list, not exposed ("+why+"): ") + muted.Render(listSome(ids, 6)))
			fmt.Println(faint.Render("    magpie provider models " + p.ID + " +<model> exposes one"))
		}
	}
	fmt.Println(faint.Render("  " + advertisedURL() + "/v1"))
	return bad
}

// notExposed are the models p's list has that it doesn't expose, and why:
// the user picked others, or no picks and the list is longer than magpie
// exposes by default.
func notExposed(p provider.Provider) ([]string, string) {
	shown := map[string]bool{}
	for _, m := range p.Exposed() {
		shown[m.ID] = true
	}
	var out []string
	for _, m := range p.Available() {
		if !shown[m.ID] {
			out = append(out, m.ID)
		}
	}
	why := "not picked"
	if len(p.Models) == 0 {
		why = fmt.Sprintf("none picked, so only the first %d", len(shown))
	}
	return out, why
}

// listSome is ids joined, the first n of them and how many more.
func listSome(ids []string, n int) string {
	if len(ids) > n {
		return strings.Join(ids[:n], ", ") + fmt.Sprintf(" … %d more", len(ids)-n)
	}
	return strings.Join(ids, ", ")
}

// providerCmd: `magpie provider <verb> …`
func providerCmd(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%s", providerUsage)
	}
	verb, rest := args[1], args[2:]
	switch verb {
	case "add":
		return addProvider(rest)
	case "set":
		// the same k=v pairs as add, on a provider already here
		if len(rest) < 2 {
			return fmt.Errorf("magpie provider set <id> k=v…\n\n%s", providerUsage)
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		// id= renames it: the rest is saved under the id it has, then the
		// groups and agents on its models move to the new one
		from := p.ID
		before := *p
		if err := applyPairs(p, rest[1:]); err != nil {
			return err
		}
		to := strings.ToLower(strings.TrimSpace(p.ID))
		p.ID = from
		if err := provider.Save(*p); err != nil {
			return err
		}
		if to != from {
			moved, err := agent.RenameProvider(from, to)
			if err != nil {
				return err
			}
			p.ID = to
			if len(moved) > 0 {
				fmt.Println(green.Render("✓"), strings.Join(moved, ", "), "moved to", to+"/…")
			}
		}
		fmt.Println(green.Render("✓"), "saved", p.Name, muted.Render("("+p.ID+")"))
		if !slices.Equal(before.Models, p.Models) {
			// models= replaces the picks, as provider models does
			printPicksChange(before, *p, nil)
		}
		return nil
	case "key":
		if len(rest) != 2 {
			return fmt.Errorf("magpie provider key <id> <key>")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		p.Key = rest[1]
		if err := provider.Save(*p); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), p.Name, "key", muted.Render(provider.Mask(p.Key)))
		return nil
	case "icon":
		if len(rest) != 2 {
			return fmt.Errorf("magpie provider icon <id> <picture file | built-in name | \"\">")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		if p.Account != nil || p.Preset != "" {
			return fmt.Errorf("%s has its own icon; only a custom provider takes one", p.Name)
		}
		p.Icon = rest[1]
		if err := applyPairs(p, []string{"icon=" + rest[1]}); err != nil {
			return err
		}
		if p.Icon == "" {
			p.Icon = "generic"
		}
		if err := provider.Save(*p); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), p.Name, "icon", muted.Render(p.Icon))
		return nil
	case "fallback":
		// where requests go when this provider is out of quota, rate
		// limited or down; "none" clears the list
		if len(rest) < 1 {
			return fmt.Errorf("magpie provider fallback <id> [provider/model… | none]")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		if len(rest) > 1 {
			p.Fallback = nil
			if !(len(rest) == 2 && rest[1] == "none") {
				for _, id := range rest[1:] {
					if _, _, ok := provider.Resolve(id); !ok {
						return fmt.Errorf("magpie knows no model %q (magpie models lists them)", id)
					}
					p.Fallback = append(p.Fallback, id)
				}
			}
			if err := provider.Save(*p); err != nil {
				return err
			}
			if p, err = provider.Find(p.ID); err != nil {
				return err
			}
		}
		if len(p.Fallback) == 0 {
			fmt.Println(p.Name, muted.Render("has no fallback · magpie provider fallback "+p.ID+" <provider/model>…"))
			return nil
		}
		fmt.Println(green.Render("✓"), p.Name, "falls back to", strings.Join(p.Fallback, muted.Render(" → ")),
			muted.Render("· when out of quota, rate limited or down"))
		return nil
	case "rm", "remove", "delete":
		if len(rest) != 1 {
			return fmt.Errorf("magpie provider rm <id>")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		moved, err := agent.Reseat(func() error { return provider.Delete(p.ID) })
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "removed", p.Name)
		printMoved(moved)
		return nil
	case "test":
		// with models named, a request to each of them; else one per endpoint
		if len(rest) < 1 {
			return fmt.Errorf("magpie provider test <id> [model…]")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		res, wait := p.Test, 30*time.Second
		if models := rest[1:]; len(models) > 0 {
			res, wait = func(ctx context.Context) []provider.Result { return p.TestModels(ctx, models) }, 90*time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		ok := true
		for _, r := range res(ctx) {
			if r.OK {
				fmt.Printf("  %s %-10s %s\n", green.Render("✓"), r.Protocol, muted.Render(fmt.Sprintf("%d ms · %s", r.Millis, r.Model)))
			} else {
				ok = false
				msg := r.Error
				if r.Status != 0 {
					msg = fmt.Sprintf("%d · %s", r.Status, r.Error)
				}
				if len(rest) > 1 {
					msg = r.Model + " · " + msg
				}
				fmt.Printf("  %s %-10s %s\n", amber.Render("✗"), r.Protocol, msg)
			}
		}
		if !ok {
			os.Exit(1)
		}
		return nil
	case "account-models", "account-model":
		return accountModelsCmd(rest)
	case "account-cap", "account-caps":
		return accountCapCmd(rest)
	case "account-concurrency", "account-limit":
		return accountConcurrencyCmd(rest)
	case "queue":
		return queueCmd(rest)
	case "rpm":
		return rpmCmd(rest)
	case "listed":
		// no: the provider's models leave the list agents see and serve
		// only through the routing groups they are in
		if len(rest) != 2 || (rest[1] != "yes" && rest[1] != "no") {
			return fmt.Errorf("magpie provider listed <id> yes|no")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		p.Unlisted = rest[1] == "no"
		if err := provider.Save(*p); err != nil {
			return err
		}
		if p.Unlisted {
			fmt.Println(green.Render("✓"), p.Name, muted.Render("serves only through routing groups"))
		} else {
			fmt.Println(green.Render("✓"), p.Name, muted.Render("its models are listed"))
		}
		return nil
	case "off", "on":
		// off: kept with its keys, but agents are given none of its
		// models and no request goes to it
		if len(rest) != 1 {
			return fmt.Errorf("magpie provider %s <id>", verb)
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		moved, err := agent.Reseat(func() error { return provider.SetOff(p.ID, verb == "off") })
		if err != nil {
			return err
		}
		defer printMoved(moved)
		if verb == "off" {
			fmt.Println(green.Render("✓"), p.Name, muted.Render("is switched off: agents are given none of its models"))
		} else {
			fmt.Println(green.Render("✓"), p.Name, muted.Render("is switched on"))
		}
		return nil
	case "models", "refresh", "fetch":
		if len(rest) < 1 {
			return fmt.Errorf("magpie provider %s <id> [model ids to expose…]", verb)
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		if len(rest) > 1 && verb != "models" {
			return fmt.Errorf("magpie provider %s <id> fetches its list · magpie provider models <id> <ids…> picks from it", verb)
		}
		if len(rest) > 1 {
			before := *p
			picks, err := editPicks(*p, rest[1:])
			if err != nil {
				return err
			}
			p.Models = picks
			if err := provider.Save(*p); err != nil {
				return err
			}
			printPicksChange(before, *p, rest[1:])
			return showProvider(*p)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ms, dropped, err := p.Refetch(ctx)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), len(ms), "models from", fetchedFrom(*p))
		if len(dropped) > 0 {
			fmt.Println(amber.Render("!"), "gone from its list, so no longer picked:", strings.Join(dropped, ", "))
		}
		if q, err := provider.Find(p.ID); err == nil {
			p = q // without the picks just dropped
		}
		return showProvider(*p)
	}
	// `magpie provider <id>`
	p, err := provider.Find(verb)
	if err != nil {
		return err
	}
	return showProvider(*p)
}

// addProvider: `magpie provider add <preset> [key]` or `magpie provider add <name> k=v…`
func addProvider(rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("magpie provider add <preset> <key>   or   magpie provider add <name> k=v…\n\n%s", providerUsage)
	}
	if len(rest) == 1 && slices.ContainsFunc(provider.Excluded(), func(x provider.Exclusion) bool { return x.Provider == strings.ToLower(rest[0]) }) {
		// a signed-in account the user removed comes back with its picks
		if err := provider.ShowAccount(strings.ToLower(rest[0])); err != nil {
			return err
		}
		return announce(strings.ToLower(rest[0]))
	}
	var p provider.Provider
	if pr, err := provider.FromPreset(strings.ToLower(rest[0])); err == nil {
		p = pr
		if len(rest) > 1 && !strings.Contains(rest[1], "=") {
			p.Key = rest[1]
			rest = rest[2:]
		} else {
			rest = rest[1:]
		}
	} else {
		p = provider.Provider{Name: rest[0]}
		rest = rest[1:]
	}
	if err := applyPairs(&p, rest); err != nil {
		return err
	}
	// adding a preset that is already here adds another of it (deepseek-2),
	// for another key or another header, rather than replacing the first
	id, err := provider.Add(p)
	if err != nil {
		return err
	}
	return announce(id)
}

// saveNew saves a provider the user just added, then asks the vendor for
// its models.
func saveNew(p provider.Provider) error {
	if err := provider.Save(p); err != nil {
		return err
	}
	id := p.ID
	if _, err := provider.Find(id); err != nil {
		id = p.Name
	}
	return announce(id)
}

// announce says a provider was added and asks the vendor for its models.
func announce(id string) error {
	saved, err := provider.Find(id)
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "added", saved.Name, muted.Render("("+saved.ID+")"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if ms, err := saved.Fetch(ctx); err == nil {
		fmt.Println(green.Render("✓"), len(ms), "models from", fetchedFrom(*saved))
	} else if !saved.DecideOnly() {
		// the URLs asked and what they said; the base stays as given
		fmt.Println(amber.Render("!"), muted.Render(err.Error()))
	}
	n := len(saved.Exposed())
	if saved.DecideOnly() {
		fmt.Println("  it routes groups: magpie group set <id> effort=auto classifier="+saved.ID+"/"+saved.Jev(),
			muted.Render("· or a rule's intent=…"))
		return nil
	}
	if n == 0 {
		fmt.Println(amber.Render("!"), "no models exposed yet ·", "magpie provider models", saved.ID, "<ids…>")
	} else {
		fmt.Printf("  %d models in the catalog · %s\n", n, muted.Render("magpie models"))
	}
	return nil
}

func showProvider(p provider.Provider) error {
	kv := func(k, v string) {
		// a plugin's provider is reached through the plugin, not a URL
		if v != "" && !strings.HasPrefix(v, "plugin://") {
			fmt.Printf("  %s %s\n", muted.Render(pad(k, 10)), v)
		}
	}
	name := bold.Render(p.Name) + muted.Render("  "+p.ID)
	if p.Preset == "" && p.Account == nil {
		name += faint.Render("  custom")
	}
	fmt.Println(" ", name)
	kv("chat", p.Chat)
	kv("responses", p.Responses)
	kv("anthropic", p.Anthropic)
	kv("gemini", p.Gemini)
	if p.Searches {
		kv("search", "by itself"+muted.Render("  a client's web search goes to it as sent"))
	}
	if p.Unredacted {
		why := "  local: Settings' redaction leaves its requests as written"
		if !p.SkipsRedaction() {
			why = "  but masked: not every address of it is on this computer or the local network"
		}
		kv("unmasked", "yes"+muted.Render(why))
	}
	switch {
	case p.Account != nil:
		who := p.Account.User
		if p.Account.Plan != "" {
			who += muted.Render("  " + p.Account.Plan)
		}
		from := p.Account.Agent + "'s own sign-in"
		if p.IsPlugin() {
			from = p.Name + "'s sign-in"
			if provider.Moved(p.ID) {
				from = p.ID + "'s own sign-in" // as the built-in said
			}
		}
		kv("account", who+muted.Render("  from "+from))
	case p.Key != "":
		kv("key", muted.Render(provider.Mask(p.Key)))
	case p.Ready():
		kv("key", muted.Render("none needed"))
	default:
		kv("key", amber.Render("not set")+muted.Render("  magpie provider key "+p.ID+" …"))
	}
	kv("catalog", p.Catalog)
	kv("website", p.Website)
	kv("keys", p.KeysURL)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	if amount, ok, err := provider.Balance(ctx, p); ok {
		from := ""
		if p.BalanceURL != "" {
			from = muted.Render("  from " + p.BalanceURL + " " + p.BalancePath)
		}
		if err != nil {
			kv("balance", amber.Render("unavailable")+muted.Render("  "+err.Error())+from)
		} else {
			kv("balance", bold.Render(amount)+from)
		}
	}
	cancel()
	for _, k := range slices.Sorted(maps.Keys(p.Headers)) {
		kv("header", k+": "+muted.Render(p.Headers[k]))
	}
	ms := p.Exposed()
	src := "models.dev"
	if t, ok := p.Listed(); ok {
		src = fetchedFrom(p) + " · fetched " + ago(t)
	}
	kv("models", fmt.Sprintf("%d exposed of %d %s", len(ms), len(p.Available()), muted.Render("from "+src)))
	if ids, why := notExposed(p); len(ids) > 0 {
		kv("not shown", fmt.Sprintf("%s %s", listSome(ids, 8), muted.Render("· "+why+" · magpie provider models "+p.ID+" +<model> exposes one")))
	}
	names := p.ModelNames()
	for i, m := range ms {
		if i == 12 {
			fmt.Println(faint.Render(fmt.Sprintf("             … %d more", len(ms)-12)))
			break
		}
		line := "             " + p.ID + "/" + m.ID
		if n, ok := names[m.ID]; ok {
			m.Name = n
		}
		if m.Name != "" && m.Name != m.ID {
			line += muted.Render("  " + m.Name)
		}
		fmt.Println(line)
	}
	if u := usesByProvider()[p.ID]; len(u) > 0 {
		kv("used by", green.Render(strings.Join(u, ", ")))
	}
	return nil
}

func applyPairs(p *provider.Provider, pairs []string) error {
	workspace := ""
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("expected key=value, got %q\n\n%s", kv, providerUsage)
		}
		switch strings.ToLower(k) {
		case "id":
			p.ID = v
		case "name":
			p.Name = v
		case "url", "chat", "openai":
			p.Chat = v
		case "responses":
			p.Responses = v
		case "anthropic":
			p.Anthropic = v
		case "gemini":
			// a Gemini API's base (…/v1beta), Google's or one that
			// answers as it does (#1346)
			p.Gemini = v
		case "decide":
			// a System One root (…/systemone is asked under it): the
			// provider routes groups, its models any name (#647)
			p.Decide = v
		case "workspace":
			workspace = strings.TrimSpace(v)
		case "region", "plan":
			pr := provider.Preset(p.Preset)
			if pr == nil || len(pr.Regions) == 0 {
				return fmt.Errorf("%s has no regions or plans to pick", p.Name)
			}
			var ids []string
			for _, r := range pr.Regions {
				ids = append(ids, r.ID)
			}
			i := slices.IndexFunc(pr.Regions, func(r provider.Region) bool { return strings.EqualFold(r.ID, v) })
			if i < 0 {
				return fmt.Errorf("%s=%s: %s's are %s", k, v, pr.Name, strings.Join(ids, ", "))
			}
			r := pr.Regions[i]
			p.Chat, p.Responses, p.Anthropic, p.Decide = r.Chat, r.Responses, r.Anthropic, r.Decide
			if r.KeysURL != "" {
				p.KeysURL = r.KeysURL
			}
		case "key":
			p.Key = v
		case "catalog":
			p.Catalog = v
		case "models":
			p.Models = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
		case "website":
			p.Website = v
		case "keys":
			p.KeysURL = v
		case "balance":
			p.BalanceURL = v
		case "balance.path":
			p.BalancePath = v
		case "balance.token":
			p.BalanceToken = v
		case "access.key":
			// a Volcengine account's access key, which Ark tells its
			// Coding or Agent Plan's windows to (#1427)
			p.AccessKeyID = v
		case "access.secret":
			p.SecretAccessKey = v
		case "models.url":
			p.ModelsURL = v
		case "search":
			// yes: the vendor searches the web by itself, a web search a
			// client offers going to it as it was sent (a relay in front of
			// Anthropic's or OpenAI's API)
			if v != "yes" && v != "no" {
				return fmt.Errorf("search=yes|no, not %q", v)
			}
			p.Searches = v == "yes"
		case "unmasked":
			// yes: a model on this computer or the local network gets the
			// requests as written, unmasked by Settings' redaction (lc on
			// Discord); only while every address of it is local
			// (provider.SkipsRedaction)
			if v != "yes" && v != "no" {
				return fmt.Errorf("unmasked=yes|no, not %q", v)
			}
			p.Unredacted = v == "yes"
		case "context":
			if err := setContext(p, "*", v); err != nil {
				return err
			}
		case "family", "tag":
			p.Family = strings.TrimSpace(v)
		case "icon":
			// a picture on disk is kept by magpie; anything else is one of
			// the built-in icons' names
			if st, err := os.Stat(v); err == nil && !st.IsDir() {
				icon, err := provider.StoreIconFile(v)
				if err != nil {
					return err
				}
				v = icon
			}
			p.Icon = v
		default:
			// header.X-Foo=bar sets a custom request header (name kept as
			// typed, replacing one of the same name in any case); an empty
			// value leaves it out
			if len(k) > len("header.") && strings.EqualFold(k[:len("header.")], "header.") {
				name := k[len("header."):]
				for h := range p.Headers {
					if strings.EqualFold(h, name) {
						delete(p.Headers, h)
					}
				}
				if strings.TrimSpace(v) == "" {
					continue
				}
				if p.Headers == nil {
					p.Headers = map[string]string{}
				}
				p.Headers[name] = v
				continue
			}
			if len(k) > len("context.") && strings.EqualFold(k[:len("context.")], "context.") {
				if err := setContext(p, k[len("context."):], v); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("unknown field %q\n\n%s", k, providerUsage)
		}
	}
	if workspace != "" {
		// Bailian's decision model is asked at the workspace's own host
		if !strings.Contains(p.Decide, provider.WorkspaceID) {
			return fmt.Errorf("workspace= fills in a Bailian workspace's host, and %s's decision API names none", p.Name)
		}
		p.Decide = strings.ReplaceAll(p.Decide, provider.WorkspaceID, workspace)
	}
	return nil
}

// setContext sets the context agents are told model takes ("*" for all
// the provider's); empty or 0 leaves it to the vendor and models.dev again.
func setContext(p *provider.Provider, model, v string) error {
	n := 0
	if strings.TrimSpace(v) != "" {
		var err error
		if n, err = parseTokens(v); err != nil {
			return fmt.Errorf("context: %w", err)
		}
	}
	if n == 0 {
		delete(p.Contexts, model)
		return nil
	}
	if p.Contexts == nil {
		p.Contexts = map[string]int{}
	}
	p.Contexts[model] = n
	return nil
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("Jan 2")
	}
}

// refreshLive re-fetches the model list of every ready provider; `magpie sync`
// calls it after the catalog download.
func refreshLive(ctx context.Context) {
	var names []string
	for _, p := range provider.All() {
		if !p.Ready() {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		ms, err := p.Fetch(c)
		cancel()
		if err == nil {
			names = append(names, fmt.Sprintf("%s (%d)", p.ID, len(ms)))
		}
	}
	if len(names) > 0 {
		fmt.Println(green.Render("✓"), "model lists:", strings.Join(names, ", "))
	}
}

// keyNote says who the gateway takes any key from: this machine alone,
// unless MAGPIE_ADDR puts it on the network (a server, a Docker image)
// without sharing it from Settings, when it is anyone who reaches it.
func keyNote() string {
	if s := settings.Load(); s.LAN {
		return "(anything works from this machine; from others, an enabled gateway key — magpie gateway-key add <name>)"
	}
	if gateway.OpenToAnyone() {
		return "(anything works, from anyone who reaches it — share it from Settings to require a key)"
	}
	return "(anything works; the gateway only listens on localhost)"
}

// containerNote follows the addresses magpie finds for itself in a
// container, which are the container's own.
const containerNote = "these are the container's own addresses: other machines use the host's, and MAGPIE_PUBLIC_URL=http://<host>:<port> puts it here"

// shareLines are where other machines reach the gateway while it is shared
// from Settings, for the banner.
func shareLines() []string {
	if s := settings.Load(); !s.LAN || s.LANKey == "" {
		return nil
	}
	var out []string
	for _, u := range gateway.LANURLs() {
		out = append(out, muted.Render("  network ")+" "+u)
	}
	if gateway.ContainerAddrs() {
		out = append(out, muted.Render("  "+containerNote))
	}
	return out
}

// advertisedURL is what the CLIs print for other machines: the public
// address when MAGPIE_PUBLIC_URL is valid, otherwise the one reached from
// this machine.
func advertisedURL() string {
	if u := gateway.PublicURL(); u != "" {
		return u
	}
	return gateway.URL()
}

// serve: `magpie serve` — the gateway alone, in the foreground.
func serve() error {
	s := gateway.New()
	go stats.Run(version, "serve")
	go catalog.KeepFresh() // new models' prices, in a gateway left running
	public := advertisedURL()
	fmt.Println(green.Render("●"), "magpie gateway on", bold.Render(gateway.URL()))
	fmt.Println(muted.Render("  OpenAI  "), public+"/v1/chat/completions", muted.Render("·"), public+"/v1/responses")
	fmt.Println(muted.Render("  Anthropic"), public+"/v1/messages")
	fmt.Println(muted.Render("  key     "), gateway.Token, muted.Render(keyNote()))
	for _, l := range shareLines() {
		fmt.Println(l)
	}
	n := len(provider.Catalog())
	if err := provider.FileError(); err != nil {
		fmt.Println(amber.Render("!"), err) // served without it, as no providers
	}
	if n == 0 {
		fmt.Println(amber.Render("!"), "no models yet ·", "magpie provider add deepseek sk-…")
	} else {
		fmt.Printf("  %d models · %s\n", n, muted.Render("magpie models"))
	}
	for _, x := range provider.Excluded() {
		if x.SignedOut {
			fmt.Println(amber.Render("!"), x.Agent+":", x.Why)
		}
	}
	return s.ListenAndServe(context.Background())
}

// importCmd adds the provider a magpie://import link describes, after
// showing it: magpie import [-y] <link>.
func importCmd(args []string) error {
	yes := false
	var link string
	for _, a := range args {
		switch a {
		case "-y", "--yes":
			yes = true
		default:
			link = a
		}
	}
	if link == "" {
		return errors.New("magpie import [-y] 'magpie://import?…'")
	}
	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	if err := showProvider(p); err != nil {
		return err
	}
	if old, err := provider.Find(p.ID); err == nil {
		fmt.Println(amber.Render("!"), "replaces your", old.Name)
	}
	if !yes {
		if st, err := os.Stdin.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
			return errors.New("not a terminal: add -y to import without asking")
		}
		fmt.Print("Add it? [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return errors.New("nothing added")
		}
	}
	if p.Key == "" && !p.Ready() {
		fmt.Println(amber.Render("!"), "the link has no key · magpie provider key", p.ID, "<key>")
	}
	return saveNew(p)
}

// fetchedFrom is where a provider's models were fetched: its API's host, or
// for a subscription run through its own CLI (Kiro, Devin, Cursor …), which
// has none, its name.
func fetchedFrom(p provider.Provider) string {
	if h := p.Host(); h != "" {
		return h
	}
	return p.Name
}

// accountModelsCmd: `magpie provider account-models <id> [account|key
// [ids…|all]]` — the models one account or key of a provider alone serves
// (#474), each account's with none named, all for every one the provider has.
func accountModelsCmd(rest []string) error {
	if len(rest) < 1 {
		return fmt.Errorf("magpie provider account-models <id> [account|key [model ids…|all]]")
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if len(rest) > 2 {
		ms := rest[2:]
		if len(ms) == 1 && (ms[0] == "all" || ms[0] == "-") {
			ms = nil
		}
		if err := provider.SetAccountModels(p.ID, rest[1], ms); err != nil {
			return err
		}
	}
	refs := p.AccountRefs()
	if len(rest) > 1 {
		ref, _, err := provider.AccountModelsOf(p.ID, rest[1])
		if err != nil {
			return err
		}
		refs = []string{ref}
	}
	if len(refs) == 0 {
		fmt.Println(muted.Render(p.Name + " has no account or key"))
		return nil
	}
	label := map[string]string{}
	for _, k := range p.KeyList() {
		if k.Name != "" {
			label[k.ID] = k.Name + " " + muted.Render(k.Masked+" · "+k.ID)
		} else {
			label[k.ID] = k.Masked + " " + muted.Render(k.ID)
		}
	}
	for _, r := range refs {
		name := r
		if l, ok := label[r]; ok {
			name = l
		}
		_, ms, _ := provider.AccountModelsOf(p.ID, r)
		if len(ms) == 0 {
			fmt.Println(name, muted.Render("· every model "+p.Name+" serves"))
		} else {
			fmt.Println(name, "· only", strings.Join(ms, ", "))
		}
	}
	return nil
}

// accountCapCmd shows, or sets with a share or off, the usage cap of a
// subscription's accounts: the share of each window one is used to at most
// (provider.AccountCaps); with --window <name>, the share of that window
// alone (provider.AccountWindowCaps).
func accountCapCmd(rest []string) error {
	usage := fmt.Errorf("magpie provider account-cap <id> [account [percent|off]]\n       magpie provider account-cap <id> <account> --window <name> [percent|none|default]")
	window, windowSet := "", false
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if v, ok := strings.CutPrefix(a, "--window="); ok {
			window, windowSet = v, true
			rest = slices.Delete(slices.Clone(rest), i, i+1)
			i--
			continue
		}
		if a == "--window" {
			if i+1 >= len(rest) {
				return usage
			}
			window, windowSet = rest[i+1], true
			rest = slices.Delete(slices.Clone(rest), i, i+2)
			i--
		}
	}
	if len(rest) < 1 || windowSet && (len(rest) < 2 || strings.TrimSpace(window) == "") {
		return usage
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if p.Account == nil {
		return fmt.Errorf("%s has keys, not subscription accounts with usage windows to cap", p.Name)
	}
	if len(rest) > 2 {
		if windowSet {
			cap, err := parseWindowCap(rest[2])
			if err != nil {
				return err
			}
			err = provider.SetWindowCap(p.ID, rest[1], window, cap)
			if err != nil {
				return err
			}
		} else {
			cap, err := provider.ParseCap(rest[2])
			if err != nil {
				return err
			}
			if err := provider.SetAccountCap(p.ID, rest[1], cap); err != nil {
				return err
			}
		}
		if p, err = provider.Find(p.ID); err != nil {
			return err
		}
	}
	refs := p.AccountRefs()
	if len(rest) > 1 {
		refs = slices.DeleteFunc(refs, func(r string) bool { return !strings.EqualFold(r, strings.TrimSpace(rest[1])) })
		if len(refs) == 0 {
			return fmt.Errorf("%s has no account %q", p.Name, rest[1])
		}
	}
	if len(refs) == 0 {
		fmt.Println(muted.Render(p.Name + " has no account"))
		return nil
	}
	for _, r := range refs {
		caps := p.CapsOf(r)
		if caps.All > 0 {
			fmt.Printf("%s · capped at %d%% of each usage window\n", r, caps.All)
		} else if len(caps.Windows) > 0 {
			fmt.Println(r, muted.Render("· no cap on its other windows: used to 100%"))
		} else {
			fmt.Println(r, muted.Render("· no cap: used to 100%"))
		}
		for _, w := range slices.Sorted(maps.Keys(caps.Windows)) {
			if c := caps.Windows[w]; c >= 100 {
				fmt.Printf("  %s · no cap on this window\n", w)
			} else {
				fmt.Printf("  %s · capped at %d%%\n", w, c)
			}
		}
	}
	return nil
}

// parseWindowCap reads a window's own share as the CLI takes it: "50",
// "50%", none (off, 100) for no cap on that window, default (-) to follow
// the account's cap.
func parseWindowCap(s string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "default", "account", "-", "0":
		return 0, nil
	case "none", "off", "no", "100", "100%":
		return 100, nil
	}
	n, err := provider.ParseCap(s)
	if err != nil {
		return 0, fmt.Errorf("a window's cap is a share from %d to %d (percent), none for no cap on it, or default to follow the account's cap, not %q", provider.MinCap, provider.MaxCap, s)
	}
	return n, nil
}

// accountConcurrencyCmd shows, or sets, the limit on requests at once of a
// provider's accounts or keys (#892): each one's own, else the provider's
// Concurrency. What runs and waits now is the gateway's, at GET
// /v1/magpie/concurrency and on the app's account rows.
func accountConcurrencyCmd(rest []string) error {
	if len(rest) < 1 {
		return fmt.Errorf("magpie provider account-concurrency <id> [account|key [n|off|default]]")
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if len(rest) > 2 {
		limit, err := provider.ParseLimit(rest[2])
		if err != nil {
			return err
		}
		if err := provider.SetAccountConcurrency(p.ID, rest[1], limit); err != nil {
			return err
		}
		if p, err = provider.Find(p.ID); err != nil {
			return err
		}
	}
	refs := p.AccountRefs()
	if len(rest) > 1 {
		ref, ok := p.AccountRefOf(rest[1])
		if !ok {
			return fmt.Errorf("%s has no account or key %q", p.Name, rest[1])
		}
		refs = []string{ref}
	}
	if len(refs) == 0 {
		fmt.Println(muted.Render(p.Name + " has no account or key"))
		return nil
	}
	label := map[string]string{}
	for _, k := range p.KeyList() {
		if k.Name != "" {
			label[k.ID] = k.Name + " " + muted.Render(k.Masked+" · "+k.ID)
		} else {
			label[k.ID] = k.Masked + " " + muted.Render(k.ID)
		}
	}
	base := p.Concurrency()
	for _, r := range refs {
		name := r
		if l, ok := label[r]; ok {
			name = l
		}
		n, own := p.AccountConcurrencyOf(r)
		switch {
		case own && n > 0:
			fmt.Printf("%s · at most %d at once (its own)\n", name, n)
		case own:
			fmt.Println(name, "· no limit at once (its own)")
		case base > 0:
			fmt.Println(name, muted.Render(fmt.Sprintf("· at most %d at once (%s's)", base, p.Name)))
		default:
			fmt.Println(name, muted.Render("· no limit at once"))
		}
	}
	return nil
}

// queueCmd shows, or sets, how many requests may wait for each of a
// provider's accounts or keys past its limit, and for how many seconds
// (#892).
func queueCmd(rest []string) error {
	if len(rest) < 1 || len(rest) > 3 {
		return fmt.Errorf("magpie provider queue <id> [length [seconds]]")
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		num := func(s, what string) (int, error) {
			if s == "off" || s == "none" || s == "-" {
				return 0, nil
			}
			n, err := strconv.Atoi(s)
			if err != nil {
				return 0, fmt.Errorf("the %s is a whole number, or off, not %q", what, s)
			}
			return n, nil
		}
		ql, err := num(rest[1], "queue length")
		if err != nil {
			return err
		}
		qw := p.QueueWait
		if len(rest) > 2 {
			if qw, err = num(rest[2], "wait in seconds"); err != nil {
				return err
			}
		}
		if err := provider.SetQueue(p.ID, ql, qw); err != nil {
			return err
		}
		if p, err = provider.Find(p.ID); err != nil {
			return err
		}
	}
	length, wait := "no bound", "as long as it takes"
	if p.QueueLimit > 0 {
		length = fmt.Sprintf("%d", p.QueueLimit)
	}
	if p.QueueWait > 0 {
		wait = fmt.Sprintf("%ds", p.QueueWait)
	}
	fmt.Printf("%s · queue for each account or key: %s waiting, each for %s\n", p.Name, length, wait)
	return nil
}

// rpmCmd shows, or sets, how many requests each of a provider's accounts
// or keys sends the vendor in any minute (coeo91 on Discord: OpenRouter's
// free models take 20).
func rpmCmd(rest []string) error {
	if len(rest) < 1 || len(rest) > 2 {
		return fmt.Errorf("magpie provider rpm <id> [n|off]")
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		n := 0
		switch s := strings.ToLower(strings.TrimSpace(rest[1])); s {
		case "off", "none", "no", "-":
		default:
			if n, err = strconv.Atoi(s); err != nil {
				return fmt.Errorf("requests a minute is a whole number, or off, not %q", rest[1])
			}
		}
		if err := provider.SetRPM(p.ID, n); err != nil {
			return err
		}
		if p, err = provider.Find(p.ID); err != nil {
			return err
		}
	}
	if n := p.RPMLimit(); n > 0 {
		fmt.Printf("%s · each account or key: at most %d requests a minute\n", p.Name, n)
	} else {
		fmt.Printf("%s · each account or key: no limit on requests a minute\n", p.Name)
	}
	return nil
}

// printMoved says which agents a change moved off models it stopped
// serving (agent.Reseat).
func printMoved(moved []agent.Move) {
	for _, m := range moved {
		if m.Error != "" {
			fmt.Println("!", m.String())
			continue
		}
		fmt.Println(green.Render("✓"), "moved", m.String())
	}
}

// editPicks works out a provider's picks from `magpie provider models <id>
// args…`: plain ids replace them, all (or -) gives the default back, and
// +id / -id add one to or take one out of the models exposed now, so a
// user adding a model keeps the rest (MOMO on Discord: 35 exposed → 8).
func editPicks(p provider.Provider, args []string) ([]string, error) {
	if len(args) == 1 && (args[0] == "-" || args[0] == "all") {
		return nil, nil
	}
	edits := 0
	for _, a := range args {
		if len(a) > 1 && (a[0] == '+' || a[0] == '-') {
			edits++
		}
	}
	if edits == 0 {
		return slices.Clone(args), nil
	}
	if edits != len(args) {
		return nil, fmt.Errorf("give either the whole list (magpie provider models %s a b c) or only changes (+a -b), not both", p.ID)
	}
	picks := slices.Clone(p.Models)
	if len(picks) == 0 {
		// no picks yet: the change is to the models exposed by default
		for _, m := range p.Exposed() {
			picks = append(picks, m.ID)
		}
	}
	for _, a := range args {
		id := strings.TrimPrefix(a[1:], p.ID+"/")
		if a[0] == '+' {
			if !slices.Contains(picks, id) {
				picks = append(picks, id)
			}
			continue
		}
		i := slices.Index(picks, id)
		if i < 0 {
			return nil, fmt.Errorf("%s/%s isn't exposed, so there is nothing to take out", p.ID, id)
		}
		picks = slices.Delete(picks, i, i+1)
	}
	if len(picks) == 0 {
		// no picks means the default list, not none
		return nil, fmt.Errorf("that leaves no model exposed · to keep %s's models out of the list: magpie provider listed %s no", p.Name, p.ID)
	}
	return picks, nil
}

// printPicksChange says what a change to a provider's picks did: how many
// models were exposed before and after, and which went or came. A list
// that replaced the old one says so, and how to add one instead.
func printPicksChange(before, after provider.Provider, args []string) {
	ids := func(p provider.Provider) []string {
		var out []string
		for _, m := range p.Exposed() {
			out = append(out, m.ID)
		}
		return out
	}
	was, now := ids(before), ids(after)
	var gone, added []string
	for _, id := range was {
		if !slices.Contains(now, id) {
			gone = append(gone, id)
		}
	}
	for _, id := range now {
		if !slices.Contains(was, id) {
			added = append(added, id)
		}
	}
	replaced := len(args) > 0 && !strings.HasPrefix(args[0], "+") && !(len(args[0]) > 1 && args[0][0] == '-')
	if len(args) == 0 {
		replaced = true // provider set models=
	}
	what := "exposed models"
	if replaced && len(after.Models) > 0 {
		what = "exposed models replaced"
	}
	fmt.Println(green.Render("✓"), what+":", len(was), "→", len(now))
	list := func(ms []string) string {
		if len(ms) > 8 {
			return strings.Join(ms[:8], ", ") + fmt.Sprintf(" … %d more", len(ms)-8)
		}
		return strings.Join(ms, ", ")
	}
	if len(added) > 0 {
		fmt.Println(" ", green.Render("+"), list(added))
	}
	if len(gone) > 0 {
		fmt.Println(" ", amber.Render("-"), list(gone))
	}
	if replaced && len(gone) > 0 && len(after.Models) > 0 {
		fmt.Println(muted.Render("  the ids given are now the whole list · to add one and keep the rest: magpie provider models " + after.ID + " +<model>"))
	}
	avail := map[string]bool{}
	for _, m := range after.Available() {
		avail[m.ID] = true
	}
	for _, id := range added {
		if len(avail) > 0 && !avail[id] {
			fmt.Println(" ", amber.Render("!"), after.ID+"/"+id, muted.Render("isn't in "+after.Name+"'s list; exposed as given"))
		}
	}
}
