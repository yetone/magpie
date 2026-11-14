package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/middleware"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/pluginrt"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"golang.org/x/term"
)

const pluginUsage = `usage: magpie plugin [list] [--json]
       magpie plugin add <name | npm | git | path> install a community plugin by its name (workbuddy, think-tags, Qoder), an OpenCode provider plugin, a pi package or a gateway hook/middleware (opencode-gemini-auth, pi-antigravity, github:owner/repo, ./my-plugin.js, ./alias.middleware.js, ./recall.hooks.ts); npm:<name> is npm's package of a name a community plugin goes by
       magpie plugin rm <name>                     remove one
       magpie plugin update                        install the newest version of each
       magpie plugin on|off <name>                 turn one on or off
       magpie plugin options <name> [<json> | off] show or set what a plugin is handed: the switches it declares for its provider (as its editor's Plugin options), else a middleware's ctx.options
       magpie plugin use <name> <provider>         have this plugin serve a provider another plugin signs in to as well
       magpie plugin login <provider> [<method>]   sign in to a provider a plugin adds
       magpie plugin logout <provider>             forget the sign-in
       magpie plugin move|migrate <subscription>   run a built-in subscription's accounts on its community plugin
       magpie plugin builtin [on|off <id>]         list magpie's built-in plugins (Sessions, Library, routing groups…), or turn one on or off
       magpie plugin preset essentials|all         start the built-in plugins from the Essentials preset, or from all of them
       magpie plugin registry [<url> | default]   show the npm registry plugins are installed from and npm is asked of, and where it comes from; or set one (an http:// or https:// address) before your .npmrc's, .bunfig.toml's and the 国内镜像 switch's, default to drop it
       magpie plugin dump [--json]                 what plugins.yml comes to: each built-in plugin, its config, and the layer each value is from`

