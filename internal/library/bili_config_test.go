package library

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/gateway"
)

// biliFileFixture is a billion-context.json written by hand, as bili
// 0.1.213's own tests write theirs (tests/external-summary-settings.test.ts):
// a recipe with a key's variable, one with a stored key, magpie's recipe
// on another computer's port, a comment and a key magpie doesn't know.
const biliFileFixture = `{
  // my summary models
  "providers": {
    "glm": { "baseUrl": "https://open.bigmodel.cn/api/paas/v4", "api": "openai", "apiKeyEnv": "GLM_KEY", "models": { "glm-4.9-flash": {} } },
    "claude": { "baseUrl": "https://api.anthropic.com/v1", "api": "anthropic", "credentialRef": "primary", "models": { "claude-haiku": { "contextWindow": 2048 } } },
    "magpie": { "baseUrl": "http://127.0.0.1:4567/v1", "api": "openai", "credentialRef": "magpie", "models": { "gpt-5.4-mini": {} } }
  },
  "compress": { "externalSummary": { "enabled": true, "targets": ["claude/claude-haiku"] } },
  "retained": true
}
`

// biliSandbox is a sandbox where bili's file is under ~/.config, this
// computer's gateway at port, and every reload asked of a running bili
// counted.
func biliSandbox(t *testing.T, port string) (string, *atomic.Int32) {
	t.Helper()
	h := sandbox(t)
	t.Setenv("BILI_CONFIG_FILE", "")
	was := biliGatewayURL
	biliGatewayURL = func() string { return "http://127.0.0.1:" + port + "/v1" }
	t.Cleanup(func() { biliGatewayURL = was })
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/__bili/config/reload" {
			http.NotFound(w, r)
			return
		}
		n.Add(1)
		w.Write([]byte(`{"ok":true,"count":0,"routes":[]}`))
	}))
	t.Cleanup(s.Close)
	biliRegister(t, h, "a1", s.URL, 1)
	return h, &n
}

