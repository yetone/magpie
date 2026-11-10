package agent

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeShelley answers on a Unix socket as `shelley serve` does, for the
// custom models, models and profiles routes magpie uses (server/
// custom_models.go, profiles.go): a strict profile PUT, a model id made
// from the endpoint and name, a thinking level refused for a model that
// doesn't reason.
type fakeShelley struct {
	mu       sync.Mutex
	builtin  []shelleyInfo
	custom   []map[string]any
	keys     map[string]string // model id → api key
	profiles []map[string]any
	writes   []string
}

func startShelley(t *testing.T) *fakeShelley {
	t.Helper()
	f := &fakeShelley{
		builtin: []shelleyInfo{{ID: "claude-opus-4.5", Name: "Claude Opus 4.5", Ready: true}, {ID: "gpt-5", Name: "GPT-5", Ready: false}},
		custom: []map[string]any{{"model_id": "my-local", "display_name": "My local", "provider_type": "openai",
			"endpoint": "http://localhost:11434/v1", "model_name": "qwen3", "reasoning_support": "yes"}},
		keys: map[string]string{"my-local": "sk-own"},
		profiles: []map[string]any{
			{"name": "work", "default": false, "builtin": false, "model": "my-local", "thinking_level": "", "system_prompt": ""},
			{"name": "default", "default": true, "builtin": true, "user_edited": true, "project": "", "model": "claude-opus-4.5",
				"thinking_level": "high", "tool_overrides": map[string]any{"bash": true}, "system_prompt": "be brief"},
		},
	}
	// macOS caps a socket's path near 104 bytes: t.TempDir() is too deep
	dir, err := os.MkdirTemp("", "sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "shelley.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	old := shelleySockets
	shelleySockets = func() []string { return []string{filepath.Join(dir, "gone.sock"), sock} }
	t.Cleanup(func() { shelleySockets = old })
	return f
}

var shelleySlug = regexp.MustCompile(`[^a-z0-9]+`)

func (f *fakeShelley) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != "GET" {
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
	}
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	path := r.URL.Path
	switch {
	case r.Method == "GET" && path == "/api/models":
		out := slices.Clone(f.builtin)
		for _, m := range f.custom {
			out = append(out, shelleyInfo{ID: m["model_id"].(string), Name: m["display_name"].(string), Ready: true})
		}
		reply(200, out)
	case r.Method == "GET" && path == "/api/custom-models":
		reply(200, f.custom)
	case r.Method == "POST" && path == "/api/custom-models":
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		for _, k := range []string{"display_name", "provider_type", "endpoint", "api_key", "model_name"} {
			if s, _ := b[k].(string); s == "" {
				http.Error(w, k+" is required", 400)
				return
			}
		}
		base := strings.Trim(shelleySlug.ReplaceAllString(strings.ToLower(b["model_name"].(string)), "-"), "-")
		id := base
		for i := 2; f.custom1(id) != nil; i++ {
			id = fmt.Sprintf("%s-%d", base, i)
		}
		f.keys[id] = b["api_key"].(string)
		delete(b, "api_key")
		b["model_id"] = id
		f.custom = append(f.custom, b)
		reply(201, b)
	case strings.HasPrefix(path, "/api/custom-models/"):
		id := strings.TrimPrefix(path, "/api/custom-models/")
		m := f.custom1(id)
		if m == nil {
			http.Error(w, "not found", 404)
			return
		}
		switch r.Method {
		case "PUT":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			if k, _ := b["api_key"].(string); k != "" {
				f.keys[id] = k
			}
			delete(b, "api_key")
			for k, v := range b {
				m[k] = v
			}
			reply(200, m)
		case "DELETE":
			f.custom = slices.DeleteFunc(f.custom, func(c map[string]any) bool { return c["model_id"] == id })
			w.WriteHeader(204)
		}
	case r.Method == "GET" && path == "/api/profiles":
		reply(200, f.profiles)
	case r.Method == "PUT" && strings.HasPrefix(path, "/api/profiles/"):
		name := strings.TrimPrefix(path, "/api/profiles/")
		i := slices.IndexFunc(f.profiles, func(p map[string]any) bool { return p["name"] == name })
		if i < 0 {
			http.Error(w, "not found", 404)
			return
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		for k := range b {
			if _, ok := f.profiles[i][k]; !ok {
				http.Error(w, "json: unknown field "+k, 400)
				return
			}
		}
		model, _ := b["model"].(string)
		if model != "" && model != f.profiles[i]["model"] && !f.ready(model) {
			http.Error(w, "model not available: "+model, 400)
			return
		}
		if lvl, _ := b["thinking_level"].(string); lvl != "" && model != "" {
			if m := f.custom1(model); m != nil && m["reasoning_support"] == "no" {
				http.Error(w, "model does not support reasoning", 400)
				return
			}
		}
		f.profiles[i] = b
		reply(200, b)
	default:
		http.Error(w, "no route", 404)
	}
}

func (f *fakeShelley) custom1(id string) map[string]any {
	for _, m := range f.custom {
		if m["model_id"] == id {
			return m
		}
	}
	return nil
}

func (f *fakeShelley) ready(id string) bool {
	return f.custom1(id) != nil || slices.ContainsFunc(f.builtin, func(m shelleyInfo) bool { return m.ID == id && m.Ready })
}

func (f *fakeShelley) def() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.profiles {
		if p["default"] == true {
			return p
		}
	}
	return nil
}

