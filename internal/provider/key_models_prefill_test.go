package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// keyRelay is a relay that hands out a key per group: each key lists its
// group's models only. A key not in lists is refused; one whose list is
// "slow" answers only after the test is done with it.
func keyRelay(t *testing.T, lists map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key == "" {
			key = r.Header.Get("x-api-key")
		}
		body, ok := lists[key]
		switch {
		case !ok:
			http.Error(rw, `{"error":{"message":"invalid key"}}`, http.StatusUnauthorized)
		case body == "slow":
			<-r.Context().Done()
		default:
			_, _ = rw.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func prefillHome(t *testing.T) {
	t.Helper()
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local", "share"))
}

const groupAll = `{"data":[{"id":"gpt-5.5"},{"id":"claude-opus-4-7"},{"id":"deepseek-v4"}]}`

// Young on Discord: a key added to a provider of several takes the models
// its own list from the vendor has, asked with that key, not every model;
// the keys it had keep their lists (none, or the user's) as they were.
func TestNewKeyTakesItsOwnModels(t *testing.T) {
	prefillHome(t)
	srv := keyRelay(t, map[string]string{
		"sk-all":    groupAll,
		"sk-mine":   groupAll,
		"sk-claude": `{"data":[{"id":"claude-opus-4-7"}]}`,
		"sk-gpt":    `{"data":[{"id":"gpt-5.5"},{"id":"deepseek-v4"}]}`,
		"sk-old":    `{"data":[{"id":"deepseek-v4"}]}`,
	})
	// sk-old was there before, with no list of its own though its group
	// lists one model: the Fetch below gives it its list (young02036 on
	// Discord, v0.1.2965: Fetch models ticks every key's own models)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-all",
		Keys:          []KeyAccount{{Key: "sk-mine"}, {Key: "sk-old"}},
		AccountModels: map[string][]string{keyID("sk-mine"): {"gpt-5.5"}}}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	had := KeysHeld("relay")
	if err := AddKey("relay", "Claude group", "sk-claude", ""); err != nil {
		t.Fatal(err)
	}
	if n := PrefillNewKeys(context.Background(), had, "relay"); n != 1 {
		t.Fatalf("%d keys given a list, want 1", n)
	}
	p, _ = Find("relay")
	if got := p.AccountModels[keyID("sk-claude")]; !slices.Equal(got, []string{"claude-opus-4-7"}) {
		t.Fatalf("the new key's models %v, want its own list", got)
	}
	if p.AccountModels[keyID("sk-all")] != nil || !slices.Equal(p.AccountModels[keyID("sk-old")], []string{"deepseek-v4"}) || !slices.Equal(p.AccountModels[keyID("sk-mine")], []string{"gpt-5.5"}) {
		t.Fatalf("the keys it had changed: %v", p.AccountModels)
	}
	// the gateway's rule: the new key is never sent a model its list lacks
	if p.AccountServes(keyID("sk-claude"), "gpt-5.5") || !p.AccountServes(keyID("sk-claude"), "claude-opus-4-7") {
		t.Fatal("the new key's list doesn't hold its requests")
	}

	// pasted several at once (keys/import): each its own list
	had = KeysHeld("relay")
	if _, _, err := AddKeys("relay", []string{"sk-gpt"}, ""); err != nil {
		t.Fatal(err)
	}
	PrefillNewKeys(context.Background(), had, "relay")
	p, _ = Find("relay")
	if got := p.AccountModels[keyID("sk-gpt")]; !slices.Equal(got, []string{"gpt-5.5", "deepseek-v4"}) {
		t.Fatalf("the pasted key's models %v", got)
	}
	// asked again, nothing is new: the lists given stay, the user's too
	if n := PrefillNewKeys(context.Background(), KeysHeld("relay"), "relay"); n != 0 {
		t.Fatalf("asked again, %d keys changed", n)
	}
}

// A key whose list can't be told keeps every model, as before, and no
// empty list is written: a list that fails, that answers no model, that
// answers past the wait, or that has every model the others have. A key
// the user gave models while its list was asked keeps theirs, and a
// provider of one key is its list.
func TestNewKeyListUnknownKeepsAll(t *testing.T) {
	prefillHome(t)
	srv := keyRelay(t, map[string]string{
		"sk-all":   groupAll,
		"sk-same":  groupAll,
		"sk-empty": `{"data":[]}`,
		"sk-slow":  "slow",
		"sk-set":   `{"data":[{"id":"deepseek-v4"}]}`,
	})
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-all"}); err != nil {
		t.Fatal(err)
	}
	// one key: nothing to tell it from
	if n := PrefillNewKeys(context.Background(), map[string]map[string]bool{}, "relay"); n != 0 {
		t.Fatalf("a provider of one key: %d", n)
	}
	p, _ := Find("relay")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	had := KeysHeld("relay")
	if _, _, err := AddKeys("relay", []string{"sk-refused", "sk-same", "sk-empty", "sk-slow", "sk-set"}, ""); err != nil {
		t.Fatal(err)
	}
	// the user's own pick for a new key, set before its list came back
	if err := SetAccountModels("relay", keyID("sk-set"), []string{"gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if n := PrefillNewKeys(ctx, had, "relay"); n != 0 {
		p, _ := Find("relay")
		t.Fatalf("%d keys given a list: %v", n, p.AccountModels)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("waited %s on a slow list past the add's wait", d)
	}
	p, _ = Find("relay")
	for _, k := range []string{"sk-all", "sk-refused", "sk-same", "sk-empty", "sk-slow"} {
		if ms, ok := p.AccountModels[keyID(k)]; ok {
			t.Errorf("%s given %v, want every model", k, ms)
		}
	}
	if got := p.AccountModels[keyID("sk-set")]; !slices.Equal(got, []string{"gpt-5.5"}) {
		t.Errorf("the user's pick became %v", got)
	}
}

// A key imported from another app (CC Switch keeps an entry a key, folded
// into one provider) is asked for its own list as one added by hand is.
func TestImportedKeysTakeTheirOwnModels(t *testing.T) {
	home := foldHome(t)
	srv := keyRelay(t, map[string]string{
		"sk-a": groupAll,
		"sk-b": `{"data":[{"id":"claude-opus-4-7"}]}`,
	})
	sqliteFixture(t, filepath.Join(home, ".cc-switch", "cc-switch.db"),
		`CREATE TABLE providers (id TEXT, app_type TEXT, name TEXT, settings_config TEXT, website_url TEXT, category TEXT, created_at INTEGER, sort_index INTEGER, PRIMARY KEY (id, app_type))`,
		`INSERT INTO providers VALUES ('a','claude','Relay #1','{"env":{"ANTHROPIC_BASE_URL":"`+srv.URL+`","ANTHROPIC_AUTH_TOKEN":"sk-a"}}','','custom',1,0)`,
		`INSERT INTO providers VALUES ('b','claude','Relay #2','{"env":{"ANTHROPIC_BASE_URL":"`+srv.URL+`","ANTHROPIC_AUTH_TOKEN":"sk-b"}}','','custom',2,1)`,
	)
	had := KeysHeld()
	if added, err := ImportFromApps([]AppPick{{Source: "cc-switch", Ref: "claude/a"}, {Source: "cc-switch", Ref: "claude/b"}}); err != nil || len(added) != 2 {
		t.Fatalf("%v %v", added, err)
	}
	if n := PrefillNewKeys(context.Background(), had); n != 1 {
		t.Fatalf("%d keys given a list, want 1", n)
	}
	ps := load().Providers
	if len(ps) != 1 {
		t.Fatalf("%d providers", len(ps))
	}
	p := ps[0]
	if got := p.AccountModels[keyID("sk-b")]; !slices.Equal(got, []string{"claude-opus-4-7"}) {
		t.Fatalf("the imported key's models %v (%+v)", got, p.AccountModels)
	}
	if _, ok := p.AccountModels[keyID("sk-a")]; ok {
		t.Fatalf("the key listing every model was given a list: %v", p.AccountModels)
	}
}

// A key whose list one of the provider's bases didn't answer is unknown:
// that base's part would be the provider's list saved before, not the
// key's own answer.
func TestNewKeyHalfAnsweredKeepsAll(t *testing.T) {
	prefillHome(t)
	chat := keyRelay(t, map[string]string{
		"sk-all":  `{"data":[{"id":"gpt-5.5"},{"id":"deepseek-v4"}]}`,
		"sk-half": `{"data":[{"id":"gpt-5.5"}]}`,
	})
	anthropic := keyRelay(t, map[string]string{
		"sk-all": `{"data":[{"id":"claude-opus-4-7"}]}`,
	})
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: chat.URL + "/v1", Anthropic: anthropic.URL, Key: "sk-all"}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	had := KeysHeld("relay")
	if err := AddKey("relay", "", "sk-half", ""); err != nil {
		t.Fatal(err)
	}
	if n := PrefillNewKeys(context.Background(), had, "relay"); n != 0 {
		p, _ := Find("relay")
		t.Fatalf("a half-answered key given a list: %v", p.AccountModels)
	}
}
