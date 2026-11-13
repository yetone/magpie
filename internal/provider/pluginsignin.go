package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// StartPluginSignIn begins signing in to a plugin's provider (OpenCode's
// id) with its OAuth method, the method's questions answered: the page to
// open, then either the plugin notices on its own or the code the page
// shows is pasted back (SubmitSignInCallback). SignInStatus follows it,
// as it does a built-in sign-in; its Agent is the provider's magpie id.
// The account goes beside those signed in already, or replaces the one it
// is. A built-in moved onto its plugin installs the CLI it runs through
// first, as the built-in's sign-in did.
func StartPluginSignIn(id string, method int, inputs map[string]string) (SignInState, error) {
	return StartPluginSignInWith(id, method, inputs, SignInOptions{})
}

// StartPluginSignInWith is StartPluginSignIn through the proxy o picks,
// else the one the provider's requests go through (#1663).
func StartPluginSignInWith(id string, method int, inputs map[string]string, o SignInOptions) (SignInState, error) {
	if settings.AccountDiscoveryOff() {
		return SignInState{}, errAccountDiscoveryOff
	}
	s := &signInFlow{done: make(chan struct{})}
	if err := s.pickProxy(PluginID(id), o); err != nil {
		return SignInState{}, err
	}
	s.st = SignInState{ID: randomToken(9), Agent: PluginID(id), State: "waiting", Proxy: s.proxy}
	cli, install := pluginCLI(id)
	var installing context.Context
	if install {
		s.st.State, s.st.Installing = "installing", cli.Name
		installing, s.stop = context.WithCancel(context.Background())
	} else if err := s.pluginBegin(id, method, inputs); err != nil {
		return SignInState{}, err
	}
	signIns.Lock()
	for sid, o := range signIns.m {
		if o.st.Agent == s.st.Agent {
			o.finish(SignInState{State: "canceled"})
			delete(signIns.m, sid)
		}
	}
	signIns.m[s.st.ID] = s
	signIns.Unlock()
	if install {
		go func() {
			err := installCLI(installing, cli)
			if err == nil {
				forgetAccountCaches()
				err = s.pluginBegin(id, method, inputs)
			}
			if err != nil {
				s.finish(SignInState{State: "failed", Error: err.Error()})
			}
		}()
	}
	return s.status(), nil
}

// pluginRunsCLI are the plugins (by OpenCode's id) that sign in with the
// vendor's CLI themselves, as the built-in did: the community Grok plugin
// runs `grok login`. One installed beside the built-in, not moved onto,
// said "install Grok Build first: curl … | bash", which a magpie in Docker
// has no shell to run; its CLI is installed first, as a moved one's is.
var pluginRunsCLI = map[string]bool{"grok": true}

// pluginCLI is the vendor CLI a plugin's sign-in (OpenCode's id) runs
// through and this machine lacks: a moved built-in's, or one in
// pluginRunsCLI.
func pluginCLI(id string) (agentCLI, bool) {
	if PluginServes(PluginID(id)) {
		return missingCLI(PluginID(id))
	}
	if pluginRunsCLI[id] {
		return missingCLI(id)
	}
	return agentCLI{}, false
}

// PluginCLIMissing is the name of the vendor CLI a plugin's sign-in runs
// through and this machine lacks (Grok Build for Grok's), "" when there is
// none to install. The command line's sign-in installs it first, as
// StartPluginSignIn does; without it, `magpie plugin login grok` in Docker
// said "install Grok Build first: curl … | bash", which the image has no
// curl to run (#2223).
func PluginCLIMissing(id string) string {
	c, ok := pluginCLI(id)
	if !ok {
		return ""
	}
	return c.Name
}

// InstallPluginCLI installs the CLI PluginCLIMissing names, with its
// vendor's installer or, where that can't run, as it installs it.
func InstallPluginCLI(ctx context.Context, id string) error {
	c, ok := pluginCLI(id)
	if !ok {
		return nil
	}
	if err := installCLI(ctx, c); err != nil {
		return err
	}
	forgetAccountCaches()
	return nil
}