func biliMust[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func biliCfgPath(h string) string {
	return filepath.Join(h, ".config", "billion-context", "billion-context.json")
}

func biliJSON(t *testing.T, p string) gjson.Result {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	j := jsonc.ToJSON(data)
	if !json.Valid(j) {
		t.Fatalf("%s isn't JSONC:\n%s", p, data)
	}
	return gjson.ParseBytes(j)
}

// TestBiliConfigFileWhereBiliLooks: BILI_CONFIG_FILE first, then
// XDG_CONFIG_HOME, then ~/.config on every system (src/paths.ts).
func TestBiliConfigFileWhereBiliLooks(t *testing.T) {
	h := sandbox(t)
	t.Setenv("BILI_CONFIG_FILE", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if got, want := BiliConfigFile(), filepath.Join(h, ".config", "billion-context", "billion-context.json"); got != want {
		t.Errorf("no XDG: %s, want %s", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "xdg"))
	if got, want := BiliConfigFile(), filepath.Join(h, "xdg", "billion-context", "billion-context.json"); got != want {
		t.Errorf("XDG_CONFIG_HOME: %s, want %s", got, want)
	}
	t.Setenv("BILI_CONFIG_FILE", filepath.Join(h, "mine.json"))
	if got := BiliConfigFile(); got != filepath.Join(h, "mine.json") {
		t.Errorf("BILI_CONFIG_FILE: %s", got)
	}
}

// TestBiliFileCarried: a sync carries billion-context.json as it is,
// comments and all, with magpie's recipe at the default address; the
// other computer gets it with its own gateway in the recipe and the key
// in bili's store, keeps the file it replaced with the backups, and its
// running bili reads it again. A bundle without it leaves the file there.
func TestBiliFileCarried(t *testing.T) {
	h, _ := biliSandbox(t, "4567")
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"claude"}}))
	write(t, biliCfgPath(h), biliFileFixture)
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	if b.Bili == nil {
		t.Fatal("billion-context.json not carried")
	}
	if !strings.Contains(*b.Bili, "// my summary models") || !strings.Contains(*b.Bili, `"retained": true`) {
		t.Errorf("carried not as written:\n%s", *b.Bili)
	}
	if u := gjson.Get(string(jsonc.ToJSON([]byte(*b.Bili))), "providers.magpie.baseUrl").String(); u != "http://"+gateway.DefaultAddr+"/v1" {
		t.Errorf("magpie's recipe carried at %s", u)
	}
	carried := mustJSON(t, b)

	h2, reloads := biliSandbox(t, "5555")
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"claude"}}))
	mine := "{\n  // this computer's\n  \"compress\": { \"nudgeGrowthTokens\": 20000 }\n}\n"
	write(t, biliCfgPath(h2), mine)
	var in Bundle
	json.Unmarshal([]byte(carried), &in)
	ok(t)(Put(&in))
	got := read(t, biliCfgPath(h2))
	if !strings.Contains(got, "// my summary models") || strings.Contains(got, "this computer's") {
		t.Errorf("billion-context.json after the sync:\n%s", got)
	}
	r := biliJSON(t, biliCfgPath(h2))
	if u := r.Get("providers.magpie.baseUrl").String(); u != "http://127.0.0.1:5555/v1" {
		t.Errorf("magpie's recipe sent to %s, not this computer's gateway", u)
	}
	if r.Get("providers.glm.apiKeyEnv").String() != "GLM_KEY" || !r.Get("retained").Bool() {
		t.Errorf("the user's own keys: %s", r.Raw)
	}
	creds := biliCfgPath(h2) + ".summary-credentials.json"
	var keys map[string]string
	if err := json.Unmarshal([]byte(read(t, creds)), &keys); err != nil || keys["magpie"] != gateway.TokenFor("billion-context") {
		t.Errorf("bili's key store: %v %v", keys, err)
	}
	if fi, err := os.Stat(creds); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("bili's key store mode: %v %v", fi, err)
	}
	if _, err := os.Stat(creds + ".lock"); err == nil {
		t.Error("the key store's lock left behind")
	}
	if n := reloads.Load(); n != 1 {
		t.Errorf("the running bili asked to reload %d times, want 1", n)
	}
	found := false
	filepath.WalkDir(BackupDir(), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(p); string(data) == mine {
				found = true
			}
		}
		return nil
	})
	if !found {
		t.Error("the billion-context.json the sync replaced isn't kept with the backups")
	}

	// the same bytes again: nothing written, nothing reloaded
	ok(t)(Put(&in))
	if n := reloads.Load(); n != 1 {
		t.Errorf("an unchanged file reloaded bili (%d)", n)
	}
	// from a magpie that didn't carry it: this computer's stays
	in.Bili = nil
	ok(t)(Put(&in))
	if read(t, biliCfgPath(h2)) != got {
		t.Error("a bundle without billion-context.json changed it")
	}
	// a carried one bili couldn't read isn't written
	bad := "{ not json"
	in.Bili = &bad
	ok(t)(Put(&in))
	if read(t, biliCfgPath(h2)) != got {
		t.Error("an unreadable billion-context.json was written")
	}
}

// TestBiliFileNotCarriedUnreadable: a file bili couldn't read, or none,
// is not carried (nil: the other computer keeps its own), never carried
// as empty.
func TestBiliFileNotCarriedUnreadable(t *testing.T) {
	h, _ := biliSandbox(t, "4567")
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"claude"}}))
	if b := biliMust[*Bundle](t)(Collect()); b.Bili != nil {
		t.Errorf("no file carried as %q", *b.Bili)
	}
	write(t, biliCfgPath(h), "{ \"compress\": ")
	if b := biliMust[*Bundle](t)(Collect()); b.Bili != nil {
		t.Errorf("a broken file carried as %q", *b.Bili)
	}
}

// TestBiliFileDSNNotInKeylessBackup: a storage DSN's password is the one
// secret billion-context.json can hold; a backup made without keys leaves
// the file out.
func TestBiliFileDSNNotInKeylessBackup(t *testing.T) {
	text := `{"storage":{"mysql":{"dsn":"mysql://bili:hunter2@127.0.0.1/bili"}}}`
	b := &Bundle{Bili: &text}
	b.WithoutSecrets(func(string) bool { return false })
	if b.Bili != nil {
		t.Errorf("a DSN carried without keys: %s", *b.Bili)
	}
	plain := biliFileFixture
	b = &Bundle{Bili: &plain}
	b.WithoutSecrets(func(string) bool { return false })
	if b.Bili == nil {
		t.Error("a file with no secret left out")
	}
}

