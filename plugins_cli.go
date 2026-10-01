package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

const pluginUsage = `usage: magpie plugin [list] [--json]
       magpie plugin add <npm package | path>      install an OpenCode provider plugin (opencode-gemini-auth, ./my-plugin.js)
       magpie plugin rm <name>                     remove one
       magpie plugin update                        install the newest version of each
       magpie plugin on|off <name>                 turn one on or off
       magpie plugin login <provider> [<method>]   sign in to a provider a plugin adds
       magpie plugin logout <provider>             forget the sign-in
       magpie plugin move|migrate <subscription>   run a built-in subscription's accounts on its community plugin
       magpie plugin move-back|unmigrate <subscription>   go back to the built-in, with its accounts`

// pluginCmd: `magpie plugin …` — OpenCode's provider plugins, which sign in
// to a subscription and carry its requests (internal/plugin).
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
		return listPlugins(ctx, false)
	case "rm", "remove", "uninstall":
		if len(rest) != 1 {
			return errors.New(pluginUsage)
		}
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
		return listPlugins(ctx, false)
	case "on", "off":
		if len(rest) != 1 {
			return errors.New(pluginUsage)
		}
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
		fmt.Println("No plugins. Add one: magpie plugin add <npm package | path>")
		return nil
	}
	loaded, lerr := plugin.Plugins(ctx)
	errs := map[string]string{}
	for _, p := range loaded {
		errs[p.Spec] = p.Error
	}
	ps, perr := plugin.Providers(ctx)
	if asJSON {
		b, _ := json.MarshalIndent(map[string]any{"plugins": l.Plugins, "loaded": loaded, "providers": ps}, "", "  ")
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
		fmt.Printf("%s  %s\n", bold.Render(e.Spec), state)
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
		if saved, err = plugin.Finish(wait, a.Session, code); err != nil {
			return err
		}
	}
	fmt.Println(green.Render("✓"), "signed in to", pp.Name, muted.Render("· its models are "+provider.PluginID(saved.Provider)+"/<model>"))
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
