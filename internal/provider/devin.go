package provider

// PLUGIN-SERVED (see AGENTS.md): Devin ("devin") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-devin-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/devin) and raise the
// mover's min in internal/provider/migrate_side.go.

// A Devin subscription is served through the API the devin CLI talks to
// (gateway/devin.go), with the key the CLI signed in with; here is who that
// account is, the models it offers, and the sign-in, which is `devin auth
// login`'s.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/catalog"
)

// DevinExecutable finds the devin CLI; a var so tests can fake it.
var DevinExecutable = func() string {
	if p, err := exec.LookPath("devin"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(home, ".local", "bin", "devin"), "/usr/local/bin/devin", "/opt/homebrew/bin/devin"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// DevinCredentialsPath is where the CLI keeps its sign-in.
func DevinCredentialsPath() string {
	if runtime.GOOS == "windows" {
		if app := os.Getenv("APPDATA"); app != "" {
			return filepath.Join(app, "devin", "credentials.toml")
		}
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "devin", "credentials.toml")
}

var devinStatus = &cliIdentity{name: "devin", exe: func() string { return DevinExecutable() }, ask: askDevinStatus}

// devinIdentity is who Devin's CLI says is signed in; see cliIdentity.
func devinIdentity() (user, plan string, ok bool) { return devinStatus.get() }

func forgetDevinStatus() { devinStatus.forget() }

// devinIdentityTimeout is how long `devin auth status` is given: it asks
// Devin's servers, and on a real one took 3 to 11 seconds — the 10 seconds
// it had dropped the account now and then.
const devinIdentityTimeout = 30 * time.Second

// askDevinStatus asks the CLI who is signed in. An ask that fails, runs out
// of time (it asks Devin's servers) or prints something else couldn't tell,
// and the account stays as it was (#154): only a CLI that says nobody is,
// whose account's token was refused, or that keeps no credentials.toml, is
// sure of nobody.
func askDevinStatus() (user, plan string, ok bool, err error) { return askDevinStatusAt("") }

// askDevinIdentity is who the CLI says is signed in, sure or not: the
// sign-in has just written the credentials and has to name the account.
func askDevinIdentity() (user, plan string, ok bool) {
	u, p, ok, _ := askDevinStatusAt("")
	return u, p, ok
}

// askDevinIdentityAt is askDevinIdentity for the account signed in in home
// ("" for the CLI's own).
func askDevinIdentityAt(home string) (user, plan string, ok bool) {
	u, p, ok, _ := askDevinStatusAt(home)
	return u, p, ok
}

// askDevinStatusAt asks the CLI who is signed in in home ("" for its own).
func askDevinStatusAt(home string) (user, plan string, ok bool, err error) {
	path := DevinExecutable()
	if path == "" {
		return "", "", false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), devinIdentityTimeout)
	defer cancel()
	out, err := devinCommand(ctx, home, path, "auth", "status").Output()
	user, plan, _ = parseDevinStatus(string(out))
	switch {
	case user != "":
		return user, plan, true, nil
	case devinSignedOut(string(out)), noDevinKey(home):
		return "", "", false, nil
	case err == nil:
		err = errors.New("devin auth status printed no account")
	}
	return "", "", false, err
}

// devinSignedOut says the CLI's own report is one of nobody signed in: it
// says so itself, or Devin's servers refused the account's token (a revoked
// or expired one) and the CLI prints that inside a report that still begins
// `Logged in`, with no account in it. A CLI that couldn't reach them says
// `Connection failed` instead, and one that ran out of time says nothing:
// neither is this, and both keep the account served (#154).
func devinSignedOut(out string) bool {
	for _, said := range []string{
		"Not logged in",
		"Authentication required",
		"Invalid token",
		"try logging out and logging in again",
	} {
		if strings.Contains(out, said) {
			return true
		}
	}
	return false
}

// noDevinKey says the account signed in in home keeps no credentials: the
// CLI can only be sure of nobody, whatever it printed.
func noDevinKey(home string) bool {
	_, _, err := DevinAuthAt(home)
	return err != nil
}

// parseDevinStatus reads `devin auth status`'s report:
//
//	Logged in (via Devin).
//	  ...
//	User:
//	  Name:              <handle>
//	  Email:             <email>
//	Account:
//	  Tier:              Devin Pro
//	  Plan:              Pro
func parseDevinStatus(out string) (user, plan string, ok bool) {
	if !strings.Contains(out, "Logged in") {
		return "", "", false
	}
	field := func(key string) string {
		for _, l := range strings.Split(out, "\n") {
			l = strings.TrimSpace(l)
			if v, found := strings.CutPrefix(l, key+":"); found {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	user = field("Email")
	if user == "" {
		user = field("Name")
	}
	plan = field("Tier")
	if plan == "" {
		plan = field("Plan")
	}
	return user, plan, true
}

// devinAccount is the Devin account in use first.
func devinAccount() (Provider, bool) {
	ls := devinLogins()
	if len(ls) == 0 {
		return Provider{}, false
	}
	return devinProvider(ls[0]), true
}

// devinProvider is one of the Devin accounts; the CLI's own has the plan
// the CLI says now.
func devinProvider(l devinLogin) Provider {
	home, plan := l.Home, l.Plan
	if home == "" {
		if _, p, ok := devinIdentity(); ok {
			plan = p
		}
	}
	acct := &Account{Agent: "devin", User: l.User, Plan: plan, Home: home}
	acct.models = func() []catalog.Model {
		// what a fetch or a picker visit last asked the CLI — never spawn
		// one here: Available() runs on every gateway request
		return devinModels(devinCached())
	}
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		families, err := askDevinFamiliesAt(ctx, home)
		if err != nil {
			return nil, err
		}
		ms := devinModels(families)
		devinFamiliesCached(families)
		return ms, catalog.SaveLive("devin", "", ms)
	}
	return Provider{ID: "devin", Name: "Devin", Icon: "devin", Website: "https://devin.ai", Account: acct}
}

// DevinFamily is one entry of `devin models list`: a name that follows the
// family's newest model, and the pinned variants under it.
type DevinFamily struct {
	UID     string
	Label   string
	Aliases []string
	Models  []catalog.Model
}

// devinModelsFlatten is the family list as a flat catalog: the family's
// follow-newest id, then each of its variants. Adaptive and Fusion, which
// route between models inside Devin's own agent, aren't served by the API.
func devinModelsFlatten(families []DevinFamily) []catalog.Model {
	var out []catalog.Model
	for _, f := range families {
		if id := strings.ToLower(f.UID); id == "adaptive" || id == "fusion" {
			continue
		}
		// Devin's own numbers, which its list gives each variant and the family
		// takes from the one its id follows; models.dev's only for a family
		// Devin gave none for. A pinned variant (claude-opus-5-5-high) has its
		// family's window: without it Claude Code takes a 1M model for 200K
		// (no [1m] mark)
		window, most := f.window(), f.reply()
		if window == 0 {
			window = catalog.ContextOf(f.UID)
		}
		if most == 0 {
			most = catalog.OutputOf(f.UID)
		}
		// Devin's word on images, as its variants all say it (#417)
		images := f.images()
		out = append(out, catalog.Model{ID: f.UID, Name: f.Label, Provider: "devin", Context: window, Output: most, ImageInput: images})
		// the family's fast run, one model for its fast variants as the
		// family's id is for the rest; not an id of Devin's, the gateway
		// asks for the variant at the effort (DevinVariant). One a family
		// of that name already is (swe-1.6-fast) isn't made up.
		for _, t := range f.tiers() {
			if id := f.UID + "-" + t; !slices.ContainsFunc(families, func(g DevinFamily) bool { return g.named(id) }) {
				out = append(out, catalog.Model{ID: id, Name: f.Label + " " + devinTierLabel(t), Provider: "devin", Context: window, Output: most, ImageInput: images})
			}
		}
		for _, m := range f.Models {
			if m.Context == 0 {
				m.Context = window
			}
			if m.Output == 0 {
				m.Output = most
			}
			out = append(out, m)
		}
	}
	return out
}

// window is how long a prompt a family takes, as its own models give it: the
// family id follows the family's newest, and its variants carry the numbers
// (swe-2's 262K, which models.dev doesn't have). 0 when Devin didn't say.
func (f DevinFamily) window() int {
	for _, m := range f.Models {
		if m.Context > 0 {
			return m.Context
		}
	}
	return 0
}

// reply is the most tokens a reply from the family may hold, likewise.
func (f DevinFamily) reply() int {
	for _, m := range f.Models {
		if m.Output > 0 {
			return m.Output
		}
	}
	return 0
}

// devinDeclared is what Devin's own list gives each model id: how long a prompt
// it takes and the most tokens its reply may hold. A family id takes the
// numbers of the variant it follows; a variant without numbers takes its
// family's, as devinModelsFlatten leaves them. Empty until the CLI list is read.
func devinDeclared() map[string][2]int {
	out := map[string][2]int{}
	for _, f := range devinCached() {
		window, most := f.window(), f.reply()
		if window > 0 || most > 0 {
			out[f.UID] = [2]int{window, most}
			for _, a := range f.Aliases {
				out[a] = [2]int{window, most}
			}
			for _, t := range f.tiers() {
				if _, ok := out[f.UID+"-"+t]; !ok {
					out[f.UID+"-"+t] = [2]int{window, most}
				}
			}
		}
		for _, m := range f.Models {
			w, o := m.Context, m.Output
			if w == 0 {
				w = window
			}
			if o == 0 {
				o = most
			}
			out[m.ID] = [2]int{w, o}
		}
	}
	return out
}

// devinDeclaredImages is what Devin said of each model id's images, as
// devinModelsFlatten gives it, for a list saved before Devin was asked.
func devinDeclaredImages() map[string]*bool {
	out := map[string]*bool{}
	for _, f := range devinCached() {
		if v := f.images(); v != nil {
			for _, id := range append([]string{f.UID}, f.Aliases...) {
				out[id] = v
			}
			for _, t := range f.tiers() {
				if _, ok := out[f.UID+"-"+t]; !ok {
					out[f.UID+"-"+t] = v
				}
			}
		}
		for _, m := range f.Models {
			if m.ImageInput != nil {
				out[m.ID] = m.ImageInput
			}
		}
	}
	return out
}

var devinEffort = regexp.MustCompile(`-(none|min|minimal|low|medium|high|xhigh|max|fast|priority)$`)

// devinKnown is models.dev's window and reply cap for a model id, with the
// effort suffix taken off: claude-opus-5-5-high-fast has claude-opus-5-5's.
func devinKnown(id string) (window, most int) {
	for base := id; ; {
		if n := catalog.ContextOf(base); n > 0 {
			return n, catalog.OutputOf(base)
		}
		b := devinEffort.ReplaceAllString(base, "")
		if b == base {
			return 0, 0
		}
		base = b
	}
}

// withDevinContexts fills in a saved list's windows and reply caps: Devin's own
// numbers over what is there, which an older magpie took from models.dev — its
// glm-5.2 at 1M where Devin gives 200K, its grok at 500K of reply where Devin
// gives 100K — and models.dev's for what Devin's list doesn't name, or a
// variant fetched before it was given its family's (claude-opus-5-5-high-fast
// has claude-opus-5-5's).
func withDevinContexts(ms []catalog.Model) []catalog.Model {
	declared := devinDeclared()
	images := devinDeclaredImages()
	out := slices.Clone(ms)
	for i, m := range out {
		if m.ImageInput == nil {
			out[i].ImageInput = images[m.ID]
		}
		// Devin's own numbers, over what an older magpie took from models.dev
		window, most := declared[m.ID][0], declared[m.ID][1]
		if window > 0 {
			out[i].Context = window
		}
		if most > 0 {
			out[i].Output = most
		}
		if out[i].Context > 0 && out[i].Output > 0 {
			continue
		}
		window, most = devinKnown(m.ID)
		if out[i].Context == 0 {
			out[i].Context = window
		}
		if out[i].Output == 0 {
			out[i].Output = most
		}
	}
	return out
}

var devinFamiliesCache struct {
	sync.Mutex
	at       time.Time
	families []DevinFamily
	// failed is when the CLI last gave no list, and err what it said: a
	// CLI signed out, or one that can't reach Devin, is asked again only
	// after devinFamiliesRetry, not on every call
	failed time.Time
	err    error
	// asking is closed when the ask in flight ends; nil with none
	asking chan struct{}
}

const (
	devinFamiliesFresh = 5 * time.Minute
	devinFamiliesRetry = time.Minute
)

type devinNoWaitKey struct{}

// DevinNoWait marks a context whose caller must not wait on the devin CLI:
// the window's state, drawn after every click. DevinFamilies then answers
// with what it knows (or an error while nothing is known) and asks the
// CLI in the background, so the next state has it.
func DevinNoWait(ctx context.Context) context.Context {
	return context.WithValue(ctx, devinNoWaitKey{}, true)
}

var errDevinAsking = errors.New("devin models list: still being read")

// DevinFamilies is the CLI's model list, kept a few minutes: the picker and
// the provider ask for it often and `devin models list` spawns a process
// that takes seconds (switching Claude Code's model took 5–10s while
// every state asked it anew). A list past its time is served as it is
// while it is read again in the background; only a caller with nothing
// known yet waits, and not one marked DevinNoWait.
func DevinFamilies(ctx context.Context) ([]DevinFamily, error) {
	c := &devinFamiliesCache
	c.Lock()
	if len(c.families) > 0 {
		if time.Since(c.at) >= devinFamiliesFresh && time.Since(c.failed) >= devinFamiliesRetry {
			devinAskLocked()
		}
		f := c.families
		c.Unlock()
		return f, nil
	}
	if time.Since(c.failed) < devinFamiliesRetry {
		err := c.err
		c.Unlock()
		return nil, err
	}
	done := devinAskLocked()
	c.Unlock()
	if ctx.Value(devinNoWaitKey{}) != nil {
		return nil, errDevinAsking
	}
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.Lock()
	defer c.Unlock()
	if len(c.families) > 0 {
		return c.families, nil
	}
	if c.err == nil {
		return nil, errDevinAsking
	}
	return nil, c.err
}

// devinAskLocked starts reading the CLI's list, unless a read is under
// way; the channel closes when it ends. devinFamiliesCache is held.
func devinAskLocked() chan struct{} {
	c := &devinFamiliesCache
	if c.asking != nil {
		return c.asking
	}
	done := make(chan struct{})
	c.asking = done
	go func() {
		families, err := askDevinFamilies(context.Background())
		c.Lock()
		if err != nil {
			c.failed, c.err = time.Now(), err
		} else {
			c.families, c.at, c.failed, c.err = families, time.Now(), time.Time{}, nil
		}
		c.asking = nil
		close(done)
		c.Unlock()
	}()
	return done
}

// devinCached is the CLI list last read, without asking it again.
func devinCached() []DevinFamily {
	devinFamiliesCache.Lock()
	defer devinFamiliesCache.Unlock()
	return devinFamiliesCache.families
}

func devinFamiliesCached(families []DevinFamily) {
	devinFamiliesCache.Lock()
	devinFamiliesCache.families, devinFamiliesCache.at = families, time.Now()
	devinFamiliesCache.failed, devinFamiliesCache.err = time.Time{}, nil
	devinFamiliesCache.Unlock()
}

// askDevinFamilies is the model list as the accounts signed in give it, the
// one in use first: an account magpie signed in is asked in its own home,
// not the CLI's, which may be signed out (蓝猫 on Discord: with only a
// magpie sign-in every request failed, as the family's id went to Devin
// unturned into a variant, until `devin auth login`). The CLI's own is
// asked last when it isn't among them, as it was before there were homes.
func askDevinFamilies(ctx context.Context) ([]DevinFamily, error) {
	var homes []string
	for _, l := range devinLogins() {
		homes = append(homes, l.Home)
	}
	if !slices.Contains(homes, "") {
		homes = append(homes, "")
	}
	var first error
	for _, home := range homes {
		families, err := askDevinFamiliesAt(ctx, home)
		if err == nil {
			return families, nil
		}
		if first == nil {
			first = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, first
}

// askDevinFamiliesAt is the model list of the account signed in in home
// ("" for the CLI's own).
func askDevinFamiliesAt(ctx context.Context, home string) ([]DevinFamily, error) {
	path := DevinExecutable()
	if path == "" {
		return nil, errors.New("devin is not installed")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := devinCommand(ctx, home, path, "models", "list", "--format", "json").Output()
	if err != nil {
		return nil, errorf("devin models list: %v", err)
	}
	families := parseDevinModels(out)
	if len(families) == 0 {
		return nil, errors.New("devin models list: no models")
	}
	// which take images, which the list doesn't say
	return withDevinImages(families, devinImagesAt(ctx, home)), nil
}

// parseDevinModels reads `devin models list --format json`:
//
//	{"families": [{"family_label": "…", "family_uid": "…", "slug": "…",
//	  "aliases": ["…"], "variants": [{"model_uid": "…", "label": "…", …}]}]}
func parseDevinModels(b []byte) []DevinFamily {
	var list struct {
		Families []struct {
			FamilyUID   string   `json:"family_uid"`
			FamilyLabel string   `json:"family_label"`
			Aliases     []string `json:"aliases"`
			Variants    []struct {
				ModelUID string `json:"model_uid"`
				Label    string `json:"label"`
				// Devin's own numbers for the variant: how long a prompt it
				// takes and the most tokens its reply may hold
				Context int `json:"max_context_tokens"`
				Output  int `json:"max_output_tokens"`
			} `json:"variants"`
		} `json:"families"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	var out []DevinFamily
	for _, f := range list.Families {
		if f.FamilyUID == "" {
			continue
		}
		fam := DevinFamily{UID: f.FamilyUID, Label: f.FamilyLabel, Aliases: f.Aliases}
		if fam.Label == "" {
			fam.Label = f.FamilyUID
		}
		for _, v := range f.Variants {
			if v.ModelUID == "" {
				continue
			}
			name := v.Label
			if name == "" {
				name = v.ModelUID
			}
			fam.Models = append(fam.Models, catalog.Model{ID: v.ModelUID, Name: name, Provider: "devin",
				Context: v.Context, Output: v.Output})
		}
		out = append(out, fam)
	}
	return out
}

// devinAPIServer is where the CLI's Connect RPCs live; devinExchangeURL is
// where the callback's code trades for the credentials `devin auth login`
// would write — a var so tests can point it elsewhere.
const devinAPIServer = "https://server.codeium.com"

var devinExchangeURL = devinAPIServer + "/exa.seat_management_pb.SeatManagementService/ExchangeDevinCLIPKCECode"

// devinExchange trades the code for the credentials file `devin auth login`
// writes, then asks the CLI who signed in. The CLI's own login makes the same
// Connect call: the answer's sessionToken — already shaped
// "devin-session-token$<jwt>" — is the windsurf_api_key the file wants.
func devinExchange(ctx context.Context, code, verifier, redirect string) (savedLogin, error) {
	body, _ := json.Marshal(map[string]string{"code": code,
		"code_verifier": verifier, "redirect_uri": redirect})
	var res struct {
		SessionToken    string `json:"sessionToken"`
		SessionTokenAlt string `json:"session_token"`
		DevinWebappHost string `json:"devinWebappHost"`
		DevinAPIURL     string `json:"devinApiUrl"`
	}
	if err := postToken(ctx, devinExchangeURL, "application/json", body, &res); err != nil {
		return savedLogin{}, err
	}
	key := res.SessionToken
	if key == "" {
		key = res.SessionTokenAlt
	}
	if key == "" {
		return savedLogin{}, errors.New("Devin's exchange returned no session token")
	}
	creds := devinCredentials(key, devinAPIServer, res.DevinWebappHost, res.DevinAPIURL)
	if _, _, err := DevinAuth(); err == nil {
		// the CLI is signed in already: this account signs in in a home of
		// magpie's, beside it
		home, err := newDevinHome()
		if err != nil {
			return savedLogin{}, err
		}
		if err := writePrivate(devinCredentialsAt(home), creds); err != nil {
			removeDevinHome(home)
			return savedLogin{}, err
		}
		user, plan, err := addDevinLogin(home)
		if err != nil {
			removeDevinHome(home)
			return savedLogin{}, err
		}
		forgetAccountCaches()
		return savedLogin{Agent: "devin", User: user, Plan: plan, Home: home}, nil
	}
	// the CLI has no account: writing its file is signing it in, and puts
	// the file where `devin auth status` reads it
	if err := writePrivate(DevinCredentialsPath(), creds); err != nil {
		return savedLogin{}, err
	}
	forgetDevinStatus()
	forgetAccountCaches()
	user, plan, ok := askDevinIdentity()
	if !ok {
		// the account is only ever seen through the CLI: without it
		// answering, magpie has nothing to show and nothing to run
		return savedLogin{}, errors.New("signed in, but `devin auth status` doesn't show the account; run it in a terminal to see why")
	}
	return savedLogin{Agent: "devin", User: user, Plan: plan}, nil
}

// devinCredentials formats credentials.toml the way `devin auth login`
// writes it, with the hosts the exchange left out defaulted.
func devinCredentials(key, server, webapp, api string) []byte {
	if server == "" {
		server = "https://server.codeium.com"
	}
	if webapp == "" {
		webapp = "app.devin.ai"
	}
	if api == "" {
		api = "https://api.devin.ai"
	}
	var b strings.Builder
	for _, kv := range [][2]string{
		{"windsurf_api_key", key},
		{"api_server_url", server},
		{"devin_webapp_host", webapp},
		{"devin_api_url", api},
	} {
		fmt.Fprintf(&b, "%s = %s\n", kv[0], tomlString(kv[1]))
	}
	return []byte(b.String())
}

// tomlString quotes a TOML basic string; the values are plain ASCII tokens
// and URLs, so only the string's own delimiters need escaping.
func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// DevinAuth is the key the CLI signed in with and the server its API is
// on, read from credentials.toml; WINDSURF_API_SERVER_URL moves the server,
// as it does the CLI's.
func DevinAuth() (key, server string, err error) { return DevinAuthAt("") }

// DevinAuthAt is DevinAuth for the account signed in in home, "" for the
// CLI's own.
func DevinAuthAt(home string) (key, server string, err error) {
	path := devinCredentialsAt(home)
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if home != "" {
				return "", "", errors.New("this Devin account's sign-in is gone; add it again in magpie")
			}
			return "", "", errors.New("Devin isn't signed in; sign in from magpie's Providers page or run `devin auth login`")
		}
		return "", "", err
	}
	var c struct {
		Key    string `toml:"windsurf_api_key"`
		Server string `toml:"api_server_url"`
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		return "", "", fmt.Errorf("%s: %w", path, err)
	}
	if c.Key == "" {
		return "", "", errors.New("Devin isn't signed in: " + path + " has no key")
	}
	server = strings.TrimRight(c.Server, "/")
	if v := os.Getenv("WINDSURF_API_SERVER_URL"); v != "" {
		server = strings.TrimRight(v, "/")
	}
	if server == "" {
		server = devinAPIServer
	}
	return c.Key, server, nil
}

// DevinVariant is the model to ask Devin's API for: a family's name
// follows its newest model, which the API takes only as one of its
// variants — the one at the effort asked for, or at the nearest effort
// the family has; with none asked, the family's default. A family's fast
// run (claude-opus-5-5-fast) is likewise its fast variant at that effort
// (claude-opus-5-5-high-fast). A variant's own id (swe-2-medium, one an
// agent was set to) goes as it is, whatever effort is asked: its id is the
// effort it runs at.
func DevinVariant(ctx context.Context, model, effort string) string {
	families, err := DevinFamilies(ctx)
	if err != nil {
		return model
	}
	return devinVariantIn(families, model, effort)
}

func devinVariantIn(families []DevinFamily, model, effort string) string {
	for _, f := range families {
		if !f.named(model) || len(f.Models) == 0 {
			continue
		}
		if effort != "" {
			if id := f.byLevel("")[devinNearest(effort, f.efforts(""))]; id != "" {
				return id
			}
		}
		return f.Models[0].ID
	}
	// a family's fast run, which isn't an id of Devin's own: its variant
	// at the effort asked, or at the family's default effort
	for _, f := range families {
		for _, tier := range f.tiers() {
			base, ok := strings.CutSuffix(model, "-"+tier)
			if !ok || !f.named(base) {
				continue
			}
			if effort == "" {
				effort = f.defaultLevel()
			}
			by, levels := f.byLevel(tier), f.efforts(tier)
			if id := by[devinNearest(effort, levels)]; id != "" {
				return id
			}
			return by[levels[0]]
		}
	}
	return model
}

// DevinSplit is an id Devin's config holds taken apart as magpie offers it:
// the model (a family, or a family's fast run) and the effort the id is at.
// A family's own id is at none — it follows the family's default — and an id
// no family has at an effort (glm-5-2-1m, one Devin no longer lists) is
// itself, at none.
func DevinSplit(families []DevinFamily, id string) (model, effort string) {
	for _, f := range families {
		for _, m := range f.Models {
			if m.ID != id || m.ID == f.UID {
				continue
			}
			tier, level := devinTierOf(id)
			if level == "" {
				return id, ""
			}
			if tier != "" {
				return f.UID + "-" + tier, level
			}
			return f.UID, level
		}
	}
	return id, ""
}

// DevinPick is the id to give Devin's config for a model and effort picked as
// magpie offers them (DevinSplit the other way round): a family's variant at
// the effort, or the nearest it has; the family's own id with no effort, so
// that it keeps following the family's default; a fast run's variant at the
// effort, or at the family's default one. Any other id goes as it is.
func DevinPick(families []DevinFamily, model, effort string) string {
	for _, f := range families {
		if f.named(model) && effort == "" {
			return model
		}
	}
	return devinVariantIn(families, model, effort)
}

// DevinOffered is Devin's list as an agent's picker offers it: each family
// one model with the efforts its variants are at, as the provider lists it
// (devinModels), and Adaptive and Fusion — which Devin's own agent runs,
// though the API doesn't serve them — as the one model each.
func DevinOffered(families []DevinFamily) []catalog.Model {
	var out []catalog.Model
	for _, f := range families {
		ms := devinModels([]DevinFamily{f})
		if len(ms) == 0 {
			ms = []catalog.Model{{ID: f.UID, Name: f.Label, Provider: "devin"}}
		}
		out = append(out, ms...)
	}
	return out
}

// named reports whether id is the family's own id or one of its aliases.
func (f DevinFamily) named(id string) bool {
	return f.UID == id || slices.Contains(f.Aliases, id)
}

// defaultLevel is the effort of the variant the family's id asks for when
// none is asked (its first), medium when that variant names none.
func (f DevinFamily) defaultLevel() string {
	if len(f.Models) > 0 {
		if tier, l := devinTierOf(f.Models[0].ID); tier == "" && l != "" {
			return l
		}
	}
	return "medium"
}

// devinLevels are the effort words Devin's variant ids end in (swe-2-high,
// …_HIGH, gpt-6-sol-none), and the level each is.
var devinLevels = []struct{ word, level string }{
	{"none", "none"}, {"xhigh", "xhigh"}, {"minimal", "minimal"}, {"min", "minimal"},
	{"low", "low"}, {"medium", "medium"}, {"high", "high"}, {"max", "max"},
}

// devinTiers are the words a variant id may end in after its effort, for a
// quicker run of the same model at that effort: claude-opus-5-5-high-fast,
// gpt-6-sol-high-priority. A family's variants in a tier are offered as one
// model of their own, the family's id and the word (claude-opus-5-5-fast),
// with the efforts they are at.
var devinTiers = []struct{ word, label string }{{"fast", "Fast"}, {"priority", "Priority"}}

// devinLevel is the effort a Devin id says it runs at, "" when it says
// none (glm-5-2-1m) or is in a tier (claude-opus-5-5-high-fast, whose
// effort devinTierOf finds).
func devinLevel(id string) string {
	id = strings.ToLower(id)
	for _, l := range devinLevels {
		if strings.HasSuffix(id, "-"+l.word) || strings.HasSuffix(id, "_"+l.word) {
			return l.level
		}
	}
	return ""
}

// devinTierOf is the tier a Devin id is in and the effort it runs at:
// claude-opus-5-5-high-fast is ("fast", "high"), swe-2-high ("", "high").
// A tier word with no effort before it (swe-1-6-fast) is no tier: that id
// is a model of its own, at none.
func devinTierOf(id string) (tier, level string) {
	low := strings.ToLower(id)
	for _, t := range devinTiers {
		for _, sep := range []string{"-", "_"} {
			if rest, ok := strings.CutSuffix(low, sep+t.word); ok {
				if l := devinLevel(rest); l != "" {
					return t.word, l
				}
			}
		}
	}
	return "", devinLevel(id)
}

// devinBase is the model a Devin variant id is a variant of: the id less
// its tier and effort words (claude-opus-5-5-low-fast is claude-opus-5-5,
// swe-2-high swe-2); an id at no effort is itself.
func devinBase(id string) string {
	tier, level := devinTierOf(id)
	if level == "" {
		return id
	}
	low := strings.ToLower(id)
	cut := func(word string) bool {
		for _, sep := range []string{"-", "_"} {
			if strings.HasSuffix(low, sep+word) {
				low, id = low[:len(low)-len(sep+word)], id[:len(low)-len(sep+word)]
				return true
			}
		}
		return false
	}
	if tier != "" {
		cut(tier)
	}
	for _, l := range devinLevels {
		if l.level == level && cut(l.word) {
			break
		}
	}
	return id
}

// devinEffortOf is the effort a Devin id runs at, in a tier or not.
func devinEffortOf(id string) string {
	_, l := devinTierOf(id)
	return l
}

// byLevel is the family's variant at each effort its ids say, in the tier
// ("" for none), the first at each.
func (f DevinFamily) byLevel(tier string) map[string]string {
	out := map[string]string{}
	for _, m := range f.Models {
		if t, l := devinTierOf(m.ID); t == tier && l != "" && m.ID != f.UID && out[l] == "" {
			out[l] = m.ID
		}
	}
	return out
}

// efforts are the levels the family's variants in the tier are at, lowest
// first.
func (f DevinFamily) efforts(tier string) []string {
	by := f.byLevel(tier)
	var out []string
	for _, l := range cursorLevelRank {
		if by[l] != "" {
			out = append(out, l)
		}
	}
	return out
}

// tiers are the tiers the family has variants at an effort in.
func (f DevinFamily) tiers() []string {
	var out []string
	for _, t := range devinTiers {
		if len(f.byLevel(t.word)) > 0 {
			out = append(out, t.word)
		}
	}
	return out
}

// devinTierLabel is how a tier's model is named after its family's.
func devinTierLabel(tier string) string {
	for _, t := range devinTiers {
		if t.word == tier {
			return t.label
		}
	}
	return tier
}

// devinNearest is the one of levels nearest the level asked for, a tie
// going up, as the gateway fits an effort to a model's levels.
func devinNearest(want string, levels []string) string {
	at := slices.Index(cursorLevelRank, want)
	if at < 0 || len(levels) == 0 || slices.Contains(levels, want) {
		return want
	}
	best, dist := want, len(cursorLevelRank)
	for _, l := range levels {
		i := slices.Index(cursorLevelRank, l)
		if d := max(i-at, at-i); d < dist || d == dist && i > at {
			best, dist = l, d
		}
	}
	return best
}

// devinModels is Devin's list as magpie offers it: each family one model,
// whose effort is picked as any model's is and turned into the variant at
// it by the gateway (DevinVariant), rather than 600-odd ids that are
// mostly one family at an effort each. See devinCollapse.
func devinModels(families []DevinFamily) []catalog.Model {
	return devinCollapse(devinModelsFlatten(families), families, nil)
}

// devinCollapse is a list of Devin's ids with each family one model: the
// family's id, with the efforts its variants are at, and its fast run
// (claude-opus-5-5-fast) with the efforts its fast variants are at. A
// variant at an effort, fast or not, is left out, as is a family's only
// variant, which the family's id already asks for — unless the user picked
// it (keep) before the families were one model: it stays, at the one effort
// its id is at, and goes to Devin as it is. A variant at none (glm-5-2-1m)
// stays. families is the CLI list last read; without it the list is as
// saved, which a fetch already collapsed.
func devinCollapse(ms []catalog.Model, families []DevinFamily, keep []string) []catalog.Model {
	efforts := map[string][]string{}
	variants := map[string]catalog.Model{}
	drop := map[string]bool{}
	for _, f := range families {
		if e := f.efforts(""); len(e) > 0 {
			efforts[f.UID] = e
		}
		for _, t := range f.tiers() {
			efforts[f.UID+"-"+t] = f.efforts(t)
		}
		for _, m := range f.Models {
			if m.ID == f.UID {
				continue
			}
			variants[m.ID] = m
			if devinEffortOf(m.ID) != "" || len(f.Models) == 1 {
				drop[m.ID] = true
			}
		}
	}
	var out []catalog.Model
	seen := map[string]bool{}
	for _, m := range ms {
		if seen[m.ID] || drop[m.ID] && !slices.Contains(keep, m.ID) {
			continue
		}
		if len(m.Efforts) == 0 {
			if e, ok := efforts[m.ID]; ok {
				m.Efforts = e
			} else if l := devinEffortOf(m.ID); l != "" {
				m.Efforts = []string{l}
			}
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	// a picked variant a saved list no longer has
	for _, id := range keep {
		m, known := variants[id]
		if seen[id] || !known && devinEffortOf(id) == "" {
			continue
		}
		m.ID, m.Provider = id, "devin"
		if m.Name == "" {
			m.Name = id
		}
		if l := devinEffortOf(id); l != "" {
			m.Efforts = []string{l}
		}
		seen[id] = true
		out = append(out, m)
	}
	return out
}