// pluginCmd: `magpie plugin …` — OpenCode's provider plugins and pi's
// packages, which sign in to a subscription and carry its requests
// (internal/plugin), and gateway middleware (internal/middleware).
func pluginCmd(args []string) error {
	sub := "list"
	if len(args) > 1 {
		sub = args[1]
	}
	rest := args[min(len(args), 2):]
	ctx, stop := interruptContext()
	defer stop()
	switch sub {
	case "list", "ls", "--json":
		return listPlugins(ctx, sub == "--json" || len(rest) > 0 && rest[0] == "--json")
	case "add", "install":
		if len(rest) != 1 {
			return errors.New(pluginUsage)
		}
		if !plugin.IsPath(rest[0]) && !plugin.HasBun() {
			fmt.Println(muted.Render("Downloading Bun " + plugin.BunInUse() + ", which plugins run on…"))
		}
		e, err := provider.AddPlugin(ctx, rest[0])
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "added", e.Spec)
		// a community plugin's name (workbuddy, WorkBuddy) stood for it
		if typed := strings.TrimSpace(rest[0]); !plugin.IsPath(typed) && !plugin.IsGit(typed) && !strings.HasPrefix(typed, "npm:") && plugin.Name(typed) != plugin.Name(e.Spec) {
			fmt.Println(faint.Render("  " + strconv.Quote(plugin.Name(typed)) + " is magpie's community plugin " + plugin.Name(e.Spec)))
		}
		// a deprecated built-in it serves, not signed in to, is its now
		provider.HandOver(ctx, false)
		return listPlugins(ctx, false)
	case "rm", "remove", "uninstall":
		if len(rest) != 1 {
			return errors.New(pluginUsage)
		}
		rest[0] = installedName(rest[0])
		moved := provider.MovedOnto(rest[0])
		wired := wiredPluginAgent(rest[0])
		if err := provider.RemovePlugin(ctx, rest[0]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "removed", rest[0])
		sayAgentBack(wired)
		for _, id := range moved {
			fmt.Println(faint.Render("  " + id + " is off until the plugin is added again; its accounts are kept"))
		}
		return nil
	case "update", "upgrade":
		if err := plugin.Update(ctx); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "plugins updated")
		provider.HandOver(ctx, false)
		return listPlugins(ctx, false)
	case "on", "off":
		if len(rest) != 1 {
			return errors.New(pluginUsage)
		}
		rest[0] = installedName(rest[0])
		var wired *agent.Agent
		if sub == "off" {
			wired = wiredPluginAgent(rest[0])
		}
		if err := provider.SetPluginOff(ctx, rest[0], sub == "off"); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), rest[0], "is", sub)
		sayAgentBack(wired)
		if sub == "off" {
			for _, id := range provider.MovedOnto(rest[0]) {
				fmt.Println(faint.Render("  " + id + " is off with it; its accounts are kept"))
			}
		}
		return nil
	case "options", "config":
		if len(rest) < 1 || len(rest) > 2 {
			return errors.New(pluginUsage)
		}
		return pluginOptions(ctx, rest[0], rest[1:])
	case "use", "prefer":
		if len(rest) != 2 {
			return errors.New(pluginUsage)
		}
		rest[0] = installedName(rest[0])
		if err := plugin.Prefer(rest[0], rest[1]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), rest[0], "serves", rest[1])
		return listPlugins(ctx, false)
	case "login", "signin":
		if len(rest) < 1 || len(rest) > 2 {
			return errors.New(pluginUsage)
		}
		method := ""
		if len(rest) == 2 {
			method = rest[1]
		}
		return pluginLogin(ctx, rest[0], method)
	case "logout", "signout":
		if len(rest) != 1 {
			return errors.New(pluginUsage)
		}
		pp, err := pluginProvider(ctx, rest[0])
		if err != nil {
			return err
		}
		if err := plugin.SignOut(ctx, pp.ID, ""); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "signed out of", pp.Name)
		return nil
	case "move", "migrate":
		if len(rest) != 1 {
			return errors.New(pluginUsage)
		}
		if !provider.Movable(rest[0]) {
			return fmt.Errorf("%s has no plugin to move to", rest[0])
		}
		if !plugin.HasBun() {
			fmt.Println(muted.Render("Downloading Bun " + plugin.BunInUse() + ", which plugins run on…"))
		}
		if err := provider.Move(ctx, rest[0]); err != nil {
			return fmt.Errorf("%s stays built-in: %w", rest[0], err)
		}
		fmt.Println(green.Render("✓"), rest[0], "runs on", provider.MovePackage(rest[0]))
		return nil
	case "move-back", "moveback", "unmigrate":
		// the built-ins are retired: there is no going back to one
		return errors.New("magpie's built-in subscriptions are retired: their community plugins run them")
	case "builtin", "builtins", "built-in":
		return builtinCmd(rest)
	case "preset":
		if len(rest) != 1 {
			return errors.New(pluginUsage)
		}
		if err := pluginrt.SetPreset(rest[0]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "built-in plugins start from", rest[0])
		return builtinCmd(nil)
	case "registry":
		if len(rest) > 1 {
			return errors.New(pluginUsage)
		}
		if len(rest) == 1 && strings.HasPrefix(rest[0], "-") {
			fmt.Println(pluginUsage)
			return nil
		}
		r := plugin.Registry()
		if len(rest) == 1 {
			var err error
			if r, err = plugin.SetRegistry(rest[0]); err != nil {
				return err
			}
			fmt.Print(green.Render("✓"), " ")
		}
		fmt.Println(r.URL, muted.Render("("+registryFrom(r)+")"))
		return nil
	case "dump":
		return dumpPlugins(len(rest) > 0 && rest[0] == "--json")
	case "help", "-h", "--help":
		fmt.Println(pluginUsage)
		return nil
	}
	return errors.New(pluginUsage)
}

