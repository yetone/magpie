package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

func TestAsideInitialReadOffersStageWithoutWrites(t *testing.T) {
	settings, models := asideHome(t)
	c := newAsideConnection(here(""))
	before := readFile(settings) + readFile(models) + readFile(c.record)
	asideRead = func() (map[string]json.RawMessage, error) { return nil, errors.New("offline") }
	asideSet = func(string, string) error { t.Fatal("unavailable apply called daemon set"); return nil }
	err := mustFindAside(t).Pick("model", "magpie/relay/glm-4.6")
	var unavailable *RuntimeUnavailableError
	if !errors.As(err, &unavailable) || unavailable.Offline != OfflineStage {
		t.Fatalf("initial read must return typed unavailable error, got %T: %v", err, err)
	}
	if got := readFile(settings) + readFile(models) + readFile(c.record); got != before {
		t.Fatal("unavailable pick wrote files")
	}
}

func TestAsideOfflineDisconnectRestoresWithoutDaemon(t *testing.T) {
	settings, models := asideHome(t)
	before := readFile(settings)
	a := mustFindAside(t)
	for _, key := range []string{"model", "fast", "deep"} {
		if err := a.Native.Stage(key, "magpie/relay/glm-4.6"); err != nil {
			t.Fatal(err)
		}
	}
	asideRead = func() (map[string]json.RawMessage, error) { t.Fatal("offline disconnect read daemon"); return nil, nil }
	asideSet = func(string, string) error { t.Fatal("offline disconnect set daemon"); return nil }
	t.Setenv("PATH", t.TempDir())
	if proc.FindTool(a.Bin) != "" {
		t.Fatal("fixture must have no Aside executable")
	}
	missing := &Agent{ID: "dsh", Name: "Native fixture", Bin: "magpie-test-missing-native", Dir: a.Dir, Path: a.Path, Native: a.Native}
	if !missing.CLIMissing() {
		t.Fatal("native fixture must report missing CLI")
	}
	if err := missing.DisconnectOffline(); err != nil {
		t.Fatal(err)
	}
	var got, want any
	json.Unmarshal([]byte(readFile(settings)), &got)
	json.Unmarshal([]byte(before), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restored %s; want %s", readFile(settings), before)
	}
	if _, ok := edit.GetJSON(models, "providers.magpie"); ok {
		t.Fatal("provider retained")
	}
	r, err := newAsideConnection(here("")).load()
	if err != nil || len(r.Fields) != 0 {
		t.Fatalf("ownership retained: %+v %v", r, err)
	}
}

func TestAsideUnavailableBoundary(t *testing.T) {
	for _, tt := range []struct {
		key, value string
		typed      bool
		action     OfflineAction
	}{
		{"model", "native/unlisted", true, OfflineStage},
		{"fast", "relay/glm-4.6", true, OfflineStage},
		{"image", "native/image", true, OfflineStage},
		{"effort", "high", true, ""},
		{"fast", "", true, ""},
		{"model", "", true, ""},
		{"unknown", "native/model", false, ""},
		{"model", "malformed", false, ""},
		{"model", "magpie/relay/missing", false, ""},
		{"image", "magpie/relay/glm-4.6", false, ""},
	} {
		t.Run(tt.key+"/"+tt.value, func(t *testing.T) {
			settings, models := asideHome(t)
			c := newAsideConnection(here(""))
			before := readFile(settings) + readFile(models) + readFile(c.record)
			reads := 0
			asideRead = func() (map[string]json.RawMessage, error) { reads++; return nil, errors.New("offline") }
			asideSet = func(string, string) error { t.Fatal("set before availability confirmed"); return nil }
			err := mustFindAside(t).Pick(tt.key, tt.value)
			var unavailable *RuntimeUnavailableError
			if errors.As(err, &unavailable) != tt.typed {
				t.Fatalf("%T %v", err, err)
			}
			if unavailable != nil && unavailable.Offline != tt.action {
				t.Fatalf("offline action %q", unavailable.Offline)
			}
			if !tt.typed && reads != 0 {
				t.Fatal("invalid selection read daemon")
			}
			if readFile(settings)+readFile(models)+readFile(c.record) != before {
				t.Fatal("initial failure wrote files")
			}
		})
	}
}

