package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// asideHome is a sandbox home with Aside's own account folder holding the
// settings and models files, on a native provider magpie knows nothing of.
// Aside itself is stood in for by asideSet answering as it does — the default
// is patched and the file written — so the tests below go through the same
// call a real change does.
func asideHome(t *testing.T) (settings, models string) {
	t.Helper()
	syncHome(t)
	dir := filepath.Join(os.Getenv("HOME"), ".aside", "u", "0")
	settings, models = filepath.Join(dir, "settings.json"), filepath.Join(dir, "models.json")
	writeFile(t, settings, `{"defaultModel":{"provider":"native","modelId":"native-model","thinkingLevel":"high","fastMode":true,"keepMe":1},"theme":"dark"}`)
	writeFile(t, models, `{"providers":{"native":{"baseUrl":"https://native.invalid/v1","apiKey":"native-key","models":[{"id":"native-model","name":"Native"}]}},"keepMe":"yes"}`)
	asideStandIn(t, settings)
	return settings, models
}

// asideStandIn is Aside's own settings API, as magpie calls it: the change
// the expression carries is put into the settings and the file is written
// under it, which is what the real Aside does. It reads the two shapes
// asideApply builds — a patch of the default, and a role of the categories —
// out of the expression rather than taking a shortcut around it, so a test
// covers the expression as well as the file.
func asideStandIn(t *testing.T, path string) {
	t.Helper()
	was := asideSet
	asideSet = func(account, expr string) error {
		if _, patch, ok := strings.Cut(expr, "Object.assign({}, m, "); ok {
			patch, _, _ = strings.Cut(patch, "));")
			var kv map[string]string
			if err := json.Unmarshal([]byte(patch), &kv); err != nil {
				return err
			}
			edits := make([]edit.KV, 0, len(kv))
			for k, v := range kv {
				edits = append(edits, edit.KV{Path: "defaultModel." + k, Value: v})
			}
			return edit.SetJSON(path, edits...)
		}
		const mark = `] = Object.assign({}, r, `
		// the role's name is in the expression twice — once where the role is
		// read and once where it is written — so the write is the last one
		if i := strings.LastIndex(expr, mark); i > 0 {
			role := expr[strings.LastIndex(expr[:i], `c["`)+3 : i-1]
			patch, _, _ := strings.Cut(expr[i+len(mark):], ");")
			var sel map[string]string
			if err := json.Unmarshal([]byte(patch), &sel); err != nil {
				return err
			}
			return edit.SetJSON(path, edit.KV{Path: "modelCategories." + role, Value: sel})
		}
		if strings.Contains(expr, "delete m.provider; delete m.modelId;") {
			return edit.DelJSON(path, "defaultModel.provider", "defaultModel.modelId")
		}
		if strings.Contains(expr, "aside.settings.set('"+asideImageKey+"', null)") {
			return edit.DelJSON(path, asideImageKey)
		}
		const imageMark = "aside.settings.set('" + asideImageKey + "', "
		if i := strings.LastIndex(expr, imageMark); i > 0 {
			sel, _, _ := strings.Cut(expr[i+len(imageMark):], ");")
			var kv map[string]string
			if err := json.Unmarshal([]byte(sel), &kv); err != nil {
				return err
			}
			return edit.SetJSON(path, edit.KV{Path: asideImageKey, Value: kv})
		}
		if _, after, ok := strings.Cut(expr, `delete c["`); ok {
			role, _, _ := strings.Cut(after, `"];`)
			return edit.DelJSON(path, "modelCategories."+role)
		}
		// the stand-in only ever sees expressions magpie built, so one it
		// cannot read is a test that stopped following the code, not an Aside
		// that failed: answering with an error would have the code fall back
		// to the file and the test pass for the wrong reason
		panic("the expression carries no change: " + expr)
	}
	t.Cleanup(func() { asideSet = was; asideStale.Store(false) })
}

