// magpie — one place to pick every agent's model.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/claudebridge"
	"github.com/yetone/magpie/internal/davsync"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/imagemcp"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/tui"
	"github.com/yetone/magpie/internal/update"
)

var version = "dev"

const usage = `magpie — one place to pick every agent's model

  magpie                          open the app: a window plus a menu bar icon
  magpie tray                     start in the menu bar only
  magpie panel                    open the menu bar icon's quick panel, or close it
  magpie autostart [on|off]       open magpie (in the menu bar) when you log in, or say whether it does
  magpie tui                      the same thing, in the terminal
  magpie web [--addr host:port] [--lan] [--no-open]
                                  the app's window in a browser, with the gateway (no desktop needed: WSL, a server over SSH)
                                  a new key each run; MAGPIE_WEB_KEY (16+ letters, digits, - . _ ~) keeps one, signed in for 400 days
  magpie ls                       list detected agents and their settings
  magpie <agent>                  show one agent
  magpie <agent> <model>          set an agent's model   e.g. magpie claude deepseek/deepseek-chat
  magpie <agent> <field> <value>  set another field   e.g. magpie codex effort high
  magpie <agent> [field] default  back to the agent's own default, magpie's wiring removed

  magpie save <name>              snapshot every agent's settings as a profile
  magpie use <name>               apply a profile
  magpie profiles                 list profiles
  magpie rm <name>                delete a profile

  magpie backup [--no-keys] [--no-library] [file]    providers, keys, settings, profiles, agent models and the library in one file, sealed with a passphrase
  magpie restore [--no-agents] [--no-library] <file> put a backup in on this machine
  magpie webdav [on <address>|set k=v…|now|off]      the same, kept the same on every computer through a WebDAV folder (magpie webdav help)
  magpie s3 [on s3://<bucket>[/<prefix>]|set k=v…|now|off]   the same through an S3-compatible bucket: AWS, R2, B2, MinIO… (magpie s3 help)

  magpie library [sync|instructions|mcp|skill]   the instructions, MCP servers and skills written into every agent (magpie library help)

  magpie providers                list your providers: host, key, models, who uses them
  magpie presets                  the vendors magpie knows: add one with just a key
  magpie provider add <preset> <key>   e.g. magpie provider add deepseek sk-…
  magpie provider add <name> k=v…      a custom vendor (magpie provider for the fields)
  magpie provider key|models|test|rm <id>
  magpie provider fallback <id> <provider/model>…   use these when it's out of quota or down
  magpie import [-y] <link>       add the provider a magpie://import?… link describes
  magpie models [<agent>]         every model agents can pick, as provider/model; an agent's, and why others aren't
  magpie model name <provider/model> <name>|--reset       the name a model goes by, everywhere
  magpie model efforts <provider/model> <l>,<l>|--reset   the reasoning levels a model offers (magpie model help)
  magpie visible [<agent> <family|provider|group>,… | all]
                                  which models an agent is shown: families (magpie provider/group set <id> family=…)
  magpie search [add <api> <key>|rm <api>]   Tavily, Brave, Exa, Firecrawl or SearXNG for web search when no provider can search
  magpie groups                   routing groups: several models agents pick as one, group/<id>
  magpie group add <name> models=<m1>,<m2> [routing=smart|order|rotate|usage] [stays=auto|session|turn|off]
  magpie group <id> | set <id> k=v… | rm <id>   show, change or remove one (magpie group help for more)
  magpie accounts [agent] [--json]  every subscription magpie knows, with each one's allowance used and when it resets
  magpie accounts add <agent>     sign in to one more Claude, ChatGPT or Google (Gemini CLI, Antigravity) subscription
  magpie accounts switch <agent> <email>   sign the agent in to another of them
  magpie accounts refresh         renew the saved ChatGPT sign-ins now (the gateway does it daily)
  magpie accounts checkin         WorkBuddy's daily check-in (签到) for each WorkBuddy account, now (Settings can do it daily)
  magpie accounts project <gemini|antigravity> <email> <project>   the Google Cloud project a Google account's requests go to
  magpie plugin [add <package>|rm|update|on|off|login <provider>|logout <provider>]
                                  OpenCode provider plugins: subscriptions signed in to, and served, through a plugin
  magpie plugin move|migrate <subscription>   run a built-in subscription's accounts on its community plugin
  magpie plugin move-back|unmigrate <subscription>   go back to the built-in, with its accounts

  magpie serve                    run the gateway alone (the app runs it too)
  magpie healthcheck              exit 0 when the gateway answers (a container's HEALTHCHECK)
  magpie gateway-key list|add <name>|rotate <id>|remove <id>   manage the keys clients use to call a shared gateway
  magpie mcp image                the image and video generation MCP server an agent is given from the library (stdio)
  magpie usage [today|7d|30d|all] tokens and cost per agent and model (30d)
  magpie usage --csv [today|7d|30d|all]   every request as CSV: the model asked for, sent and served, tokens, cost, time, status
  magpie sessions [--model <m>] [--folder <f>] [--json]   the latest Claude Code, Codex, OpenCode and Pi sessions, with what each cost
  magpie sessions --days N|today|all [--model <m>] [--folder <f>] [--json]
                                  what every session spent, day by day, with the top models and folders (7 days)
  magpie quota [<provider>] [--json]  what is left of every subscription, plan and key balance
  magpie sync                     refresh the model catalog and vendor model lists
  magpie agents                   list every supported agent
  magpie update [check]           install the newest release (check: only say if there is one)
  magpie update auto [on|off] [30m|1h|6h|24h]  whether the app looks for updates by itself, and how often (6h)

agents: claude (cc), codex, gemini, opencode (oc), mimocode, pi, goose, cursor, copilot, crush
`

