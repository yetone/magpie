package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// CC Switch's Codex config, as it writes it: its "custom" table, one of
// its older versions' named by a profile, and a table of the user's own.
const ccSwitchCodex = "model_provider = \"custom\"\nmodel = \"gpt-5.4\"\nmodel_reasoning_effort = \"high\"\n\n" +
	"[model_providers.custom]\nname = \"custom\"\nbase_url = \"https://relay.example/v1\"\nwire_api = \"responses\"\nrequires_openai_auth = true\nexperimental_bearer_token = \"sk-relay\"\n\n" +
	"[model_providers.cc-switch-2]\nname = \"old\"\nbase_url = \"https://old.example/v1\"\n\n" +
	"[model_providers.mine]\nname = \"mine\"\nbase_url = \"https://mine.example/v1\"\n\n" +
	"[profiles.work]\nmodel_provider = \"cc-switch-2\"\n"

// A Codex thread keeps the provider it was started on, and CC Switch puts
// every third-party one on its "custom" table: reopened after magpie took
// Codex over, it still went to the relay, a magpie model picked in it
// too, and the relay said the model wasn't found. While a magpie model is
// on, CC Switch's tables go through magpie too, and get their own base URL
// back when Codex steps off magpie; a profile's table and the user's own
// stay as they are.
func TestCodexTakesCCSwitchTables(t *testing.T) {
	for _, auth := range []string{`{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, ""} {
		home, read := codexHome(t, auth, ccSwitchCodex)
		path := filepath.Join(home, ".codex", "config.toml")
		cx := codex(home)
		table := func(id string) map[string]string {
			tb, err := edit.GetTOMLTable(path, "model_providers."+id)
			if err != nil {
				t.Fatal(err)
			}
			return tb
		}
		v1 := here(home).v1()
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		if c := table("custom"); c["base_url"] != v1 || c["experimental_bearer_token"] != "sk-relay" || c["requires_openai_auth"] != "true" {
			t.Fatalf("auth %q: custom not through magpie:\n%s", auth, read())
		}
		if table("cc-switch-2")["base_url"] != "https://old.example/v1" || table("mine")["base_url"] != "https://mine.example/v1" {
			t.Fatalf("auth %q: other tables changed:\n%s", auth, read())
		}
		if d := cx.Check(); d != "" {
			t.Fatalf("auth %q: check: %s", auth, d)
		}
		// CC Switch writes its table again: the row says so, and magpie's
		// next sync takes it over again
		if err := edit.SetTOMLKey(path, "model_providers.custom", "base_url", "https://relay2.example/v1"); err != nil {
			t.Fatal(err)
		}
		if d := cx.Check(); !strings.Contains(d, "[model_providers.custom]") || !strings.Contains(d, "relay2.example") {
			t.Fatalf("auth %q: check: %q", auth, d)
		}
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		if table("custom")["base_url"] != v1 || cx.Check() != "" {
			t.Fatalf("auth %q: sync:\n%s", auth, read())
		}
		// one of Codex's own models: the table's own base URL is back
		if err := cx.Fields[0].Set("gpt-5.4"); err != nil {
			t.Fatal(err)
		}
		if table("custom")["base_url"] != "https://relay2.example/v1" {
			t.Fatalf("auth %q: own model:\n%s", auth, read())
		}
		// and a reset gives it back too
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		if err := cx.Fields[0].Set(""); err != nil {
			t.Fatal(err)
		}
		if table("custom")["base_url"] != "https://relay2.example/v1" {
			t.Fatalf("auth %q: reset:\n%s", auth, read())
		}
		if _, ok := stashLoad()[here(home).key("codex.tables")]; ok {
			t.Errorf("auth %q: stash left", auth)
		}
	}
}

// A CC Switch table the user pointed at magpie by hand stays so when Codex
// steps off magpie: magpie gives back only what it took.
func TestCodexKeepsHandPointedCCSwitchTable(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	v1 := here(home).v1()
	os.WriteFile(filepath.Join(home, ".codex", "config.toml"),
		[]byte("[model_providers.custom]\nname = \"custom\"\nbase_url = \""+v1+"\"\nwire_api = \"responses\"\n"), 0o644)
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Fields[0].Set(""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(), `base_url = "`+v1+`"`) {
		t.Fatalf("\n%s", read())
	}
}

// CC Switch's official OpenAI provider mirror writes model_provider = "custom"
// and a [model_providers.custom] table with name = "OpenAI", requires_openai_auth = true
// and no base_url (#504): it is the official provider, so its models are grouped
// under OpenAI, and neither it nor a relay's table is pointed at magpie for a
// Codex not connected while another account is on (TylerBai_loveai on X).
func TestCodexCCSwitchOfficialOpenAIMirror(t *testing.T) {
	const ccSwitchOfficialMirror = "model_provider = \"custom\"\nmodel = \"gpt-5.4\"\n\n" +
		"[model_providers.custom]\nname = \"OpenAI\"\nrequires_openai_auth = true\nsupports_websockets = true\nwire_api = \"responses\"\n"

	writeCache := func(h string) {
		dir := filepath.Join(h, ".codex")
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(`{"models":[
			{"slug":"gpt-5.4","display_name":"5.4","priority":1},
			{"slug":"gpt-5.5","display_name":"5.5","priority":2}]}`), 0o644)
	}

	// 1. Grouped under OpenAI, while custom tables that differ stay in their own group
	home, _ := codexHome(t, "", ccSwitchOfficialMirror)
	writeCache(home)
	cx := codex(home)
	opts := cx.Fields[0].Options(nil)
	if len(opts) == 0 || opts[0].Group != "OpenAI" {
		t.Fatalf("mirror grouped under %q, want OpenAI", opts[0].Group)
	}

	homeRelay, _ := codexHome(t, "", ccSwitchCodex)
	writeCache(homeRelay)
	cxRelay := codex(homeRelay)
	optsRelay := cxRelay.Fields[0].Options(nil)
	if len(optsRelay) == 0 || optsRelay[0].Group != "custom" {
		t.Fatalf("relay grouped under %q, want custom", optsRelay[0].Group)
	}

	// A table named "OpenAI" with a base_url is a relay, not the official mirror
	const relayNamedOpenAI = "model_provider = \"custom\"\nmodel = \"gpt-5.4\"\n\n" +
		"[model_providers.custom]\nname = \"OpenAI\"\nbase_url = \"https://relay.example/v1\"\nrequires_openai_auth = true\n"
	homeRelayName, _ := codexHome(t, "", relayNamedOpenAI)
	writeCache(homeRelayName)
	if opts := codex(homeRelayName).Fields[0].Options(nil); len(opts) == 0 || opts[0].Group != "custom" {
		t.Fatalf("relay with base_url grouped under %q, want custom", opts[0].Group)
	}

	// A table with no base_url but name != "OpenAI" is left alone
	const customOtherName = "model_provider = \"custom\"\nmodel = \"gpt-5.4\"\n\n" +
		"[model_providers.custom]\nname = \"MyCustom\"\nrequires_openai_auth = true\n"
	homeOther, _ := codexHome(t, "", customOtherName)
	writeCache(homeOther)
	if opts := codex(homeOther).Fields[0].Options(nil); len(opts) == 0 || opts[0].Group != "custom" {
		t.Fatalf("other custom name grouped under %q, want custom", opts[0].Group)
	}

	// 2. Account failover points the mirror table's base_url at magpie, not openai_base_url
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	me, _ := json.Marshal(auth("me@example.com", "acct-1"))
	homeFailover, readFailover := codexHome(t, string(me), ccSwitchOfficialMirror)
	pathFailover := filepath.Join(homeFailover, ".codex", "config.toml")
	cxFailover := codex(homeFailover)
	logins := func(on bool) {
		b, _ := json.Marshal([]map[string]any{{"agent": "codex", "user": "spare@example.com", "on": on,
			"seen": time.Now(), "auth": auth("spare@example.com", "acct-2")}})
		os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
		os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), b, 0o600)
	}
	logins(true)
	for i := 0; i < 2; i++ {
		if err := cxFailover.Sync(); err != nil {
			t.Fatal(err)
		}
		if got := readFailover(); got != ccSwitchOfficialMirror {
			t.Fatalf("sync %d with another account on changed CC Switch's mirror:\ngot:\n%s\nwant:\n%s", i, got, ccSwitchOfficialMirror)
		}
	}
	_ = pathFailover

	// Real relays with a base_url never get pointed at magpie gateway for failover
	homeRelayFailover, readRelayFailover := codexHome(t, string(me), ccSwitchCodex)
	cxRelayFailover := codex(homeRelayFailover)
	pathRelay := filepath.Join(homeRelayFailover, ".codex", "config.toml")
	logins(true)
	if err := cxRelayFailover.Sync(); err != nil {
		t.Fatal(err)
	}
	tbRelay, _ := edit.GetTOMLTable(pathRelay, "model_providers.custom")
	if tbRelay["base_url"] != "https://relay.example/v1" {
		t.Fatalf("relay base_url changed to %q", tbRelay["base_url"])
	}
	if strings.Contains(readRelayFailover(), "openai_base_url") {
		t.Fatalf("real relay got openai_base_url:\n%s", readRelayFailover())
	}

	// Switching to a relay while failover from before was on the mirror clears the marker
	// without deleting the relay's own base_url on Sync or Set.
	homeSwitch, readSwitch := codexHome(t, string(me), ccSwitchOfficialMirror)
	cxSwitch := codex(homeSwitch)
	pathSwitch := filepath.Join(homeSwitch, ".codex", "config.toml")
	logins(true)
	// mirror failover as a magpie from before left it, marker and all
	if err := edit.SetTOMLKey(pathSwitch, "model_providers.custom", "base_url", here(homeSwitch).codexURL()); err != nil {
		t.Fatal(err)
	}
	stash(map[string]string{"codex.mirror_failover": "custom"})
	// User switches CC Switch to a relay, reusing the custom table
	if err := os.WriteFile(pathSwitch, []byte(ccSwitchCodex), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cxSwitch.Sync(); err != nil {
		t.Fatal(err)
	}
	// The relay's own base_url must survive Sync and subsequent model sets
	tbAfterSync, _ := edit.GetTOMLTable(pathSwitch, "model_providers.custom")
	if tbAfterSync["base_url"] != "https://relay.example/v1" {
		t.Fatalf("relay base_url after Sync = %q, want https://relay.example/v1", tbAfterSync["base_url"])
	}
	modelField := cxSwitch.Fields[0]
	if err := modelField.Set(""); err != nil {
		t.Fatal(err)
	}
	tbAfterSetEmpty, _ := edit.GetTOMLTable(pathSwitch, "model_providers.custom")
	if tbAfterSetEmpty["base_url"] != "https://relay.example/v1" {
		t.Fatalf("relay base_url after Set(\"\") = %q, want https://relay.example/v1", tbAfterSetEmpty["base_url"])
	}
	_ = readSwitch
}

// CC Switch's OpenAI mirror ("custom": name OpenAI, the ChatGPT sign-in, no
// base_url) left in config.toml after the user moved Codex onto the
// built-in provider and a magpie model. Codex's openai_base_url, which
// takes the built-in provider to magpie, doesn't reach a table of another
// id, so a thread started on "custom" and reopened went straight to
// ChatGPT, which refused the magpie model picked in it, and magpie never
// saw the request (#1796, WardLu). While Codex goes to magpie by the base
// URL the mirror does too; a profile's mirror is the user's to pick, and
// stepping off magpie takes the base URL off again.
func TestCodexTakesCCSwitchMirrorForItsThreads(t *testing.T) {
	const cfg = "model = \"gpt-5.4\"\n\n" +
		"[model_providers.custom]\nname = \"OpenAI\"\nrequires_openai_auth = true\nsupports_websockets = true\nwire_api = \"responses\"\n\n" +
		"[model_providers.cc-switch-3]\nname = \"OpenAI\"\nrequires_openai_auth = true\nwire_api = \"responses\"\n\n" +
		"[profiles.p]\nmodel_provider = \"cc-switch-3\"\n"
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, cfg)
	path := filepath.Join(home, ".codex", "config.toml")
	cx := codex(home)
	table := func(id string) map[string]string {
		tb, err := edit.GetTOMLTable(path, "model_providers."+id)
		if err != nil {
			t.Fatal(err)
		}
		return tb
	}
	at := here(home)
	for round, off := range []string{"gpt-5.4", ""} {
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		if c := table("custom"); c["base_url"] != at.codexURL() || c["requires_openai_auth"] != "true" || c["name"] != "OpenAI" {
			t.Fatalf("round %d: the mirror isn't through magpie:\n%s", round, read())
		}
		if _, ok := table("cc-switch-3")["base_url"]; ok {
			t.Fatalf("round %d: the profile's mirror changed:\n%s", round, read())
		}
		if d := cx.Check(); d != "" {
			t.Fatalf("round %d: check: %s\n%s", round, d, read())
		}
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		if table("custom")["base_url"] != at.codexURL() || cx.Check() != "" {
			t.Fatalf("round %d: sync:\n%s", round, read())
		}
		if err := cx.Fields[0].Set(off); err != nil {
			t.Fatal(err)
		}
		if _, ok := table("custom")["base_url"]; ok {
			t.Fatalf("round %d: off magpie (%q), the mirror keeps magpie's URL:\n%s", round, off, read())
		}
		if _, ok := stashLoad()[at.key("codex.mirrors")]; ok {
			t.Errorf("round %d: stash left", round)
		}
	}
	// one the user changed while magpie had it stays as they left it
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetTOMLKey(path, "model_providers.custom", "base_url", "https://mine.example/v1"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Fields[0].Set(""); err != nil {
		t.Fatal(err)
	}
	if table("custom")["base_url"] != "https://mine.example/v1" {
		t.Fatalf("hand-set base_url lost:\n%s", read())
	}
}
