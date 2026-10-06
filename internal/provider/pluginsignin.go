package provider

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/plugin"
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
	s := &signInFlow{done: make(chan struct{})}
	s.st = SignInState{ID: randomToken(9), Agent: PluginID(id), State: "waiting"}
	var cli agentCLI
	install := false
	if Moved(s.st.Agent) {
		cli, install = missingCLI(s.st.Agent)
	} else if pluginRunsCLI[id] {
		cli, install = missingCLI(id)
	}
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

// pluginBegin asks the plugin for the page to open and waits for the
// sign-in to finish there, or for the code the page shows.
func (s *signInFlow) pluginBegin(id string, method int, inputs map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
	s.st.URL, s.st.Instructions, s.st.Code = a.URL, a.Instructions, codeIn(a.Instructions)
	s.stop = stop
	if a.Method == "code" {
		s.st.PasteCode, s.plugin = true, a.Session
	} else if PluginPastesCallback(id, a.URL) {
		// a browser that can't reach the plugin's port here ends on a page
		// that won't load: its address finishes it
		s.st.PasteCallback, s.pluginPorts = true, loopbackPorts(a.URL)
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
var deviceCode = regexp.MustCompile(`(?i:\bcode):?\s+([A-Z0-9]+(?:-[A-Z0-9]+)+|[A-Z0-9]{6,})\b`)

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
	if strings.TrimSpace(key) == "" {
		return "", errors.New("enter the key")
	}
	saved, err := plugin.APIKey(ctx, id, method, inputs, strings.TrimSpace(key), plugin.NewAccount)
	if err != nil {
		return "", err
	}
	return PluginSignedIn(saved), nil
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
			return pluginLabels(pp)[saved.Account]
		}
	}
	return ""
}