// TestBiliSettingsChanged: the tab's settings are written where bili
// reads them, with the comments, recipes and keys magpie doesn't know as
// they were, and the running bili asked to reload.
func TestBiliSettingsChanged(t *testing.T) {
	h, reloads := biliSandbox(t, "4567")
	write(t, biliCfgPath(h), biliFileFixture)
	growth, nudge, on := 80000, true, true
	model := "gpt-5.4-mini"
	phil := "Keep every file path and error verbatim."
	s := biliMust[*BiliSettings](t)(SetBiliSettings(BiliChange{Growth: &growth, Nudge: &nudge, Model: &model, Summary: &on, Philosophy: &phil}))
	if s.Reloaded != 1 || s.Running != 1 || reloads.Load() != 1 {
		t.Errorf("reloaded %d of %d", s.Reloaded, s.Running)
	}
	got := read(t, biliCfgPath(h))
	if !strings.Contains(got, "// my summary models") {
		t.Errorf("comment lost:\n%s", got)
	}
	r := biliJSON(t, biliCfgPath(h))
	for k, want := range map[string]string{
		"compress.nudgeGrowthTokens":            "80000",
		"compress.requireNudge":                 "true",
		"compress.externalSummary.enabled":      "true",
		"compress.externalSummary.targets":      `["magpie/gpt-5.4-mini","claude/claude-haiku"]`,
		"compress.prompts.compressPhilosophy":   phil,
		"compress.acknowledgePromptsRisk":       "true",
		"providers.magpie.baseUrl":              "http://127.0.0.1:4567/v1",
		"providers.claude.credentialRef":        "primary",
		"retained":                              "true",
		`providers.magpie.models.gpt-5\.4-mini`: "{}",
	} {
		v := r.Get(k)
		if got := v.String(); v.IsArray() || v.IsObject() {
			got = strings.Join(strings.Fields(v.Raw), "")
			if got != want {
				t.Errorf("%s = %s, want %s", k, got, want)
			}
		} else if got != want {
			t.Errorf("%s = %s, want %s", k, got, want)
		}
	}
	if s.Growth != 80000 || !s.Nudge || !s.Summary || s.Targets[0] != "magpie/gpt-5.4-mini" || s.Philosophy != phil {
		t.Errorf("read back: %+v", s)
	}
	if strings.Join(s.Recipes, ",") != "claude/claude-haiku,glm/glm-4.9-flash" {
		t.Errorf("recipes: %v", s.Recipes)
	}

	// back to bili's own: the keys go
	zero, empty, off := 0, "", false
	biliMust[*BiliSettings](t)(SetBiliSettings(BiliChange{Growth: &zero, Philosophy: &empty, Nudge: &off}))
	r = biliJSON(t, biliCfgPath(h))
	if r.Get("compress.nudgeGrowthTokens").Exists() || r.Get("compress.prompts.compressPhilosophy").Exists() || r.Get("compress.requireNudge").Exists() {
		t.Errorf("not taken out: %s", r.Get("compress").Raw)
	}
	// a recipe's model of the file's own
	glm := "glm/glm-4.9-flash"
	biliMust[*BiliSettings](t)(SetBiliSettings(BiliChange{Target: &glm}))
	if got := biliJSON(t, biliCfgPath(h)).Get("compress.externalSummary.targets.0").String(); got != glm {
		t.Errorf("target %s", got)
	}
	nope := "glm/unknown-model"
	if _, err := SetBiliSettings(BiliChange{Target: &nope}); err == nil {
		t.Error("a model the file has no recipe for was taken")
	}
}