// mustFindAside is Aside as Find answers for it, the check the tests below
// would otherwise skip past into a panic.
func mustFindAside(t *testing.T) *Agent {
	t.Helper()
	a, err := Find("aside")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// asideSettings is what Aside's defaultModel says now, whole: the thinking
// level and fast mode beside the model are the user's, and a check that only
// read provider and modelId would not see them go.
func asideSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	var m struct {
		DefaultModel map[string]any `json:"defaultModel"`
		Theme        string         `json:"theme"`
	}
	if err := json.Unmarshal([]byte(readFile(path)), &m); err != nil {
		t.Fatal(err)
	}
	if m.Theme != "dark" {
		t.Fatalf("Aside's other settings went: %s", readFile(path))
	}
	return m.DefaultModel
}

// asideCategories is what Aside holds its task roles as now, whole, so a check
// that only read one role would not see the others go.
func asideCategories(t *testing.T, path string) map[string]any {
	t.Helper()
	var m struct {
		ModelCategories map[string]any `json:"modelCategories"`
		Theme           string         `json:"theme"`
	}
	if err := json.Unmarshal([]byte(readFile(path)), &m); err != nil {
		t.Fatal(err)
	}
	if m.Theme != "dark" {
		t.Fatalf("Aside's other settings went: %s", readFile(path))
	}
	return m.ModelCategories
}

// editAsideCategory writes one of Aside's task roles into the file, as a role
// the user set in Aside itself would be left.
func editAsideCategory(t *testing.T, path, role, sel string) {
	t.Helper()
	if err := edit.SetJSON(path, edit.KV{
		Path:  "modelCategories." + role,
		Value: json.RawMessage(sel),
	}); err != nil {
		t.Fatal(err)
	}
}

// asideImage is what Aside holds its picture model as now, whole. A nil map is
// the key being gone, which is how an account that has never picked one reads.
func asideImage(t *testing.T, path string) map[string]any {
	t.Helper()
	var m struct {
		Image map[string]any `json:"imageGenerationModel"`
		Theme string         `json:"theme"`
	}
	if err := json.Unmarshal([]byte(readFile(path)), &m); err != nil {
		t.Fatal(err)
	}
	if m.Theme != "dark" {
		t.Fatalf("Aside's other settings went: %s", readFile(path))
	}
	return m.Image
}

