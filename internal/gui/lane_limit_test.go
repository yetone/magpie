package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A key's own limit on requests at once is set, lifted and given back to
// the provider's with provider/accountconcurrency (#892); the editor's save
// sets the queue's size and wait, keeps them when it leaves them out, keeps
// each key's own limit, and refuses a queue out of range.
func TestLaneLimitRoutes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "lane-test", Name: "Lane", Chat: "https://example.invalid/v1", Key: "primary", Keys: []provider.KeyAccount{{Key: "second"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(action, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/"+action, strings.NewReader(body)))
		return w
	}
	find := func() provider.Provider {
		got, err := provider.Find(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return *got
	}
	id := provider.KeyID("second")
	if w := post("accountconcurrency", `{"id":"lane-test","account":"`+id+`","limit":2}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if n, ok := find().AccountConcurrencyOf(id); !ok || n != 2 {
		t.Fatalf("own limit %d %v", n, ok)
	}
	// the providers' state tells it, for the row's pill
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
	var state providersJSON
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	var shown map[string]int
	for _, q := range state.Providers {
		if q.ID == p.ID {
			shown = q.AccountConcurrency
		}
	}
	if shown[strings.ToLower(id)] != 2 {
		t.Fatalf("shown %v", shown)
	}
	if w := post("accountconcurrency", `{"id":"lane-test","account":"`+id+`","limit":1001}`); w.Code == 200 {
		t.Fatal("a limit past 1000 was taken")
	}
	if w := post("accountconcurrency", `{"id":"lane-test","account":"nokey","limit":1}`); w.Code == 200 {
		t.Fatal("a key it hasn't was limited")
	}

	// the editor's save: the queue set, the key's own limit kept
	if w := post("save", `{"id":"lane-test","name":"Lane","chat":"https://example.invalid/v1","maxConcurrency":4,"queueLimit":5,"queueWait":30}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	got := find()
	if got.QueueLimit != 5 || got.QueueWait != 30 || got.Concurrency() != 4 {
		t.Fatalf("queue %d wait %d concurrency %d", got.QueueLimit, got.QueueWait, got.Concurrency())
	}
	if n, ok := got.AccountConcurrencyOf(id); !ok || n != 2 {
		t.Fatalf("the save dropped the key's own limit: %d %v", n, ok)
	}
	if got.LaneLimit() != 4 {
		t.Fatalf("the first key takes the provider's 4, not %d", got.LaneLimit())
	}
	// an older form, without the queue: kept
	if w := post("save", `{"id":"lane-test","name":"Lane","chat":"https://example.invalid/v1"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got = find(); got.QueueLimit != 5 || got.QueueWait != 30 {
		t.Fatalf("an older form lost the queue: %d %d", got.QueueLimit, got.QueueWait)
	}
	for _, bad := range []string{`"queueLimit":-1`, `"queueLimit":10001`, `"queueWait":3601`, `"queueWait":"x"`} {
		if w := post("save", `{"id":"lane-test","name":"Lane","chat":"https://example.invalid/v1",`+bad+`}`); w.Code == 200 {
			t.Fatalf("%s taken", bad)
		}
	}
	// null: no bound
	if w := post("save", `{"id":"lane-test","name":"Lane","chat":"https://example.invalid/v1","queueLimit":null,"queueWait":0}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got = find(); got.QueueLimit != 0 || got.QueueWait != 0 {
		t.Fatalf("null left %d %d", got.QueueLimit, got.QueueWait)
	}
	// back to the provider's
	if w := post("accountconcurrency", `{"id":"lane-test","account":"`+id+`","limit":null}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if _, ok := find().AccountConcurrencyOf(id); ok {
		t.Fatal("null kept its own limit")
	}

	// the lanes, with no gateway served here: none
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/lanes", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("lanes %d %s", w.Code, w.Body)
	}
}

// The editor's save sets a provider's requests a minute (coeo91 on
// Discord), shows it in the providers' state, keeps it when a save leaves
// it out, takes 0 or null for no limit, and refuses one out of range.
func TestRPMSave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "or", Name: "OpenRouter", Chat: "https://example.invalid/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		return w
	}
	rpm := func() int {
		got, err := provider.Find("or")
		if err != nil {
			t.Fatal(err)
		}
		return got.MaxRPM
	}
	const base = `{"id":"or","name":"OpenRouter","chat":"https://example.invalid/v1"`
	if w := post(base + `,"maxRPM":20}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if n := rpm(); n != 20 {
		t.Fatalf("saved %d", n)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
	var state providersJSON
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	shown := -1
	for _, q := range state.Providers {
		if q.ID == "or" {
			shown = q.MaxRPM
		}
	}
	if shown != 20 {
		t.Fatalf("shown %d", shown)
	}
	if w := post(base + `}`); w.Code != 200 || rpm() != 20 {
		t.Fatalf("an older form: %d %s, limit %d", w.Code, w.Body, rpm())
	}
	for _, bad := range []string{`"maxRPM":-1`, `"maxRPM":10001`, `"maxRPM":"x"`, `"maxRPM":2.5`} {
		if w := post(base + `,` + bad + `}`); w.Code == 200 {
			t.Fatalf("%s taken", bad)
		}
	}
	if rpm() != 20 {
		t.Fatalf("a refused save changed it: %d", rpm())
	}
	if w := post(base + `,"maxRPM":null}`); w.Code != 200 || rpm() != 0 {
		t.Fatalf("null: %d %s, limit %d", w.Code, w.Body, rpm())
	}
}