// TestBiliMarkersSwitch (@JamesYu32 on X: a switch for visibilityMarkers):
// compress.visibilityMarkers reads as on until the file says false, the
// switch writes false where bili reads it (CONFIGURATION.md, 0.1.213),
// keeps the rest of the file, and turning it back on takes the key out to
// bili's default rather than leaving a value magpie chose. A provider's or
// model's own setting is the user's and stays as it is.
func TestBiliMarkersSwitch(t *testing.T) {
	h, _ := biliSandbox(t, "4567")
	write(t, biliCfgPath(h), biliFileFixture)
	if s := biliMust[*BiliSettings](t)(ReadBiliSettings()); !s.Markers {
		t.Fatal("markers read as off with nothing set")
	}
	off, on := false, true
	s := biliMust[*BiliSettings](t)(SetBiliSettings(BiliChange{Markers: &off}))
	r := biliJSON(t, biliCfgPath(h))
	if v := r.Get("compress.visibilityMarkers"); v.Type != gjson.False || s.Markers {
		t.Fatalf("not turned off: %s (read back %v)", r.Get("compress").Raw, s.Markers)
	}
	if !strings.Contains(read(t, biliCfgPath(h)), "// my summary models") || !r.Get("retained").Bool() {
		t.Error("the rest of the file wasn't kept")
	}
	s = biliMust[*BiliSettings](t)(SetBiliSettings(BiliChange{Markers: &on}))
	if biliJSON(t, biliCfgPath(h)).Get("compress.visibilityMarkers").Exists() || !s.Markers {
		t.Error("turning markers back on left the key in")
	}
	// a provider's own setting is left alone, and a non-boolean bili
	// rejects is not read as off
	write(t, biliCfgPath(h), `{"compress":{"visibilityMarkers":"no"},"providers":{"x":{"compress":{"visibilityMarkers":false}}}}`)
	if s := biliMust[*BiliSettings](t)(ReadBiliSettings()); !s.Markers {
		t.Error(`"no" read as off`)
	}
	biliMust[*BiliSettings](t)(SetBiliSettings(BiliChange{Markers: &off}))
	if r := biliJSON(t, biliCfgPath(h)); r.Get("providers.x.compress.visibilityMarkers").Type != gjson.False || r.Get("compress.visibilityMarkers").Type != gjson.False {
		t.Errorf("got %s", r.Raw)
	}
}

// TestBiliSettingsRefused: what bili would refuse, and so turn its whole
// compress section off over, is never written: an out-of-range step, a
// summary switched on with no model, a recipe of the user's named magpie,
// a file bili can't read.
func TestBiliSettingsRefused(t *testing.T) {
	h, _ := biliSandbox(t, "4567")
	on := true
	if _, err := SetBiliSettings(BiliChange{Summary: &on}); err == nil {
		t.Error("summary on with no model")
	}
	for _, g := range []int{-1, 5, 20_000_000} {
		if _, err := SetBiliSettings(BiliChange{Growth: &g}); err == nil {
			t.Errorf("growth %d taken", g)
		}
	}
	if _, err := os.Stat(biliCfgPath(h)); err == nil {
		t.Error("a refused change wrote the file")
	}
	theirs := `{"providers":{"magpie":{"baseUrl":"https://api.example.com/v1","api":"openai","apiKeyEnv":"K","models":{"m":{}}}}}`
	write(t, biliCfgPath(h), theirs)
	model := "gpt-5.4-mini"
	if _, err := SetBiliSettings(BiliChange{Model: &model}); err == nil {
		t.Error("the user's own recipe named magpie written over")
	}
	write(t, biliCfgPath(h), "{ broken")
	growth := 60000
	if _, err := SetBiliSettings(BiliChange{Growth: &growth}); err == nil || read(t, biliCfgPath(h)) != "{ broken" {
		t.Errorf("a broken file changed: %v", err)
	}
	if s := biliMust[*BiliSettings](t)(ReadBiliSettings()); s.Bad == "" {
		t.Error("a broken file read as fine")
	}
}

// TestBiliKeyStoreKeptPrivate: a store bili wouldn't read (open to
// others) is refused, not written over, and a lock another writer holds
// is left alone.
func TestBiliKeyStoreKeptPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bili checks the store's mode off Windows only")
	}
	h, _ := biliSandbox(t, "4567")
	creds := biliCfgPath(h) + ".summary-credentials.json"
	write(t, creds, `{"primary":"sk-mine"}`)
	os.Chmod(creds, 0o644)
	model := "m"
	if _, err := SetBiliSettings(BiliChange{Model: &model}); err == nil {
		t.Error("a store open to others was written")
	}
	os.Chmod(creds, 0o600)
	write(t, creds+".lock", "other-writer")
	if _, err := SetBiliSettings(BiliChange{Model: &model}); err == nil || read(t, creds+".lock") != "other-writer" {
		t.Errorf("another writer's lock: %v", err)
	}
	os.Remove(creds + ".lock")
	biliMust[*BiliSettings](t)(SetBiliSettings(BiliChange{Model: &model}))
	var keys map[string]string
	json.Unmarshal([]byte(read(t, creds)), &keys)
	if keys["primary"] != "sk-mine" || keys["magpie"] == "" {
		t.Errorf("store: %v", keys)
	}
}
