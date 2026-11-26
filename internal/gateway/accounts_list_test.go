package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// keyVendor answers a chat request by its key: 401 with the vendor's own
// words for a key in bad, else a reply naming the key. It notes each key
// asked.
type keyVendor struct {
	mu  sync.Mutex
	bad map[string]bool
	// limited answers 429 with a Retry-After, which rests a key on
	limited map[string]bool
	asked   []string
}

func (v *keyVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	v.mu.Lock()
	v.asked = append(v.asked, key)
	v.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if v.limited[key] {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"Rate limit reached for requests","type":"requests","code":"rate_limit_exceeded"}}`)
		return
	}
	if v.bad[key] {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Incorrect API key provided: `+provider.Mask(key)+`","type":"invalid_request_error","code":"invalid_api_key"}}`)
		return
	}
	io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"from `+key+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
}

func (v *keyVendor) seen() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.asked)
}

// keyedPost asks model on /v1/chat/completions from this computer, pinned
// to account when it isn't "".
func keyedPost(t *testing.T, s *Server, model, account string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"ping"}]}`))
	req.Header.Set("Authorization", "Bearer magpie")
	if account != "" {
		req.Header.Set(AccountHeader, account)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

const (
	firstKey  = "sk-first-0123456789abcdef"
	secondKey = "sk-second-0123456789abcdef"
	offKey    = "sk-off-0123456789abcdef"
)

// @tmr11235 on X: a plugin testing whether an account works could find
// only the accounts /v1/magpie/quotas has a card for. GET
// /v1/magpie/accounts lists every one, a signed-in account and an API
// key alike, with no quota read, by identifiers alone: no key, token or
// refresh token is in it.
func TestAccountsListsEveryAccountAndKey(t *testing.T) {
	twoAccounts(t)
	if err := provider.Save(provider.Provider{ID: "keyed", Name: "Keyed", Chat: "https://example.invalid/v1", Models: []string{"m"},
		Key: firstKey, Keys: []provider.KeyAccount{{Key: secondKey, Name: "work"}, {Key: offKey, Off: true}}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	get := func(path, from string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = from
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body := get(AccountsPath, "127.0.0.1:5000")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	for _, secret := range []string{firstKey, secondKey, offKey, "r-acct-1", "r-acct-2"} {
		if strings.Contains(body, secret) {
			t.Fatalf("a secret (%s) is told: %s", secret, body)
		}
	}
	var out struct {
		Data []listedAccount `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	find := func(pid, id string) *listedAccount {
		for i, a := range out.Data {
			if a.Provider == pid && a.ID == id {
				return &out.Data[i]
			}
		}
		t.Fatalf("%s/%s not listed: %s", pid, id, body)
		return nil
	}
	for _, k := range []struct {
		key, label string
		on         bool
	}{{firstKey, provider.Mask(firstKey), true}, {secondKey, "work", true}, {offKey, provider.Mask(offKey), false}} {
		a := find("keyed", provider.KeyID(k.key))
		if a.Kind != "key" || a.Label != k.label || a.On != k.on || a.Pin != "keyed/"+provider.KeyID(k.key) || a.User != "" {
			t.Errorf("key %s listed %+v", k.label, a)
		}
	}
	for _, user := range []string{"me@example.com", "spare@example.com"} {
		a := find("codex", provider.LoginID("codex", user))
		if a.Kind != "account" || a.User != user || a.Label != user || !a.On || a.Pin != "codex/"+a.ID {
			t.Errorf("account %s listed %+v", user, a)
		}
	}
	// ?provider= keeps one provider's
	if _, b := get(AccountsPath+"?provider=keyed", "127.0.0.1:5000"); strings.Contains(b, "codex") || !strings.Contains(b, "keyed") {
		t.Errorf("?provider=keyed: %s", b)
	}
	// another computer is told none of it without the shared gateway's key
	if code, b := get(AccountsPath, "192.168.1.9:5000"); code != http.StatusForbidden || strings.Contains(b, "keyed") {
		t.Errorf("from the network: %d %s", code, b)
	}
}