func asideBlock(t *testing.T, path string) map[string]any {
	t.Helper()
	var m struct {
		Providers map[string]json.RawMessage `json:"providers"`
		KeepMe    string                     `json:"keepMe"`
	}
	if err := json.Unmarshal([]byte(readFile(path)), &m); err != nil {
		t.Fatal(err)
	}
	if m.KeepMe != "yes" {
		t.Fatalf("Aside's own models.json keys went: %s", readFile(path))
	}
	var out map[string]any
	if b, ok := m.Providers[magpieID]; ok {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// Aside is an agent magpie sets up like any other: found by its name, with a
// model and a thinking level, whether or not its files are here.
func TestAsideIsAnAgent(t *testing.T) {
	asideHome(t)
	a := mustFindAside(t)
	if a.ID != "aside" || a.Name != "Aside" {
		t.Fatalf("%+v", a)
	}
	if got := a.Field("model"); got == nil {
		t.Fatalf("Aside has no model field: %v", a.Fields)
	}
	if !a.Detected() {
		t.Fatal("Aside's own folder is here and it is not detected")
	}
}

// Picking one of magpie's models gives Aside the whole catalogue in a
// provider of its own and points its default at one entry of it, with the
// user's thinking level, fast mode and the rest of the default left as they
// were, and their own provider untouched beside magpie's.
func TestAsidePicksAMagpieModel(t *testing.T) {
	settings, models := asideHome(t)
	a, err := Find("aside")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	dm := asideSettings(t, settings)
	if dm["provider"] != magpieID || dm["modelId"] != "relay/glm-4.6" {
		t.Fatalf("defaultModel: %v", dm)
	}
	if dm["thinkingLevel"] != "high" || dm["fastMode"] != true || dm["keepMe"] != float64(1) {
		t.Fatalf("the user's own default was not kept: %v", dm)
	}
	if got := a.Field("model").Get(); got != magpieID+"/relay/glm-4.6" {
		t.Fatalf("read back as %q", got)
	}
	block := asideBlock(t, models)
	if block["baseUrl"] != gateway.URL()+"/v1" {
		t.Fatalf("the provider isn't pointing at the gateway: %v", block)
	}
	if block["apiKey"] != gateway.TokenFor("aside") {
		t.Fatalf("the caller token is %v, so requests are not attributed to Aside", block["apiKey"])
	}
	if block["api"] != "openai-completions" {
		t.Fatalf("api: %v", block["api"])
	}
	models_ := block["models"].([]any)
	if models_[0].(map[string]any)["id"] != "relay/glm-4.6" {
		t.Fatalf("catalogue: %v", models_)
	}
	if !a.Wired() {
		t.Fatal("Aside is on one of magpie's models and is not wired")
	}
	if d := a.Check(); d != "" {
		t.Fatalf("wiring reported off: %s", d)
	}
	// their own provider is still there, beside magpie's
	if !strings.Contains(readFile(models), `"native"`) {
		t.Fatalf("Aside's own provider went: %s", readFile(models))
	}
}

// The default puts back the model the user had before magpie took the field,
// and takes magpie's provider out with it, leaving everything else of the
// account as it was.
func TestAsideDefaultPutsTheUsersOwnBack(t *testing.T) {
	settings, models := asideHome(t)
	a := mustFindAside(t)
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	dm := asideSettings(t, settings)
	if dm["provider"] != "native" || dm["modelId"] != "native-model" {
		t.Fatalf("the user's own model was not put back: %v", dm)
	}
	if dm["thinkingLevel"] != "high" || dm["fastMode"] != true {
		t.Fatalf("their own default was changed by the way back: %v", dm)
	}
	if b := asideBlock(t, models); b != nil {
		t.Fatalf("magpie's provider is still in Aside's models.json: %v", b)
	}
	if a.Wired() {
		t.Fatal("still wired after the default")
	}
}

// A model of their own is theirs: the default is pointed at it, and magpie's
// provider is not written in for a model it doesn't serve.
func TestAsidePicksItsOwnModel(t *testing.T) {
	settings, models := asideHome(t)
	a := mustFindAside(t)
	if err := a.Apply("model", "native/native-model"); err != nil {
		t.Fatal(err)
	}
	if dm := asideSettings(t, settings); dm["provider"] != "native" || dm["thinkingLevel"] != "high" {
		t.Fatalf("%v", dm)
	}
	if b := asideBlock(t, models); b != nil {
		t.Fatalf("magpie was written in for a model of Aside's own: %v", b)
	}
	if a.Wired() {
		t.Fatal("wired, on a model of Aside's own")
	}
}

// The thinking level is Aside's own setting under the default: changing it
// touches that key alone, the model beside it included, and it is offered
// what the model magpie wrote for it takes, as Pi's is (#597).
func TestAsideEffortIsItsOwnField(t *testing.T) {
	settings, _ := asideHome(t)
	writeFile(t, catalog.CachePath(), `{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","name":"GLM-4.6","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high"]}],"limit":{"context":204800}}}}}`)
	catalog.Reset()
	a := mustFindAside(t)
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("effort", "low"); err != nil {
		t.Fatal(err)
	}
	dm := asideSettings(t, settings)
	if dm["thinkingLevel"] != "low" || dm["provider"] != magpieID || dm["modelId"] != "relay/glm-4.6" {
		t.Fatalf("%v", dm)
	}
	if got := a.Field("effort").Get(); got != "low" {
		t.Fatalf("read back as %q", got)
	}
	var levels []string
	for _, o := range a.Field("effort").Options(a.Values()) {
		levels = append(levels, o.Value)
	}
	if !slices.Equal(levels, []string{"low", "high"}) {
		t.Fatalf("offered %v, want the model's own low and high", levels)
	}
}

// An Aside that is installed and not yet used has no account folder yet, and
// the binary is not a second look: its installer puts that in ~/.local/bin,
// which a magpie launched from Finder has no PATH entry for. The row still has
// to be there, so it is the folder the installer makes that counts.
func TestAsideIsDetectedBeforeAnyAccount(t *testing.T) {
	syncHome(t)
	// the CLI's installer puts the binary in ~/.local/bin, which a magpie
	// launched from Finder has no PATH entry for, so the folder is the only
	// thing left to go on
	t.Setenv("PATH", t.TempDir())
	dir := filepath.Join(os.Getenv("HOME"), ".aside", "cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := mustFindAside(t)
	if !a.Detected() {
		t.Fatalf("an installed Aside is not detected: dir %q, path %q", a.Dir, a.Path)
	}
	if a.Field("model").Get() != "" {
		t.Fatalf("a model is read out of an account that is not there: %q", a.Field("model").Get())
	}
}

// A task role is a model of its own in Aside, unset while it follows the
// default: setting one writes the whole selection Aside takes — a role is not
// a pair of keys beside the model — with the thinking level the default is on,
// and leaves the default and the other roles alone.
func TestAsideRoleIsItsOwnField(t *testing.T) {
	settings, models := asideHome(t)
	a := mustFindAside(t)
	for _, role := range asideRoles {
		f := a.Field(role)
		if f == nil {
			t.Fatalf("Aside has no %s field: %v", role, a.Fields)
		}
		if !f.Quiet || f.Follows != "model" {
			t.Fatalf("%s is not a field that follows the model: quiet %v, follows %q", role, f.Quiet, f.Follows)
		}
		if got := f.Get(); got != "" {
			t.Fatalf("%s is on %q before anything is set", role, got)
		}
	}
	if err := a.Apply("fast", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	cat := asideCategories(t, settings)
	sel, _ := cat["fast"].(map[string]any)
	if sel["provider"] != magpieID || sel["modelId"] != "relay/glm-4.6" {
		t.Fatalf("the fast role: %v", sel)
	}
	// Aside keeps the level and fast mode inside a role and refuses one with
	// no level, so a level is always named: a new role takes the default's
	if sel["thinkingLevel"] != "high" {
		t.Fatalf("the role's thinking level is %v, not the default's high: %v", sel["thinkingLevel"], sel)
	}
	// fast mode is Aside's own to default and its own roles leave it off, so a
	// new role does not pick the default's up
	if sel["fastMode"] != false {
		t.Fatalf("the role's fast mode is %v: %v", sel["fastMode"], sel)
	}
	if got := a.Field("fast").Get(); got != magpieID+"/relay/glm-4.6" {
		t.Fatalf("read back as %q", got)
	}
	// the default is the user's own still, and the roles beside it are unset
	if dm := asideSettings(t, settings); dm["provider"] != "native" {
		t.Fatalf("a role moved the default: %v", dm)
	}
	for _, role := range asideRoles[1:] {
		if _, ok := cat[role]; ok {
			t.Fatalf("%s was set too: %v", role, cat)
		}
	}
	// a role of magpie's needs the provider to reach it through
	if block := asideBlock(t, models); block["apiKey"] != gateway.TokenFor("aside") {
		t.Fatalf("the provider the role names is not there: %v", block)
	}
}

// A role is changed through Aside itself, not behind it, so a model id that
// would break the expression around it is passed as the string it is.
func TestAsideRoleGoesThroughAside(t *testing.T) {
	settings, _ := asideHome(t)
	seen := ""
	inner := asideSet
	asideSet = func(account, expr string) error {
		seen = expr
		return inner(account, expr)
	}
	t.Cleanup(func() { asideSet = inner })
	a := mustFindAside(t)
	if err := a.Apply("visual", magpieID+`/x","provider":"someone-else`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, `"modelId":"x\",`) || !strings.Contains(seen, asideOK) {
		t.Fatalf("the change went out as %q", seen)
	}
	sel, _ := asideCategories(t, settings)["visual"].(map[string]any)
	if sel["modelId"] != `x","provider":"someone-else` || sel["provider"] != magpieID {
		t.Fatalf("the id was read as part of the expression: %v", sel)
	}
}

// A role that already has a level of its own keeps it when magpie points it
// at another model, the same way the default keeps the user's level beside a
// model magpie picked.
func TestAsideRoleKeepsItsOwnLevel(t *testing.T) {
	settings, _ := asideHome(t)
	editAsideCategory(t, settings, "visual",
		`{"provider":"native","modelId":"native-model","thinkingLevel":"low","fastMode":true}`)
	a := mustFindAside(t)
	if err := a.Apply("visual", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	sel, _ := asideCategories(t, settings)["visual"].(map[string]any)
	if sel["thinkingLevel"] != "low" || sel["fastMode"] != true {
		t.Fatalf("the role's own level went: %v", sel)
	}
	if sel["provider"] != magpieID || sel["modelId"] != "relay/glm-4.6" {
		t.Fatalf("%v", sel)
	}
}

// Taking magpie out puts a role back to the model it had, and takes away one
// that was only magpie's, so no role is left naming a provider that is gone.
func TestAsideDefaultPutsTheRolesBack(t *testing.T) {
	settings, models := asideHome(t)
	a := mustFindAside(t)
	if err := a.Apply("fast", "native/native-model"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("deep", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	cat := asideCategories(t, settings)
	if got, _ := cat["fast"].(map[string]any)["provider"]; got != "native" {
		t.Fatalf("the role the user had went: %v", cat)
	}
	if _, ok := cat["deep"]; ok {
		t.Fatalf("a role of magpie's is left behind: %v", cat)
	}
	if b := asideBlock(t, models); b != nil {
		t.Fatalf("magpie's provider is still in Aside's models.json: %v", b)
	}
	if a.Wired() {
		t.Fatal("still wired after the default")
	}
}

// Setting a role back to nothing takes the role off, which is how Aside itself
// puts a task back on the default model.
func TestAsideRoleDefaultFollowsTheDefault(t *testing.T) {
	settings, _ := asideHome(t)
	a := mustFindAside(t)
	if err := a.Apply("standard", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("standard", ""); err != nil {
		t.Fatal(err)
	}
	if cat := asideCategories(t, settings); len(cat) != 0 {
		t.Fatalf("the role was not taken off: %v", cat)
	}
	if got := a.Field("standard").Get(); got != "" {
		t.Fatalf("read back as %q", got)
	}
}

// The model Aside draws with is a field of its own, quiet and taking after the
// model, because most accounts have not picked one.
func TestAsideImageIsItsOwnField(t *testing.T) {
	asideHome(t)
	f := mustFindAside(t).Field("image")
	if f == nil {
		t.Fatal("Aside has no image field")
	}
	if !f.Quiet || f.Follows != "model" {
		t.Fatalf("image is not a field that follows the model: quiet %v, follows %q", f.Quiet, f.Follows)
	}
}

// Picking a picture model of magpie's goes through Aside, and carries the two
// keys Aside takes and no others: a thinking level and fast mode belong to the
// model it talks, and it refuses a value that has them.
func TestAsideImageGoesThroughAside(t *testing.T) {
	settings, _ := asideHome(t)
	seen := ""
	inner := asideSet
	asideSet = func(account, expr string) error {
		seen = expr
		return inner(account, expr)
	}
	t.Cleanup(func() { asideSet = inner })
	a := mustFindAside(t)
	if err := a.Apply("image", magpieID+`/x","provider":"someone-else`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, asideOK) {
		t.Fatalf("the change went out as %q", seen)
	}
	sel := asideImage(t, settings)
	if len(sel) != 2 || sel["provider"] != magpieID || sel["modelId"] != `x","provider":"someone-else` {
		t.Fatalf("the image model is not the two keys Aside takes: %v", sel)
	}
	if got := a.Field("image").Get(); got != magpieID+`/x","provider":"someone-else` {
		t.Fatalf("read back as %q", got)
	}
}

// Taking magpie out of Aside puts the picture model the user had back, and one
// of magpie's goes off rather than naming a provider that is no longer there.
func TestAsideDefaultPutsTheImageModelBack(t *testing.T) {
	settings, models := asideHome(t)
	a := mustFindAside(t)
	if err := a.Apply("image", "native/native-model"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("image", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := asideImage(t, settings); got["provider"] != "native" || got["modelId"] != "native-model" {
		t.Fatalf("the picture model the user had went: %v", got)
	}
	if b := asideBlock(t, models); b != nil {
		t.Fatalf("magpie's provider is still in Aside's models.json: %v", b)
	}
}

// An image model of magpie's that had no picture model behind it leaves Aside
// with none, which is what it had before, and the key is gone rather than left
// empty for Aside to read as a model with no name.
func TestAsideImageWithNothingBehindItGoesOff(t *testing.T) {
	settings, _ := asideHome(t)
	a := mustFindAside(t)
	if err := a.Apply("image", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("image", ""); err != nil {
		t.Fatal(err)
	}
	if got := asideImage(t, settings); got != nil {
		t.Fatalf("the picture model is still there: %v", got)
	}
	if got := a.Field("image").Get(); got != "" {
		t.Fatalf("read back as %q", got)
	}
}

// A picture model of magpie's is a model of magpie's like any other, so the
// wiring check covers it: a provider block gone is worth saying so about even
// though no model of magpie's was left behind to reach through it.
func TestAsideImageIsWired(t *testing.T) {
	_, models := asideHome(t)
	a := mustFindAside(t)
	if err := a.Apply("image", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if n := a.Check(); n != "" {
		t.Fatalf("a wired picture model is reported as %q", n)
	}
	if err := edit.DelJSON(models, "providers."+magpieID); err != nil {
		t.Fatal(err)
	}
	if n := a.Check(); n == "" {
		t.Fatal("a picture model on magpie with no provider block is not noticed")
	}
}

// The catalog sync refreshes a provider magpie has already written, and only
// that one: an Aside with no magpie provider is left as it is rather than
// wired in unasked, and a refresh never moves the user's default. Who owns
// the block it does write is TestAsideRefusesAnotherProvidersMagpie.
func TestAsideSyncOnlyRewritesItsOwnProvider(t *testing.T) {
	settings, models := asideHome(t)
	a := mustFindAside(t)
	before := readFile(models)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if readFile(models) != before {
		t.Fatalf("an Aside never connected was written to:\n%s", readFile(models))
	}
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	editDelAsideModel(t, models)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(models), `"relay/glm-4.6"`) {
		t.Fatalf("the catalog was not put back:\n%s", readFile(models))
	}
	if dm := asideSettings(t, settings); dm["provider"] != magpieID {
		t.Fatalf("the sync moved the user's default: %v", dm)
	}
	// the same content again writes nothing
	at := readFile(models)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if readFile(models) != at {
		t.Fatal("a sync with nothing to change rewrote the file")
	}
}

// A provider of the name magpie uses that is not magpie's own is the user's
// (or another tool's): it is refused, not written over.
func TestAsideRefusesAnotherProvidersMagpie(t *testing.T) {
	settings, models := asideHome(t)
	writeFile(t, models, `{"providers":{"magpie":{"baseUrl":"https://someone.else/v1","apiKey":"their-key","models":[]}}}`)
	before := readFile(models) + readFile(settings)
	a := mustFindAside(t)
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err == nil {
		t.Fatal("another provider's magpie was written over")
	}
	if readFile(models)+readFile(settings) != before {
		t.Fatalf("a refused pick still changed the files:\n%s", readFile(models))
	}
}

// A models.json that can't be read is the user's file to fix, not one to
// write over: the pick is refused and nothing moves.
func TestAsideRefusesABrokenModelsFile(t *testing.T) {
	settings, models := asideHome(t)
	writeFile(t, models, "{ this is not json")
	before := readFile(models) + readFile(settings)
	a := mustFindAside(t)
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err == nil {
		t.Fatal("a models.json that can't be read was written over")
	}
	if readFile(models)+readFile(settings) != before {
		t.Fatal("a refused pick still changed the files")
	}
}

// Aside answers a settings write with the settings as they now are, and drops
// what it will not take without a word — a role naming a model it has not
// resolved yet is the one that happens. A change that went nowhere is caught
// by the expression and lands in the file, with the notice that it only takes
// hold at Aside's next start.
func TestAsideCatchesAChangeAsideDrops(t *testing.T) {
	settings, _ := asideHome(t)
	// the check in the expression, answered the way Aside answers a write it
	// refused: the value came back without the change in it
	asideSet = func(account, expr string) error {
		if i := strings.Index(expr, "if ("); i > 0 {
			return errors.New("Aside did not take it: " + expr[i:])
		}
		return errors.New("the expression is not one to check: " + expr)
	}
	a := mustFindAside(t)
	if err := a.Apply("fast", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatalf("a change Aside dropped is not worth failing over: %v", err)
	}
	sel, _ := asideCategories(t, settings)["fast"].(map[string]any)
	if sel["modelId"] != "relay/glm-4.6" || sel["provider"] != magpieID {
		t.Fatalf("the file was not written: %v", asideCategories(t, settings))
	}
	if a.Notice() == "" {
		t.Fatal("a change that only takes hold at Aside's next start says nothing about it")
	}
}

// The check is in the expression, so a write Aside answers for a different
// value than was asked for is caught rather than taken for done.
func TestAsideChecksWhatCameBack(t *testing.T) {
	asideHome(t)
	inner := asideSet
	var seen string
	asideSet = func(account, expr string) error {
		seen = expr
		return inner(account, expr)
	}
	t.Cleanup(func() { asideSet = inner })
	a := mustFindAside(t)
	if err := a.Apply("deep", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, `["deep"] || {}).modelId !== "relay/glm-4.6"`) {
		t.Fatalf("the change is not checked against what came back: %q", seen)
	}
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, `after.defaultModel.modelId !== "relay/glm-4.6"`) {
		t.Fatalf("the change is not checked against what came back: %q", seen)
	}
}

// A repl that never answers must not leave a pick hanging: the change waits
// about ten seconds and is then written to the file, which is what Aside
// reads at its next start, and the user is told so. This drives the real
// command, against an aside that hangs.
func TestAsideGivesUpOnAReplThatHangs(t *testing.T) {
	settings, _ := asideHome(t)
	// a shell builtin loop, not sleep: the PATH here is only the folder the
	// stand-in is in, so there is no sleep to be found
	hangingAside(t, "while :; do :; done")
	// TestMain stands in for the binary; a test that hangs has to run the real one
	real := asideRealSet
	asideSet = real
	t.Cleanup(func() { asideSet = func(string, string) error { return errors.New("aside: no Aside in a test") } })

	was := asideTimeout
	asideTimeout = 200 * time.Millisecond
	t.Cleanup(func() { asideTimeout = was })

	a := mustFindAside(t)
	start := time.Now()
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("a change waited %s on a repl that never answered", elapsed)
	}
	if dm := asideSettings(t, settings); dm["provider"] != magpieID {
		t.Fatalf("a change Aside could not be asked for is not in the file: %v", dm)
	}
	if a.Notice() == "" {
		t.Fatal("a change that only takes hold at Aside's next start says nothing about it")
	}
}

// asideRealSet is the command as it runs, kept beside the stand-in the package
// tests with, for a test that needs the process itself.
var asideRealSet = asideSet

// hangingAside puts an aside on PATH that runs body and never gets on with it.
func hangingAside(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script is not an exe")
	}
	dir := t.TempDir()
	aside := filepath.Join(dir, "aside")
	if err := os.WriteFile(aside, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// Switching off with no model of the user's behind magpie's takes the model
// off through Aside, not through the file: the daemon is still on the model,
// and reading the file behind it leaves it naming a provider that is on its
// way out.
func TestAsideDefaultWithNothingSavedGoesThroughAside(t *testing.T) {
	settings, _ := asideHome(t)
	inner := asideSet
	var seen []string
	asideSet = func(account, expr string) error {
		seen = append(seen, expr)
		return inner(account, expr)
	}
	t.Cleanup(func() { asideSet = inner })
	a := mustFindAside(t)
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	forget("aside.was") // nothing of the user's behind it
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(seen, "\n")
	if !strings.Contains(joined, "delete m.provider") {
		t.Fatalf("taking the model off went around Aside: %v", seen)
	}
	if dm := asideSettings(t, settings); dm["provider"] != nil && dm["provider"] != "" {
		t.Fatalf("the model is still named: %v", dm)
	}
}

// The provider block goes last on the way out: while the settings are still
// being changed, the provider to reach magpie's models through has to be
// there, or a step that fails on the way leaves the settings naming one that
// is gone.
func TestAsideDefaultTakesTheProviderOutLast(t *testing.T) {
	_, models := asideHome(t)
	a := mustFindAside(t)
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("fast", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	inner := asideSet
	var goneBefore bool
	asideSet = func(account, expr string) error {
		if _, ok := edit.GetJSON(models, "providers."+magpieID); !ok {
			goneBefore = true
		}
		return inner(account, expr)
	}
	t.Cleanup(func() { asideSet = inner })
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if goneBefore {
		t.Fatal("the provider was taken out before the settings were changed")
	}
	if b := asideBlock(t, models); b != nil {
		t.Fatal("the provider was not taken out at all")
	}
}

// editDelAsideModel empties the model list of the provider magpie wrote, as
// a catalog that has since changed would leave it.
func editDelAsideModel(t *testing.T, path string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(readFile(path)), &m); err != nil {
		t.Fatal(err)
	}
	prov := m["providers"].(map[string]any)[magpieID].(map[string]any)
	prov["models"] = []any{}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The change goes to Aside itself, not behind it: the default is written
// through the settings API, and a model id that would break the expression
// around it is passed as the string it is.
func TestAsideWritesItsDefaultThroughAside(t *testing.T) {
	settings, _ := asideHome(t)
	seen := ""
	inner := asideSet
	asideSet = func(account, expr string) error {
		seen = expr
		return inner(account, expr)
	}
	t.Cleanup(func() { asideSet = inner })
	a := mustFindAside(t)
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, `"provider":"magpie"`) || !strings.Contains(seen, asideOK) {
		t.Fatalf("the change went out as %q", seen)
	}
	if dm := asideSettings(t, settings); dm["provider"] != magpieID {
		t.Fatalf("%v", dm)
	}
	if n := asideNoticeRestart(); n != "" {
		t.Fatalf("a change Aside took is said to need a restart: %s", n)
	}
}

// A model id is never spliced into the expression as anything but a string.
func TestAsideQuotesAModelId(t *testing.T) {
	settings, _ := asideHome(t)
	asideStandIn(t, settings)
	if err := asideApplyDefault(settings, map[string]string{
		"modelId": `x","provider":"someone-else`,
	}); err != nil {
		t.Fatal(err)
	}
	if dm := asideSettings(t, settings); dm["modelId"] != `x","provider":"someone-else` || dm["provider"] != "native" {
		t.Fatalf("the id was read as part of the expression: %v", dm)
	}
}

// Where Aside cannot be reached the file is written all the same — it is what
// Aside reads at its next start — and the user is told it has to start again.
func TestAsideFallsBackToTheFileAndAsksForARestart(t *testing.T) {
	settings, _ := asideHome(t)
	inner := asideSet
	asideSet = func(account, expr string) error { return errors.New("aside: not installed") }
	t.Cleanup(func() { asideSet = inner })
	a := mustFindAside(t)
	if err := a.Apply("model", magpieID+"/relay/glm-4.6"); err != nil {
		t.Fatalf("a change Aside could not be asked for is not worth failing over: %v", err)
	}
	if dm := asideSettings(t, settings); dm["provider"] != magpieID || dm["modelId"] != "relay/glm-4.6" {
		t.Fatalf("the file was not written: %v", dm)
	}
	if dm := asideSettings(t, settings); dm["thinkingLevel"] != "high" || dm["fastMode"] != true {
		t.Fatalf("the rest of the default went with it: %v", dm)
	}
	if n := a.Notice(); n == "" {
		t.Fatal("a change that only takes hold at Aside's next start says nothing about it")
	}
}