// repoint is something else changing a custom model's endpoint in Shelley:
// how many it changed.
func (f *fakeShelley) repoint(from, to string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.custom {
		if u, _ := m["endpoint"].(string); strings.Contains(u, from) {
			m["endpoint"] = strings.ReplaceAll(u, from, to)
			n++
		}
	}
	return n
}

func (f *fakeShelley) takeWrites() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := f.writes
	f.writes = nil
	return w
}

func TestShelley(t *testing.T) {
	home := syncHome(t)
	f := startShelley(t)
	a := shelley(home)
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !a.Detected() {
		t.Fatal("not detected")
	}
	fl := a.Field("model")
	if got := fl.Get(); got != "claude-opus-4.5" {
		t.Fatalf("get: %q", got)
	}
	// nothing of magpie's: Sync writes nothing
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if w := f.takeWrites(); len(w) != 0 {
		t.Fatalf("sync wrote %v", w)
	}

	var ref string
	own := map[string]bool{}
	for _, o := range fl.Options(a.Values()) {
		if o.Ref != "" && ref == "" {
			ref = o.Ref
		}
		if o.Ref == "" {
			own[o.Value] = true
		}
	}
	if ref == "" {
		t.Fatal("no magpie model offered")
	}
	// the user's own ready models are offered, a model not ready isn't
	if !own["claude-opus-4.5"] || !own["my-local"] || own["gpt-5"] {
		t.Fatalf("own options: %v", own)
	}

	if err := a.Apply("model", "magpie/"+ref); err != nil {
		t.Fatal(err)
	}
	var mine map[string]any
	for _, m := range f.custom {
		if m["model_id"] != "my-local" {
			mine = m
		}
	}
	if len(f.custom) != 2 || mine == nil {
		t.Fatalf("custom models: %v", f.custom)
	}
	id := mine["model_id"].(string)
	if mine["provider_type"] != "openai" || mine["endpoint"] != gatewayV1() || mine["model_name"] != ref || f.keys[id] != agentKey("shelley") {
		t.Fatalf("magpie's model: %v key %q", mine, f.keys[id])
	}
	d := f.def()
	if d["model"] != id || d["system_prompt"] != "be brief" || d["tool_overrides"] == nil || d["user_edited"] != true {
		t.Fatalf("default profile: %v", d)
	}
	if got := fl.Get(); got != "magpie/"+ref {
		t.Fatalf("get: %q", got)
	}
	// the user's own model and other profile are as they were
	if m := f.custom1("my-local"); m["endpoint"] != "http://localhost:11434/v1" || f.keys["my-local"] != "sk-own" {
		t.Fatalf("own model: %v", m)
	}
	if f.profiles[0]["model"] != "my-local" {
		t.Fatalf("work profile: %v", f.profiles[0])
	}
	// magpie's own models aren't offered as the user's
	for _, o := range fl.Options(a.Values()) {
		if o.Value == id {
			t.Fatalf("magpie's model offered as the user's: %+v", o)
		}
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift: %+v", d)
	}
	if s := a.Check(); s != "" {
		t.Fatalf("check: %q", s)
	}
	f.takeWrites()

	// picked again: the same model, no second one
	if err := a.Apply("model", "magpie/"+ref); err != nil {
		t.Fatal(err)
	}
	if len(f.custom) != 2 {
		t.Fatalf("custom models: %v", f.custom)
	}

	// the gateway moved: Sync points magpie's model back, the user's stays
	f.repoint(gatewayV1(), "http://127.0.0.1:9/v1")
	if s := a.Check(); s == "" {
		t.Fatal("check: nothing off")
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if f.custom1(id)["endpoint"] != gatewayV1() || f.custom1("my-local")["endpoint"] != "http://localhost:11434/v1" {
		t.Fatalf("custom models: %v", f.custom)
	}

	// back to Shelley's own: the profile is as it was, thinking level
	// included, and magpie's model comes out
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	d = f.def()
	if d["model"] != "claude-opus-4.5" || d["thinking_level"] != "high" || d["system_prompt"] != "be brief" {
		t.Fatalf("default profile: %v", d)
	}
	if len(f.custom) != 1 || f.custom[0]["model_id"] != "my-local" {
		t.Fatalf("custom models: %v", f.custom)
	}
	for _, k := range []string{shelleyModelsKey, shelleyWasKey, shelleyTakeOut} {
		if v := stashShared()[k]; v != "" {
			t.Fatalf("stash %s left: %q", k, v)
		}
	}
	if got := fl.Get(); got != "claude-opus-4.5" {
		t.Fatalf("get: %q", got)
	}
}

// A model the user wired to the gateway by hand is theirs: magpie neither
// takes it for its own nor takes it out.
func TestShelleyKeepsHandWired(t *testing.T) {
	home := syncHome(t)
	f := startShelley(t)
	f.custom = append(f.custom, map[string]any{"model_id": "by-hand", "display_name": "By hand", "provider_type": "openai",
		"endpoint": gatewayV1(), "model_name": "gpt-5"})
	f.keys["by-hand"] = "magpie"
	a := shelley(home)
	fl := a.Field("model")
	ref := ""
	for _, o := range fl.Options(a.Values()) {
		if o.Ref != "" {
			ref = o.Ref
			break
		}
	}
	if err := a.Apply("model", "magpie/"+ref); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if m := f.custom1("by-hand"); m == nil || m["endpoint"] != gatewayV1() {
		t.Fatalf("hand-wired model: %v", f.custom)
	}
	if len(f.custom) != 2 {
		t.Fatalf("custom models: %v", f.custom)
	}
}

// A model that doesn't reason is no place for a thinking level Shelley
// would refuse: it is cleared while magpie's model is picked, and the
// user's comes back after.
func TestShelleyThinkingLevel(t *testing.T) {
	home := syncHome(t)
	f := startShelley(t)
	a := shelley(home)
	var ref string
	for _, m := range sharedModels("shelley") {
		if !m.Reasoning && len(m.Efforts) == 0 {
			ref = m.ID
			break
		}
	}
	if ref == "" {
		t.Skip("every model magpie offers reasons")
	}
	if err := a.Apply("model", "magpie/"+ref); err != nil {
		t.Fatal(err)
	}
	if d := f.def(); d["thinking_level"] != "" {
		t.Fatalf("default profile: %v", d)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if d := f.def(); d["thinking_level"] != "high" || d["model"] != "claude-opus-4.5" {
		t.Fatalf("default profile: %v", d)
	}
}

// The model the user had is gone by the time magpie is turned off: the
// profile goes to Shelley's default, not to a model Shelley would refuse.
func TestShelleyOwnModelGone(t *testing.T) {
	home := syncHome(t)
	f := startShelley(t)
	a := shelley(home)
	ref := ""
	for _, o := range a.Field("model").Options(a.Values()) {
		if o.Ref != "" {
			ref = o.Ref
			break
		}
	}
	if err := a.Apply("model", "magpie/"+ref); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.builtin[0].Ready = false
	f.mu.Unlock()
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if d := f.def(); d["model"] != "" {
		t.Fatalf("default profile: %v", d)
	}
}

func TestShelleyDown(t *testing.T) {
	home := syncHome(t)
	dir, err := os.MkdirTemp("", "sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	old := shelleySockets
	shelleySockets = func() []string { return []string{filepath.Join(dir, "shelley.sock")} }
	t.Cleanup(func() { shelleySockets = old })
	a := shelley(home)
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/x"); err == nil || !strings.Contains(err.Error(), "shelley serve") {
		t.Fatalf("set with Shelley down: %v", err)
	}
	if a.Check() != "" {
		t.Fatal("check with Shelley down")
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	// a socket a stopped Shelley left behind is no Shelley
	ln, err := net.Listen("unix", filepath.Join(dir, "shelley.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	ln.Close()
	if err := a.Field("model").Set("magpie/x"); err == nil || !strings.Contains(err.Error(), "shelley serve") {
		t.Fatalf("set with a stale socket: %v", err)
	}
}

func TestShelleyNoneUnderTest(t *testing.T) {
	if s := shelleySockets(); len(s) != 0 {
		t.Fatalf("sockets under test: %v", s)
	}
}
