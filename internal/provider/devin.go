package provider

// A Devin subscription is used the way a Cursor one is: through the vendor's
// own agent. Devin has no endpoint a key can be sent to, so magpie runs the
// genuine devin CLI (gateway/devin_subscription.go drives `devin acp`) with
// the account it is signed in to; here is only who that account is, the
// models it offers, and the sign-in, which is `devin auth login`.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

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

var devinStatus struct {
	sync.Mutex
	at         time.Time
	refreshing bool
	user, plan string
	ok         bool
}

// devinIdentity asks the CLI who is signed in. `devin auth status` takes a
// moment, so after the first answer a stale one is served while a fresh one
// is fetched behind it.
func devinIdentity() (user, plan string, ok bool) {
	devinStatus.Lock()
	defer devinStatus.Unlock()
	if devinStatus.at.IsZero() {
		devinStatus.user, devinStatus.plan, devinStatus.ok = askDevinIdentity()
		devinStatus.at = time.Now()
	} else if time.Since(devinStatus.at) > time.Minute && !devinStatus.refreshing {
		devinStatus.refreshing = true
		go func() {
			u, p, ok := askDevinIdentity()
			devinStatus.Lock()
			devinStatus.user, devinStatus.plan, devinStatus.ok = u, p, ok
			devinStatus.at, devinStatus.refreshing = time.Now(), false
			devinStatus.Unlock()
		}()
	}
	return devinStatus.user, devinStatus.plan, devinStatus.ok
}

func forgetDevinStatus() {
	devinStatus.Lock()
	devinStatus.at = time.Time{}
	devinStatus.Unlock()
}

func askDevinIdentity() (user, plan string, ok bool) {
	path := DevinExecutable()
	if path == "" {
		return "", "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "auth", "status").Output()
	if err != nil {
		return "", "", false
	}
	return parseDevinStatus(string(out))
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
// follow-newest id, then each of its variants.
func devinModelsFlatten(families []DevinFamily) []catalog.Model {
	var out []catalog.Model
	for _, f := range families {
		out = append(out, catalog.Model{ID: f.UID, Name: f.Label, Provider: "devin"})
		out = append(out, f.Models...)
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
	out, err := exec.CommandContext(ctx, path, "models", "list", "--format", "json").Output()
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
			fam.Models = append(fam.Models, catalog.Model{ID: v.ModelUID, Name: name, Provider: "devin"})
		}
		out = append(out, fam)
	}
	return out
}

// startDevinSignIn runs `devin auth login`, hands the link it prints to the
// window, and finishes when the CLI says the account is in.
func startDevinSignIn(s *signInFlow) error {
	path := DevinExecutable()
	if path == "" {
		return errorf("install Devin's CLI first: https://docs.devin.ai/cli")
	}
	return runCLISignIn(s, "devin auth login", os.Environ(), true, nil, func() (string, string, bool) {
		forgetDevinStatus()
		return askDevinIdentity()
	}, path, "auth", "login")
}
