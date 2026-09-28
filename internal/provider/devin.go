package provider

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

var devinStatus = &cliIdentity{name: "devin", exe: func() string { return DevinExecutable() }, ask: func() (string, string, bool, error) { return askDevinIdentity() }}

// devinIdentity is who Devin's CLI says is signed in; see cliIdentity.
func devinIdentity() (user, plan string, ok bool) { return devinStatus.get() }

func forgetDevinStatus() { devinStatus.forget() }

// askDevinIdentity asks `devin auth status`; an error is a CLI that didn't
// answer, not one saying nobody is signed in — signed in, it asks Devin's
// server who, which takes seconds and can outrun the timeout on a slow
// network; signed out, it says "Not logged in." from its credentials file.
func askDevinIdentity() (user, plan string, ok bool, err error) {
	path := DevinExecutable()
	if path == "" {
		return "", "", false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := agentCommand(ctx, path, "auth", "status").Output()
	if err != nil {
		return "", "", false, fmt.Errorf("devin auth status: %w", err)
	}
	if !strings.Contains(string(out), "Logged in") && !strings.Contains(string(out), "Not logged in") {
		return "", "", false, errors.New("devin auth status: says neither signed in nor out")
	}
	user, plan, ok = parseDevinStatus(string(out))
	return user, plan, ok, nil
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

func devinAccount() (Provider, bool) {
	user, plan, ok := devinIdentity()
	if !ok {
		return Provider{}, false
	}
	acct := &Account{Agent: "devin", User: user, Plan: plan}
	acct.models = func() []catalog.Model {
		// what a fetch or a picker visit last asked the CLI — never spawn
		// one here: Available() runs on every gateway request
		devinFamiliesCache.Lock()
		families := devinFamiliesCache.families
		devinFamiliesCache.Unlock()
		return devinModelsFlatten(families)
	}
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		families, err := askDevinFamilies(ctx)
		if err != nil {
			return nil, err
		}
		ms := devinModelsFlatten(families)
		devinFamiliesCached(families)
		return ms, catalog.SaveLive("devin", "", ms)
	}
	return Provider{ID: "devin", Name: "Devin", Icon: "devin", Website: "https://devin.ai", Account: acct}, true
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
		out = append(out, catalog.Model{ID: f.UID, Name: f.Label, Provider: "devin", Context: window, Output: most})
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
	devinFamiliesCache.Lock()
	families := devinFamiliesCache.families
	devinFamiliesCache.Unlock()
	out := map[string][2]int{}
	for _, f := range families {
		window, most := f.window(), f.reply()
		if window > 0 || most > 0 {
			out[f.UID] = [2]int{window, most}
			for _, a := range f.Aliases {
				out[a] = [2]int{window, most}
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

var devinEffort = regexp.MustCompile(`-(min|low|medium|high|xhigh|max|fast)$`)

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
	out := slices.Clone(ms)
	for i, m := range out {
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
}

// DevinFamilies is the CLI's model list, kept a few minutes: the picker and
// the provider ask for it often and `devin models list` spawns a process.
func DevinFamilies(ctx context.Context) ([]DevinFamily, error) {
	devinFamiliesCache.Lock()
	defer devinFamiliesCache.Unlock()
	if time.Since(devinFamiliesCache.at) < 5*time.Minute && len(devinFamiliesCache.families) > 0 {
		return devinFamiliesCache.families, nil
	}
	families, err := askDevinFamilies(ctx)
	if err != nil {
		return nil, err
	}
	devinFamiliesCache.families, devinFamiliesCache.at = families, time.Now()
	return families, nil
}

func devinFamiliesCached(families []DevinFamily) {
	devinFamiliesCache.Lock()
	devinFamiliesCache.families, devinFamiliesCache.at = families, time.Now()
	devinFamiliesCache.Unlock()
}

func askDevinFamilies(ctx context.Context) ([]DevinFamily, error) {
	path := DevinExecutable()
	if path == "" {
		return nil, errors.New("devin is not installed")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := agentCommand(ctx, path, "models", "list", "--format", "json").Output()
	if err != nil {
		return nil, errorf("devin models list: %v", err)
	}
	families := parseDevinModels(out)
	if len(families) == 0 {
		return nil, errors.New("devin models list: no models")
	}
	return families, nil
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
	// devin keeps the one account it is signed in to: writing the file is
	// signing it in, and puts the file where `devin auth status` reads it
	if err := writePrivate(DevinCredentialsPath(), creds); err != nil {
		return savedLogin{}, err
	}
	forgetDevinStatus()
	forgetAccountCaches()
	user, plan, ok, _ := askDevinIdentity()
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
func DevinAuth() (key, server string, err error) {
	b, err := os.ReadFile(DevinCredentialsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", errors.New("Devin isn't signed in; sign in from magpie's Providers page or run `devin auth login`")
		}
		return "", "", err
	}
	var c struct {
		Key    string `toml:"windsurf_api_key"`
		Server string `toml:"api_server_url"`
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		return "", "", fmt.Errorf("%s: %w", DevinCredentialsPath(), err)
	}
	if c.Key == "" {
		return "", "", errors.New("Devin isn't signed in: " + DevinCredentialsPath() + " has no key")
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
// variants — the one at the effort asked for, else the family's default.
func DevinVariant(ctx context.Context, model, effort string) string {
	families, err := DevinFamilies(ctx)
	if err != nil {
		return model
	}
	for _, f := range families {
		if f.UID != model && !slices.Contains(f.Aliases, model) || len(f.Models) == 0 {
			continue
		}
		if effort != "" {
			for _, m := range f.Models {
				if strings.HasSuffix(strings.ToLower(m.ID), "-"+effort) || strings.HasSuffix(m.ID, "_"+strings.ToUpper(effort)) {
					return m.ID
				}
			}
		}
		return f.Models[0].ID
	}
	return model
}
