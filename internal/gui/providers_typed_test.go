package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Refresh and Test models ask with the key the editor has, before a Save
// (the user: a key pasted over the saved one only errored, and Save closes
// the editor); the saved key stays as it was.
func TestProviderTypedKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-new" || r.Header.Get("X-Team") != "b" {
			http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"data":[{"id":"m1"},{"id":"m2"}]}`))
			return
		}
		w.Write([]byte(`{"id":"x","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer vendor.Close()
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(action, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/"+action, strings.NewReader(body)))
		return w
	}
	if w := post("save", `{"id":"relay","name":"Relay","chat":"`+vendor.URL+`/v1","key":"sk-old","headers":{"X-Team":"a"},"proxy":"direct","new":true}`); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	form := `"typed":true,"key":"sk-new","chat":"` + vendor.URL + `/v1","headers":{"X-Team":"b"}`

	// the saved key is turned away, as before
	if w := post("models", `{"id":"relay"}`); w.Code == 200 {
		t.Fatalf("the saved key fetched: %s", w.Body)
	}
	w := post("models", `{"id":"relay",`+form+`}`)
	if w.Code != 200 {
		t.Fatalf("models with the typed key: %d %s", w.Code, w.Body)
	}
	var got struct{ Count int }
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Count != 2 {
		t.Fatalf("count %d: %s", got.Count, w.Body)
	}

	var res struct {
		Results []provider.Result `json:"results"`
	}
	json.Unmarshal(post("test", `{"id":"relay","test":["m1"]}`).Body.Bytes(), &res)
	if len(res.Results) != 1 || res.Results[0].OK {
		t.Fatalf("the saved key answered: %+v", res.Results)
	}
	w = post("test", `{"id":"relay","test":["m1"],`+form+`}`)
	res.Results = nil
	json.Unmarshal(w.Body.Bytes(), &res)
	if len(res.Results) != 1 || !res.Results[0].OK {
		t.Fatalf("the typed key didn't answer: %s", w.Body)
	}

	// nothing saved
	p, err := provider.Find("relay")
	if err != nil || p.Key != "sk-old" || p.Headers["X-Team"] != "a" {
		t.Fatalf("%v key %q headers %v", err, p.Key, p.Headers)
	}
}