// pluginBegin asks the plugin for the page to open and waits for the
// sign-in to finish there, or for the code the page shows.
func (s *signInFlow) pluginBegin(id string, method int, inputs map[string]string) error {
	ctx, cancel := context.WithTimeout(s.via(context.Background()), time.Minute)
	a, err := plugin.Authorize(ctx, id, method, inputs, plugin.NewAccount)
	cancel()
	if err != nil {
		return err
	}
	wait, stop := context.WithTimeout(context.Background(), signInTimeout)
	s.mu.Lock()
	if s.st.State != "waiting" && s.st.State != "installing" {
		// canceled while the CLI was installed
		s.mu.Unlock()
		stop()
		return nil
	}
	s.st.State, s.st.Installing = "waiting", ""
	s.st.URL, s.st.Instructions, s.st.Code, s.st.Opened = a.URL, a.Instructions, codeIn(a.Instructions), a.Opened
	s.stop = stop
	if a.Method == "code" {
		s.st.PasteCode, s.plugin = true, a.Session
	} else if PluginPastesCallback(id, a.URL) {
		// a browser that can't reach the plugin's port here ends on a page
		// that won't load: its address finishes it
		s.st.PasteCallback, s.pluginPorts = true, loopbackPorts(a.URL)
	} else if m, keys, ok := pluginKeyMethod(id); ok && m != method {
		// its page posts where nothing can be pasted: a key made on the
		// keys page finishes it
		s.st.PasteKey, s.st.KeysURL, s.pluginKey = true, keys, &pluginKeyWay{id: id, method: m}
	}
	s.mu.Unlock()
	if a.Method == "code" {
		go func() {
			<-wait.Done()
			if errors.Is(wait.Err(), context.DeadlineExceeded) {
				s.finish(SignInState{State: "failed", Error: "the sign-in timed out; start it again"})
			}
		}()
	} else {
		go func() {
			saved, err := plugin.Finish(wait, a.Session, "")
			s.pluginDone(saved, err)
		}()
	}
	return nil
}

// deviceCode is the code a device sign-in's instructions ask to check on
// the vendor's page ("Confirm the code ABCD-EFGH on Factory's page").
var deviceCode = lazyre.New(`(?i:\bcode):?\s+([A-Z0-9]+(?:-[A-Z0-9]+)+|[A-Z0-9]{6,})\b`)

// codeIn is that code, shown to copy as the built-in's was.
func codeIn(instructions string) string {
	if m := deviceCode.FindStringSubmatch(instructions); m != nil {
		return m[1]
	}
	return ""
}

// pluginCode finishes a plugin's sign-in with the code its page showed.
func (s *signInFlow) pluginCode(code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return errors.New("paste the code the sign-in page showed")
	}
	s.mu.Lock()
	session := s.plugin
	s.plugin = ""
	waiting := s.st.State == "waiting"
	s.mu.Unlock()
	if session == "" || !waiting {
		return errors.New("this sign-in is over; start it again")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	saved, err := plugin.Finish(ctx, session, code)
	s.pluginDone(saved, err)
	return err
}

func (s *signInFlow) pluginDone(saved plugin.Saved, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		s.finish(SignInState{State: "canceled"})
	case errors.Is(err, context.DeadlineExceeded):
		s.finish(SignInState{State: "failed", Error: "the sign-in timed out; start it again"})
	case err != nil:
		s.finish(SignInState{State: "failed", Error: err.Error()})
	default:
		clearPluginLapse(saved)
		user := pluginUser(saved)
		s.finish(SignInState{State: "done", User: user, Using: pluginUsing(saved, user)})
	}
}

