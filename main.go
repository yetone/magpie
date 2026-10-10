// magpie — one place to pick every agent's model.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/appdir"
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
  magpie tui                      the same thing, in the terminal (serves the gateway while open when no magpie does)
  magpie web [--addr host:port] [--lan] [--no-open] [--gateway]
                                  the app's window in a browser, with the gateway (no desktop needed: WSL, a server over SSH)
                                  a new key each run; MAGPIE_WEB_KEY (16+ letters, digits, - . _ ~) keeps one, signed in for 400 days
                                  behind a reverse proxy, MAGPIE_WEB_URL=https://<the page there> prints the link through it
                                  --gateway: gateway mode, no Agents, Sessions or Library (on by itself with no agents here; Settings › General turns it off)
  magpie ls                       list detected agents and their settings
  magpie <agent>                  show one agent
  magpie <agent> help             its fields, and how to set them
  magpie <agent> <model>          set an agent's model   e.g. magpie claude deepseek/deepseek-chat
  magpie <agent> <field>          show one field   e.g. magpie codex effort
  magpie <agent> <field> <value>  set another field   e.g. magpie codex effort high
  magpie <agent> default          take magpie out: the agent back on what it had before
  magpie <agent> <field> default  that field back to the agent's own default

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
  magpie visible [<agent> <family|provider|group>,… | all | --only-picked | --show-new]
                                  which models an agent is shown: families (magpie provider/group set <id> family=…),
                                  or only the models ticked for it, a new one off until it is ticked
  magpie search [add <api> <key>|rm <api>]   Tavily, Brave, Exa, Firecrawl or SearXNG for web search when no provider can search
  magpie groups                   routing groups: several models agents pick as one, group/<id>
  magpie group add <name> models=<m1>,<m2> [routing=smart|order|rotate|usage|pace] [stays=auto|session|turn|off]
  magpie group <id> | set <id> k=v… | rm <id>   show, change or remove one (magpie group help for more)
  magpie accounts [agent] [--json]  every subscription magpie knows, with each one's allowance used and when it resets
  magpie accounts add <agent>     sign in to one more Claude, ChatGPT or Google (Gemini CLI, Antigravity) subscription
  magpie accounts add copilot [--host <name>.ghe.com]   one more Copilot account, on github.com or an enterprise's GHE.com
  magpie accounts import <codex|claude|antigravity|factory> <file>... [--yes]   bring in accounts from other tools' files (Codex's auth.json, Cockpit Tools, CLIProxyAPI, Sub2API); the tool a ChatGPT or Claude file came from is signed out of it
  magpie accounts switch <agent> <email>   sign the agent in to another of them
  magpie accounts refresh         renew the saved ChatGPT sign-ins now (the gateway does it daily)
  magpie accounts checkin         WorkBuddy's daily check-in (签到) for each WorkBuddy account, now (Settings can do it daily)
  magpie accounts project <gemini|antigravity> <email> <project>   the Google Cloud project a Google account's requests go to
  magpie claude-code [install [--yes]|remove]   the Claude Code a Claude subscription runs; install downloads Anthropic's own build, checked against its manifest, for a server or container without one
  magpie plugin [add <package>|rm|update|on|off|login <provider>|logout <provider>]
                                  OpenCode provider plugins and pi packages: subscriptions signed in to, and served, through a plugin
  magpie plugin move|migrate <subscription>   run a built-in subscription's accounts on its community plugin
  magpie plugin move-back|unmigrate <subscription>   go back to the built-in, with its accounts

  magpie serve                    run the gateway alone (the app runs it too)
  magpie healthcheck              exit 0 when the gateway answers (a container's HEALTHCHECK)
  magpie gateway-key list|add <name>|rotate <id>|remove <id>   manage the keys clients use to call a shared gateway
  magpie gateway-key limit <id> [off|day|week|month --tokens N --cost USD --cache-reads]   a key's own limit, and what it used
  magpie gateway-key models <id> [all|<provider>/<model>|<provider>/* ...]   the models a key may use, every one unless it names some
  magpie mcp image                the image and video generation MCP server an agent is given from the library (stdio)
  magpie usage [today|7d|30d|all] tokens and cost per agent, model and subscription account (30d)
  magpie usage --csv [--account <name>] [today|7d|30d|all]   every request as CSV (or one account's): the model asked for, sent and served, tokens, cost, time, status, account
  magpie sessions [--model <m>] [--folder <f>] [--json]   the latest Claude Code, Codex, OpenCode and Pi sessions, with what each cost
  magpie sessions --days N|today|all [--model <m>] [--folder <f>] [--json]
                                  what every session spent, day by day, with the top models and folders (7 days)
  magpie quota [<provider>] [--json]  what is left of every subscription, plan and key balance
  magpie quota wait <provider|account> [--timeout <d>] [--quiet]
                                  block until that subscription (any of its accounts) or account has allowance again
  magpie quota history [<provider|account>] [--days N] [--json]
                                  each window's readings over time, kept 45 days
  magpie sync                     refresh the model catalog and vendor model lists
  magpie agents                   list every supported agent
  magpie update [check] [--proxy <url>] [--mirror <prefix>]
                                  install the newest release (check: only say if there is one); --proxy: an
                                  http(s):// or socks5:// proxy for it; --mirror: a GitHub download mirror put
                                  before the github.com URL (none unless given; still checked against usemagpie.ai's SHA-256)
  magpie update mirror [<prefix>|off]  the mirror every update, the app's own too, is downloaded through
  magpie update auto [on|off] [30m|1h|6h|24h]  whether the app looks for updates by itself, and how often (6h)

agents: claude (cc), codex, gemini, opencode (oc), mimocode, pi, goose, cursor, zed, copilot, crush, aside
`

var (
	bold  = lipgloss.NewStyle().Bold(true)
	muted = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8B8F98", Dark: "#7C8290"})
	faint = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#C4C7CE", Dark: "#4A4F5A"})
	green = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0F9D58", Dark: "#7EE2A8"})
)

func main() {
	// before anything reads or writes a file: no home, or a relative one,
	// would put the agents' configs and magpie's keys under the working
	// folder. What needs no file still answers (magpie version in a
	// container or a script without HOME): run makes magpie's folders first.
	ignored, err := appdir.CheckEnv()
	if err != nil {
		if len(os.Args) > 1 {
			switch os.Args[1] {
			case "-v", "--version", "version":
				fmt.Println("magpie", version)
				return
			case "-h", "--help", "help":
				fmt.Print(usage)
				return
			}
		}
		fmt.Fprintln(os.Stderr, "magpie:", err)
		os.Exit(1)
	}
	slices.Sort(ignored)
	for _, v := range ignored {
		fmt.Fprintf(os.Stderr, "magpie: ignoring %s: not an absolute path (the programs magpie starts still get it)\n", v)
	}
	if provider.TookOpenedURL(os.Args[1:]) {
		// Claude Code, signing in for magpie, handed over the page to open
		return
	}
	endProbesOnSignal()
	gateway.Version = version
	netproxy.Install()
	update.GUI = hasGUI
	err = run(os.Args[1:])
	proc.EndProbes() // a CLI still being asked something isn't left to init
	sessions.Saved() // the session index kept, for the next run
	if err != nil {
		// a command with exit codes of its own (quota wait) says which
		code := 1
		var e exitError
		if errors.As(err, &e) {
			code = e.code
		}
		if msg := err.Error(); msg != "" {
			fmt.Fprintln(os.Stderr, "magpie:", msg)
		}
		os.Exit(code)
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
	if len(args) == 2 && args[0] == agent.DryRunArg {
		// an agent disconnected on a copy of its files under a temporary
		// home, for the Agents page to show what disconnecting changes
		// (agent.DisconnectPreview)
		return agent.DryRun(args[1])
	}
	// started by an update, the magpie it replaces goes first: the moves
	// below write the files it may still be writing
	update.AwaitPredecessor()
	makeDirs()
	settings.Migrate()
	// the providers and settings read once for every agent's fields, which
	// the moves below look at (a write among them reads them again)
	release := provider.Hold()
	agent.RenameLegacy()
	agent.MoveCursorEfforts()
	agent.MoveAntigravityEfforts()
	agent.MoveOffAccountIDs()
	release()
	// a provider added, edited or removed, or a list fetched anew, reaches
	// the model lists agents keep in files of their own
	catalog.Changed = agent.SyncCatalog
	// a model Claude Code names that magpie doesn't serve goes to the one
	// it is set to use for that tier
	gateway.StandIn = agent.StandIn
	// a Codex that reaches magpie for account failover alone is handed
	// only its own models (#1385)
	gateway.CodexOwnOnly = agent.CodexOwnOnly
	// the setup kept the same on every computer, by whichever serves
	gateway.WhileServing = append(gateway.WhileServing, davsync.Run)
	// and dsh's patch lists, which dsh reads live: a route left behind by
	// something else writing the file fails every session there until
	// magpie writes its own list again
	gateway.WhileServing = append(gateway.WhileServing, agent.KeepDshWired)
	// and Cursor Private Inference's variables, which the Mac's launchd
	// forgets at a restart, for the gateway's address now
	gateway.WhileServing = append(gateway.WhileServing, agent.KeepCursorLocalEnv)
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
	// `magpie save --help` saved a profile named --help, `magpie provider
	// key deepseek -h` made -h DeepSeek's key and `magpie rm p1 help`
	// deleted p1: after a command's name, the help words were its values,
	// or ignored. Wherever they come they now show the command's usage,
	// as they do an agent's below
	if help, ok := commandHelp(args[0]); ok && helpAsked(args) {
		return help()
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
	case "claude-code":
		return claudeCodeCmd(args[1:])
	case "claude-mcp-helper": // internal: stdio MCP subprocess spawned by Claude Code
		return claudebridge.RunMCP(args[1:])
	}

	a, err := agent.Find(args[0])
	if err != nil {
		return err
	}
	// `magpie codex --help` asks how, and a word that starts with "-" is a
	// flag: no field takes either, and both were written into the agent's
	// config as its model (model = "--help"), magpie taken out first. Ahead
	// of Cindy's link too, which any word opened
	for _, v := range args[1:] {
		if isHelp(v) {
			fmt.Print(agentUsage(a))
			return nil
		}
	}
	for _, v := range args[1:] {
		if strings.HasPrefix(v, "-") {
			return fmt.Errorf("unknown flag %s (magpie %s help)", v, a.ID)
		}
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
		// default is magpie's word in any case, as help and a field's name
		// are: magpie codex DEFAULT wrote model = "DEFAULT"
		if strings.EqualFold(args[1], "default") {
			if a.Wired() {
				return disconnect(a)
			}
			return set(a, a.Fields[0].Key, "")
		}
		// `magpie codex model`: a field's name alone asks what it is set to,
		// where it was set as the model (model = "model"). Claude Code's
		// opus is a model's name as well as a tier's, and stays the model
		if f := a.Field(args[1]); f != nil && !modelAlias(a, args[1]) {
			return showField(a, f)
		}
		// `magpie codex xhigh`: a bare value that belongs to a non-model field
		// (effort levels, for instance) is routed there; anything else is a model.
		if f := fieldForValue(a, args[1]); f != nil {
			return set(a, f.Key, args[1])
		}
		return set(a, a.Fields[0].Key, args[1])
	case 3:
		value := args[2]
		if strings.EqualFold(value, "default") {
			value = ""
		}
		return set(a, args[1], value)
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
	// a field's name is no value: magpie codex subagent effort ran Codex's
	// subagents on a model called effort. Claude Code's opus is a model's
	// name as well as a tier's
	if g := a.Field(value); value != "" && g != nil && !modelAlias(a, value) {
		return fieldAsValue(a, f, g, value)
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

// modelAlias says whether a word names the agent's model, in any case,
// though a field goes by it too: Claude Code's opus, a tier's name as well
func modelAlias(a *agent.Agent, w string) bool {
	return slices.ContainsFunc(a.ModelAliases, func(m string) bool { return strings.EqualFold(m, w) })
}

// fieldAsValue refuses field g's name given as field f's value, for what
// was meant: two words that are one field's label (magpie codex subagent
// effort), a switch, which is turned on (Claude Code's ultracode), or the
// field, which its name alone shows.
func fieldAsValue(a *agent.Agent, f, g *agent.Field, value string) error {
	for _, name := range []string{f.Key + " " + value, f.Key + "_" + value} {
		if h := a.Field(name); h != nil {
			return fmt.Errorf("%q is one field of %s's: magpie %s %s shows it", h.Label, a.Name, a.ID, h.Key)
		}
	}
	if g.Options != nil {
		if o := g.Options(a.Values()); len(o) == 1 && o[0].Value == "on" {
			return fmt.Errorf("%q is a switch of %s's, not a value for its %s: magpie %s %s on", value, a.Name, f.Label, a.ID, g.Key)
		}
	}
	return fmt.Errorf("%q is a field of %s's, not a value for its %s: magpie %s %s shows it", value, a.Name, f.Label, a.ID, g.Key)
}

// showField is `magpie <agent> <field>`: what the field is set to, as set
// prints it
func showField(a *agent.Agent, f *agent.Field) error {
	v := f.Get()
	if v == "" {
		v = muted.Render("default")
	}
	fmt.Println(bold.Render(a.Name), muted.Render(f.Label), v)
	return nil
}

// disconnect is `magpie <agent> default` on an agent magpie is wired into:
// the Agents page's Disconnect, which puts back what the user had before
// magpie — Claude Code's own model, its endpoint — where a field's default
// leaves the agent as installed (__jingling on X: magpie claude default
// took the model they had set away with magpie's)
func disconnect(a *agent.Agent) error {
	before := a.Values()
	if err := a.Disconnect(); err != nil {
		return err
	}
	now := a.Values()
	for _, f := range a.Fields {
		if before[f.Key] == now[f.Key] {
			continue
		}
		shown := now[f.Key]
		if shown == "" {
			shown = muted.Render("default")
		}
		fmt.Println(green.Render("✓"), bold.Render(a.Name), muted.Render(f.Label), shown)
	}
	fmt.Println(muted.Render("  disconnected from magpie, back to what it had before"))
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

// isHelp says whether a word asks how: help, -h or --help, in any case
// (magpie codex Help wrote model = "Help")
func isHelp(w string) bool {
	switch strings.ToLower(w) {
	case "help", "-h", "--help":
		return true
	}
	return false
}

// helpAsked says whether a help word follows the command args[0] names.
// What follows an MCP server's command in magpie library mcp add <name>
// <command> [args…] is that server's own, -h too.
func helpAsked(args []string) bool {
	rest := args[1:]
	if (args[0] == "library" || args[0] == "lib") && len(args) > 5 && args[1] == "mcp" && args[2] == "add" {
		rest = args[1:5]
	}
	return slices.ContainsFunc(rest, isHelp)
}

// commandHelp is how a command of magpie's shows its usage: its own help
// where it has one, else its lines of magpie help. Not an agent's
// (agentUsage), nor what only magpie or a container runs
// (claude-mcp-helper, -Embedding, healthcheck).
func commandHelp(cmd string) (func() error, bool) {
	lines := func(words ...string) func() error {
		return func() error {
			fmt.Print(usageOf(words...))
			return nil
		}
	}
	switch cmd {
	case "group":
		return func() error { return groupCmd([]string{cmd, "help"}) }, true
	case "library", "lib":
		return func() error { return libraryCmd([]string{cmd, "help"}) }, true
	case "model":
		return func() error { return modelCmd([]string{"help"}) }, true
	case "plugin", "plugins":
		return func() error { return pluginCmd([]string{cmd, "help"}) }, true
	case "quota", "quotas":
		return func() error { return quotaCmd([]string{cmd, "help"}) }, true
	case "search":
		return func() error { return searchCmd([]string{"help"}) }, true
	case "webdav", "dav":
		return func() error { return webdavCmd([]string{"help"}) }, true
	case "s3":
		return func() error { return s3Cmd([]string{"help"}) }, true
	case "sessions":
		return func() error {
			fmt.Println(sessionsUsage)
			return nil
		}, true
	case "save", "use", "rm", "profiles":
		return lines("save", "use", "profiles", "rm"), true
	case "ls", "list":
		return lines("ls"), true
	case "accounts", "account":
		return lines("accounts"), true
	case "app", "gui":
		return lines(), true
	case "tui", "web", "tray", "panel", "autostart", "agents", "sync", "import", "providers", "presets",
		"provider", "models", "visible", "groups", "serve", "gateway-key", "usage", "update", "backup",
		"restore", "mcp":
		return lines(cmd), true
	}
	return nil, false
}

// usageOf is the lines of magpie help for the commands words names, a line
// that wraps with the line it wraps from; all of it for none.
func usageOf(words ...string) string {
	var b strings.Builder
	in := false
	for _, l := range strings.Split(usage, "\n") {
		t := strings.TrimLeft(l, " ")
		if f := strings.Fields(t); strings.HasPrefix(t, "magpie ") && len(f) > 1 {
			in = slices.Contains(words, f[1])
		} else if !strings.HasPrefix(l, "      ") {
			in = false
		}
		if in {
			b.WriteString(l + "\n")
		}
	}
	if b.Len() == 0 {
		return usage
	}
	return b.String()
}

// agentUsage is `magpie <agent> help`: the commands for that agent, and
// the names its fields go by, the key and the label it is shown with
func agentUsage(a *agent.Agent) string {
	at := "magpie " + a.ID
	rows := [][2]string{{at, "show " + a.Name}}
	if len(a.Fields) == 0 {
		// Cindy: it takes magpie through its own link, confirmed there
		rows = append(rows, [2]string{at + " add", "open the link that adds magpie, to confirm it in " + a.Name})
	} else {
		first := a.Fields[0].Label
		rows = append(rows,
			[2]string{at + " <" + first + ">", "set its " + first},
			[2]string{at + " <field>", "show one field"},
			[2]string{at + " <field> <value>", "set a field"},
			[2]string{at + " default", "take magpie out: " + a.Name + " back on what it had before"},
			[2]string{at + " <field> default", "that field back to " + a.Name + "'s own default"})
	}
	w := 0
	for _, r := range rows {
		w = max(w, lipgloss.Width(r[0]))
	}
	var b strings.Builder
	b.WriteString("usage:\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "  %s  %s\n", pad(r[0], w), r[1])
	}
	if len(a.Fields) == 0 {
		return b.String()
	}
	b.WriteString("\n")
	line := "  fields:"
	for i, f := range a.Fields {
		name := f.Key
		if f.Label != f.Key {
			name += " (" + f.Label + ")"
		}
		if i < len(a.Fields)-1 {
			name += ","
		}
		// Claude Code's thirteen wrap, under the first
		if i > 0 && lipgloss.Width(line)+1+lipgloss.Width(name) > 100 {
			b.WriteString(line + "\n")
			line = "         "
		}
		line += " " + name
	}
	b.WriteString(line + "\n")
	// Claude Code's tiers are named as its model's aliases are
	var both []string
	for _, f := range a.Fields {
		if slices.Contains(a.ModelAliases, f.Key) {
			both = append(both, f.Key)
		}
	}
	if n := len(both); n > 0 {
		names := both[0]
		if n > 1 {
			names = strings.Join(both[:n-1], ", ") + " and " + both[n-1]
		}
		fmt.Fprintf(&b, "  %s alone name the %s: %s %s sets its %s to %s\n", names, a.Fields[0].Label, at, both[0], a.Fields[0].Label, both[0])
	}
	return b.String()
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
			// through magpie for account failover while not connected (#1385)
			if said := a.FailoverSaid(); said != "" {
				r.vals += label.Render("  ·  " + said)
			}
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
