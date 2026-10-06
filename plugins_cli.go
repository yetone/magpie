package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/middleware"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

const pluginUsage = `usage: magpie plugin [list] [--json]
       magpie plugin add <npm | git | path>        install an OpenCode provider plugin, a pi package or a gateway middleware (opencode-gemini-auth, pi-antigravity, github:owner/repo, ./my-plugin.js, ./alias.middleware.js)
       magpie plugin rm <name>                     remove one
       magpie plugin update                        install the newest version of each
       magpie plugin on|off <name>                 turn one on or off
       magpie plugin options <name> [<json> | off] show or set what a plugin is handed (a middleware's ctx.options)
       magpie plugin login <provider> [<method>]   sign in to a provider a plugin adds
       magpie plugin logout <provider>             forget the sign-in
       magpie plugin move|migrate <subscription>   run a built-in subscription's accounts on its community plugin
       magpie plugin move-back|unmigrate <subscription>   go back to the built-in, with its accounts`

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
		e, err := plugin.Add(ctx, rest[0])
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "added", e.Spec)
		// a deprecated built-in it serves, not signed in to, is its now
		provider.HandOver(ctx, false)
		return listPlugins(ctx, false)
	case "rm", "remove", "uninstall":
		if len(rest) != 1 {
			return errors.New(pluginUsage)
		}
		rest[0] = installedName(rest[0])
		back := provider.MovedOnto(rest[0])
		if err := provider.RemovePlugin(ctx, rest[0]); err != nil {
			return err
		}
		for _, id := range back {
			fmt.Println(green.Render("✓"), id, "is back on its built-in")
		}
		fmt.Println(green.Render("✓"), "removed", rest[0])
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
		var back []string
		if sub == "off" {
			back = provider.MovedOnto(rest[0])
		}
		if err := provider.SetPluginOff(ctx, rest[0], sub == "off"); err != nil {
			return err
		}
		for _, id := range back {
			fmt.Println(green.Render("✓"), id, "is back on its built-in")
		}
		fmt.Println(green.Render("✓"), rest[0], "is", sub)
		return nil
	case "options", "config":
		if len(rest) < 1 || len(rest) > 2 {
			return errors.New(pluginUsage)
		}
		return pluginOptions(rest[0], rest[1:])
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
		fmt.Println(green.Render("✓"), rest[0], "runs on", provider.MovePackage(rest[0]), muted.Render("(magpie plugin move-back "+rest[0]+" to undo)"))
		return nil
	case "move-back", "moveback", "unmigrate":
		if len(rest) != 1 {
			return errors.New(pluginUsage)
		}
		if !provider.Moved(rest[0]) {
			return fmt.Errorf("%s isn't on its plugin", rest[0])
		}
		if err := provider.MoveBack(ctx, rest[0]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), rest[0], "is built-in again")
		return nil
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
	ps, perr := plugin.Providers(ctx)
	mws := middleware.States()
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
		// its calls are counted in the gateway's process, not this one
		if m, ok := mws[e.Spec]; ok && !e.Off {
			if m.Error != "" {
				fmt.Printf("  %s  %s\n", muted.Render("gateway middleware"), amber.Render("didn't load: "+m.Error))
			} else {
				fmt.Printf("  %s  %s\n", muted.Render("gateway middleware"), strings.Join(m.Hooks, ", "))
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
		if plugin.ShortName(plugin.Name(x.Spec)) == name {
			return plugin.Name(x.Spec)
		}
	}
	return name
}

func pluginOptions(name string, set []string) error {
	var e *plugin.Entry
	ps := plugin.Load().Plugins
	for i, x := range ps {
		if plugin.Name(x.Spec) == name || x.Spec == name {
			e = &ps[i]
			break
		}
	}
	// a short name, as the community's READMEs write it
	for i, x := range ps {
		if e == nil && plugin.ShortName(plugin.Name(x.Spec)) == name {
			e = &ps[i]
		}
	}
	if e == nil {
		return fmt.Errorf("no plugin %q", name)
	}
	if len(set) == 0 {
		if len(e.Options) == 0 {
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
	if err := plugin.SetOptions(e.Spec, opts); err != nil {
		return err
	}
	if opts == nil {
		fmt.Println(green.Render("✓"), name, "has no options")
	} else {
		fmt.Println(green.Render("✓"), name, "options set")
	}
	return nil
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
	var saved plugin.Saved
	if pp.Methods[m].Type == "api" {
		key, err := secret("key", keyPrompt(pp.Name, pp.Methods[m]), false)
		if err != nil {
			return err
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
			fmt.Println("Sign in in your browser. If it didn't open, go to:")
			fmt.Println(faint.Render(a.URL))
			openInBrowser(a.URL)
		}
		if a.Instructions != "" {
			fmt.Println(a.Instructions)
		}
		code := ""
		if a.Method == "code" {
			fmt.Print("Code: ")
			line, err := stdin.ReadString('\n')
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
					line, err := stdin.ReadString('\n')
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
	return nil
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
		line, err := stdin.ReadString('\n')
		if i, e := strconv.Atoi(strings.TrimSpace(line)); e == nil && i >= 1 && i <= n {
			return i - 1, nil
		}
		if err != nil {
			return 0, err
		}
		fmt.Printf("A number from 1 to %d\n", n)
	}
}

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
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
