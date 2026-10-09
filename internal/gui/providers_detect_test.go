package gui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The editor's Detect asks which APIs answer at the URL typed, with the
// form as it stands: a new provider's key, or a saved one's when none is
// typed. Nothing is saved by it. A Save's modelPrefs carry the API picked
// for a model, and the editor is told it back.
func TestProviderDetect(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var mu sync.Mutex
	var auths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		// Gemini's API is asked with the key in x-goog-api-key
		auth := r.Header.Get("Authorization")
		if g := r.Header.Get("x-goog-api-key"); g != "" {
			auth = "goog " + g
		}
		auths = append(auths, r.Method+" "+r.URL.Path+" "+auth)
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/chat/completions":
			io.WriteString(w, `{"choices":[{"message":{"content":"hi"}}]}`)
		case "/v1/messages":
			io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"hi"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(action, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/"+action, strings.NewReader(body)))
		return w
	}
	detect := func(body string) []provider.Detection {
		t.Helper()
		w := post("detect", body)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		var got struct{ Results []provider.Detection }
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.Results
	}
	oks := func(rs []provider.Detection) string {
		var s []string
		for _, r := range rs {
			if r.OK {
				s = append(s, string(r.Protocol)+"="+r.Base)
			}
		}
		return strings.Join(s, " ")
	}
	want := "chat=" + up.URL + "/v1 anthropic=" + up.URL
	if got := oks(detect(`{"typed":true,"key":"sk-new","base":"` + up.URL + `","model":"m"}`)); got != want {
		t.Fatalf("new: %s", got)
	}
	if ps := provider.All(); len(ps) != 0 {
		t.Fatalf("detect saved %v", ps)
	}
	if w := post("save", `{"id":"relay","name":"Relay","key":"sk-saved","chat":"`+up.URL+`/v1","anthropic":"`+up.URL+`","models":["m","n"],"new":true}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	mu.Lock()
	auths = nil
	mu.Unlock()
	if got := oks(detect(`{"id":"relay","typed":true,"key":"","base":"` + up.URL + `/v1","model":"m"}`)); got != want {
		t.Fatalf("saved: %s", got)
	}
	mu.Lock()
	for _, a := range auths {
		if !strings.HasSuffix(a, "Bearer sk-saved") && !strings.HasSuffix(a, "goog sk-saved") {
			t.Fatalf("asked %s, not with the saved key", a)
		}
	}
	mu.Unlock()
	if w := post("detect", `{"typed":true,"key":"k","base":""}`); w.Code == 200 || !strings.Contains(w.Body.String(), "base URL") {
		t.Fatalf("no URL: %d %s", w.Code, w.Body)
	}

	// the API picked for a model, with the Save
	if w := post("save", `{"id":"relay","from":"relay","name":"Relay","chat":"`+up.URL+`/v1","anthropic":"`+up.URL+`","models":["m","n"],"modelPrefs":{"m":{"api":"anthropic"}}}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := settings.Load().ModelAPIs["relay/m"]; got != "anthropic" {
		t.Fatalf("kept %q", got)
	}
	p, _ := provider.Find("relay")
	info := providerInfo(*p, nil)
	for _, m := range info.Models {
		if want := map[string]string{"m": "anthropic"}[m.ID]; m.API != want {
			t.Fatalf("%s told api %q", m.ID, m.API)
		}
	}
	if w := post("save", `{"id":"relay","from":"relay","name":"Relay","chat":"`+up.URL+`/v1","anthropic":"`+up.URL+`","models":["m","n"],"modelPrefs":{"n":{"api":"responses"}}}`); w.Code == 200 || !strings.Contains(w.Body.String(), "responses") {
		t.Fatalf("an API with no URL: %d %s", w.Code, w.Body)
	}
}
