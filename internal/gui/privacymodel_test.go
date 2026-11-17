package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/pii"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
)

// The local masking model is on only once it is on disk: turned on where
// it can't be downloaded, nothing is saved; the Settings page's own save
// never turns it on or off; and off is saved at once.
func TestPrivacyModelOnlyOnOnceDownloaded(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	srv := webPageHandler(t, webHost{func() {}}, nil)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	var st privacyModelJSON
	if rec := do("GET", "/api/privacy-model", ""); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil {
		t.Fatalf("state: %d %s", rec.Code, rec.Body)
	}
	// on by default, not by the user, and not downloaded yet
	if !st.On || st.Chosen || st.Installed || st.Size == 0 {
		t.Fatalf("state %+v", st)
	}
	if !pii.Supported() {
		if rec := do("POST", "/api/privacy-model", `{"do":"on","model":"privacy-filter"}`); rec.Code == http.StatusOK {
			t.Fatalf("on where it can't run: %d %s", rec.Code, rec.Body)
		}
		if settings.Load().RedactModel {
			t.Fatal("saved on with no model")
		}
	}
	if rec := do("POST", "/api/privacy-model", `{"do":"on!"}`); rec.Code == http.StatusOK {
		t.Fatalf("a bad do: %d", rec.Code)
	}
	if rec := do("POST", "/api/settings", `{"theme":"dark","lang":"en","redactModel":true}`); rec.Code != http.StatusOK {
		t.Fatalf("settings save: %d %s", rec.Code, rec.Body)
	}
	if settings.Load().RedactModel {
		t.Fatal("the Settings page's save turned it on")
	}
	if err := settings.SetRedactModel(true); err != nil {
		t.Fatal(err)
	}
	if rec := do("POST", "/api/settings", `{"theme":"dark","lang":"en"}`); rec.Code != http.StatusOK {
		t.Fatalf("settings save: %d %s", rec.Code, rec.Body)
	}
	if !settings.Load().RedactModel {
		t.Fatal("the Settings page's save turned it off")
	}
	if rec := do("POST", "/api/privacy-model", `{"do":"off"}`); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil || st.On {
		t.Fatalf("off: %d %s", rec.Code, rec.Body)
	}
	if st := settings.Load(); st.RedactModel || !st.RedactModelOff {
		t.Fatal("off not saved")
	}
}