func TestAsidePostReadFailuresNeverOfferOffline(t *testing.T) {
	for _, kind := range []string{"corrupt record", "save record", "set refusal", "readback failure", "readback mismatch"} {
		t.Run(kind, func(t *testing.T) {
			_, models := asideHome(t)
			a := mustFindAside(t)
			c := newAsideConnection(here(""))
			if err := a.Connect(); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "corrupt record":
				writeFile(t, c.record, "{invalid")
			case "save record":
				if err := a.Native.Stage("model", "magpie/relay/glm-4.6"); err != nil {
					t.Fatal(err)
				}
				refuseWrites(t, c.record)
			case "set refusal":
				asideSet = func(string, string) error { return errors.New("refused") }
			case "readback mismatch":
				asideSet = func(string, string) error { return nil }
			case "readback failure":
				inner := asideRead
				reads := 0
				asideRead = func() (map[string]json.RawMessage, error) {
					reads++
					if reads > 1 {
						return nil, errors.New("readback")
					}
					return inner()
				}
			}
			err := a.Pick("fast", "magpie/relay/glm-4.6")
			var unavailable *RuntimeUnavailableError
			if err == nil || errors.As(err, &unavailable) {
				t.Fatalf("post-read failure offered fallback: %T %v", err, err)
			}
			if _, ok := edit.GetJSON(models, "providers.magpie"); !ok {
				t.Fatal("failed apply removed provider")
			}
		})
	}
}