func listPlugins(ctx context.Context, asJSON bool) error {
	l := plugin.Load()
	if len(l.Plugins) == 0 {
		if asJSON {
			fmt.Println("[]")
			return nil
		}
		fmt.Println("No plugins. Add one: magpie plugin add <npm package | git repo | path>")
		return nil
	}
	loaded, lerr := plugin.Plugins(ctx)
	errs := map[string]string{}
	for _, p := range loaded {
		errs[p.Spec] = p.Error
	}
	clashes := plugin.Clashes(loaded)
	ps, perr := plugin.Providers(ctx)
	mws := middleware.States()
	var pts []plugin.PluginTools
	for _, p := range loaded {
		if len(p.Tools) > 0 {
			pts = append(pts, plugin.PluginTools{Spec: p.Spec, Tools: p.Tools})
		}
	}
	toolNames := gateway.ToolNames(pts)
	if asJSON {
		b, _ := json.MarshalIndent(map[string]any{"plugins": l.Plugins, "loaded": loaded, "providers": ps, "middleware": mws}, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	// a built-in with accounts the plugin could run: not signed in to the
	// plugin is its normal state, not something to fix
	onBuiltin := map[string]provider.MoveCandidate{}
	for _, c := range provider.MoveCandidates() {
		onBuiltin[c.ID] = c
	}
	for _, e := range l.Plugins {
		state := green.Render("on")
		switch {
		case e.Off:
			state = muted.Render("off")
		case errs[e.Spec] != "":
			state = "failed: " + errs[e.Spec]
		}
		what := bold.Render(e.Spec)
		// a git repository's package is named by its own package.json
		if n := plugin.Name(e.Spec); plugin.IsGit(e.Spec) && n != e.Spec {
			what += " " + muted.Render("("+strings.TrimSpace(n+" "+plugin.Installed(e.Spec))+")")
		}
		fmt.Printf("%s  %s\n", what, state)
		if plugin.Gateway(plugin.Target(e.Spec)) != "" {
			fmt.Println("  " + muted.Render("async gateway hook") + "  onRequest")
		}
		// its calls are counted in the gateway's process, not this one
		if m, ok := mws[e.Spec]; ok && !e.Off {
			if m.Error != "" {
				fmt.Printf("  %s  %s\n", muted.Render("gateway middleware"), amber.Render("didn't load: "+m.Error))
			} else {
				fmt.Printf("  %s  %s\n", muted.Render("gateway middleware"), mwPoints(m))
			}
		}
		// the tools it gives agents, by their names on the plugin tools
		// MCP server (the host loads no plugin that is off, so one has
		// none), and the page it adds to the window
		if ts := toolsLine(toolNames[e.Spec]); ts != "" {
			fmt.Printf("  %s  %s\n", muted.Render("tools"), ts)
		}
		if w, _ := plugin.Window(plugin.Target(e.Spec)); w != nil {
			if w.Page != nil {
				fmt.Printf("  %s  %s\n", muted.Render("page"), pageLine(w, w.Page))
			}
			if len(w.Panels) > 0 {
				fmt.Printf("  %s  %s\n", muted.Render("panels"), panelsLine(w))
			}
		}
		for _, p := range ps {
			if p.Spec != e.Spec {
				continue
			}
			who := muted.Render("not signed in · magpie plugin login " + p.ID)
			if c, ok := onBuiltin[p.ID]; ok && !p.SignedIn {
				who = muted.Render(fmt.Sprintf("runs on magpie's built-in (%d accounts) · magpie plugin move %s", c.Accounts, p.ID))
			}
			if p.SignedIn {
				who = green.Render("signed in")
				if p.AccountID != "" {
					who += " as " + p.AccountID
				}
			}
			fmt.Printf("  %s %s  %s\n", provider.PluginID(p.ID), muted.Render("("+p.Name+", "+strconv.Itoa(len(p.Models))+" models)"), who)
		}
		// a provider another plugin signs in to as well: one of them runs
		// it, and the other's is said, not left out
		for _, c := range clashes[e.Spec] {
			if e.Off || errs[e.Spec] != "" {
				continue
			}
			if c.By == e.Spec {
				fmt.Printf("  %s  %s\n", provider.PluginID(c.ID), muted.Render("also signed in to by "+strings.Join(c.With, ", ")+"; this one serves it"))
				continue
			}
			fmt.Printf("  %s  %s\n", provider.PluginID(c.ID), amber.Render("served by "+c.By+", which signs in to it too · magpie plugin use "+e.Spec+" "+c.ID))
		}
	}
	if lerr != nil {
		return lerr
	}
	return perr
}

// pluginOptions prints a plugin's options, or sets them from a JSON
// object; off takes them away. With none set, it prints what its
// package suggests.
// installedName is the package name of the plugin a short name, as the
// community's READMEs write it (think-tags for
// @magpie-community/middleware-think-tags), stands for; a name a plugin
// has itself, or one no plugin has, is kept as it is.
func installedName(name string) string {
	ps := plugin.Load().Plugins
	for _, x := range ps {
		if plugin.Name(x.Spec) == name || x.Spec == name {
			return name
		}
	}
	for _, x := range ps {
		if plugin.Called(plugin.Name(x.Spec), name) {
			return plugin.Name(x.Spec)
		}
	}
	return name
}

func pluginOptions(ctx context.Context, name string, set []string) error {
	var e *plugin.Entry
	ps := plugin.Load().Plugins
	// the one added as name exactly, else the one of that package
	for i, x := range ps {
		if e == nil && x.Spec == name {
			e = &ps[i]
		}
	}
	for i, x := range ps {
		if e == nil && plugin.Name(x.Spec) == name {
			e = &ps[i]
		}
	}
	// a short name, as the community's READMEs write it
	for i, x := range ps {
		if e == nil && plugin.Called(plugin.Name(x.Spec), name) {
			e = &ps[i]
		}
	}
	if e == nil {
		return fmt.Errorf("no plugin %q", name)
	}
	if len(set) == 0 {
		switches := declaredSwitches(ctx, e.Spec, false)
		if len(switches) > 0 {
			chosen := settings.Load().PluginOptions
			for _, sw := range switches {
				v := "off"
				if plugin.OptionOn(chosen, sw.provider, sw.Option) {
					v = "on"
				}
				fmt.Printf("%s  %s %s  %s\n", sw.Key, sw.provider, v, muted.Render(sw.Label))
			}
		}
		if len(e.Options) == 0 {
			if len(switches) > 0 {
				return nil
			}
			ex := plugin.OptionsExample(plugin.Target(e.Spec))
			if ex == nil {
				fmt.Println(muted.Render("No options set."))
				return nil
			}
			b, _ := json.MarshalIndent(ex, "", "  ")
			fmt.Println(muted.Render("No options set. Its package suggests:"))
			fmt.Println(string(b))
			return nil
		}
		b, _ := json.MarshalIndent(e.Options, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	var opts map[string]any
	if set[0] != "off" {
		if err := json.Unmarshal([]byte(set[0]), &opts); err != nil || opts == nil {
			return errors.New(`options are a JSON object ('{"key": "value"}'), or off`)
		}
	}
	// A switch the plugin declares for its provider (auth.options) is read
	// from settings' PluginOptions (input.magpie.option), as the editor's
	// Plugin options saves it; plugins.json's options are a middleware's
	// ctx.options, which a provider plugin never sees. `options cursor
	// '{"wire_trace": true}'` wrote the second and nothing turned on
	// (plugins#550): a declared key goes to the first, the rest as before.
	switches := declaredSwitches(ctx, e.Spec, opts != nil)
	byKey := map[string][]string{}
	for _, sw := range switches {
		byKey[sw.Key] = append(byKey[sw.Key], sw.provider)
	}
	on := map[string]bool{}
	rest := map[string]any{}
	for k, v := range opts {
		if len(byKey[k]) == 0 {
			rest[k] = v
			continue
		}
		b, ok := v.(bool)
		if !ok {
			return fmt.Errorf("%s is a switch %s declares: true or false", k, name)
		}
		on[k] = b
	}
	if len(switches) > 0 && (opts == nil || len(on) > 0) {
		err := settings.EditPluginOptions(func(m map[string]map[string]bool) (map[string]map[string]bool, error) {
			if m == nil {
				m = map[string]map[string]bool{}
			}
			for _, sw := range switches {
				if opts == nil {
					// off: every switch back to the plugin's default
					delete(m[sw.provider], sw.Key)
					continue
				}
				v, ok := on[sw.Key]
				if !ok {
					continue
				}
				if m[sw.provider] == nil {
					m[sw.provider] = map[string]bool{}
				}
				m[sw.provider][sw.Key] = v
			}
			return m, nil
		})
		if err != nil {
			return err
		}
		// best effort: a host that misses it reads them when it next starts
		if plugin.Running() {
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			_ = plugin.SetProviderOptions(c)
			cancel()
		}
	}
	// plugins.json keeps what isn't a declared switch: set with only
	// switches leaves a middleware's options as they were, less any
	// switch written there before
	write, keep := opts == nil || len(rest) > 0, rest
	if !write && len(on) > 0 {
		keep = map[string]any{}
		for k, v := range e.Options {
			if len(byKey[k]) == 0 {
				keep[k] = v
			}
		}
		write = len(keep) != len(e.Options)
	}
	if write {
		if len(keep) == 0 {
			keep = nil
		}
		if err := plugin.SetOptions(e.Spec, keep); err != nil {
			return err
		}
	}
	switch {
	case opts == nil:
		fmt.Println(green.Render("✓"), name, "has no options")
	case len(on) > 0:
		keys := make([]string, 0, len(on))
		for k := range on {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := "off"
			if on[k] {
				v = "on"
			}
			fmt.Println(green.Render("✓"), k, "is", v, "for", strings.Join(byKey[k], ", "))
		}
		if len(rest) > 0 {
			fmt.Println(green.Render("✓"), name, "options set")
		}
	default:
		fmt.Println(green.Render("✓"), name, "options set")
	}
	return nil
}

// declaredSwitch is a switch a plugin declares for one of its providers.
type declaredSwitch struct {
	plugin.Option
	provider string
}

// declaredSwitches are the switches the plugin spec declares for its
// providers (auth.options), from the providers last listed; ask lists them
// from the plugin host when none were listed for it yet (a plugin just
// added from the command line). A host that can't be asked is none known.
func declaredSwitches(ctx context.Context, spec string, ask bool) []declaredSwitch {
	of := func(ps []plugin.Provider) (out []declaredSwitch, listed bool) {
		for _, p := range ps {
			if p.Spec != spec {
				continue
			}
			listed = true
			for _, o := range p.Options {
				out = append(out, declaredSwitch{o, p.ID})
			}
		}
		return out, listed
	}
	out, listed := of(plugin.Cached())
	if listed || !ask {
		return out
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ps, err := plugin.Providers(c)
	if err != nil {
		return nil
	}
	out, _ = of(ps)
	return out
}

// pluginProvider is the plugins' provider named by OpenCode's id or
// magpie's.
func pluginProvider(ctx context.Context, name string) (plugin.Provider, error) {
	ps, err := plugin.Providers(ctx)
	if err != nil {
		return plugin.Provider{}, err
	}
	for _, p := range ps {
		if p.ID == name || provider.PluginID(p.ID) == name || strings.EqualFold(p.Name, name) {
			return p, nil
		}
	}
	var ids []string
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	if len(ids) == 0 {
		return plugin.Provider{}, fmt.Errorf("no plugin signs in to %q — add one: magpie plugin add <npm package>", name)
	}
	return plugin.Provider{}, fmt.Errorf("no plugin signs in to %q; they sign in to %s", name, strings.Join(ids, ", "))
}

// pluginSignInCLI finds and installs the vendor CLI a plugin's sign-in
// runs through; tests stand in for it.
var pluginSignInCLI = struct {
	missing func(id string) string
	install func(ctx context.Context, id string) error
}{provider.PluginCLIMissing, provider.InstallPluginCLI}

// pluginLogin signs in to a plugin's provider as OpenCode's `auth login`
// does: the method, its questions, then a browser (and a code pasted
// back) or a key.
func pluginLogin(ctx context.Context, name, method string) error {
	pp, err := pluginProvider(ctx, name)
	if err != nil {
		return err
	}
	if len(pp.Methods) == 0 {
		return fmt.Errorf("%s's plugin has no way to sign in", pp.Name)
	}
	m := 0
	switch {
	case method != "":
		m = -1
		for i, x := range pp.Methods {
			if strconv.Itoa(i+1) == method || strings.EqualFold(x.Label, method) || x.Type == method {
				m = i
				break
			}
		}
		if m < 0 {
			return fmt.Errorf("%s has no sign-in method %q", pp.Name, method)
		}
	case len(pp.Methods) > 1:
		fmt.Println("How do you sign in to", pp.Name+"?")
		for i, x := range pp.Methods {
			fmt.Printf("  %d. %s\n", i+1, x.Label)
		}
		n, err := askNumber(len(pp.Methods))
		if err != nil {
			return err
		}
		m = n
	}
	if pp.Methods[m].Type != "api" {
		// the vendor's CLI it signs in with, installed first as the
		// window's sign-in installs it (#2223: Grok Build in Docker)
		if name := pluginSignInCLI.missing(pp.ID); name != "" {
			fmt.Println(amber.Render("!"), name, "isn't installed here: "+pp.Name+" signs in through it. Installing it, as its vendor's installer does…")
			if err := pluginSignInCLI.install(ctx, pp.ID); err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), name, "installed")
		}
	}
	inputs := map[string]string{}
	for {
		q, err := plugin.NextPrompt(ctx, pp.ID, m, inputs)
		if err != nil {
			return err
		}
		if q == nil {
			break
		}
		for {
			v, err := askPrompt(q)
			if err != nil {
				return err
			}
			msg, err := plugin.Validate(ctx, pp.ID, m, q.Key, v)
			if err != nil {
				return err
			}
			if msg == "" {
				inputs[q.Key] = v
				break
			}
			fmt.Println(msg)
		}
	}
	// signed in through the proxy its requests will go through (#1663)
	ctx = provider.Via(ctx, provider.PluginID(pp.ID))
	var saved plugin.Saved
	if pp.Methods[m].Type == "api" {
		key := ""
		if !pp.Methods[m].NoKey() {
			var err error
			key, err = secret("key", keyPrompt(pp.Name, pp.Methods[m]), false)
			if err != nil {
				return err
			}
		}
		if saved, err = plugin.APIKey(ctx, pp.ID, m, inputs, key, plugin.NewAccount); err != nil {
			return err
		}
	} else {
		a, err := plugin.Authorize(ctx, pp.ID, m, inputs, plugin.NewAccount)
		if err != nil {
			return err
		}
		if a.URL != "" {
			// the user may keep sign-in pages to a browser of their own; a
			// page the plugin's CLI opened itself is open either way
			linkOnly := settings.Shared().SignInLinkOnly && !a.Opened
			if linkOnly {
				fmt.Println("Open this link in the browser you sign in with:")
			} else {
				fmt.Println("Sign in in your browser. If it didn't open, go to:")
			}
			fmt.Println(faint.Render(a.URL))
			if !a.Opened && !linkOnly {
				openInBrowser(a.URL)
			}
		}
		if a.Instructions != "" {
			fmt.Println(a.Instructions)
		}
		code := ""
		if a.Method == "code" {
			fmt.Print("Code: ")
			line, err := stdin().ReadString('\n')
			if err != nil && line == "" {
				return err
			}
			code = strings.TrimSpace(line)
		}
		wait, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if a.Method != "code" && provider.PluginPastesCallback(pp.ID, a.URL) {
			// a browser on another computer (magpie on a server or in
			// Docker) ends on a page that won't load: its address finishes it
			fmt.Println("If the page the browser ends on won't load (magpie on a server or in Docker), paste its whole address here and press Enter:")
			go func() {
				for wait.Err() == nil {
					line, err := stdin().ReadString('\n')
					if strings.TrimSpace(line) != "" && wait.Err() == nil {
						if next, err := provider.PastePluginCallback(wait, a.URL, line); err != nil {
							fmt.Println(err)
						} else if next != "" {
							fmt.Println("Go on signing in at:")
							fmt.Println(faint.Render(next))
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
		if saved, err = plugin.Finish(wait, a.Session, code); err != nil {
			return err
		}
	}
	// as the window's sign-in does: its lapsed mark goes and, removed from
	// magpie, it comes back
	id := provider.PluginSignedIn(saved)
	fmt.Println(green.Render("✓"), "signed in to", pp.Name, muted.Render("· its models are "+id+"/<model>"))
	sayStillOff(id)
	return nil
}

// sayStillOff tells, after a sign-in, that the provider it went to is
// switched off. A sign-in keeps the user's own switch as it was, but
// "signed in" and then "○ switched off" in magpie providers read as a bug
// with nothing saying how to turn it on (Atlas, WorkBuddy AI). A provider
// that can't be found says nothing: not known to be off.
func sayStillOff(id string) {
	if p, err := provider.Find(id); err == nil && p.Off {
		fmt.Println(amber.Render("!"), p.Name, "is switched off in magpie, so agents aren't given its models · turn it on: magpie provider on "+p.ID)
	}
}

// keyPrompt asks an "api" method's key: its title, then the hint the
// plugin gives (its placeholder), as a question's is.
func keyPrompt(name string, m plugin.Method) string {
	p := m.KeyTitle(name)
	if m.Placeholder != "" {
		p += " " + muted.Render("("+m.Placeholder+")")
	}
	return p + ": "
}

func askNumber(n int) (int, error) {
	for {
		fmt.Print("> ")
		line, err := stdin().ReadString('\n')
		if i, e := strconv.Atoi(strings.TrimSpace(line)); e == nil && i >= 1 && i <= n {
			return i - 1, nil
		}
		if err != nil {
			return 0, err
		}
		fmt.Printf("A number from 1 to %d\n", n)
	}
}

// promptTerminal and promptHidden read a hidden answer; tests stand in for
// the terminal.
var (
	promptTerminal = term.IsTerminal
	promptHidden   = term.ReadPassword
)

func askPrompt(q *plugin.Prompt) (string, error) {
	if q.Type == "select" {
		fmt.Println(q.Message)
		for i, o := range q.Options {
			hint := ""
			if o.Hint != "" {
				hint = muted.Render("  " + o.Hint)
			}
			fmt.Printf("  %d. %s%s\n", i+1, o.Label, hint)
		}
		i, err := askNumber(len(q.Options))
		if err != nil {
			return "", err
		}
		return q.Options[i].Value, nil
	}
	p := q.Message
	if q.Placeholder != "" {
		p += " " + muted.Render("("+q.Placeholder+")")
	}
	fmt.Print(p + " ")
	// a "password" question (a token, a PAT) is typed unseen on a
	// terminal, as an API key is (#1982); piped in, it is a line
	if q.Type == "password" && promptTerminal(int(os.Stdin.Fd())) {
		b, err := promptHidden(int(os.Stdin.Fd()))
		fmt.Println()
		return strings.TrimSpace(string(b)), err
	}
	line, err := stdin().ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// mwPoints is what a gateway module serves, as the Plugins page's mwLine
// tells it: its hooks, the routings its strategies serve, and whether it
// has onUsage.
func mwPoints(m middleware.State) string {
	var parts []string
	if len(m.Hooks) > 0 {
		parts = append(parts, strings.Join(m.Hooks, ", "))
	}
	if len(m.Strategies) > 0 {
		parts = append(parts, "routings "+strings.Join(m.Strategies, ", "))
	}
	if m.Usage {
		parts = append(parts, "onUsage")
	}
	return strings.Join(parts, " · ")
}

// toolsLine is a plugin's tools by their MCP names, sorted.
func toolsLine(names map[string]string) string {
	var out []string
	for _, n := range names {
		out = append(out, n)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// pageLine is a plugin's page: its title, the built-in page it takes the
// place of, and what it may read of magpie's.
func pageLine(w *plugin.WindowSpec, pg *plugin.PageSpec) string {
	s := pg.Title
	if pg.Replaces != "" {
		s += " " + muted.Render("in place of "+pg.Replaces)
	}
	return s + " " + muted.Render("("+readsText(w.Reads)+")")
}

// panelsLine is the panels a plugin adds to magpie's own pages: the page
// each is in, and what they may read of magpie's.
func panelsLine(w *plugin.WindowSpec) string {
	var at []string
	for _, p := range w.Panels {
		s := p.Slot
		if p.Title != "" {
			s = p.Title + " on " + p.Slot
		}
		at = append(at, s)
	}
	return strings.Join(at, ", ") + " " + muted.Render("("+readsText(w.Reads)+")")
}

func readsText(reads []string) string {
	if len(reads) == 0 {
		return "reads nothing"
	}
	return "reads " + strings.Join(reads, ", ")
}

// wiredPluginAgent is the agent the plugin named adds, while magpie is in
// its config: turning the plugin off or removing it takes magpie out
// first (provider.PluginAgentGoing, #3810).
func wiredPluginAgent(name string) *agent.Agent {
	for _, a := range agent.All() {
		if a.Plugin != "" && (a.Plugin == name || plugin.Name(a.Plugin) == name) && a.Wired() {
			return a
		}
	}
	return nil
}

// sayAgentBack says that the agent a plugin added was taken off magpie.
func sayAgentBack(a *agent.Agent) {
	if a == nil {
		return
	}
	fmt.Println(faint.Render("  " + a.Name + " is disconnected from magpie, back to what it had before; with the plugin back, connecting it (its switch on the Agents page) puts back the model it was on"))
}

// registryFrom says where the npm registry in use came from.
func registryFrom(r plugin.RegistryInUse) string {
	switch r.From {
	case plugin.FromMagpie:
		return "set in magpie; magpie plugin registry default drops it"
	case plugin.FromEnv:
		return "from " + r.Var
	case plugin.FromShell:
		return "from your shell's " + r.Var
	case plugin.FromBunfig, plugin.FromNPMRC:
		return "from " + r.Path
	case plugin.FromChina:
		return "the 国内镜像 switch on the Plugins page"
	}
	return "npm's own"
}
