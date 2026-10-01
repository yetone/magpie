package settings

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/redact"
)

func TestRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if s := Load(); s.Theme != "system" || s.Lang != "system" {
		t.Fatalf("defaults: %+v", s)
	}
	if err := Save(Settings{Theme: "dark", Lang: "zh"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Theme != "dark" || s.Lang != "zh" {
		t.Fatalf("saved: %+v", s)
	}
	if err := Save(Settings{Theme: "sepia"}); err == nil {
		t.Fatal("bad theme accepted")
	}
	if Save(Settings{}) != nil || Load().Theme != "system" {
		t.Fatal("empty means system")
	}
	// costs show in dollars unless cny is chosen
	if s := Load(); s.Currency != "usd" {
		t.Fatalf("default currency: %+v", s)
	}
	if err := Save(Settings{Currency: "cny"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Currency != "cny" {
		t.Fatalf("saved currency: %+v", s)
	}
	if Save(Settings{Currency: "eur"}) == nil {
		t.Fatal("bad currency accepted")
	}
	if Load().SessionTerminal != "" {
		t.Fatal("the system handler should be the default")
	}
	if err := Save(Settings{SessionTerminal: "com.mitchellh.ghostty"}); err != nil || Load().SessionTerminal != "com.mitchellh.ghostty" {
		t.Fatalf("session terminal not kept: %v", err)
	}
	if err := Save(Settings{SessionTerminal: "system"}); err != nil || Load().SessionTerminal != "system" {
		t.Fatalf("system terminal not kept: %v", err)
	}
	if Save(Settings{SessionTerminal: "Ghostty; rm -rf /"}) == nil {
		t.Fatal("bad terminal app id accepted")
	}
	// the text size is 100% until one of the sizes is chosen
	if Save(Settings{}) != nil || Load().TextSize != 100 {
		t.Fatalf("default text size: %+v", Load())
	}
	if Save(Settings{TextSize: 125}) != nil || Load().TextSize != 125 {
		t.Fatal("text size not kept")
	}
	for _, bad := range []int{90, 120, 300, -1} {
		if Save(Settings{TextSize: bad}) == nil {
			t.Fatalf("text size %d accepted", bad)
		}
	}
	if filepath.Base(Path()) != "settings.json" {
		t.Fatal(Path())
	}
	if Save(Settings{CodexWarmup: "week"}) != nil || Load().CodexWarmup != "week" {
		t.Fatal("codex warm-up not kept")
	}
	if Save(Settings{CodexWarmup: "off"}) != nil || Load().CodexWarmup != "" {
		t.Fatal("off is off")
	}
	if Save(Settings{CodexWarmup: "daily"}) == nil {
		t.Fatal("bad codex warm-up accepted")
	}
	if Save(Settings{ClaudeWarmup: "all"}) != nil || Load().ClaudeWarmup != "all" {
		t.Fatal("claude warm-up not kept")
	}
	if Save(Settings{ClaudeWarmup: "hourly"}) == nil {
		t.Fatal("bad claude warm-up accepted")
	}
	// a time of day to start the 5-hour windows at, as 06:00 however given
	if Save(Settings{CodexWarmAt: "6:00", ClaudeWarmAt: "21:30:00"}) != nil || Load().CodexWarmAt != "06:00" || Load().ClaudeWarmAt != "21:30" {
		t.Fatalf("warm-up times not kept: %+v", Load())
	}
	for _, bad := range []string{"25:00", "6am", "06:60"} {
		if Save(Settings{CodexWarmAt: bad}) == nil {
			t.Fatalf("bad time %q accepted", bad)
		}
	}
	if Save(Settings{}) != nil || Load().CodexWarmAt != "" {
		t.Fatal("no time is off")
	}
	// the menu bar's usage: every 3 minutes unless told, left or used
	if s := Load(); s.TrayUsageEvery != 3 || s.QuotaLeft {
		t.Fatalf("tray defaults: %+v", s)
	}
	if Save(Settings{TrayUsageEvery: 10, QuotaLeft: true}) != nil || Load().TrayUsageEvery != 10 || !Load().QuotaLeft {
		t.Fatal("tray refresh or left not kept")
	}
	if Save(Settings{TrayUsageEvery: 7}) == nil {
		t.Fatal("bad tray refresh accepted")
	}
	// the user's masking rules: kept tidied, and one that doesn't compile
	// said, not saved and left out (#195)
	if err := Save(Settings{RedactRules: []redact.Rule{{Kind: "gw key", Prefix: " oc_sk_ "}, {Kind: "ns", Regex: `ns-[0-9a-f]{12}`}}}); err != nil {
		t.Fatal(err)
	}
	if r := Load().RedactRules; len(r) != 2 || r[0] != (redact.Rule{Kind: "GW_KEY", Prefix: "oc_sk_"}) || r[1].Kind != "NS" {
		t.Fatalf("rules not kept: %+v", r)
	}
	if err := Save(Settings{RedactRules: []redact.Rule{{Kind: "bad", Regex: `ns-[0-9a-f`}}}); err == nil || !strings.Contains(err.Error(), "BAD") {
		t.Fatalf("bad regex: %v", err)
	}
	if len(Load().RedactRules) != 2 {
		t.Fatal("a failed save changed the rules")
	}
}

func TestMigrate(t *testing.T) {
	cfg, cache := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", cache)
	old := filepath.Join(cfg, "dial")
	if err := os.MkdirAll(filepath.Join(old, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "providers.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "sub", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	Migrate()
	fi, err := os.Stat(filepath.Join(cfg, "magpie", "providers.json"))
	if err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 { // Windows has no such bits
		t.Fatalf("providers.json not copied with its mode: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(cfg, "magpie", "sub", "x")); err != nil {
		t.Fatal("nested file not copied")
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("the old folder must stay")
	}
	if _, err := os.Stat(filepath.Join(cache, "magpie")); err == nil {
		t.Fatal("no old cache, so no new one")
	}
	// a second run must not touch an existing folder
	if err := os.WriteFile(filepath.Join(cfg, "magpie", "providers.json"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	Migrate()
	if b, _ := os.ReadFile(filepath.Join(cfg, "magpie", "providers.json")); string(b) != "new" {
		t.Fatal("existing folder overwritten")
	}
}

func TestArrange(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := Settings{AgentOrder: []string{"codex", "gone", "claude", "codex"}, AgentsHidden: []string{"crush"}}
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	s = Load()
	if strings.Join(s.AgentOrder, ",") != "codex,gone,claude" {
		t.Fatalf("order kept as %v", s.AgentOrder)
	}
	// an agent the order doesn't name follows, in the order it came
	shown, hidden := Arrange(s, []string{"claude", "gemini", "crush", "codex", "pi"}, func(x string) string { return x })
	if strings.Join(shown, ",") != "codex,claude,gemini,pi" || strings.Join(hidden, ",") != "crush" {
		t.Fatalf("arranged %v, hidden %v", shown, hidden)
	}
	// the rest of the settings leave it as it is
	s.Theme = "dark"
	if Save(s) != nil || len(Load().AgentsHidden) != 1 {
		t.Fatal("arrangement lost")
	}
}

// setPerModel puts key, with a value of each field's own type, into every
// per-model map of s and returns the fields it did so in. The fields come
// from the settings' own walk of them (perModelFields), so a map added
// later is seeded without this helper being told of it, and the key is
// converted to each map's own key type, a named string type among them.
func setPerModel(t *testing.T, s *Settings, key string) []string {
	t.Helper()
	v := reflect.ValueOf(s).Elem()
	var names []string
	for _, f := range perModelFields(v.Type()) {
		given := reflect.New(f.Type.Elem()).Elem()
		switch given.Kind() {
		case reflect.String:
			given.SetString("given")
		case reflect.Bool:
			given.SetBool(true)
		case reflect.Slice:
			given.Set(reflect.MakeSlice(given.Type(), 1, 1))
		}
		m := reflect.MakeMap(f.Type)
		m.SetMapIndex(reflect.ValueOf(key).Convert(f.Type.Key()), given)
		v.FieldByIndex(f.Index).Set(m)
		names = append(names, f.Name)
	}
	if len(names) == 0 {
		t.Fatal("the settings hold no per-model map")
	}
	return names
}

// A field is per-model for the name and the type and nothing else: the walk
// takes the settings' own word for it, so a field that is not a map is left
// out however it is named, and a field that is one is walked even when its
// keys are not a model's. That is the whole rule on purpose — a map added to
// the settings later is carried and renamed by it without being told of
// anywhere else — and it is why a field keyed by something other than a
// model is not named Model*.
func TestPerModelRule(t *testing.T) {
	type pages struct {
		ModelNames map[string]string   // per-model
		ModelBits  map[int]string      // named as one, but not of strings to X
		ModelCount map[string]int      // a map of strings to X, though not of models
		Visible    map[string][]string // by agent id
		Theme      string
	}
	var got []string
	for _, f := range perModelFields(reflect.TypeFor[pages]()) {
		got = append(got, f.Name)
	}
	if strings.Join(got, ",") != "ModelNames,ModelCount" {
		t.Fatalf("walked %v, want ModelNames and ModelCount only", got)
	}
}

// Every per-model map of the settings is walked, and nothing else: each one
// the walk reaches is marked with a key, and the marks are read back against
// the fields they are on.
func TestPerModelKeysWalkEveryPerModelMap(t *testing.T) {
	s := Settings{Visible: map[string][]string{"code": {"openai/gpt-5-mini"}}}
	names := setPerModel(t, &s, "openai/gpt-5-mini")
	PerModelKeys(&s, func(_ string, m reflect.Value) {
		m.SetMapIndex(reflect.ValueOf("walked").Convert(m.Type().Key()), reflect.New(m.Type().Elem()).Elem())
	})
	v := reflect.ValueOf(&s).Elem()
	for i := range v.NumField() {
		f := v.Type().Field(i)
		if f.Type.Kind() != reflect.Map {
			continue
		}
		marked := v.Field(i).MapIndex(reflect.ValueOf("walked").Convert(f.Type.Key())).IsValid()
		if marked != slices.Contains(names, f.Name) {
			t.Errorf("%s walked = %v, want %v", f.Name, marked, slices.Contains(names, f.Name))
		}
	}
}

// Carrying puts cur's per-model maps into in's whole, whichever page wrote
// them, and leaves everything else as the request body had it.
func TestCarryPerModel(t *testing.T) {
	var cur, in Settings
	names := setPerModel(t, &cur, "openai/gpt-5-mini")
	in.Theme, in.Visible = "light", map[string][]string{"code": {"openai/gpt-5-mini"}}
	CarryPerModel(&in, &cur)
	c, i := reflect.ValueOf(&cur).Elem(), reflect.ValueOf(&in).Elem()
	for _, name := range names {
		if !reflect.DeepEqual(i.FieldByName(name).Interface(), c.FieldByName(name).Interface()) {
			t.Errorf("%s not carried over: %v", name, i.FieldByName(name).Interface())
		}
	}
	if in.Theme != "light" || !reflect.DeepEqual(in.Visible, map[string][]string{"code": {"openai/gpt-5-mini"}}) {
		t.Fatalf("carried what is not per-model: %+v", in)
	}
}

// The Settings page sends only its own choices, and the save keeps what the
// pages beside it set: the per-model maps whole as they were (gui/api.go's
// POST /api/settings), so a change of theme no longer loses the names, the
// levels and the image answers the user gave their models.
func TestSettingsPageSaveKeepsWhatOtherPagesSet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var want Settings
	names := setPerModel(t, &want, "openai/gpt-5-mini")
	want.Visible = map[string][]string{"code": {"openai/gpt-5-mini"}}
	want.LANKey, want.TextSize = "magpie-lan", 125
	if err := Save(want); err != nil {
		t.Fatal(err)
	}

	// the request body: the theme, and nothing of what another page set
	var in Settings
	if err := json.Unmarshal([]byte(`{"theme":"light"}`), &in); err != nil {
		t.Fatal(err)
	}
	cur := Load()
	in.AgentOrder, in.AgentsHidden, in.AgentsShown = cur.AgentOrder, cur.AgentsHidden, cur.AgentsShown
	in.Window = cur.Window
	in.Visible = cur.Visible
	CarryPerModel(&in, &cur)
	in.LAN, in.LANKey = cur.LAN, cur.LANKey
	in.RedactRules = cur.RedactRules
	in.QuotaLeft = cur.QuotaLeft
	in.TextSize = cur.TextSize
	if err := Save(in); err != nil {
		t.Fatal(err)
	}

	// every per-model map there are, the three there are now and any added
	// later: what the user said of their models is all of them, and none of
	// it is this page's to send
	got := Load()
	if got.Theme != "light" {
		t.Fatalf("the page's own choice not saved: %+v", got)
	}
	for _, name := range names {
		g, w := reflect.ValueOf(got).FieldByName(name), reflect.ValueOf(want).FieldByName(name)
		if !reflect.DeepEqual(g.Interface(), w.Interface()) {
			t.Errorf("%s was lost: %v", name, g.Interface())
		}
	}
	if !reflect.DeepEqual(got.Visible, want.Visible) || got.LANKey != want.LANKey || got.TextSize != want.TextSize {
		t.Fatalf("the rest of what other pages keep was lost: %+v", got)
	}
}

// Renaming a provider moves what was said of its models in every per-model
// map, all of them and not only the ones before one that moved something,
// and leaves keys that are not a model's of it as they were.
func TestRenamePerModel(t *testing.T) {
	var s Settings
	names := setPerModel(t, &s, "old/model")
	s.ModelNames["older/model"] = "a provider whose id begins the same"
	s.ModelNames["old"] = "not a model of old either"
	if !s.RenamePerModel("old", "new") {
		t.Fatal("renaming moved nothing")
	}
	v := reflect.ValueOf(&s).Elem()
	for _, name := range names {
		m := v.FieldByName(name)
		if !m.MapIndex(reflect.ValueOf("new/model")).IsValid() || m.MapIndex(reflect.ValueOf("old/model")).IsValid() {
			t.Errorf("%s holds %v", name, m.Interface())
		}
	}
	if s.ModelNames["older/model"] == "" || s.ModelNames["old"] == "" {
		t.Fatalf("a key that is not a model of old was moved: %v", s.ModelNames)
	}
	if s.RenamePerModel("old", "new") {
		t.Error("renaming again moved something")
	}
}

// A per-model field keyed by a named string type is one of them all the same
// — the walk goes by the key's kind, not by its spelling — and renaming a
// provider moves its keys. The key it writes is a string, which is not
// assignable to a named string type, so it is converted to the map's own key
// type; every per-model field the settings hold today is keyed by a plain
// string, so a map on such a type is the only thing that reaches that
// conversion, and this is what says it works.
func TestRenamePerModelWithNamedKeyType(t *testing.T) {
	type keyed struct {
		ModelNames map[modelKey]string
		ModelCount map[modelKey]int
		Visible    map[modelKey][]string // by agent id, so not a model's
	}
	if got := perModelFields(reflect.TypeFor[keyed]()); len(got) != 2 {
		t.Fatalf("walked %d fields, want the two named Model* of %v", len(got), reflect.TypeFor[keyed]())
	}

	byName := map[modelKey]string{"old/m": "given", "other/m": "left"}
	if !renameInMap(reflect.ValueOf(byName), "old", "new") {
		t.Fatal("renaming moved nothing")
	}
	if want := (map[modelKey]string{"new/m": "given", "other/m": "left"}); !reflect.DeepEqual(byName, want) {
		t.Fatalf("renamed to %v, want %v", byName, want)
	}
	if renameInMap(reflect.ValueOf(byName), "old", "new") {
		t.Error("renaming again moved something")
	}
}

// modelKey is a string type of its own, which a per-model map may be keyed
// by as well as a plain string.
type modelKey string

// A key of a per-model map names a provider and a model, or a provider and
// "*" for all of its models; the model's own id may hold slashes. This is
// the check the price, output-limit and wire changes on top of this one take
// the keys they are given against, and what the error says of them is part
// of it: it names the setting being checked.
func TestCheckModelKey(t *testing.T) {
	for _, key := range []string{"a/m", "a/vendor/m", "a/*"} {
		if err := CheckModelKey("a price", key); err != nil {
			t.Errorf("%q refused: %v", key, err)
		}
	}
	for _, key := range []string{"", "m", "a", "a/", "/m", "A/m", "a b/m", "a m/n"} {
		err := CheckModelKey("a price", key)
		if err == nil {
			t.Errorf("%q taken as a model key", key)
			continue
		}
		if !strings.Contains(err.Error(), "a price") {
			t.Errorf("%q: the error does not say what was being checked: %v", key, err)
		}
	}
}

// A price is usable only when every part is a number a vendor could charge.
// JSON has no NaN or Infinity, so nothing reaches here from the file with
// one; a price typed in, or passed in by a caller, still can, and a NaN
// would sail past a < 0 check and poison every total it reached.
func TestModelPriceNeedsEveryPartToBeAFiniteNumber(t *testing.T) {
	nan, inf, ninf := math.NaN(), math.Inf(1), math.Inf(-1)
	full := func(in, out, cr, cw *float64) ModelPrice {
		return ModelPrice{Input: in, Output: out, CacheRead: cr, CacheWrite: cw}
	}
	ok := full(new(2.0), new(10.0), new(0.25), new(2.5))
	if _, bad := ok.Price(); bad != "" {
		t.Errorf("a price with every part given: named %q as bad", bad)
	}
	for _, tc := range []struct {
		what string
		m    ModelPrice
		part string
	}{
		{"input missing", full(nil, new(1.0), new(0.1), new(0.1)), "input"},
		{"output missing", full(new(1.0), nil, new(0.1), new(0.1)), "output"},
		{"cache read missing", full(new(1.0), new(1.0), nil, new(0.1)), "cache read"},
		{"cache write missing", full(new(1.0), new(1.0), new(0.1), nil), "cache write"},
		{"input NaN", full(&nan, new(1.0), new(0.1), new(0.1)), "input"},
		{"output NaN", full(new(1.0), &nan, new(0.1), new(0.1)), "output"},
		{"input infinite", full(&inf, new(1.0), new(0.1), new(0.1)), "input"},
		{"cache read infinite", full(new(1.0), new(1.0), &inf, new(0.1)), "cache read"},
		{"cache write negative infinite", full(new(1.0), new(1.0), new(0.1), &ninf), "cache write"},
		{"input negative", full(new(-1.0), new(1.0), new(0.1), new(0.1)), "input"},
		{"output negative", full(new(1.0), new(-1.0), new(0.1), new(0.1)), "output"},
	} {
		if _, bad := tc.m.Price(); bad != tc.part {
			t.Errorf("%s: named %q as the bad part, want %q", tc.what, bad, tc.part)
		}
	}
	// zero is a price: a free model is one the vendor charges nothing for.
	if _, bad := full(new(0.0), new(0.0), new(0.0), new(0.0)).Price(); bad != "" {
		t.Errorf("a price of zero: named %q as bad", bad)
	}
}

// A price's parts are named one way on disk and another in a message about
// one: the file's cache_read and cache_write are its own keys and every
// price already written is under them, so they stay, while a message names
// the parts as the CLI and the README say them, "cache read" and "cache
// write". Naming the file's keys in a message reads as a JSON key leaking
// into prose, and there is nothing in the CLI that says "cache_write".
func TestModelPriceKeysStayTheFilesOwnWhileMessagesSayTheParts(t *testing.T) {
	m := ModelPrice{Input: new(0.5), Output: new(1.5), CacheRead: new(0.05), CacheWrite: new(0.1)}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"input":0.5`, `"output":1.5`, `"cache_read":0.05`, `"cache_write":0.1`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("a price is written %s; want %s among its keys", b, key)
		}
	}
	// and a file written that way is read back whole, and the message a
	// half-given one leaves behind says the parts as prose does
	var back ModelPrice
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if p, bad := back.Price(); bad != "" || p.Input != 0.5 || p.CacheRead != 0.05 || p.CacheWrite != 0.1 {
		t.Errorf("read back as %+v, %q; want the whole price and no part named", p, bad)
	}
	back.CacheRead = nil
	if _, bad := back.Price(); bad != "cache read" {
		t.Errorf("a part left out is named %q; want %q, as the CLI and the README say it", bad, "cache read")
	}
	if err := CheckModelPrice("relay/sol", back); !strings.Contains(err.Error(), "a cache read price") {
		t.Errorf("refusing a price: %v; want it to ask for a cache read price", err)
	}
}

// the usage alerts' share and amount (#368): 0 is off, the rest in range
func TestAlertsChecked(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, s := range []Settings{{UsageAlert: 101}, {UsageAlert: -1}, {BalanceAlert: -0.5}} {
		if err := Save(s); err == nil {
			t.Errorf("saved %+v", s)
		}
	}
	if err := Save(Settings{UsageAlert: 80, BalanceAlert: 2.5}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.UsageAlert != 80 || s.BalanceAlert != 2.5 {
		t.Fatalf("read back %d %v", s.UsageAlert, s.BalanceAlert)
	}
}
