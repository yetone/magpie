package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/plugin"
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
  magpie provider add <name> k=v…         add a custom vendor   k: url, anthropic, responses, gemini, decide, key, models, catalog, icon, header.X-Foo, balance, balance.path, balance.method, balance.token, models.url, list, search, user-agent,
                                          access.key, access.secret (a Volcengine access key, or a Tencent Cloud SecretId and SecretKey, for its plan's windows)
  magpie provider set <id> k=v…           change a provider's settings, with the same k=v pairs as add;
                                          codex=as-sent sends Codex's Responses requests as Codex sent them
                                          (a relay that checks they are Codex's own), codex=default fits them;
                                          user-agent=agent, the default, sends the User-Agent the agent gave
                                          (pi, opencode…); user-agent=magpie sends magpie's in its place;
                                          compaction=native asks the provider Codex's own compaction and hands
                                          Codex its sealed item as it came (a Responses API, a ChatGPT account,
                                          Copilot), compaction=magpie, the default, has magpie summarise
                                          routing=smart|order|rotate|usage|pace|weight|free|fast|drain spreads requests
                                          over its keys or accounts (fast: the one that answered fastest lately first;
                                          drain: one until it is used up, then the next, never back to an earlier one);
                                          prompt=as-written sends Antigravity the system prompt exactly as written;
                                          prompt=default, the default, breaks up the lines Google answers with a
                                          false 429 (Codex's, Claude Code's, Hermes' old persona) with a zero-width space
                                          additional-tools=on sends a tool Claude Code's tool search loads to its
                                          Responses API where it was loaded (an additional_tools item, which
                                          keeps the cache); OpenAI's API and a ChatGPT (Codex) account do so by
                                          default; additional-tools=off puts it with the other tools,
                                          additional-tools=default is magpie's
                                          image-files=off sends DeepSeek's API an agent's images inline on every
                                          turn; image-files=on, the default, uploads each once (for a day)
                                          and names it by its file id
                                          upstream-providers=on, on a Remote magpie, names its models by the
                                          provider serving each there (Claude · Office › RelayA); off, the
                                          default, as that magpie's own list names them
                                          patience=2m keeps a request to it alone (not through a group) that long
                                          while it is busy or briefly rate limited, asking again after a pause
                                          (at most 4m); patience=off hands the agent the error at once;
                                          patience=default, 1m
                                          socket=auto sends its Responses turns over the Responses WebSocket,
                                          over HTTP when it can't, and over HTTP alone for 10m once the host
                                          refuses it; socket=on tries it every turn; socket=off, the default
                                          errors=pass sends the agent its errors as the vendor sent them:
                                          asked once, nothing read into them, no other provider tried;
                                          errors=magpie, the default, has magpie handle them
  magpie provider key <id> <key>          change the API key
  magpie provider icon <id> <file|name>   give a custom provider a picture (PNG, JPEG, SVG…) or a built-in icon
  magpie provider fallback <id> <provider/model>…   where requests go when it's out of quota or down (none clears)
  magpie provider models <id> [ids…]      fetch the vendor's model list, or choose which models to expose:
                                          ids… replace the list, +id adds one, -id takes one out, all: the default,
                                          none: serve none of them, its balance and usage still shown,
                                          best: only the newest of each family it lists (Opus 5.6 replaces 5.5 once listed)
  magpie provider refresh <id>            fetch the vendor's model list again (as the app's Fetch models)
  magpie provider refresh-every <id> [6h|1d|off]
                                          have the gateway (magpie serve, the app) fetch the list again by itself
                                          once it is that old (at least 10m; off: only when asked)
  magpie provider auto-include <id> [rule…|none]
                                          rules over a model's id, in any case (a glob with *, e.g. 'gpt-*' '*claude*',
                                          or 're:…' a regular expression, where (?-i) makes case matter): the listed models one matches are exposed
                                          now, and each one a later fetch newly lists; picks are only added to
  magpie provider account-models <id> [account|key [ids…|all]]
                                          the models one account or key alone serves; all: every model the provider has
  magpie provider account-cap <id> [account [percent|off]]
                                          use a subscription account up to a share of each usage window (e.g. 70):
                                          at it, routing takes the account for used up until the window renews
  magpie provider account-cap <id> <account> --window <name> [percent|none|default]
                                          a share for one window alone (e.g. "5 hours" 50): none is no cap on it,
                                          default has it follow the account's cap
  magpie provider account-cap <id> <account> --window <name> --amount <n|off>
                                          a fixed amount of a window counted in a unit (e.g. "Daily credits" 300),
                                          by the vendor's count, spending outside magpie included
  magpie provider pool <id> [--accounts a,b,…] [--cap percent|off] [--stop on|off] [--careful on|off]
                                          a subscription's accounts as one pool: which are in use and in what order,
                                          each one's cap, --stop: answer 429 at the caps rather than go on to the
                                          fallbacks, --careful: read usage again near a cap, holding what can't be read
  magpie provider account-concurrency <id> [account|key [n|off|default]]
                                          how many requests one account or key has out at once, over every model,
                                          routing group and agent: its own, off for none, default for the provider's
  magpie provider spend-cap <id> [key [usd [day|month|total]|off]]
                                          how much an API key may spend, in USD as the Usage page prices its calls,
                                          a day (the default), a month or in all from now: at it, routing takes the
                                          key for used up and goes on to the other keys and the fallbacks
  magpie provider key-balance <id> [key [usd|¥cny|off] [--refill monthly[:day]|weekly[:mon…sun]|off] [--expiring amount@yyyy-mm-dd|off]… [--expiring-remove [amount@]yyyy-mm-dd]…]
                                          the balance on an API key whose vendor tells none, typed from its own page
                                          in USD or in yuan (¥100): the Usage page shows it less what magpie prices the
                                          key's calls at since; with --refill it is whole again each month or week;
                                          --expiring adds credit that counts through a day and is spent first
                                          (each one a grant, the soonest spent first; off takes every one off)
  magpie provider account-weight <id> [account [n|default]]
                                          a subscription account's share of the requests with routing=weight:
                                          one weighing 3 takes three for each one weighing 1 takes (default 1)
  magpie provider queue <id> [length [seconds]]
                                          how many may wait for each account or key past its limit, and how long;
                                          past either a request is turned away with a 429 (0: no bound)
  magpie provider rpm <id> [n|off]        how many requests each account or key sends the vendor in any minute;
                                          one more waits for room, up to 2 minutes, then is turned away with a 429
  magpie provider at-once <id> [n|off|default]
                                          how many of its accounts or keys take requests at the same time, the first
                                          in the list; the next takes over when one runs out or fails (off: no limit,
                                          default: what its plugin says)
  magpie provider quota-rest <id> [30m|6h|7d|until 08:00|until 00:00+08:00|default]
                                          how long a key sits out once the vendor says it is out of its allowance
                                          without saying when it is back (default: 15 minutes; at most 8 days),
                                          or until the time of day a daily allowance comes back, on this
                                          computer's clock or at the vendor's offset from UTC
  magpie provider rest-cap <id> [1m|30m|6h|1d|off|default]
                                          the most a key or account sits out while it keeps failing or being
                                          rate limited, its rest doubling each time from a minute (default:
                                          10 minutes for a failure, 30 for a rate limit; at most 8 days);
                                          off: one that fails without saying why (a relay's 5xx) doesn't
                                          rest at all, while out of quota, its key refused or rate limited
                                          still rests it
  magpie provider listed <id> yes|no      no: its models serve only through routing groups, not in the list
  magpie provider off|on <id>             switch it off (kept, but no agent or request uses it), or on again
  magpie provider test <id> [--as codex|claude-code] [model…]
                                          send a tiny request through each endpoint, or to each model;
                                          --as asks it as that agent does, for a relay that serves only it
  magpie provider rm <id>                 remove a provider

  e.g. magpie provider add "My Relay" url=https://relay.example.com/v1 key=sk-…
       magpie provider add "Own Claude" anthropic=https://gw.example.com key=sk-… catalog=anthropic
       magpie provider add "My Relay" url=https://relay.example.com/v1 key=sk-… header.X-Org-Id=acme
       magpie provider add remote-magpie sk-magpie-… url=http://192.168.1.20:3425 id=office
                                   (another computer's magpie, shared on its network: its models and routing
                                    groups as office/…, each request sent on in the API the agent spoke)
       magpie provider add bailian-decision sk-… workspace=<workspace id>   (or region=ap-southeast-1, or region=token-plan with an sk-sp- key)
       magpie provider add bailian-token-plan sk-sp-…   (Bailian's Token Plan: its chat models, and its decision model for routing groups;
                                    qwen-token-plan is the Qwen AI platform's, qwen-decision its pay-as-you-go decision model)
       magpie provider add qwencloud sk-ws-… region=us-east-1   (Qwen Cloud, Alibaba's international platform, pay as you go;
                                    qwencloud-token-plan sk-sp-… is its Token Plan)
       magpie provider add google-vertex project=my-project impersonate=vertex@my-project.iam.gserviceaccount.com
                                   (Google Vertex AI in your Google Cloud project, with no key: signed with gcloud's
                                    Application Default Credentials (gcloud auth application-default login) unless
                                    credentials=<file> names another, a service account key too; impersonate= needs
                                    roles/iam.serviceAccountTokenCreator on that account; location= global (the
                                    default), us, eu or a region such as us-central1; set changes them, empty clears)
       magpie provider add "My Decider" decide=https://decide.example.com/v1 key=sk-… models=my-decision-model
                                   (a System One API, POST …/systemone: it routes groups, its models are never an agent's)
       magpie provider add anthropic sk-… id=anthropic-ws2 name="Anthropic WS2" header.anthropic-workspace-id=wrkspc_…
       magpie provider set my-relay models.url=https://relay.example.com/api/models catalog=
       magpie provider set volcengine list=off models=ep-20261108-abcde
                                   (its model list is never asked for: only the models given by id are served)
       magpie provider set my-relay search=yes
                                   (the relay answers Claude Code's WebSearch and Codex's web_search itself:
                                    those go to it as sent, not through magpie's own search)
       magpie provider add lmstudio address=http://192.168.1.5:1234
       magpie provider set lmstudio address=192.168.1.5:1234
                                   (Ollama, LM Studio, oMLX or MLX-Serve on another port or computer: each of its
                                    URLs moves there, keeping its path; address= alone puts it back at the default)
       magpie provider set ollama unmasked=yes
                                   (Settings' redaction leaves its requests as written, at any address: your call,
                                    so mind a relay that passes them on to a vendor; a subscription or a plugin's
                                    provider too, whose vendor gets them so; never another magpie)
       magpie provider set claude fallback-for-others=no
                                   (no other model's fallback, nor a routing group's next after one:
                                    a local model that is down or full doesn't send its requests on to it)
       magpie provider set modelscope free=yes
                                   (every model of it counts as free, so a Free first routing spends it before
                                    paid ones, as a free daily quota; prices and Usage's costs stay as listed)
       magpie provider set my-relay billing=plan
                                   (marks it on a plan, or billing=payg pay as you go, on Providers and Routing;
                                    billing=auto goes back to magpie's reading of its endpoints)
       magpie provider add "My Relay" url=https://relay.example.com/v1 key=sk-… balance=https://relay.example.com/api/usage/token balance.path='$data.total_available / 500000'
       magpie provider set my-relay balance.path='(1 - credits.monthlyCredits / 70) %'
       magpie provider set my-relay balance.method=POST
                                   (a balance endpoint that answers only a POST: asked with no body)
       magpie provider set my-relay balance=https://relay.example.com/api/user/self balance.path='$data.quota / 500000' balance.token=<access token> header.New-Api-User=<user id>
                                   (a new-api relay's whole account: its access token and user id, from its personal settings)
                                   (balance.path: where the amount is in the reply, or a sum of those with + - * / and
                                    brackets; $ or ¥ in front adds the sign, % after it shows a percent of 1;
                                    several go apart by ; each with a label: '5h: a.used / a.cap %; $credits.left';
                                    a list's items are a.*.used, or a[status=active].used for those that match,
                                    in sum() max() min() count(): 'sum(a.*.used) / sum(a.*.cap) %'; one the reply
                                    hasn't shows — and the others still show)
       magpie provider set my-relay context=272k context.gpt-6=1m
                                   (context: how long a request agents are told the models take, over what the
                                    vendor or models.dev says; context.<model> for one of them; empty clears)
       magpie provider set opencode-go family=ocgo
       magpie provider set my-relay id=relay   (renames it: groups and agents on my-relay/… move to relay/…)
                                   (family: a tag for which agents are shown its models, see magpie visible)`

// keyColumn is what magpie providers says of p's key or account.
func keyColumn(p provider.Provider) string {
	switch {
	case p.Off:
		return faint.Render("○ switched off")
	case p.Account != nil && signedOut(p):
		// every account in use refused: listed to be signed in again,
		// as the app's row says, never "signed in"
		return amber.Render("○ "+p.AccountShown(p.Account.User)+" is signed out") + muted.Render(" · sign in again")
	case p.Account != nil:
		return green.Render("●") + " " + muted.Render("signed in as "+p.AccountShown(p.Account.User))
	case p.IsVertex() && !p.Ready():
		// no project an address can be made of (providers.json edited
		// by hand): nothing is asked, as the app's row says
		return amber.Render("○ needs a project")
	case p.IsVertex():
		// no key: a token minted from the user's Google credentials,
		// traded for a service account's when it impersonates one
		who := "Google credentials"
		if p.Vertex != nil && p.Vertex.Impersonate != "" {
			who += " as " + p.Vertex.Impersonate
		}
		return green.Render("●") + " " + muted.Render(who)
	case p.Key != "":
		return green.Render("●") + " " + muted.Render(provider.Mask(p.Key))
	case p.Ready():
		return green.Render("●") + " " + muted.Render("no key needed")
	default:
		return amber.Render("○ no key")
	}
}

// signedOut is Provider.SignedOut, for a test to stand in for.
var signedOut = provider.Provider.SignedOut

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
		r.key = keyColumn(p)
		n := len(p.Exposed())
		if t, ok := p.Listed(); ok {
			r.models = fmt.Sprintf("%d of %d models", n, len(p.Available())) + muted.Render(" · fetched "+ago(t))
		} else {
			r.models = fmt.Sprintf("%d models", n)
		}
		if u := uses[p.ID]; len(u) > 0 {
			r.uses = green.Render("← " + strings.Join(u, ", "))
		}
		if p.Off {
			// the user's own switch, kept as they set it: how to undo it
			// is said beside it, never done for them
			r.uses = strings.TrimSpace(muted.Render("magpie provider on "+p.ID) + "  " + r.uses)
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
		if provider.IsGroupRef(v) {
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
			fmt.Println(faint.Render("  " + map[provider.Kind]string{provider.KindPartner: "partners (sponsors)", provider.KindVendor: "vendors", provider.KindRelay: "relays", provider.KindLocal: "local", provider.KindRemote: "another computer's magpie"}[kind]))
		}
		name := bold.Render(pr.Name)
		if pr.Sponsored && pr.Kind != provider.KindPartner {
			name += " " + faint.Render("sponsored")
		}
		state := muted.Render("magpie provider add " + pr.ID + " <key>")
		if pr.NoKey {
			state = muted.Render("magpie provider add " + pr.ID)
		}
		if pr.ID == provider.VertexPreset {
			state = muted.Render("magpie provider add " + pr.ID + " project=<project id>")
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
		q := strings.TrimPrefix(args[0], "--agent=")
		if q == "--agent" && len(args) > 1 {
			q = args[1]
		}
		a, err := agent.Find(q)
		if err != nil {
			return err
		}
		agentID = a.ID
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
		if !p.On() || p.Unlisted || p.ServesNone() {
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
	if len(p.Models) == 0 && p.BestInFamily {
		why = "best in family: an older one of its family"
	} else if len(p.Models) == 0 {
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
		if p.IsVertex() {
			return fmt.Errorf("%s is asked with your Google credentials, not an API key · magpie provider set %s credentials=<file> impersonate=<service account> changes them", p.Name, p.ID)
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
			return fmt.Errorf("magpie provider fallback <id> [provider/model… | group/<id>… | none]")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		if len(rest) > 1 {
			p.Fallback = nil
			if !(len(rest) == 2 && rest[1] == "none") {
				for _, id := range rest[1:] {
					// a routing group's id ("group/<id>") resolves too: the
					// gateway sends the request through the group
					if _, _, ok := provider.Resolve(id); !ok {
						return fmt.Errorf("magpie knows no model or group %q (magpie models lists them)", id)
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
		// --as codex|claude-code, anywhere after test: asked as that agent asks
		as := ""
		for i := 0; i < len(rest); i++ {
			if v, ok := strings.CutPrefix(rest[i], "--as="); ok {
				as, rest = v, slices.Delete(slices.Clone(rest), i, i+1)
				i--
			} else if rest[i] == "--as" && i+1 < len(rest) {
				as, rest = rest[i+1], slices.Delete(slices.Clone(rest), i, i+2)
				i--
			}
		}
		if len(rest) < 1 {
			return fmt.Errorf("magpie provider test <id> [--as codex|claude-code] [model…]")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		res, wait := p.Test, 30*time.Second
		if models := rest[1:]; len(models) > 0 {
			res, wait = func(ctx context.Context) []provider.Result { return p.TestModels(ctx, models) }, 90*time.Second
		}
		ctx, cancel := context.WithTimeout(provider.TestAs(context.Background(), as), wait)
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
	case "pool":
		return poolCmd(rest)
	case "account-concurrency", "account-limit":
		return accountConcurrencyCmd(rest)
	case "key-balance", "key-balances":
		return keyBalanceCmd(rest)
	case "spend-cap", "spend-caps":
		return spendCapCmd(rest)
	case "account-weight", "account-weights":
		return accountWeightCmd(rest)
	case "queue":
		return queueCmd(rest)
	case "rpm":
		return rpmCmd(rest)
	case "at-once", "accounts-at-once":
		return atOnceCmd(rest)
	case "refresh-every":
		return refreshEveryCmd(rest)
	case "auto-include":
		return autoIncludeCmd(rest)
	case "quota-rest":
		return quotaRestCmd(rest)
	case "rest-cap":
		return restCapCmd(rest)
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
			// none: no model served at all, the provider kept for its
			// balance and usage (#1818); best: the newest of each family
			// the vendor lists (Best in family); all is the vendor's list
			// again
			none := len(rest) == 2 && rest[1] == "none"
			best := len(rest) == 2 && rest[1] == "best"
			picks, err := editPicks(*p, rest[1:])
			if none || best {
				picks, err = nil, nil
			}
			if err != nil {
				return err
			}
			p.Models, p.NoModels, p.BestInFamily = picks, none, best
			if err := provider.Save(*p); err != nil {
				return err
			}
			printPicksChange(before, *p, rest[1:])
			return showProvider(*p)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ms, dropped, taken, err := p.RefetchTaking(ctx)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), len(ms), "models from", fetchedFrom(*p))
		if len(taken) > 0 {
			fmt.Println(green.Render("+"), "newly listed, taken by its auto-include rules:", strings.Join(taken, ", "))
		}
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
			if p.IsVertex() {
				return fmt.Errorf("%s is asked with your Google credentials, not an API key · magpie provider add %s project=<your Google Cloud project's id>", p.Name, p.ID)
			}
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
	if err != nil && p.IsVertex() && (p.Vertex == nil || strings.TrimSpace(p.Vertex.Project) == "") {
		// the pair that gives the project, which Save's words don't name
		return fmt.Errorf("%w · magpie provider add %s project=<your Google Cloud project's id>", err, p.Preset)
	}
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
	if saved.ListsOff() {
		// its list is never asked for: the models given by id are its
	} else if ms, err := saved.Fetch(ctx); err == nil {
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

func showProvider(p provider.Provider) error { return showProviderAsking(p, true) }

// showProviderAsking is showProvider; with ask false (an import not yet
// confirmed) the balance query is named, not asked: nothing goes out with
// the key before the user says yes.
func showProviderAsking(p provider.Provider, ask bool) error {
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
	if p.PassesErrors() {
		kv("errors", "pass"+muted.Render("  sent to the agent as the vendor sent them, asked once"))
	}
	if p.MagpieUserAgent {
		kv("user-agent", "magpie's"+muted.Render("  not the one the agent gave"))
	}
	if p.Unredacted {
		why := "  Settings' redaction leaves its requests as written"
		if !p.SkipsRedaction() {
			why = "  but masked: no address"
		}
		kv("unmasked", "yes"+muted.Render(why))
	}
	if p.NoStandIn {
		kv("fallback-for-others", "no"+muted.Render("  never takes another model's request"))
	}
	switch b := p.BillingKind(); {
	case b == "":
	case p.Billing != "":
		kv("billing", billingWord(b)+muted.Render("  by your word"))
	default:
		kv("billing", billingWord(b))
	}
	if p.CountFree {
		kv("free", "yes"+muted.Render("  counted free by Free first and Cheapest routing; prices and Usage's costs stay as listed"))
	}
	if p.NativeCompaction {
		kv("compaction", "native"+muted.Render("  Codex's own, sealed by the key or account that answers it"))
	}
	if p.Socket != "" {
		kv("socket", p.Socket)
	}
	switch {
	case p.Account != nil:
		who := p.AccountShown(p.Account.User)
		if p.Account.Plan != "" {
			who += muted.Render("  " + p.Account.Plan)
		}
		from := p.Account.Agent + "'s own sign-in"
		if p.IsPlugin() {
			from = p.Name + "'s sign-in"
			if provider.PluginServes(p.ID) {
				from = p.ID + "'s own sign-in" // as the built-in said
			}
		}
		kv("account", who+muted.Render("  from "+from))
	case p.IsVertex():
		// no key: where its requests go, and the Google credentials that
		// sign them
		v := provider.Vertex{}
		if p.Vertex != nil {
			v = *p.Vertex
		}
		project := v.Project
		if !p.Ready() {
			project = amber.Render(cmp.Or(project, "not set")) + muted.Render("  magpie provider set "+p.ID+" project=…")
		}
		kv("project", project)
		kv("location", v.Location)
		creds := v.Credentials
		if creds == "" {
			creds = "gcloud's Application Default Credentials" + muted.Render("  gcloud auth application-default login")
		}
		kv("signs with", creds)
		kv("as", v.Impersonate)
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
	if !ask {
		if u, field := provider.BalanceQuery(p); u != "" {
			if p.BalanceMethod != "" {
				u = strings.ToUpper(p.BalanceMethod) + " " + u
			}
			kv("balance", u+" "+muted.Render(field+"  asked once added"))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	if !ask {
		cancel()
	} else if amount, ok, err := provider.Balance(ctx, p); ok {
		from := ""
		// where it was asked: a panel's key query when its account query
		// has no token left to ask with
		if u, field := provider.BalanceQuery(p); u != "" {
			if p.BalanceMethod != "" {
				u = strings.ToUpper(p.BalanceMethod) + " " + u
			}
			from = muted.Render("  from " + u + " " + field)
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
	if d := p.RefreshInterval(); d > 0 {
		kv("refresh", "every "+p.RefreshSchedule()+muted.Render("  the gateway fetches its list by itself"))
	}
	if len(p.AutoInclude) > 0 {
		kv("include", strings.Join(p.AutoInclude, " ")+muted.Render("  newly listed models these match are exposed"))
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

// atAddress moves p's URLs to addr (provider.AtAddress). A provider
// saved with none, the gateway taking its preset's, is moved from the
// preset's; an empty addr puts a local server's back at its preset's.
func atAddress(p *provider.Provider, addr string) error {
	pr := provider.Preset(p.Preset)
	if pr != nil && pr.Kind != provider.KindLocal {
		pr = nil
	}
	if pr != nil && (strings.TrimSpace(addr) == "" || p.Chat+p.Responses+p.Anthropic+p.Decide == "") {
		p.Chat, p.Responses, p.Anthropic, p.Decide = pr.Chat, pr.Responses, pr.Anthropic, pr.Decide
	}
	if strings.TrimSpace(addr) == "" {
		if pr == nil {
			return fmt.Errorf("address= needs an address, such as http://192.168.1.5:1234")
		}
		return nil
	}
	if p.Chat+p.Responses+p.Anthropic+p.Decide == "" {
		return fmt.Errorf("%s has no URL to move: url= gives its base URL", cmp.Or(p.Name, p.ID))
	}
	return p.AtAddress(addr)
}

func applyPairs(p *provider.Provider, pairs []string) error {
	workspace := ""
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("expected key=value, got %q\n\n%s", kv, providerUsage)
		}
		if p.IsVertex() && slices.Contains([]string{"url", "chat", "openai", "responses", "anthropic", "decide", "address"}, strings.ToLower(k)) {
			// asked only at the address its project and location make, so
			// that its Google token goes nowhere else: one given here would
			// be dropped without a word
			return fmt.Errorf("%s is asked at its Google Cloud project's own address: project= and location= say where", p.Name)
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
		case "address":
			// a local server (Ollama, LM Studio, oMLX …) on another port
			// or computer: every URL it has moves there, each API keeping
			// its path, as the window's Server address does
			if err := atAddress(p, v); err != nil {
				return err
			}
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
		case "project", "location", "credentials", "impersonate":
			// Google Vertex AI's project and location, and the Google
			// credentials that sign its requests in place of a key; empty
			// clears one, and location= is global again
			if !p.IsVertex() {
				return fmt.Errorf("%s= is Google Vertex AI's, and %s isn't Vertex AI", strings.ToLower(k), p.Name)
			}
			x := provider.Vertex{}
			if p.Vertex != nil {
				x = *p.Vertex
			}
			switch v = strings.TrimSpace(v); strings.ToLower(k) {
			case "project":
				x.Project = v
			case "location":
				x.Location = v
			case "credentials":
				// a file named from where the command runs: the gateway,
				// which runs nowhere in particular, needs its full path
				if v != "" && !filepath.IsAbs(v) && !strings.HasPrefix(v, "~") {
					if abs, err := filepath.Abs(v); err == nil {
						v = abs
					}
				}
				x.Credentials = v
			default:
				x.Impersonate = v
			}
			p.Vertex = &x
		case "region", "plan":
			pr := provider.Preset(p.Preset)
			if p.IsVertex() {
				// what Google Cloud calls a region is Vertex AI's location
				return fmt.Errorf("%s has no regions or plans to pick: location= says where it is asked (global, us, eu or a region such as us-central1)", p.Name)
			}
			if pr == nil || len(pr.Regions) == 0 {
				return fmt.Errorf("%s has no regions or plans to pick", p.Name)
			}
			var ids []string
			for _, r := range pr.Regions {
				ids = append(ids, r.ID)
			}
			id := provider.RegionID(p.Preset, v)
			i := slices.IndexFunc(pr.Regions, func(r provider.Region) bool { return strings.EqualFold(r.ID, id) })
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
			p.Models = splitIDs(v, nil)
		case "website":
			p.Website = v
		case "keys":
			p.KeysURL = v
		case "balance":
			p.BalanceURL = v
		case "balance.path":
			p.BalancePath = v
		case "balance.method":
			// GET (the default) or POST, for an endpoint that answers
			// only a POST (#2368)
			switch strings.ToUpper(strings.TrimSpace(v)) {
			case "GET", "":
				p.BalanceMethod = ""
			case "POST":
				p.BalanceMethod = "POST"
			default:
				return fmt.Errorf("balance.method=GET|POST, not %q", v)
			}
		case "balance.token":
			p.BalanceToken = v
		case "access.key":
			// a Volcengine account's access key, which Ark tells its
			// Coding or Agent Plan's windows to (#1427), or a Tencent Cloud
			// SecretId, which TokenHub tells Token Plan 企业版's usage to (#1574)
			p.AccessKeyID = v
		case "access.secret":
			p.SecretAccessKey = v
		case "models.url":
			p.ModelsURL = v
		case "list":
			// off: the vendor's model list is never asked for, and only
			// the models given by id are served (axelburks on Discord)
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "off", "no":
				p.ListOff = true
			case "on", "yes", "":
				p.ListOff = false
			default:
				return fmt.Errorf("list=on|off, not %q", v)
			}
		case "codex":
			// as-sent: Codex's requests go to it as Codex sent them, for
			// a relay that checks they are Codex's own (#1758)
			switch strings.ToLower(v) {
			case "as-sent", "yes", "on":
				p.CodexAsSent = true
			case "default", "no", "off":
				p.CodexAsSent = false
			default:
				return fmt.Errorf("codex=as-sent|default, not %q", v)
			}
		case "image-files":
			// off: DeepSeek's own API is sent an agent's images inline on
			// every turn, not uploaded once and named by file id (#5407)
			switch strings.ToLower(v) {
			case "off", "no", "inline":
				p.ImageFilesOff = true
			case "on", "yes", "default":
				p.ImageFilesOff = false
			default:
				return fmt.Errorf("image-files=on|off, not %q", v)
			}
		case "upstream-providers":
			// on: a Remote magpie's models named by the provider serving
			// each there (Provider.UpstreamProviders, lcc580 on X)
			if !p.IsRemoteMagpie() {
				return fmt.Errorf("upstream-providers is a Remote magpie's; %s is not one", p.ID)
			}
			switch strings.ToLower(v) {
			case "on", "yes":
				p.UpstreamProviders = true
			case "off", "no", "default":
				p.UpstreamProviders = false
			default:
				return fmt.Errorf("upstream-providers=on|off, not %q", v)
			}
		case "prompt":
			// as-written: Antigravity's subscription is sent the system
			// prompt exactly as the agent wrote it, the lines Google turns
			// away with a 429 included; default breaks those lines up
			// with a zero-width space (Provider.PromptAsWritten)
			switch strings.ToLower(v) {
			case "as-written", "as-sent", "on", "yes":
				p.PromptAsWritten = true
			case "default", "no", "off":
				p.PromptAsWritten = false
			default:
				return fmt.Errorf("prompt=as-written|default, not %q", v)
			}
		case "additional-tools":
			// on: a tool an Anthropic client's tool search loads goes to
			// the provider's Responses API in an additional_tools item
			// where it was loaded, which keeps the cached prefix (#2660);
			// off: in the tools, where magpie's default sends the item
			// (OpenAI's API, a ChatGPT account, #4738); default: magpie's
			switch strings.ToLower(v) {
			case "on", "yes":
				p.AdditionalTools, p.AdditionalToolsOff = true, false
			case "off", "no":
				p.AdditionalTools, p.AdditionalToolsOff = false, true
			case "default":
				p.AdditionalTools, p.AdditionalToolsOff = false, false
			default:
				return fmt.Errorf("additional-tools=on|off|default, not %q", v)
			}
		case "socket":
			// its Responses turns over the Responses WebSocket magpie
			// speaks to chatgpt.com (#5781): auto leaves a host that
			// refuses it to HTTP for a while, on tries it every turn
			s, ok := provider.ParseSocket(v)
			if !ok {
				return fmt.Errorf("socket=auto|on|off, not %q", v)
			}
			p.Socket = s
		case "patience":
			// how long a request to the provider alone is kept while it
			// is busy, as a group's Patience (zoecats on Discord)
			n, err := patienceArg(v)
			if err != nil {
				return err
			}
			p.Patience = n
		case "errors":
			// pass: its errors go to the agent as the vendor sent them,
			// asked once, nothing inferred, no other provider tried and
			// none rested for (ne9ko9 on Discord); magpie: handled by
			// magpie, the default
			var on bool
			switch strings.ToLower(v) {
			case "pass", "pass-through", "passthrough", "on", "yes":
				on = true
			case "magpie", "default", "off", "no":
			default:
				return fmt.Errorf("errors=pass|magpie, not %q", v)
			}
			if on && !p.TakesPassErrors() {
				return fmt.Errorf("%s isn't a provider with an API key; errors= is for one", p.Name)
			}
			p.PassErrors = on
		case "user-agent", "ua":
			// agent, the default: a request goes with the User-Agent the
			// agent that sent it gave (#1851); magpie: magpie's own. A
			// header.User-Agent the user set stands over both
			var on bool
			switch strings.ToLower(v) {
			case "agent", "pass", "yes", "on":
				on = true
			case "magpie", "default", "no", "off":
			default:
				return fmt.Errorf("user-agent=agent|magpie, not %q", v)
			}
			if on && p.Account != nil {
				return fmt.Errorf("%s signs in as its own client, which sends the User-Agent it does; user-agent= is for a provider with an API key", p.Name)
			}
			// an account goes as its own client either way
			p.MagpieUserAgent = !on && p.Account == nil
		case "search":
			// yes: the vendor searches the web by itself, a web search a
			// client offers going to it as it was sent (a relay in front of
			// Anthropic's or OpenAI's API)
			if v != "yes" && v != "no" {
				return fmt.Errorf("search=yes|no, not %q", v)
			}
			p.Searches = v == "yes"
		case "compaction":
			// native: Codex's compaction_trigger goes to the provider as
			// Codex sent it, its sealed compaction back to Codex as it
			// came (#1099); magpie: magpie's summary, the default
			switch strings.ToLower(v) {
			case "native", "yes", "on":
				p.NativeCompaction = true
			case "magpie", "default", "no", "off":
				p.NativeCompaction = false
			default:
				return fmt.Errorf("compaction=native|magpie, not %q", v)
			}
		case "unmasked":
			// yes: the provider gets the requests as written, unmasked by
			// Settings' redaction (lc, AcMilan on Discord): the user's word
			// at any address, a subscription's or a plugin's provider's too
			// (isee1_1), another magpie's too, which then masks by its own
			// settings (provider.SkipsRedaction)
			if v != "yes" && v != "no" {
				return fmt.Errorf("unmasked=yes|no, not %q", v)
			}
			p.Unredacted = v == "yes"
		case "fallback-for-others":
			// no: never a fallback for another model, nor a routing
			// group's next after one (sonnnnnnnnnnnna on X: a local
			// uncensored model's request reaching a vendor that bans for
			// it); yes, the default, again (provider.NoStandIn)
			switch strings.ToLower(v) {
			case "yes", "on":
				p.NoStandIn = false
			case "no", "off":
				p.NoStandIn = true
			default:
				return fmt.Errorf("fallback-for-others=yes|no, not %q", v)
			}
		case "free":
			// yes: every model of it counts as free for Free first and
			// Cheapest routing, by the user's word (jiabubu on Discord:
			// ModelScope's free daily quota); prices and Usage's costs
			// stay as listed (provider.CountFree)
			switch strings.ToLower(v) {
			case "yes", "on":
				p.CountFree = true
			case "no", "off":
				p.CountFree = false
			default:
				return fmt.Errorf("free=yes|no, not %q", v)
			}
		case "billing":
			// plan or payg: how the provider bills, by the user's word,
			// marked on Providers and Routing (EV_John_Liu on X); auto:
			// magpie's reading of it (provider.BillingKind)
			switch strings.ToLower(v) {
			case "plan":
				p.Billing = provider.BillingPlan
			case "payg", "pay-as-you-go":
				p.Billing = provider.BillingPAYG
			case "auto", "":
				p.Billing = ""
			default:
				return fmt.Errorf("billing=plan|payg|auto, not %q", v)
			}
		case "context":
			if err := setContext(p, "*", v); err != nil {
				return err
			}
		case "family", "tag":
			p.Family = strings.TrimSpace(v)
		case "routing", "strategy":
			// how requests spread over its keys or accounts, as the
			// provider editor's Routing says: a group's names, but a
			// provider has no member to pick by hand
			r, err := parseRouting(v)
			if err != nil {
				return err
			}
			if r == provider.Manual || r == provider.Cheapest || r == provider.FreeExpiring {
				// cheap: its keys and accounts all bill the provider's one
				// price, so there is nothing to weigh; expiring weighs
				// several subscriptions' free quota, a group's members
				return fmt.Errorf("routing=%s is a group's: a provider's keys or accounts are routed smart, order, rotate, usage, pace, weight, free, fast or drain", r)
			}
			p.Routing = r
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
	if p.Socket != "" && !p.TakesSocket() {
		return fmt.Errorf("socket=%s: %s has no Responses API of its own to speak a WebSocket to; only a provider with responses= and a key can", p.Socket, p.Name)
	}
	if p.NativeCompaction && !p.CarriesCompaction() {
		return fmt.Errorf("compaction=native: %s can't carry Codex's own compaction; only a provider with a Responses API (responses=), a ChatGPT account or GitHub Copilot can", p.Name)
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

// refreshLive re-fetches the model list of every ready provider not
// switched off; `magpie sync` calls it after the catalog download.
func refreshLive(ctx context.Context) {
	var names []string
	for _, l := range provider.RefreshLists(ctx) {
		names = append(names, fmt.Sprintf("%s (%d)", l.ID, l.Models))
	}
	if len(names) > 0 {
		fmt.Println(green.Render("✓"), "model lists:", strings.Join(names, ", "))
	}
}

// keyNote says who the gateway takes any key from: this machine (in a
// container, the container) alone. Shared from Settings, or put on the
// network by MAGPIE_ADDR (a server, a Docker image), it takes others with
// an enabled gateway key.
func keyNote() string {
	if settings.Load().LAN || gateway.OnNetwork() {
		if gateway.InContainer() {
			return "(anything works inside the container; from its host and other machines, an enabled gateway key — magpie gateway-key add <name>)"
		}
		return "(anything works from this machine; from others, an enabled gateway key — magpie gateway-key add <name>)"
	}
	return "(anything works; the gateway only listens on localhost)"
}

// containerNote follows the addresses magpie finds for itself in a
// container, which are the container's own: other machines reach it at
// the host's, and other containers on the host at the bridge's gateway
// (jiezhengj on Discord: 172.17.0.1 on a NAS), containerLine's.
const containerNote = "these are the container's own addresses: other machines use the host's, and MAGPIE_PUBLIC_URL=http://<host>:<port> puts it here"

// containerLine is where other containers on this host reach the gateway
// while magpie runs in one, "" when that isn't known. In a LazyCat app on
// the box's bridge network it says how the box's LAN address gets the
// port, which LazyCat publishes only to its client's devices (mtoolstec
// on X).
func containerLine() string {
	if gateway.LazyCatURL() != "" && !gateway.LazyCatBox() {
		port := gateway.Port()
		return muted.Render("  only devices with the LazyCat client reach " + gateway.LazyCatURL() + "; for the box's LAN IP on port " + port + ", forward port " + port + " to Magpie in LazyCat's LAN port forwarding app")
	}
	if u := gateway.ContainerHostURL(); u != "" {
		return muted.Render("  other containers on this host ") + u
	}
	return ""
}

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
	if l := containerLine(); l != "" {
		out = append(out, l)
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
	catalog.Seed()         // the shipped copy, when models.dev can't be reached
	go catalog.KeepFresh() // new models' prices, in a gateway left running
	// a signed-in account's list exists only at its vendor: asked for here
	// as the app and the terminal ask (#1789), or a headless gateway offered
	// the agents Cursor's fallback Auto alone; asked again while one is left
	go func() {
		for provider.FetchNew(20 * time.Second) {
			time.Sleep(time.Minute)
		}
	}()
	// Codex's background app-server, restarted when it has the list from
	// before a change and no codex session is on it
	go agent.KeepCodexDaemonCurrent(context.Background())
	go agent.KeepCodexRolloutsPortable(context.Background())
	public := advertisedURL()
	fmt.Println(green.Render("●"), "magpie gateway on", bold.Render(gateway.URL()))
	fmt.Println(muted.Render("  OpenAI  "), public+"/v1/chat/completions", muted.Render("·"), public+"/v1/responses")
	fmt.Println(muted.Render("  Anthropic"), public+"/v1/messages")
	fmt.Println(muted.Render("  key     "), gateway.Token, muted.Render(keyNote()))
	if l := tlsLine(); l != "" {
		fmt.Println(l)
	}
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
	// an update put in where this runs from is restarted into
	// (serve_follow.go)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	follow := newServeFollow(s, stop)
	if follow != nil {
		gateway.SetRestarter(follow.answer)
		go follow.run(ctx)
	}
	// another magpie using this one updated (serve_update.go), and this
	// one's own Remote magpies follow it
	gateway.SetUpdater(serveUpdateAsked(follow))
	go provider.KeepRemotesUpdated(ctx, version)
	err := s.ListenAndServe(ctx)
	if exe := follow.restartTo(); exe != "" {
		// the plugin hosts end, waited for, first, as magpie web's
		// reexec does (#3285)
		plugin.EndHosts()
		log.Printf("magpie serve: restarting into %s", exe)
		return serveReexec(exe, os.Args[1:], os.Environ())
	}
	return err
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
	if err := showProviderAsking(p, false); err != nil {
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
// alone (provider.AccountWindowCaps); with --amount too, a fixed amount of
// that window's own unit, credits or requests (provider.AccountWindowAmounts).
func accountCapCmd(rest []string) error {
	usage := fmt.Errorf("magpie provider account-cap <id> [account [percent|off]]\n       magpie provider account-cap <id> <account> --window <name> [percent|none|default]\n       magpie provider account-cap <id> <account> --window <name> --amount <n|off>")
	window, windowSet := "", false
	amount, amountSet := "", false
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		for _, f := range []struct {
			name string
			v    *string
			set  *bool
		}{{"--window", &window, &windowSet}, {"--amount", &amount, &amountSet}} {
			if v, ok := strings.CutPrefix(a, f.name+"="); ok {
				*f.v, *f.set = v, true
				rest = slices.Delete(slices.Clone(rest), i, i+1)
				i--
				break
			}
			if a == f.name {
				if i+1 >= len(rest) {
					return usage
				}
				*f.v, *f.set = rest[i+1], true
				rest = slices.Delete(slices.Clone(rest), i, i+2)
				i--
				break
			}
		}
	}
	if len(rest) < 1 || windowSet && (len(rest) < 2 || strings.TrimSpace(window) == "") || amountSet && (!windowSet || len(rest) != 2) {
		return usage
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if p.Account == nil {
		return fmt.Errorf("%s has keys, not subscription accounts with usage windows to cap", p.Name)
	}
	if amountSet {
		n, err := parseWindowAmount(amount)
		if err != nil {
			return err
		}
		if err := provider.SetWindowAmount(p.ID, rest[1], []string{window}, n); err != nil {
			return err
		}
		if p, err = provider.Find(p.ID); err != nil {
			return err
		}
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
		// by its name or the name the user gave it, as the caps are set
		r, ok := p.AccountRefOf(rest[1])
		if !ok {
			return fmt.Errorf("%s has no account %q", p.Name, rest[1])
		}
		refs = []string{r}
	}
	if len(refs) == 0 {
		fmt.Println(muted.Render(p.Name + " has no account"))
		return nil
	}
	for _, r := range refs {
		caps := p.CapsOf(r)
		if caps.All > 0 {
			fmt.Printf("%s · capped at %d%% of each usage window\n", r, caps.All)
		} else if len(caps.Windows) > 0 || len(caps.Amounts) > 0 {
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
		for _, w := range slices.Sorted(maps.Keys(caps.Amounts)) {
			fmt.Printf("  %s · capped at %s of its unit\n", w, strconv.FormatFloat(caps.Amounts[w], 'f', -1, 64))
		}
	}
	return nil
}

// parseWindowAmount reads a window's fixed amount as the CLI takes it: a
// number above 0, "300" or "1,000", or off (0) to lift it.
func parseWindowAmount(s string) (float64, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "off" || t == "none" || t == "default" || t == "0" {
		return 0, nil
	}
	n, err := strconv.ParseFloat(strings.ReplaceAll(t, ",", ""), 64)
	if err != nil || !(n > 0) || math.IsInf(n, 0) {
		return 0, fmt.Errorf("a window's amount is a number above 0, in the window's own unit, or off, not %q", s)
	}
	return n, nil
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

// accountWeightCmd shows, or sets, the weight of a subscription's accounts
// under By weight routing (provider.AccountWeights): each one's share of
// the requests, 1 when none is set.
func accountWeightCmd(rest []string) error {
	if len(rest) < 1 || len(rest) > 3 {
		return fmt.Errorf("magpie provider account-weight <id> [account [n|default]]")
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if p.Account == nil {
		return fmt.Errorf("%s has keys, not subscription accounts: a key's weight is set beside it in the app", p.Name)
	}
	if len(rest) > 2 {
		w, err := provider.ParseWeight(rest[2])
		if err != nil {
			return err
		}
		if err := provider.SetAccountWeight(p.ID, rest[1], w); err != nil {
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
			return fmt.Errorf("%s has no account %q", p.Name, rest[1])
		}
		refs = []string{ref}
	}
	if len(refs) == 0 {
		fmt.Println(muted.Render(p.Name + " has no account"))
		return nil
	}
	for _, r := range refs {
		if w := p.AccountWeight(r); w > 1 {
			fmt.Printf("%s · weight %d\n", r, w)
		} else {
			fmt.Println(r, muted.Render("· weight 1"))
		}
	}
	if p.Routing != provider.Weighted {
		fmt.Println(muted.Render("Weights count with routing=weight: magpie provider set " + p.ID + " routing=weight"))
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

// atOnceCmd shows, or sets, how many of a provider's accounts or keys take
// requests at the same time (01huadalang on Discord: three ZCode accounts
// at once got the address refused with a 405, two didn't). The others wait
// in reserve, in the list's order.
func atOnceCmd(rest []string) error {
	usage := fmt.Errorf("magpie provider at-once <id> [n|off|default]")
	if len(rest) < 1 || len(rest) > 2 {
		return usage
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		var n *int
		switch s := strings.ToLower(strings.TrimSpace(rest[1])); s {
		case "default", "plugin":
		case "off", "none", "no", "-":
			n = new(int)
		default:
			v, err := strconv.Atoi(s)
			if err != nil {
				return fmt.Errorf("accounts at once is a whole number, off or default, not %q", rest[1])
			}
			n = &v
		}
		if err := provider.SetAtOnce(p.ID, n); err != nil {
			return err
		}
		if p, err = provider.Find(p.ID); err != nil {
			return err
		}
	}
	if n := p.AtOnce(); n > 0 {
		fmt.Printf("%s · %d of its accounts or keys take requests at once, the first in the list; the others wait in reserve\n", p.Name, n)
	} else {
		fmt.Printf("%s · every account or key takes requests\n", p.Name)
	}
	return nil
}

// quotaRestCmd shows, or sets, how long a provider's keys sit out once the
// vendor says one is out of its allowance without saying when it is back
// (#1772: Ollama Cloud's weekly limit, asked again every 15 minutes).
func quotaRestCmd(rest []string) error {
	usage := fmt.Errorf("magpie provider quota-rest <id> [30m|6h|7d|until 08:00|until 00:00+08:00|default]")
	if len(rest) == 3 && (rest[1] == "until" || rest[1] == "at") {
		rest = []string{rest[0], rest[2]}
	}
	if len(rest) < 1 || len(rest) > 2 {
		return usage
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		// a time of day (#2008): until the daily allowance comes back
		if strings.Contains(rest[1], ":") {
			err = provider.SetQuotaResetAt(p.ID, rest[1])
		} else {
			var minutes int
			if minutes, err = restMinutes(rest[1]); err == nil {
				if err = provider.SetQuotaRest(p.ID, minutes); err == nil && minutes == 0 {
					err = provider.SetQuotaResetAt(p.ID, "")
				}
			}
		}
		if err != nil {
			return err
		}
		if p, err = provider.Find(p.ID); err != nil {
			return err
		}
	}
	if t, ok := provider.NextResetAt(p.QuotaResetAt, time.Now()); ok {
		fmt.Printf("%s · a key out of its allowance, with no word of when it is back, sits out until %s (next %s)\n", p.Name, p.QuotaResetAt, t.Local().Format("Jan 2 15:04"))
	} else if d := p.QuotaRestFor(); d > 0 {
		fmt.Printf("%s · a key out of its allowance, with no word of when it is back, sits out %s\n", p.Name, restWords(d))
	} else {
		fmt.Printf("%s · a key out of its allowance, with no word of when it is back, sits out 15 minutes (magpie's own)\n", p.Name)
	}
	return nil
}

// restCapCmd sets or shows the most a provider's keys and accounts sit out
// while they keep failing or being rate limited (#2008).
func restCapCmd(rest []string) error {
	if len(rest) < 1 || len(rest) > 2 {
		return fmt.Errorf("magpie provider rest-cap <id> [1m|30m|6h|1d|off|default]")
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		minutes, err := restCapMinutes(rest[1])
		if err != nil {
			return err
		}
		if err := provider.SetRestCap(p.ID, minutes); err != nil {
			return err
		}
		if p, err = provider.Find(p.ID); err != nil {
			return err
		}
	}
	if p.NoFailRest() {
		fmt.Printf("%s · a key or account that fails without saying why doesn't rest: the next request asks it again. Out of quota or credit, its key refused or its model not served still rests it; rate limited, at most 30 minutes (magpie's own)\n", p.Name)
	} else if p.RestCap > 0 {
		fmt.Printf("%s · a key or account that keeps failing sits out at most %s\n", p.Name, restWords(time.Duration(p.RestCap)*time.Minute))
	} else {
		fmt.Printf("%s · a key or account that keeps failing sits out at most 10 minutes, 30 when rate limited (magpie's own)\n", p.Name)
	}
	return nil
}

// restCapMinutes reads a rest cap as restCapCmd takes it: as restMinutes
// does, but off (or never) is provider.RestCapOff, no rest after an
// unknown failure, and a cap under a minute is turned away with what to
// set instead, as the first rest is a minute already.
func restCapMinutes(s string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "never", "no":
		return provider.RestCapOff, nil
	}
	if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil && d > 0 && d < time.Minute {
		return 0, fmt.Errorf("the shortest rest cap is 1m, which rests a minute every time, not longer; off doesn't rest after an unknown failure at all")
	}
	return restMinutes(s)
}

// restMinutes reads a rest as quotaRestCmd takes it: default (0), a
// number of minutes, a Go duration (90m, 6h) or a number of days (7d).
func restMinutes(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "default", "off", "none", "-", "0":
		return 0, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil {
			return n * 24 * 60, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < time.Minute {
		return 0, fmt.Errorf("a rest is minutes, or as 30m, 6h or 7d, or default, not %q", s)
	}
	return int(d / time.Minute), nil
}

// restWords is a rest as a person says it: 7 days, 6 hours, 90 minutes.
func restWords(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return plural(int(d/(24*time.Hour)), "day")
	case d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d/time.Minute), "minute")
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
		return nil, fmt.Errorf("that leaves no model exposed · to serve none of %s's models: magpie provider models %s none; to keep them for routing groups alone: magpie provider listed %s no", p.Name, p.ID, p.ID)
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

// refreshEveryCmd shows, or sets, how often the gateway fetches a
// provider's list by itself (#2135).
func refreshEveryCmd(rest []string) error {
	if len(rest) < 1 || len(rest) > 2 {
		return fmt.Errorf("magpie provider refresh-every <id> [30m|6h|1d|off]")
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		if err := provider.SetRefreshEvery(p.ID, rest[1]); err != nil {
			return err
		}
		if p, err = provider.Find(p.ID); err != nil {
			return err
		}
	}
	if d := p.RefreshInterval(); d > 0 {
		fmt.Printf("%s · its model list is fetched by the gateway every %s (magpie serve, or the app, running)\n", p.Name, p.RefreshSchedule())
	} else {
		fmt.Printf("%s · its model list is fetched only when asked (magpie provider refresh %s, or Fetch models)\n", p.Name, p.ID)
	}
	return nil
}

// autoIncludeCmd shows, or sets, the rules a provider's newly listed models
// are exposed by (#2135); none clears them.
func autoIncludeCmd(rest []string) error {
	if len(rest) < 1 {
		return fmt.Errorf("magpie provider auto-include <id> [rule…|none]")
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		rules := rest[1:]
		if len(rules) == 1 && (rules[0] == "none" || rules[0] == "off") {
			rules = nil
		}
		taken, err := provider.SetAutoInclude(p.ID, rules)
		if err != nil {
			return err
		}
		if len(taken) > 0 {
			fmt.Println(green.Render("+"), "exposed now, as its rules match them:", strings.Join(taken, ", "))
		}
		if p, err = provider.Find(p.ID); err != nil {
			return err
		}
	}
	if len(p.AutoInclude) == 0 {
		fmt.Printf("%s · no auto-include rules: a newly listed model is exposed as its picks say\n", p.Name)
		return nil
	}
	fmt.Printf("%s · a newly listed model is exposed when one of these matches its id: %s\n", p.Name, strings.Join(p.AutoInclude, " "))
	return nil
}

// patienceArg reads patience=: off, default, or a time up to
// provider.PatienceLongest seconds, as 90, 90s or 2m.
func patienceArg(v string) (int, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "off", "no", "none", "0":
		return provider.PatienceOff, nil
	case "default", "":
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		n, nerr := strconv.Atoi(v)
		if nerr != nil {
			return 0, fmt.Errorf("patience=off|default|<time, e.g. 2m>, not %q", v)
		}
		d = time.Duration(n) * time.Second
	}
	if d == 0 {
		return provider.PatienceOff, nil
	}
	if d < 0 || d > provider.PatienceLongest*time.Second {
		return 0, fmt.Errorf("patience is at most %s, not %q", provider.PatienceLongest*time.Second, v)
	}
	return int((d + time.Second - 1) / time.Second), nil
}

// billingWord is how a provider bills, in words (provider.BillingKind).
func billingWord(b string) string {
	switch b {
	case provider.BillingPlan:
		return "plan"
	case provider.BillingPAYG:
		return "pay as you go"
	case provider.BillingMixed:
		return "plan and pay as you go"
	}
	return b
}