// A request pinned by a listed account's pin, or its id alone, goes to
// that key and no other: a provider's only key on is pinned by its own
// fingerprint too, and a key the vendor refuses is answered with the
// vendor's own refusal, the next key not asked in its place.
func TestAccountPinnedByListedID(t *testing.T) {
	fresh(t)
	v := &keyVendor{bad: map[string]bool{secondKey: true}}
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "keyed", Name: "Keyed", Chat: up.URL + "/v1", Models: []string{"m"}, Routing: provider.Ordered,
		Key: firstKey, Keys: []provider.KeyAccount{{Key: secondKey, Name: "work"}}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	// unpinned, the first answers
	if code, body := keyedPost(t, s, "keyed/m", ""); code != 200 || !strings.Contains(body, "from "+firstKey) {
		t.Fatalf("unpinned: %d %s", code, body)
	}
	v.asked = nil
	// pinned to the refused key by its pin: its 401 is the answer, and the
	// first key, which would answer, isn't asked
	code, body := keyedPost(t, s, "keyed/m", "keyed/"+provider.KeyID(secondKey))
	if code != http.StatusUnauthorized || !strings.Contains(body, "Incorrect API key") {
		t.Fatalf("pinned to the refused key: %d %s", code, body)
	}
	if got := v.seen(); !slices.Equal(got, []string{secondKey}) {
		t.Fatalf("keys asked %v, want the pinned one alone", got)
	}
	// and the first by its id alone
	v.asked = nil
	if code, body := keyedPost(t, s, "keyed/m", provider.KeyID(firstKey)); code != 200 || !strings.Contains(body, "from "+firstKey) {
		t.Fatalf("pinned by id: %d %s", code, body)
	}
	if got := v.seen(); !slices.Equal(got, []string{firstKey}) {
		t.Fatalf("keys asked %v", got)
	}
	// one key on beside one off is planned as the provider: pinned by its
	// fingerprint all the same, and the one off isn't taken for it
	if err := provider.Save(provider.Provider{ID: "keyed", Name: "Keyed", Chat: up.URL + "/v1", Models: []string{"m"},
		Key: firstKey, Keys: []provider.KeyAccount{{Key: secondKey, Off: true}}}); err != nil {
		t.Fatal(err)
	}
	v.asked = nil
	if code, body := keyedPost(t, s, "keyed/m", "keyed/"+provider.KeyID(firstKey)); code != 200 || !strings.Contains(body, "from "+firstKey) {
		t.Fatalf("the only key on, pinned: %d %s", code, body)
	}
	if got := v.seen(); !slices.Equal(got, []string{firstKey}) {
		t.Fatalf("keys asked %v", got)
	}
	// the one off, pinned, is asked alone (TestAccountPinnedOffKey): its
	// 401 is the answer
	v.asked = nil
	if code, body := keyedPost(t, s, "keyed/m", "keyed/"+provider.KeyID(secondKey)); code != http.StatusUnauthorized {
		t.Fatalf("the key off, pinned: %d %s", code, body)
	}
	if got := v.seen(); !slices.Equal(got, []string{secondKey}) {
		t.Fatalf("keys asked %v", got)
	}
}

// A signed-in account is pinned by its listed pin as well, as by its user.
func TestAccountPinnedByLoginID(t *testing.T) {
	heads := twoAccounts(t)
	s := New()
	pin := "codex/" + provider.LoginID("codex", "spare@example.com")
	code, body := pinnedPost(t, s, "/v1/responses", "codex/gpt-5.5", pin)
	if code != 200 || !strings.Contains(body, "from acct-2") {
		t.Fatalf("%d %s", code, body)
	}
	if len(*heads) != 1 || (*heads)[0].Get("chatgpt-account-id") != "acct-2" {
		t.Fatalf("upstream %v", *heads)
	}
}