var (
	bold  = lipgloss.NewStyle().Bold(true)
	muted = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8B8F98", Dark: "#7C8290"})
	faint = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#C4C7CE", Dark: "#4A4F5A"})
	green = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0F9D58", Dark: "#7EE2A8"})
)

func main() {
	if provider.TookOpenedURL(os.Args[1:]) {
		// Claude Code, signing in for magpie, handed over the page to open
		return
	}
	endProbesOnSignal()
	gateway.Version = version
	netproxy.Install()
	update.GUI = hasGUI
	err := run(os.Args[1:])
	proc.EndProbes() // a CLI still being asked something isn't left to init
	sessions.Saved() // the session index kept, for the next run
	if err != nil {
		fmt.Fprintln(os.Stderr, "magpie:", err)
		os.Exit(1)
	}
}

// runTUI runs the TUI, which quits on Ctrl+C and SIGTERM itself once it
// has started; it asks CLIs first, and a signal then ends those.
func runTUI() error {
	return tuiRun(ownSignals)
}

// tuiRun is tui.Run; a var so tests can stand in for it.
var tuiRun = tui.Run

func run(args []string) error {
	if len(args) > 0 && args[0] == "healthcheck" {
		return healthcheck() // every few seconds in a container: nothing else
	}
	makeDirs()
	settings.Migrate()
	agent.RenameLegacy()
	agent.MoveCursorEfforts()
	agent.MoveAntigravityEfforts()
	agent.MoveOffAccountIDs()
	// a provider added, edited or removed, or a list fetched anew, reaches
	// the model lists agents keep in files of their own
	catalog.Changed = agent.SyncCatalog
	// a model Claude Code names that magpie doesn't serve goes to the one
	// it is set to use for that tier
	gateway.StandIn = agent.StandIn
	// the setup kept the same on every computer, by whichever serves
	gateway.WhileServing = append(gateway.WhileServing, davsync.Run)
	// and the request archive, when it is on, goes to the bucket sync is to
	gateway.ArchiveBucket = func() (gateway.Putter, bool) {
		if b, ok := davsync.S3Bucket(); ok {
			return b, true
		}
		return nil, false
	}
	if len(args) == 0 {
		if hasGUI {
			return runGUI(true, "")
		}
		return runTUI()
	}
	// a magpie:// link the system handed over (Windows, Linux): the app
	// opens it for the user to confirm
	if strings.HasPrefix(strings.ToLower(args[0]), "magpie:") {
		if !hasGUI {
			return importCmd(args)
		}
		return runGUI(false, args[0])
	}
	switch args[0] {
	case "tui":
		return runTUI()
	case "web":
		return webCmd(args[1:])
	case "app", "gui":
		// `magpie gui settings`: the window on that tab, as a restart to
		// update from it comes back (update.RelaunchArgs)
		if len(args) > 1 {
			return runWindow(args[1])
		}
		return runGUI(true, "")
	case "tray":
		return runGUI(false, "")
	case "-Embedding":
		// Windows starting magpie for a click on one of its notifications
		// (a usage alert, #368) left in the Action Center after it quit:
		// the window, on the Usage page
		return runWindow("usage")
	case "panel":
		return runPanel()
	case "autostart":
		return autostartCmd(args[1:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	case "-v", "--version", "version":
		fmt.Println("magpie", version)
		return nil
	case "ls", "list":
		// in the order the app lists them; those hidden there come last, dimmed
		shown, hidden := settings.Arrange(settings.Load(), agent.Detected(), func(a *agent.Agent) string { return a.ID })
		return list(append(shown, hidden...), true, len(shown))
	case "agents":
		return list(agent.All(), false, -1)
	case "sync":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := catalog.Sync(ctx); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "catalog saved to", catalog.CachePath())
		refreshLive(ctx)
		return nil
	case "save", "use", "rm", "profiles":
		return profiles(args)
	case "import":
		return importCmd(args[1:])
	case "providers":
		return providers()
	case "presets":
		return presets()
	case "provider":
		return providerCmd(args)
	case "models":
		return models(args[1:])
	case "model":
		return modelCmd(args[1:])
	case "visible":
		return visibleCmd(args[1:])
	case "search":
		return searchCmd(args[1:])
	case "groups":
		return groups()
	case "group":
		return groupCmd(args)
	case "serve":
		return serve()
	case "gateway-key":
		return gatewayKeys(args)
	case "accounts", "account":
		return accountsCmd(args)
	case "usage":
		return usageCmd(args)
	case "sessions":
		return sessionsCmd(args)
	case "quota", "quotas":
		return quotaCmd(args)
	case "update":
		return updateCmd(args)
	case "library", "lib":
		return libraryCmd(args)
	case "backup":
		return backupCmd(args[1:])
	case "restore":
		return restoreCmd(args[1:])
	case "webdav", "dav":
		return webdavCmd(args[1:])
	case "plugin", "plugins":
		return pluginCmd(args)
	case "s3":
		return s3Cmd(args[1:])
	case "mcp":
		return imagemcp.Run(args[1:])
	case "claude-mcp-helper": // internal: stdio MCP subprocess spawned by Claude Code
		return claudebridge.RunMCP(args[1:])
	}

	a, err := agent.Find(args[0])
	if err != nil {
		return err
	}
	if len(a.Fields) == 0 && a.Import != nil {
		// `magpie cindy`: it takes magpie through its own link, confirmed there
		if len(args) > 1 {
			link := a.Import()
			openInBrowser(link)
			fmt.Println(green.Render("✓"), bold.Render(a.Name), muted.Render("opened to add magpie — confirm it there"))
			fmt.Println(muted.Render("  " + link))
			return nil
		}
	}
	switch len(args) {
	case 1:
		return list([]*agent.Agent{a}, true, -1)
	case 2:
		if args[1] == "default" {
			return set(a, a.Fields[0].Key, "")
		}
		// `magpie codex xhigh`: a bare value that belongs to a non-model field
		// (effort levels, for instance) is routed there; anything else is a model.
		if f := fieldForValue(a, args[1]); f != nil {
			return set(a, f.Key, args[1])
		}
		return set(a, a.Fields[0].Key, args[1])
	case 3:
		if args[2] == "default" {
			args[2] = ""
		}
		return set(a, args[1], args[2])
	}
	return fmt.Errorf("too many arguments\n\n%s", usage)
}

func set(a *agent.Agent, key, value string) error {
	f := a.Field(key)
	if f == nil {
		var keys []string
		for _, f := range a.Fields {
			keys = append(keys, f.Key)
		}
		return fmt.Errorf("%s has no field %q (fields: %s)", a.Name, key, strings.Join(keys, ", "))
	}
	value, err := a.Spell(f.Key, value)
	if err != nil {
		return err
	}
	before := f.Get()
	if err := a.Apply(f.Key, value); err != nil {
		return err
	}
	// what the config reads now, not what was asked: an agent may name the
	// model under a provider of its own (OpenCode's magpie-relay/…), and a
	// value it had already is said to be so
	now := f.Get()
	shown := now
	if value == "" || now == "" {
		shown = muted.Render("default")
	}
	if now == before {
		shown += " " + muted.Render("(unchanged)")
	}
	fmt.Println(green.Render("✓"), bold.Render(a.Name), muted.Render(f.Label), shown)
	if a.Notice != nil {
		if n := a.Notice(); n != "" {
			fmt.Println(muted.Render("  ↻ " + n))
		}
	}
	return nil
}

func fieldForValue(a *agent.Agent, v string) *agent.Field {
	vals := a.Values()
	// the agent's suffix after a model (omp's ":max") aside: a role on the
	// same model at that level offers it as typed, and would take it. A list
	// of models no picker offers: it is for the model
	if a.SplitSuffix != nil {
		m, _, one := a.SplitSuffix(v)
		if !one {
			return nil
		}
		v = m
	}
	// a model stays with the model, even where other fields offer it too
	// (Claude Code's opus/sonnet/haiku/fable)
	for _, o := range a.Fields[0].Options(vals) {
		if o.Value == v {
			return nil
		}
	}
	for i := 1; i < len(a.Fields); i++ {
		for _, o := range a.Fields[i].Options(vals) {
			if o.Value == v {
				return &a.Fields[i]
			}
		}
	}
	return nil
}

// list prints the agents; those from dimFrom on (when not -1) are the ones
// hidden in the app, and are dimmed.
func list(agents []*agent.Agent, detectedOnly bool, dimFrom int) error {
	if len(agents) == 0 {
		return fmt.Errorf("no supported agents found on this machine")
	}
	type row struct{ name, vals, path string }
	var rows []row
	nameW, valW := 0, 0
	for i, a := range agents {
		r := row{name: a.Name, path: tilde(a.Path)}
		if !detectedOnly && !a.Detected() {
			r.name = faint.Render(a.Name)
			r.vals = faint.Render("not detected")
			r.path = ""
		} else {
			vals := a.Values()
			dim := dimFrom >= 0 && i >= dimFrom
			label, value := muted, lipgloss.NewStyle()
			if dim {
				label, value = faint, faint
			}
			var parts []string
			for _, f := range a.Fields {
				v := vals[f.Key]
				if v == "" && f.Quiet {
					continue
				}
				if v == "" {
					v = faint.Render("—")
				} else {
					v = value.Render(v)
				}
				if f.Label == "model" {
					parts = append(parts, v)
				} else {
					parts = append(parts, label.Render(f.Label)+" "+v)
				}
			}
			r.name = bold.Render(a.Name)
			if dim {
				r.name = faint.Render(a.Name) + " " + faint.Render("hidden")
			}
			r.vals = strings.Join(parts, label.Render("  ·  "))
			if a.Import != nil {
				if a.Added != nil && a.Added() {
					r.vals = value.Render("magpie added")
				} else {
					r.vals = label.Render("magpie "+a.ID+" add") + faint.Render("  to add magpie")
				}
			}
		}
		nameW = max(nameW, lipgloss.Width(r.name))
		valW = max(valW, lipgloss.Width(r.vals))
		rows = append(rows, r)
	}
	for _, r := range rows {
		fmt.Printf("  %s  %s  %s\n", pad(r.name, nameW), pad(r.vals, valW), faint.Render(r.path))
	}
	return nil
}

func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func profiles(args []string) error {
	switch args[0] {
	case "profiles":
		ps, err := profile.Load()
		if err != nil {
			return err
		}
		if len(ps) == 0 {
			fmt.Println(muted.Render("no profiles yet · magpie save <name>"))
			return nil
		}
		for _, n := range profile.Names(ps) {
			fmt.Printf("  %s  %s\n", bold.Render(n), muted.Render(profile.LongSummary(ps[n])))
		}
		return nil
	case "save":
		if len(args) < 2 {
			return fmt.Errorf("usage: magpie save <name>")
		}
		p, err := profile.Snapshot()
		if err != nil {
			return err
		}
		if err := profile.Save(args[1], p); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "saved profile", bold.Render(args[1]))
		if p.Library != nil {
			fmt.Println(" ", muted.Render("with the "+p.Library.Summary()))
		}
		return nil
	case "use":
		if len(args) < 2 {
			return fmt.Errorf("usage: magpie use <name>")
		}
		ps, err := profile.Load()
		if err != nil {
			return err
		}
		p, ok := ps[args[1]]
		if !ok {
			return fmt.Errorf("no profile named %q", args[1])
		}
		a, err := profile.Apply(p)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "applied", bold.Render(args[1]), muted.Render(fmt.Sprintf("(%d changed)", a.Changed)))
		for _, line := range profile.Report(a) {
			fmt.Println(" ", muted.Render(line))
		}
		return nil
	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: magpie rm <name>")
		}
		if err := profile.Delete(args[1]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "deleted profile", bold.Render(args[1]))
		return nil
	}
	return nil
}

func pad(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}