func TestAsideOfflineRestoresCompleteSelectionsAndPreservesUserChanges(t *testing.T) {
	settings, models := asideHome(t)
	if err := provider.Save(provider.Provider{ID: "art", Key: "fixture", Chat: "https://art.invalid/v1", Models: []string{"gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetJSON(settings,
		edit.KV{Path: "modelCategories.fast", Value: json.RawMessage(`{"provider":"native","modelId":"small","thinkingLevel":"low","extra":{"keep":true}}`)},
		edit.KV{Path: "modelCategories.standard", Value: json.RawMessage(`{"provider":"native","modelId":"standard","thinkingLevel":"high"}`)}); err != nil {
		t.Fatal(err)
	}
	before := readFile(settings)
	a := mustFindAside(t)
	for _, key := range []string{"model", "fast", "standard", "deep", "visual", "image"} {
		value := "magpie/relay/glm-4.6"
		if key == "image" {
			value = "magpie/art/gpt-image-1"
		}
		if err := a.Native.Stage(key, value); err != nil {
			t.Fatal(err)
		}
	}
	changed := json.RawMessage(`{"provider":"native","modelId":"user-choice","custom":"yes"}`)
	if err := edit.SetJSON(settings, edit.KV{Path: "modelCategories.standard", Value: changed}); err != nil {
		t.Fatal(err)
	}
	asideRead = func() (map[string]json.RawMessage, error) { t.Fatal("offline read daemon"); return nil, nil }
	asideSet = func(string, string) error { t.Fatal("offline set daemon"); return nil }
	if err := a.DisconnectOffline(); err != nil {
		t.Fatal(err)
	}
	var want, got map[string]json.RawMessage
	json.Unmarshal([]byte(before), &want)
	json.Unmarshal([]byte(readFile(settings)), &got)
	var roles map[string]json.RawMessage
	json.Unmarshal(want["modelCategories"], &roles)
	roles["standard"] = changed
	want["modelCategories"], _ = json.Marshal(roles)
	if !asideSameSetting(asideTestJSON(t, got), asideTestJSON(t, want)) {
		t.Fatalf("got %s; want %v", readFile(settings), want)
	}
	if v, _ := edit.GetJSON(models, "keepMe"); v != "yes" {
		t.Fatal("unrelated models data lost")
	}
	if _, ok := edit.GetJSON(models, "providers.native"); !ok {
		t.Fatal("native provider lost")
	}
	if _, ok := edit.GetJSON(models, "providers.magpie"); ok {
		t.Fatal("magpie retained")
	}
	c := newAsideConnection(here(""))
	r, err := c.load()
	if err != nil || len(r.Fields) != 0 {
		t.Fatalf("record not cleared: %+v %v", r, err)
	}
	// a file's mode is Unix's: Windows keeps the read-only bit and no more
	if runtime.GOOS != "windows" {
		mode, err := os.Stat(c.record)
		if err != nil || mode.Mode().Perm() != 0600 {
			t.Fatalf("record mode: %v %v", mode, err)
		}
	}
}

func TestAsideOfflinePlanGuards(t *testing.T) {
	for _, file := range []string{"settings", "models", "record", "foreign", "nil", "structure", "restore"} {
		t.Run(file, func(t *testing.T) {
			settings, models := asideHome(t)
			a := mustFindAside(t)
			c := newAsideConnection(here(""))
			if err := a.Native.Stage("model", "magpie/relay/glm-4.6"); err != nil {
				t.Fatal(err)
			}
			plan, err := a.Native.Disconnect()
			if err != nil {
				t.Fatal(err)
			}
			switch file {
			case "settings":
				edit.SetJSON(settings, edit.KV{Path: "theme", Value: "light"})
			case "models":
				edit.SetJSON(models, edit.KV{Path: "keepMe", Value: "changed"})
			case "record":
				edit.SetJSON(c.record, edit.KV{Path: "fields.model.before.modelId", Value: "changed"})
			case "foreign":
				edit.SetJSON(models, edit.KV{Path: "providers.magpie.apiKey", Value: "someone-else"})
			case "nil":
				plan = nil
			case "structure":
				plan.Files[0].Path = filepath.Join(t.TempDir(), "unexpected")
			case "restore":
				writeFile(t, c.record, `{"fields":{}}`)
			}
			before := readFile(settings) + readFile(models) + readFile(c.record)
			asideRead = func() (map[string]json.RawMessage, error) { t.Fatal("guard read daemon"); return nil, nil }
			asideSet = func(string, string) error { t.Fatal("guard set daemon"); return nil }
			if err := a.Native.ExecuteOffline(plan); err == nil {
				t.Fatal("unsafe plan accepted")
			}
			if readFile(settings)+readFile(models)+readFile(c.record) != before {
				t.Fatal("guard failure wrote files")
			}
		})
	}
}

func TestAsideOfflineLegacyRecordDoesNotResurrect(t *testing.T) {
	settings, _ := asideHome(t)
	edit.SetJSON(settings, edit.KV{Path: "defaultModel.provider", Value: "magpie"}, edit.KV{Path: "defaultModel.modelId", Value: "relay/glm-4.6"})
	stash(map[string]string{"aside.was": "native/native-model"})
	a := mustFindAside(t)
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	plan, err := a.Native.Disconnect()
	if err != nil {
		t.Fatal(err)
	}
	stash(map[string]string{"aside.was": "native/changed"})
	if err := a.Native.ExecuteOffline(plan); err == nil {
		t.Fatal("changed legacy restore point accepted")
	}
	asideRead = func() (map[string]json.RawMessage, error) { t.Fatal("legacy offline read daemon"); return nil, nil }
	if err := a.DisconnectOffline(); err != nil {
		t.Fatal(err)
	}
	if got := mustFindAside(t).Field("model").Get(); got != "native/changed" {
		t.Fatalf("restored %s", got)
	}
	c := newAsideConnection(here(""))
	r, err := c.load()
	if err != nil || len(r.Fields) != 0 {
		t.Fatal("legacy restore points resurrected")
	}
	if runtime.GOOS != "windows" {
		mode, err := os.Stat(c.record)
		if err != nil || mode.Mode().Perm() != 0600 {
			t.Fatalf("record mode: %v %v", mode, err)
		}
	}
}

func TestAsideOfflineWriteFailureRollsBack(t *testing.T) {
	settings, models := asideHome(t)
	a := mustFindAside(t)
	c := newAsideConnection(here(""))
	if err := a.Native.Stage("model", "magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	plan, err := a.Native.Disconnect()
	if err != nil {
		t.Fatal(err)
	}
	before := readFile(settings) + readFile(models) + readFile(c.record)
	// the settings file refuses a write: the first of the three the plan
	// restores, so nothing of them goes through
	refuseWrites(t, settings)
	err = a.Native.ExecuteOffline(plan)
	// the refusal each platform names its own way: Unix "permission denied",
	// Windows "Access is denied"
	if err == nil || !refused(err) {
		t.Fatalf("expected write refusal after settings restoration: %v", err)
	}
	if readFile(settings)+readFile(models)+readFile(c.record) != before {
		t.Fatal("write failure did not roll back")
	}
}

func TestAsideDisconnectUnavailableOnlyAtInitialRead(t *testing.T) {
	for _, kind := range []string{"initial", "record", "refusal", "readback", "mismatch"} {
		t.Run(kind, func(t *testing.T) {
			_, models := asideHome(t)
			a := mustFindAside(t)
			if err := a.Native.Stage("model", "magpie/relay/glm-4.6"); err != nil {
				t.Fatal(err)
			}
			plan, err := a.Native.Disconnect()
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "initial":
				asideRead = func() (map[string]json.RawMessage, error) { return nil, errors.New("offline") }
			case "record":
				refuseWrites(t, newAsideConnection(here("")).record)
			case "refusal":
				asideSet = func(string, string) error { return errors.New("refused") }
			case "mismatch":
				asideSet = func(string, string) error { return nil }
			case "readback":
				inner := asideRead
				reads := 0
				asideRead = func() (map[string]json.RawMessage, error) {
					reads++
					if reads > 1 {
						return nil, errors.New("offline")
					}
					return inner()
				}
			}
			err = a.Native.Execute(plan)
			var unavailable *RuntimeUnavailableError
			if err == nil || errors.As(err, &unavailable) != (kind == "initial") {
				t.Fatalf("%s: %T %v", kind, err, err)
			}
			if unavailable != nil && unavailable.Offline != OfflineDisconnect {
				t.Fatal("disconnect action missing")
			}
			if _, ok := edit.GetJSON(models, "providers.magpie"); !ok {
				t.Fatal("failed live disconnect removed provider")
			}
		})
	}
}

func asideTestJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAsideCorruptRecordDoesNotOfferStageWhenOffline(t *testing.T) {
	asideHome(t)
	c := newAsideConnection(here(""))
	writeFile(t, c.record, "{corrupt")
	asideRead = func() (map[string]json.RawMessage, error) { return nil, errors.New("offline") }
	err := mustFindAside(t).Pick("model", "native/unlisted")
	var unavailable *RuntimeUnavailableError
	if err == nil || errors.As(err, &unavailable) {
		t.Fatalf("corrupt record offered staging: %T %v", err, err)
	}
}

func TestAsideStageAndApplyShareSpellingAndValidation(t *testing.T) {
	settings, models := asideHome(t)
	a := mustFindAside(t)
	if err := a.Pick("model", "relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if got := mustFindAside(t).Field("model").Get(); got != "magpie/relay/glm-4.6" {
		t.Fatalf("apply spelling %s", got)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	value, err := a.Spell("model", "relay/glm-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Native.Stage("model", value); err != nil {
		t.Fatal(err)
	}
	if got := mustFindAside(t).Field("model").Get(); got != "magpie/relay/glm-4.6" {
		t.Fatalf("stage spelling %s", got)
	}
	c := newAsideConnection(here(""))
	before := readFile(settings) + readFile(models) + readFile(c.record)
	for _, tt := range []struct{ key, value string }{{"unknown", "native/model"}, {"model", "invalid"}, {"model", "magpie/relay/missing"}, {"image", "magpie/relay/glm-4.6"}, {"effort", "high"}, {"fast", ""}} {
		if err := a.Native.Stage(tt.key, tt.value); err == nil {
			t.Fatalf("invalid stage accepted: %+v", tt)
		}
	}
	if readFile(settings)+readFile(models)+readFile(c.record) != before {
		t.Fatal("invalid stage wrote files")
	}
}

func TestAsideOfflineUnsetDefaultIsRemoved(t *testing.T) {
	settings, _ := asideHome(t)
	if err := edit.DelJSON(settings, "defaultModel"); err != nil {
		t.Fatal(err)
	}
	a := mustFindAside(t)
	if err := a.Native.Stage("model", "magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	asideRead = func() (map[string]json.RawMessage, error) { t.Fatal("offline read daemon"); return nil, nil }
	if err := a.DisconnectOffline(); err != nil {
		t.Fatal(err)
	}
	if _, ok := edit.GetJSON(settings, "defaultModel"); ok {
		t.Fatal("absent default restored as null")
	}
}

func TestDisconnectOfflineUnsupportedCapability(t *testing.T) {
	for _, a := range []*Agent{{Name: "legacy"}, {Name: "native", Native: &NativeConnection{Disconnect: func() (*DisconnectPlan, error) { t.Fatal("unsupported capability built plan"); return nil, nil }}}} {
		if err := a.DisconnectOffline(); err == nil {
			t.Fatal("unsupported offline action accepted")
		}
	}
}

func TestAsideRawNativeSettersKeepCatalogNames(t *testing.T) {
	asideHome(t)
	a := mustFindAside(t)
	if err := a.Field("model").Set("relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if got := mustFindAside(t).Field("model").Get(); got != "relay/glm-4.6" {
		t.Fatalf("native apply rewritten: %s", got)
	}
	if err := a.Native.Stage("fast", "relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if got := mustFindAside(t).Field("fast").Get(); got != "relay/glm-4.6" {
		t.Fatalf("native stage rewritten: %s", got)
	}
}

func TestAsideOfflineImageRestoresFullSavedObject(t *testing.T) {
	settings, _ := asideHome(t)
	before := json.RawMessage(`{"provider":"native","modelId":"image","thinkingLevel":"high","extra":{"keep":true}}`)
	if err := edit.SetJSON(settings, edit.KV{Path: asideImageKey, Value: before}); err != nil {
		t.Fatal(err)
	}
	a := mustFindAside(t)
	if err := provider.Save(provider.Provider{ID: "art", Key: "fixture", Chat: "https://art.invalid/v1", Models: []string{"gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Native.Stage("image", "magpie/art/gpt-image-1"); err != nil {
		t.Fatal(err)
	}
	asideRead = func() (map[string]json.RawMessage, error) { t.Fatal("offline image read daemon"); return nil, nil }
	asideSet = func(string, string) error { t.Fatal("offline image set daemon"); return nil }
	if err := a.DisconnectOffline(); err != nil {
		t.Fatal(err)
	}
	got, ok := edit.GetJSON(settings, asideImageKey)
	if !ok || !asideSameSetting(json.RawMessage(got), before) {
		t.Fatalf("image restored %s", got)
	}
}

func TestAsideOfflineProviderWriteFailureRollsBackAllFiles(t *testing.T) {
	// the rollback is over hard links to a symlink, which needs a privilege
	// Windows does not grant by default
	if runtime.GOOS == "windows" {
		t.Skip("a symlink needs a privilege Windows does not grant by default")
	}
	settings, models := asideHome(t)
	a := mustFindAside(t)
	if err := a.Native.Stage("model", "magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "models.json")
	contents := readFile(models)
	writeFile(t, target, contents)
	if err := os.Remove(models); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, models); err != nil {
		t.Fatal(err)
	}
	c := newAsideConnection(here(""))
	plan, err := a.Native.Disconnect()
	if err != nil {
		t.Fatal(err)
	}
	before := readFile(settings) + readFile(models) + readFile(c.record)
	if err := os.Chmod(targetDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(targetDir, 0700) })
	asideRead = func() (map[string]json.RawMessage, error) { t.Fatal("rollback read daemon"); return nil, nil }
	if err := a.Native.ExecuteOffline(plan); err == nil {
		t.Fatal("provider write should fail")
	}
	if readFile(settings)+readFile(models)+readFile(c.record) != before {
		t.Fatal("final provider failure did not roll back settings and record")
	}
}
