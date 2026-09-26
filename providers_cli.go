package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

var amber = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#F2B544"})

const providerUsage = `usage:
  magpie providers                        list providers, keys and who uses them
  magpie presets                          list the vendors magpie knows out of the box
  magpie provider <id>                    show one provider and its models
  magpie provider add <preset> <key>      add a preset vendor   e.g. magpie provider add deepseek sk-…
                                          again, it adds another (deepseek-2); k=v pairs too: id, name, header.X-Foo
  magpie provider add <name> k=v…         add a custom vendor   k: url, anthropic, responses, key, models, catalog, icon, header.X-Foo, balance, balance.path, balance.token, models.url
  magpie provider set <id> k=v…           change a provider's settings, with the same k=v pairs as add
  magpie provider key <id> <key>          change the API key
  magpie provider icon <id> <file|name>   give a custom provider a picture (PNG, JPEG, SVG…) or a built-in icon
  magpie provider fallback <id> <provider/model>…   where requests go when it's out of quota or down (none clears)
  magpie provider models <id> [ids…]      fetch the vendor's model list, or choose which models to expose
  magpie provider listed <id> yes|no      no: its models serve only through routing groups, not in the list
  magpie provider test <id>               send a tiny request through each endpoint
  magpie provider rm <id>                 remove a provider

  e.g. magpie provider add "My Relay" url=https://relay.example.com/v1 key=sk-…
       magpie provider add "Own Claude" anthropic=https://gw.example.com key=sk-… catalog=anthropic
       magpie provider add "My Relay" url=https://relay.example.com/v1 key=sk-… header.X-Org-Id=acme
       magpie provider add anthropic sk-… id=anthropic-ws2 name="Anthropic WS2" header.anthropic-workspace-id=wrkspc_…
       magpie provider set my-relay models.url=https://relay.example.com/api/models catalog=
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
	if len(all) == 0 && len(provider.Excluded()) == 0 {
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
		if t, ok := p.Fetched(); ok {
			r.models = fmt.Sprintf("%d of %d models", n, len(p.Available())) + muted.Render(" · fetched "+ago(t))
		} else {
			r.models = fmt.Sprintf("%d models", n)
		}
		if u := uses[p.ID]; len(u) > 0 {
			r.uses = green.Render("← " + strings.Join(u, ", "))
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
		if a, err := agent.Find(x.Agent); err == nil {
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
	return nil
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
	for _, pr := range provider.Presets() {
		if pr.Kind != kind {
			kind = pr.Kind
			fmt.Println(faint.Render("  " + map[provider.Kind]string{provider.KindVendor: "vendors", provider.KindRelay: "relays", provider.KindLocal: "local"}[kind]))
		}
		name := bold.Render(pr.Name)
		if pr.Sponsored {
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
	if len(entries) == 0 && agentID != "" {
		names, _ := provider.VisibleTo(agentID)
		fmt.Println(amber.Render("!"), agentID, "is shown none of them: nothing is in", strings.Join(names, ", "), muted.Render("· magpie visible "+agentID+" all shows it every model"))
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
	fmt.Println(faint.Render("  " + gateway.URL() + "/v1"))
	return nil
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
		if err := provider.Delete(p.ID); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "removed", p.Name)
		return nil
	case "test":
		if len(rest) != 1 {
			return fmt.Errorf("magpie provider test <id>")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ok := true
		for _, r := range p.Test(ctx) {
			if r.OK {
				fmt.Printf("  %s %-10s %s\n", green.Render("✓"), r.Protocol, muted.Render(fmt.Sprintf("%d ms · %s", r.Millis, r.Model)))
			} else {
				ok = false
				msg := r.Error
				if r.Status != 0 {
					msg = fmt.Sprintf("%d · %s", r.Status, r.Error)
				}
				fmt.Printf("  %s %-10s %s\n", amber.Render("✗"), r.Protocol, msg)
			}
		}
		if !ok {
			os.Exit(1)
		}
		return nil
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
	case "models":
		if len(rest) < 1 {
			return fmt.Errorf("magpie provider models <id> [model ids to expose…]")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		if len(rest) > 1 {
			p.Models = rest[1:]
			if len(rest) == 2 && (rest[1] == "-" || rest[1] == "all") {
				p.Models = nil
			}
			if err := provider.Save(*p); err != nil {
				return err
			}
			return showProvider(*p)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ms, err := p.Fetch(ctx)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), len(ms), "models from", fetchedFrom(*p))
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
	}
	n := len(saved.Exposed())
	if saved.Decides() {
		fmt.Println("  it routes groups: magpie group set <id> effort=auto classifier="+saved.ID+"/"+provider.JevLatest,
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
		if v != "" {
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
	switch {
	case p.Account != nil:
		who := p.Account.User
		if p.Account.Plan != "" {
			who += muted.Render("  " + p.Account.Plan)
		}
		kv("account", who+muted.Render("  from "+p.Account.Agent+"'s own sign-in"))
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
	if t, ok := p.Fetched(); ok {
		src = fetchedFrom(p) + " · fetched " + ago(t)
	}
	kv("models", fmt.Sprintf("%d exposed of %d %s", len(ms), len(p.Available()), muted.Render("from "+src)))
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
		case "models.url":
			p.ModelsURL = v
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

// serve: `magpie serve` — the gateway alone, in the foreground.
func serve() error {
	s := gateway.New()
	fmt.Println(green.Render("●"), "magpie gateway on", bold.Render(gateway.URL()))
	fmt.Println(muted.Render("  OpenAI  "), gateway.URL()+"/v1/chat/completions", muted.Render("·"), gateway.URL()+"/v1/responses")
	fmt.Println(muted.Render("  Anthropic"), gateway.URL()+"/v1/messages")
	fmt.Println(muted.Render("  key     "), gateway.Token, muted.Render("(anything works; the gateway only listens on localhost)"))
	n := len(provider.Catalog())
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