// PluginAPIKey signs in to a plugin's provider with a key, as its "api"
// method does, beside the accounts signed in already; it gives the
// provider's magpie id.
func PluginAPIKey(ctx context.Context, id string, method int, inputs map[string]string, key string) (string, error) {
	// with subscription accounts off, a key is refused as a browser
	// sign-in is (StartPluginSignInWith), but for a public plugin's: one
	// saved would be signed in and listed nowhere
	if settings.AccountDiscoveryOff() {
		if pp, ok := PluginOf(PluginID(id)); !ok || !publicPlugin(pp) {
			return "", errAccountDiscoveryOff
		}
	}
	if strings.TrimSpace(key) == "" {
		pp, ok := PluginOf(PluginID(id))
		if !ok || method < 0 || method >= len(pp.Methods) || pp.Methods[method].Type != "api" || !pp.Methods[method].NoKey() {
			return "", errors.New("enter the key")
		}
	}
	// through the proxy the provider's requests go through (#1663)
	saved, err := plugin.APIKey(Via(ctx, PluginID(id)), id, method, inputs, strings.TrimSpace(key), plugin.NewAccount)
	if err != nil {
		return "", err
	}
	return PluginSignedIn(saved), nil
}

// PluginImportable is whether the subscription id (a magpie id) is a
// plugin's that takes accounts from a file (auth.import). Factory's
// plugin has its own importer here (ImportFactoryKeys), which goes first.
func PluginImportable(id string) bool {
	pp, ok := PluginOf(id)
	return ok && pp.Importable
}

// ImportPluginFiles brings in the accounts the files' texts hold for the
// plugin's subscription id (a magpie id): the plugin reads them, checks
// each with its vendor as a sign-in does, and keeps the ones it takes,
// through the proxy the subscription's requests go through (#1663).
func ImportPluginFiles(ctx context.Context, id string, texts []string) ([]ImportedAccount, error) {
	if settings.AccountDiscoveryOff() {
		return nil, errAccountDiscoveryOff
	}
	pp, ok := PluginOf(id)
	if !ok || !pp.Importable {
		return nil, errors.New("this subscription takes no accounts from a file")
	}
	if len(texts) > maxGoogleImport {
		return nil, fmt.Errorf("%d files; at most %d at a time", len(texts), maxGoogleImport)
	}
	got, err := plugin.ImportFiles(Via(ctx, id), pp.ID, texts)
	if err != nil {
		return nil, err
	}
	if len(got) == 0 {
		return nil, errors.New("no account to import was found in it")
	}
	out := make([]ImportedAccount, 0, len(got))
	for i, r := range got {
		user := r.User
		if user == "" {
			user = fmt.Sprintf("#%d", i+1)
		}
		if r.Status == "failed" {
			out = append(out, ImportedAccount{User: user, Status: "failed", Error: r.Error})
			continue
		}
		PluginSignedIn(plugin.Saved{Provider: r.Provider, Account: r.Account})
		out = append(out, ImportedAccount{User: user, Status: r.Status})
	}
	forgetAccountCaches()
	return out, nil
}

// PluginSignedIn finishes a sign-in to a plugin's provider that magpie
// doesn't follow, a key or the command line's own, once the plugin has
// saved the account: as for one it follows (pluginDone), the account's
// lapsed mark goes and, removed from magpie, it comes back. It gives the
// provider's magpie id.
func PluginSignedIn(saved plugin.Saved) string {
	clearPluginLapse(saved)
	id := PluginID(saved.Provider)
	_ = ShowAccount(id)
	return id
}

// pluginUsing is whether the account signed in to is the one in use, the
// first, as a built-in's sign-in says.
func pluginUsing(saved plugin.Saved, user string) bool {
	for _, l := range Logins(PluginID(saved.Provider)) {
		if l.Active {
			return user != "" && strings.EqualFold(l.User, user)
		}
	}
	return false
}

// pluginUser is the account signed in to, as the accounts list names it.
func pluginUser(saved plugin.Saved) string {
	for _, pp := range plugin.Cached() {
		if pp.ID == saved.Provider {
			return pluginNames(pp)[saved.Account]
		}
	}
	return ""
}