// The model on by default whose download is cancelled is turned off, the
// user's own off: the next request would start the download again.
func TestPrivacyModelCancelledDefaultIsOff(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	srv := webPageHandler(t, webHost{func() {}}, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/privacy-model", strings.NewReader(`{"do":"cancel"}`)))
	var st privacyModelJSON
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil || st.On {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	if !settings.Load().RedactModelOff {
		t.Fatal("cancelled default not saved off")
	}
}

// fakeModel is a pii.Model that is on disk, or downloads at once, and
// says what was done to it.
type fakeModel struct {
	id        string
	installed bool
	did       *[]string
}

func (f *fakeModel) ID() string           { return f.id }
func (f *fakeModel) Supported() bool      { return true }
func (f *fakeModel) Installed() bool      { return f.installed }
func (f *fakeModel) ManagesWeights() bool { return true }
func (f *fakeModel) Generation() string   { return "" }
func (f *fakeModel) MinScore() float64    { return 0.5 }
func (f *fakeModel) Size() int64          { return 1 }
func (f *fakeModel) Find(context.Context, []string) ([][]redact.Found, error) {
	return nil, nil
}
func (f *fakeModel) Now() pii.Status {
	return pii.Status{Model: f.id, ManagedWeights: true, Supported: true, Installed: f.installed, Size: 1}
}
func (f *fakeModel) Download(done func()) error {
	*f.did = append(*f.did, "download "+f.id)
	f.installed = true
	done()
	return nil
}
func (f *fakeModel) Cancel() {}
func (f *fakeModel) Warm()   { *f.did = append(*f.did, "warm "+f.id) }
func (f *fakeModel) Stop()   { *f.did = append(*f.did, "stop "+f.id) }
func (f *fakeModel) Remove() error {
	*f.did = append(*f.did, "remove "+f.id)
	f.installed = false
	return nil
}

// A new user's local model is AstrLink Guard; one picked while it is off
// is kept for turning it on, and one picked while it is on is downloaded
// and then masks, the one before let go.
func TestPrivacyModelPick(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	var did []string
	fakes := map[string]*fakeModel{
		pii.PrivacyFilter: {id: pii.PrivacyFilter, did: &did},
		pii.AstrLinkGuard: {id: pii.AstrLinkGuard, did: &did},
	}
	wasOf, wasModels := modelOf, models
	t.Cleanup(func() { modelOf, models = wasOf, wasModels })
	modelOf = func(id string) pii.Model { return fakes[id] }
	models = func() []pii.Model { return []pii.Model{fakes[pii.PrivacyFilter], fakes[pii.AstrLinkGuard]} }
	srv := webPageHandler(t, webHost{func() {}}, nil)
	do := func(body string) privacyModelJSON {
		t.Helper()
		rec := httptest.NewRecorder()
		method := "POST"
		if body == "" {
			method = "GET"
		}
		srv.ServeHTTP(rec, httptest.NewRequest(method, "/api/privacy-model", strings.NewReader(body)))
		var st privacyModelJSON
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
		return st
	}
	if st := do(""); !st.On || st.Chosen || st.Model != pii.AstrLinkGuard || len(st.Models) != 2 || st.Models[0].Model != pii.PrivacyFilter {
		t.Fatalf("a new user's state: %+v", st)
	}
	if st := do(`{"do":"off"}`); st.On || !settings.Load().RedactModelOff {
		t.Fatalf("turned off: %+v", st)
	}
	did = nil
	if st := do(`{"do":"pick","model":"privacy-filter"}`); st.On || st.Model != pii.PrivacyFilter || len(did) != 0 {
		t.Fatalf("picked while off: %+v, %v", st, did)
	}
	if st := do(`{"do":"on"}`); !st.On || st.Model != pii.PrivacyFilter || settings.Load().RedactModelName != pii.PrivacyFilter {
		t.Fatalf("on: %+v", st)
	}
	did = nil
	if st := do(`{"do":"pick","model":"astrlink-guard"}`); !st.On || st.Model != pii.AstrLinkGuard {
		t.Fatalf("picked while on: %+v", st)
	}
	if strings.Join(did, ", ") != "download astrlink-guard, stop privacy-filter, warm astrlink-guard" {
		t.Fatalf("did %v", did)
	}
	// the threshold: the model's own until one is set, for the model
	// masking uses with none named; 0 goes back to its own, and one out
	// of range is refused
	if st := do(""); st.MinScore != 0.5 || st.DefaultScore != 0.5 {
		t.Fatalf("threshold before one is set: %+v", st)
	}
	if st := do(`{"do":"score","score":0.9}`); st.MinScore != 0.9 || st.DefaultScore != 0.5 || settings.Load().RedactModelScore[pii.AstrLinkGuard] != 0.9 {
		t.Fatalf("threshold set: %+v, %v", st, settings.Load().RedactModelScore)
	}
	if st := do(`{"do":"score","model":"privacy-filter","score":0.6}`); st.MinScore != 0.9 || settings.Load().RedactModelScore[pii.PrivacyFilter] != 0.6 {
		t.Fatalf("another model's threshold: %+v, %v", st, settings.Load().RedactModelScore)
	}
	for _, bad := range []string{`0.3`, `1`, `-1`} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/privacy-model", strings.NewReader(`{"do":"score","score":`+bad+`}`)))
		if rec.Code == http.StatusOK || settings.Load().RedactModelScore[pii.AstrLinkGuard] != 0.9 {
			t.Fatalf("threshold %s: %d", bad, rec.Code)
		}
	}
	if st := do(`{"do":"score","score":0}`); st.MinScore != 0.5 || len(settings.Load().RedactModelScore) != 1 {
		t.Fatalf("back to the default: %+v, %v", st, settings.Load().RedactModelScore)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/privacy-model", strings.NewReader(`{"do":"pick","model":"gpt"}`)))
	if rec.Code == http.StatusOK || settings.Load().RedactModelName != pii.AstrLinkGuard {
		t.Fatalf("a model there's none of: %d", rec.Code)
	}
	did = nil
	if st := do(`{"do":"remove"}`); st.On || strings.Join(did, ", ") != "remove privacy-filter, remove astrlink-guard" {
		t.Fatalf("remove: %+v %v", st, did)
	}
}

// Models folder (MetalHqa on X: both went onto the C: drive): Move… takes
// the picker's folder and moves what is downloaded there, the state says
// where the models are and that the user picked it, Move back returns them
// to magpie's cache, and a cancelled picker moves nothing.
func TestPrivacyModelFolder(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Cleanup(func() { settings.SetRedactModelDir("") })
	def := pii.Root()
	held := filepath.Join(def, "astrlink-guard", "rev", "model.onnx")
	if err := os.MkdirAll(filepath.Dir(held), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(held, []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	picked := filepath.Join(t.TempDir(), "models")
	pick := &pickFolder{dir: picked}
	srv := webPageHandler(t, folderPick{p: pick}, nil)
	do := func(body string) privacyModelJSON {
		t.Helper()
		rec := httptest.NewRecorder()
		method := "POST"
		if body == "" {
			method = "GET"
		}
		srv.ServeHTTP(rec, httptest.NewRequest(method, "/api/privacy-model", strings.NewReader(body)))
		var st privacyModelJSON
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
		return st
	}
	settle := func() privacyModelJSON {
		t.Helper()
		for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			if st := do(""); !st.Moving.Moving {
				return st
			}
		}
		t.Fatal("the move never ended")
		return privacyModelJSON{}
	}
	if st := do(""); st.FolderSet || st.Folder != tilde(def) {
		t.Fatalf("before: %+v", st)
	}
	pick.dir = ""
	do(`{"do":"folder"}`)
	if st := settle(); st.FolderSet {
		t.Fatalf("a cancelled picker moved: %+v", st)
	}
	pick.dir = picked
	do(`{"do":"folder"}`)
	st := settle()
	if !st.FolderSet || st.Folder != tilde(picked) || st.Moving.Error != "" || settings.Load().RedactModelDir != picked {
		t.Fatalf("moved: %+v", st)
	}
	if b, err := os.ReadFile(filepath.Join(picked, "astrlink-guard", "rev", "model.onnx")); err != nil || string(b) != "weights" {
		t.Fatalf("the model isn't in the picked folder: %v", err)
	}
	if _, err := os.Stat(held); !os.IsNotExist(err) {
		t.Fatalf("the old copy is still there: %v", err)
	}
	do(`{"do":"folder","dir":"default"}`)
	st = settle()
	if st.FolderSet || st.Folder != tilde(def) || settings.Load().RedactModelDir != "" {
		t.Fatalf("back: %+v", st)
	}
	if _, err := os.Stat(held); err != nil {
		t.Fatalf("the model didn't come back: %v", err)
	}
	// a folder that can't be used is an error, not a move
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/privacy-model", strings.NewReader(`{"do":"folder","dir":"relative/path"}`)))
	if rec.Code == http.StatusOK {
		t.Fatalf("a relative folder was accepted")
	}
}

// folderPick answers the picker with whatever its pointer holds now.
type folderPick struct {
	folderOnly
	p *pickFolder
}

func (f folderPick) ChooseFolder(s string) (string, error) { return f.p.ChooseFolder(s) }

// pluginFakeModel is a plugin's model (pii/external.go): nothing to
// download, and unavailable while its plugin is off.
type pluginFakeModel struct{ *fakeModel }

func (pluginFakeModel) ManagesWeights() bool { return false }
func (p pluginFakeModel) Now() pii.Status {
	s := p.fakeModel.Now()
	s.ManagedWeights, s.Name = false, "My detector"
	if !p.installed {
		s.Failed = "the local masking model My detector is unavailable"
	}
	return s
}

// A plugin's model is picked by its id (#6959). One whose plugin is off is
// refused with why, nothing downloaded and the pick before kept; one whose
// files are there masks at once, the one before let go. An id no model
// has is refused.
func TestPrivacyModelPickPluginModel(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	const id = "external/plugin/0123456789abcdef/v1"
	var did []string
	guard := &fakeModel{id: pii.AstrLinkGuard, installed: true, did: &did}
	mine := pluginFakeModel{&fakeModel{id: id, did: &did}}
	wasOf, wasModels := modelOf, models
	t.Cleanup(func() { modelOf, models = wasOf, wasModels })
	modelOf = func(m string) pii.Model {
		if m == id {
			return mine
		}
		return guard
	}
	models = func() []pii.Model { return []pii.Model{guard, mine} }
	srv := webPageHandler(t, webHost{func() {}}, nil)
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/privacy-model", strings.NewReader(body)))
		return rec
	}
	if rec := post(`{"do":"on","model":"astrlink-guard"}`); rec.Code != http.StatusOK {
		t.Fatalf("on: %d %s", rec.Code, rec.Body)
	}
	did = nil
	if rec := post(`{"do":"pick","model":"` + id + `"}`); rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "My detector is unavailable") {
		t.Fatalf("picked while its plugin is off: %d %s", rec.Code, rec.Body)
	}
	if len(did) != 0 || settings.Load().RedactModelName != pii.AstrLinkGuard {
		t.Fatalf("refused, yet did %v, name %q", did, settings.Load().RedactModelName)
	}
	mine.installed = true
	if rec := post(`{"do":"pick","model":"` + id + `"}`); rec.Code != http.StatusOK || settings.Load().RedactModelName != id {
		t.Fatalf("picked: %d %s", rec.Code, rec.Body)
	}
	if strings.Join(did, ", ") != "stop astrlink-guard, warm "+id {
		t.Fatalf("did %v", did)
	}
	if rec := post(`{"do":"pick","model":"external/plugin/0123456789abcdef/nope"}`); rec.Code == http.StatusOK {
		t.Fatalf("an unknown id picked: %d", rec.Code)
	}
}