// @tmr11235 on X again: agents are offered the first 24 of a provider's
// list when the user picked none (manyModels), and a plugin testing each
// model wants them all. ?models=1 gives each account and key every model
// it can be asked for, less what the user's list for it leaves out, and a
// model past the 24 is asked as "<provider>/<model>", pinned or not.
func TestAccountsListTellsEveryModel(t *testing.T) {
	fresh(t)
	v := &keyVendor{}
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "wide", Name: "Wide", Chat: up.URL + "/v1", Key: firstKey,
		Keys:          []provider.KeyAccount{{Key: secondKey, Name: "small"}},
		AccountModels: map[string][]string{provider.KeyID(secondKey): {"m3", "m29"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	var ms []catalog.Model
	var all []string
	for i := range 30 {
		ms = append(ms, catalog.Model{ID: fmt.Sprintf("m%d", i)})
		all = append(all, fmt.Sprintf("m%d", i))
	}
	if err := catalog.SaveLive("wide", p.Chat, ms); err != nil {
		t.Fatal(err)
	}
	if got, err := provider.Find("wide"); err != nil || len(got.Exposed()) != 24 {
		t.Fatalf("the list agents are offered is not the first 24: %v", err)
	}
	s := New()
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	// not asked for, none: the list stays as it was
	if code, body := get(AccountsPath + "?provider=wide"); code != 200 || strings.Contains(body, `"models"`) {
		t.Fatalf("without ?models: %d %s", code, body)
	}
	code, body := get(AccountsPath + "?provider=wide&models=1")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var out struct {
		Data []idsListed `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 2 {
		t.Fatalf("listed %s", body)
	}
	for _, a := range out.Data {
		want := all
		if a.ID == provider.KeyID(secondKey) {
			want = []string{"m3", "m29"}
		}
		if !slices.Equal(a.Models, want) {
			t.Errorf("%s's models %v, want %v", a.Label, a.Models, want)
		}
	}
	// a model past the 24 is served, unpinned and pinned
	if code, body := keyedPost(t, s, "wide/m29", ""); code != 200 {
		t.Fatalf("wide/m29: %d %s", code, body)
	}
	if code, body := keyedPost(t, s, "wide/m28", out.Data[0].Pin); code != 200 || !strings.Contains(body, "from "+firstKey) {
		t.Fatalf("wide/m28 pinned to %s: %d %s", out.Data[0].Pin, code, body)
	}
}

// A held key is told a provider only when it may use every model agents
// are offered of it (quotaScope), and then only the models it may use:
// past the 24, those its list names.
func TestAccountsListModelsOfAHeldKey(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	p := provider.Provider{ID: "wide", Name: "Wide", Chat: "https://wide.example.invalid/v1", Key: firstKey}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	var ms []catalog.Model
	for i := range 30 {
		ms = append(ms, catalog.Model{ID: fmt.Sprintf("m%d", i)})
	}
	if err := catalog.SaveLive("wide", p.Chat, ms); err != nil {
		t.Fatal(err)
	}
	var may, want []string
	for i := range 24 {
		may, want = append(may, fmt.Sprintf("wide/m%d", i)), append(want, fmt.Sprintf("m%d", i))
	}
	may, want = append(may, "wide/m28"), append(want, "m28")
	keys, secrets := newCaller(t, "Owner", "Held")
	for action, c := range map[string]access.Change{
		"models-key": {Key: keys[1].ID, Models: may},
		"quotas-key": {Key: keys[1].ID, Quotas: true},
	} {
		if _, err := access.Update(action, c); err != nil {
			t.Fatal(action, err)
		}
	}
	r := httptest.NewRequest("GET", AccountsPath+"?provider=wide&models=1", nil)
	r.RemoteAddr = "192.168.1.9:5000"
	r.Header.Set("Authorization", "Bearer "+secrets[1])
	w := httptest.NewRecorder()
	lanGuard(New().Handler()).ServeHTTP(w, r)
	var out struct {
		Data []idsListed `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(out.Data) != 1 || !slices.Equal(out.Data[0].Models, want) {
		t.Fatalf("the held key is told %s", w.Body.String())
	}
}

// idsListed is a listed account as ?models=1 tells it, its models by id.
type idsListed struct {
	listedAccount
	Models []string `json:"models"`
}

// @tmr11235 on X, a third time: ?models=1's ids gave a plugin none of what
// /v1/models tells of a model, its name, levels, window or price, and a
// model past the 24 is in no other list. ?models=full gives each account
// its models as /v1/models' rows, built by the same code: a model both
// list is told the same in both, one past the 24 is told as fully, and
// ?models=1 still gives ids.
func TestAccountsListTellsEveryModelInFull(t *testing.T) {
	fresh(t)
	p := provider.Provider{ID: "wide", Name: "Wide", Chat: "https://wide.example.invalid/v1", Key: firstKey,
		Keys:          []provider.KeyAccount{{Key: secondKey, Name: "small"}},
		AccountModels: map[string][]string{provider.KeyID(secondKey): {"m3", "m29"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	var ms []catalog.Model
	for i := range 30 {
		ms = append(ms, catalog.Model{ID: fmt.Sprintf("m%d", i), Name: fmt.Sprintf("Model %d", i),
			Efforts: []string{"low", "high"}, Context: 200000 + i, Output: 32000})
	}
	if err := catalog.SaveLive("wide", p.Chat, ms); err != nil {
		t.Fatal(err)
	}
	f := func(v float64) *float64 { return &v }
	if err := settings.Save(settings.Settings{
		ModelPrices: map[string]settings.ModelPrice{"wide/*": {Input: f(1), Output: f(4), CacheRead: f(0.1), CacheWrite: f(0)}},
		ModelNames:  map[string]string{"wide/m29": "Far Model"},
	}); err != nil {
		t.Fatal(err)
	}
	s := New()
	get := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), firstKey) || strings.Contains(rec.Body.String(), secondKey) {
			t.Fatalf("%s tells a key: %s", path, rec.Body.String())
		}
		return rec.Body.String()
	}
	var listed struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(get("/v1/models")), &listed); err != nil {
		t.Fatal(err)
	}
	row := map[string]map[string]any{}
	for _, m := range listed.Data {
		row[m["id"].(string)] = m
	}
	if row["wide/m0"] == nil || row["wide/m29"] != nil {
		t.Fatalf("/v1/models lists the first 24 alone: %v", listed.Data)
	}
	var full struct {
		Data []struct {
			ID     string           `json:"id"`
			Models []map[string]any `json:"models"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(get(AccountsPath+"?provider=wide&models=full")), &full); err != nil {
		t.Fatal(err)
	}
	if len(full.Data) != 2 {
		t.Fatalf("listed %v", full.Data)
	}
	for _, a := range full.Data {
		want := 30
		if a.ID == provider.KeyID(secondKey) {
			want = 2
		}
		if len(a.Models) != want {
			t.Fatalf("%s is told %d models, want %d", a.ID, len(a.Models), want)
		}
		for _, m := range a.Models {
			id, _ := m["id"].(string)
			if !strings.HasPrefix(id, "wide/m") {
				t.Fatalf("a model not asked as <provider>/<id>: %v", m)
			}
			// a model /v1/models lists is told the same, field for field
			if want, ok := row[id]; ok && !reflect.DeepEqual(m, want) {
				t.Errorf("%s: accounts tell %v, /v1/models %v", id, m, want)
			}
		}
	}
	// one past the 24 is told as fully as one in the list
	far := full.Data[1].Models[1]
	if far["id"] != "wide/m29" || far["display_name"] != "Far Model" || far["context_window"] != float64(200029) ||
		fmt.Sprint(far["reasoning_levels"]) != "[low high]" || far["max_output_tokens"] != float64(32000) ||
		far["pricing"] == nil || far["magpie_label"] != "Far Model · Wide" || far["owned_by"] != "wide" {
		t.Fatalf("wide/m29 is told %v", far)
	}
	// ?models=1 is as it shipped: ids
	var ids struct {
		Data []idsListed `json:"data"`
	}
	if err := json.Unmarshal([]byte(get(AccountsPath+"?provider=wide&models=1")), &ids); err != nil {
		t.Fatal(err)
	}
	if len(ids.Data) != 2 || len(ids.Data[0].Models) != 30 || !slices.Equal(ids.Data[1].Models, []string{"m3", "m29"}) {
		t.Fatalf("?models=1 gives %v", ids.Data)
	}
}
