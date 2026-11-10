package gui

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/provider"
)

// #1545 (thedavidweng): Detect APIs and Each picked model ask with the
// form as it stands, not saved: a new provider's typed key and the models
// typed in its Models field, a saved one's picks not saved yet with its
// saved key when none is typed, and a copy not saved yet with the key of
// the one it copies. None of it writes anything: providers.json and the
// rest of the config folder are as they were, and no provider is added.
func TestProviderDetectDraft(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var mu sync.Mutex
	var asked []string
	// a relay with no model list (its /v1/models is a 404) that chats only
	// for the model typed in the form, "draft-m", and the keys it knows
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body struct{ Model string }
		json.Unmarshal(b, &body)
		auth := r.Header.Get("Authorization")
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.Path+" "+body.Model+" "+auth)
		mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if auth != "Bearer sk-typed" && auth != "Bearer sk-saved" {
			http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
			return
		}
		if body.Model != "draft-m" {
			http.Error(w, `{"error":{"message":"model not found"}}`, http.StatusNotFound)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"hi"}}]}`)
	}))
	defer up.Close()
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(h http.Handler, action, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/"+action, strings.NewReader(body)))
		return w
	}
	type answer struct {
		Results []provider.Detection
		Models  []provider.ModelDetection
	}
	detect := func(body string) answer {
		t.Helper()
		w := post(mux, "detect", body)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
		var got answer
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	chat := func(rs []provider.Detection) provider.Detection {
		for _, r := range rs {
			if r.Protocol == provider.Chat {
				return r
			}
		}
		t.Fatalf("no chat in %+v", rs)
		return provider.Detection{}
	}
	keys := func() []string {
		mu.Lock()
		defer mu.Unlock()
		var out []string
		for _, a := range asked {
			if strings.Contains(a, "/chat/completions") {
				out = append(out, a[strings.LastIndex(a, " ")+1:])
			}
		}
		asked = nil
		return out
	}
	base := up.URL + "/v1"

	// a new provider: the key just typed and the models typed in its form
	if r := chat(detect(`{"typed":true,"key":"sk-typed","base":"` + base + `","model":"","picked":["draft-m","other"]}`).Results); !r.OK || r.Model != "draft-m" {
		t.Fatalf("new, models typed: %+v", r)
	}
	if got := keys(); len(got) != 1 || got[0] != "sk-typed" {
		t.Fatalf("new asked with %v", got)
	}
	got := detect(`{"typed":true,"key":"sk-typed","base":"` + base + `","detectModels":["draft-m","gone"]}`)
	if len(got.Models) != 2 || !chat(got.Models[0].Results).OK || chat(got.Models[1].Results).OK {
		t.Fatalf("new, each model: %+v", got.Models)
	}
	keys()
	if ps := provider.All(); len(ps) != 0 {
		t.Fatalf("a detection added %v", ps)
	}

	// a saved provider, its key shown masked and not typed again, with a
	// model picked in the form and not saved
	if w := post(mux, "save", `{"id":"relay","name":"Relay","key":"sk-saved","chat":"`+base+`","models":["saved-m"],"new":true}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	keys()
	before := draftFiles(t, appdir.Config())
	if _, ok := before["providers.json"]; !ok {
		t.Fatalf("no providers.json in %v", before)
	}
	if r := chat(detect(`{"id":"relay","typed":true,"key":"","base":"` + base + `","model":"","picked":["draft-m"]}`).Results); !r.OK || r.Model != "draft-m" {
		t.Fatalf("saved, a pick not saved: %+v", r)
	}
	if got := keys(); len(got) != 1 || got[0] != "sk-saved" {
		t.Fatalf("saved asked with %v", got)
	}
	// a copy of it, not saved yet, with no key typed: the copied one's key
	if r := chat(detect(`{"new":true,"copyOf":"relay","typed":true,"key":"","base":"` + base + `","model":"","picked":["draft-m"]}`).Results); !r.OK {
		t.Fatalf("copy: %+v", r)
	}
	if got := keys(); len(got) != 1 || got[0] != "sk-saved" {
		t.Fatalf("copy asked with %v", got)
	}
	// a key typed over the saved one is the one asked with
	if r := chat(detect(`{"new":true,"copyOf":"relay","typed":true,"key":"sk-typed","base":"` + base + `","model":"draft-m"}`).Results); !r.OK {
		t.Fatalf("copy, key typed: %+v", r)
	}
	if got := keys(); len(got) != 1 || got[0] != "sk-typed" {
		t.Fatalf("copy with a key typed asked with %v", got)
	}
	// the request log is the one file a detection writes: each request it
	// sent, as magpie's own (usage.SourceProbe), and nothing else changes
	after := draftFiles(t, appdir.Config())
	logBefore, logAfter := before["usage.jsonl"], after["usage.jsonl"]
	delete(before, "usage.jsonl")
	delete(after, "usage.jsonl")
	if !draftSame(before, after) {
		t.Fatalf("a detection wrote to the config folder:\nbefore %v\nafter  %v", draftNames(before), draftNames(after))
	}
	if added := strings.TrimPrefix(logAfter, logBefore); added == logAfter || !strings.Contains(added, `"source":"probe"`) ||
		!strings.Contains(added, `"kind":"api_detect"`) || strings.Contains(added, "sk-") {
		t.Fatalf("the request log has no detection of its own:\n%s", added)
	}
	if ps := provider.All(); len(ps) != 1 || ps[0].ID != "relay" || len(ps[0].Models) != 1 {
		t.Fatalf("providers now %+v", ps)
	}

	// magpie web: without its key a draft probe is turned away, and the
	// vendor is never asked
	guarded := webHandler("3999", "0123456789abcdef-1545", time.Hour, nil, mux)
	if w := post(guarded, "detect", `{"typed":true,"key":"sk-typed","base":"`+base+`","picked":["draft-m"]}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("no web key: %d %s", w.Code, w.Body)
	}
	if got := keys(); len(got) != 0 {
		t.Fatalf("asked without the web key: %v", got)
	}
}

// draftFiles is every file under dir, by its path, with its bytes.
func draftFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	return out
}

func draftSame(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func draftNames(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
