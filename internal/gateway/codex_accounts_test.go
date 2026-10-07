package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// codexSignedIn signs a test's Codex in to ChatGPT (acct-1), with the
// saved accounts spares (acct-2, …) on beside it in magpie.
func codexSignedIn(t *testing.T, spares ...string) {
	t.Helper()
	fresh(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows finds the home there
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": acct}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(auth("me@example.com", "acct-1")), 0o600)
	var saved []map[string]any
	for i, s := range spares {
		saved = append(saved, map[string]any{"agent": "codex", "user": s, "on": true, "seen": time.Now(),
			"auth": auth(s, "acct-"+string(rune('2'+i)))})
	}
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON(saved), 0o600)
	// signed in behind magpie's back: an earlier test's look at its own
	// home within 30s would otherwise keep Codex's sign-in off the list
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
}

// usedUp stands in for the ChatGPT backend with acct-1 out of its
// allowance, as it says so; it notes the accounts asked and the models.
func usedUp(t *testing.T, tried, models *[]string) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*tried = append(*tried, r.Header.Get("chatgpt-account-id"))
		*models = append(*models, modelOf(b))
		if r.Header.Get("chatgpt-account-id") == "acct-1" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":7200}}`)
			return
		}
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
}

// Codex on one of its own models, signed in to a ChatGPT account that has
// used its allowance up, with another of its accounts on in magpie: the
// turn goes on to that one, asked for the model Codex asked for, and the
// first sits out until ChatGPT says it is back — two hours, not an hour
// at a time (#147).
func TestCodexOwnModelMovesToNextAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var tried, models []string
	usedUp(t, &tried, &models)

	code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("status %d: %s", code, body)
	}
	if strings.Join(tried, ",") != "acct-1,acct-2" || strings.Join(models, ",") != "gpt-5.5,gpt-5.5" {
		t.Fatalf("tried %v models %v", tried, models)
	}
	restingUntil.Lock()
	var rest time.Duration
	for _, u := range restingUntil.m {
		rest = max(rest, time.Until(u))
	}
	restingUntil.Unlock()
	if rest < 2*time.Hour-time.Minute || rest > 2*time.Hour {
		t.Errorf("rests %v, not ChatGPT's two hours", rest)
	}
	tried, models = nil, nil
	codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if strings.Join(tried, ",") != "acct-2" {
		t.Fatalf("second turn tried %v", tried)
	}
}

// Codex signed in to the next account once the one it was on is out
// (provider.SwitchWhenSpent): the one out still rests, now beside it,
// and the one it is on now doesn't take that rest over.
func TestCodexSwitchedAccountRestsAsItself(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var tried, models []string
	usedUp(t, &tried, &models)
	codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if err := provider.SwitchLogin("codex", "spare@example.com"); err != nil {
		t.Fatal(err)
	}
	tried, models = nil, nil
	code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if code != 200 || strings.Join(tried, ",") != "acct-2" {
		t.Fatalf("%d %s tried %v", code, body, tried)
	}
}

// With no other account on, Codex's own model goes as it came: its own
// sign-in, and the refusal back to Codex as ChatGPT said it.
func TestCodexOwnModelOneAccountRelayed(t *testing.T) {
	codexSignedIn(t)
	var tried, models []string
	usedUp(t, &tried, &models)
	code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if code != 429 || !strings.Contains(body, "usage_limit_reached") || strings.Join(tried, ",") != "acct-1" {
		t.Fatalf("%d %s tried %v", code, body, tried)
	}
}

// The account Codex is signed in to, paused in magpie while another is on
// (#263: the user's own Plus kept for Codex's remote control, a shared Pro
// doing the work): no request goes to it, Codex staying signed in to it,
// until it is resumed or the other is turned off.
func TestCodexPausedOwnAccountPassedOver(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var tried, models []string
	usedUp(t, &tried, &models)
	if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	for _, l := range provider.Logins("codex") {
		if l.User == "me@example.com" && (!l.Active || !l.Paused) {
			t.Fatalf("own account %+v", l)
		}
	}
	code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if code != 200 || strings.Join(tried, ",") != "acct-2" {
		t.Fatalf("%d %s tried %v", code, body, tried)
	}
	// the other off: the paused one is all there is, and is used
	if err := provider.SetLoginOn("codex", "spare@example.com", false); err != nil {
		t.Fatal(err)
	}
	tried = nil
	codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if strings.Join(tried, ",") != "acct-1" {
		t.Fatalf("with the other off, tried %v", tried)
	}
	if err := provider.SetLoginOn("codex", "me@example.com", false); err == nil {
		t.Fatal("the only account in use paused")
	}
	// back on beside it, and resumed: it is first again
	if err := provider.SetLoginOn("codex", "spare@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetLoginOn("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	tried = nil
	codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if len(tried) == 0 || tried[0] != "acct-1" {
		t.Fatalf("resumed, tried %v", tried)
	}
}

// A gateway key held to some accounts (#905) holds Codex's own models
// too: asked for by its bare native name — the request Codex would relay
// to its own sign-in when that one account is all that is on — the turn
// goes through routing instead, which holds it to the key's accounts: a
// key held to an account that is gone (signed out, its id kept) is
// refused with nothing asked, and one held to the account in use spends
// it alone, asked for the model Codex asked for.
func TestGatewayKeyAccountsHoldCodexOwnModel(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	// spare off: an id the key keeps, matching no account (#905), with
	// me the only account on
	if err := provider.SetLoginOn("codex", "spare@example.com", false); err != nil {
		t.Fatal(err)
	}
	var heads []http.Header
	var models []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		heads = append(heads, r.Header.Clone())
		models = append(models, modelOf(b))
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"from `+r.Header.Get("chatgpt-account-id")+`"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	s := New()
	keys, secrets := newCaller(t, "Held", "Free")
	setAccounts := func(t *testing.T, id string, as ...string) {
		t.Helper()
		if _, err := access.Update("accounts-key", access.Change{Key: id, Accounts: as}); err != nil {
			t.Fatal(err)
		}
	}
	post := func(secret string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
		req.Header.Set("Authorization", "Bearer "+secret)
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	// the key's account is gone: refused, nothing asked of OpenAI
	setAccounts(t, keys[0].ID, "codex/spare@example.com")
	code, body := post(secrets[0])
	var e struct {
		Error struct{ Message, Type string }
	}
	json.Unmarshal([]byte(body), &e)
	if code != 403 || e.Error.Type != "permission_error" || !strings.Contains(e.Error.Message, `"Held"`) || !strings.Contains(e.Error.Message, "is not allowed to use the accounts behind") {
		t.Fatalf("held to a gone account: %d %s", code, body)
	}
	if len(heads) != 0 {
		t.Fatalf("a held-out native model reached OpenAI: %v", heads)
	}
	// the account in use is the key's: served, by it alone, the model
	// Codex asked for
	setAccounts(t, keys[0].ID, "codex/me@example.com")
	code, body = post(secrets[0])
	if code != 200 || !strings.Contains(body, "from acct-1") {
		t.Fatalf("held to the account in use: %d %s", code, body)
	}
	if len(heads) != 1 || heads[0].Get("chatgpt-account-id") != "acct-1" || models[0] != "gpt-5.5" {
		t.Fatalf("asked %v of %v", models, heads)
	}
	// a key that names no account relays as it always did
	code, body = post(secrets[1])
	if code != 200 || len(heads) != 2 {
		t.Fatalf("a free key: %d %s, %d upstream requests", code, body, len(heads))
	}
}

// A gateway key held to some accounts (#905) holds Codex's native
// compaction too: /responses/compact for one of Codex's own models,
// kept native (#876) for the encrypted history only the ChatGPT backend
// can read, still relays on the account Codex is signed in to, with
// nothing of the key asked — so a key held to other accounts is refused
// with nothing asked of OpenAI, as its turns are (#967), the message
// saying the two ways out; one held to the account in use, or naming no
// Codex account at all (AllowsAccount's rule), compacts on it as it
// always did, as a key that names no account does; and the gate fails
// closed — Codex switched off in magpie, the sign-in nowhere magpie can
// resolve, is refused as an account the key may not use is.
func TestGatewayKeyAccountsHoldCodexCompact(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var asked, bodies []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		asked = append(asked, strings.TrimPrefix(r.URL.Path, "/backend-api/codex")+" "+modelOf(b))
		bodies = append(bodies, string(b))
		io.WriteString(w, sse(
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"compaction","id":"cmp_openai","encrypted_content":"openai-own"}}`,
			`data: {"type":"response.completed","response":{"id":"resp_openai","status":"completed","output":[{"type":"compaction","id":"cmp_openai","encrypted_content":"openai-own"}]}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	s := New()
	keys, secrets := newCaller(t, "Held", "Free")
	setAccounts := func(t *testing.T, id string, as ...string) {
		t.Helper()
		if _, err := access.Update("accounts-key", access.Change{Key: id, Accounts: as}); err != nil {
			t.Fatal(err)
		}
	}
	compact := `{"model":"gpt-5.5","stream":true,"tools":[{"type":"function","name":"shell"}],"input":[{"type":"message","role":"user","content":"remember this"},{"type":"compaction_trigger"}]}`
	post := func(secret string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+"/responses/compact", strings.NewReader(compact))
		req.Header.Set("Authorization", "Bearer "+secret)
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	// the sign-in isn't the key's: refused, nothing asked of OpenAI, the
	// message saying the two ways out
	setAccounts(t, keys[0].ID, "codex/spare@example.com")
	code, body := post(secrets[0])
	var e struct {
		Error struct{ Message, Type string }
	}
	json.Unmarshal([]byte(body), &e)
	if code != 403 || e.Error.Type != "permission_error" || !strings.Contains(e.Error.Message, `"Held"`) || !strings.Contains(e.Error.Message, "may not use") ||
		!strings.Contains(e.Error.Message, "it may use codex/spare@example.com") || !strings.Contains(e.Error.Message, "add the signed-in account to the key") ||
		!strings.Contains(e.Error.Message, "compaction_trigger on /responses") {
		t.Fatalf("held off the sign-in: %d %s", code, body)
	}
	if len(asked) != 0 {
		t.Fatalf("a compaction on an account the key may not use reached OpenAI: %v", asked)
	}
	// the sign-in is the key's: compaction relays as it came, on the
	// model Codex asked for
	setAccounts(t, keys[0].ID, "codex/me@example.com")
	code, body = post(secrets[0])
	if code != 200 || !strings.Contains(body, `"id":"cmp_openai"`) {
		t.Fatalf("held to the sign-in: %d %s", code, body)
	}
	if len(asked) != 1 || asked[0] != "/responses/compact gpt-5.5" || bodies[0] != compact {
		t.Fatalf("compacted %v", asked)
	}
	// a key that names no Codex account uses the sign-in as it always
	// did: the list holds it to some accounts of the providers it names
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "relay-secret", Chat: "https://relay.invalid/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	setAccounts(t, keys[0].ID, "relay/"+provider.KeyID("relay-secret"))
	code, body = post(secrets[0])
	if code != 200 || len(asked) != 2 {
		t.Fatalf("naming no Codex account: %d %s, %d upstream requests", code, body, len(asked))
	}
	// a key that names no account compacts as it always did
	code, body = post(secrets[1])
	if code != 200 || !strings.Contains(body, `"id":"cmp_openai"`) || len(asked) != 3 {
		t.Fatalf("a free key: %d %s, %d upstream requests", code, body, len(asked))
	}
	// Codex switched off in magpie, the sign-in still on it: the gate
	// fails closed, refused as an account the key may not use is
	setAccounts(t, keys[0].ID, "codex/spare@example.com")
	if err := provider.SetOff("codex", true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.SetOff("codex", false); err != nil {
			t.Errorf("switching Codex back on: %v", err)
		}
	})
	provider.ForgetAccounts()
	code, body = post(secrets[0])
	json.Unmarshal([]byte(body), &e)
	if code != 403 || e.Error.Type != "permission_error" || len(asked) != 3 {
		t.Fatalf("the provider off, held off the sign-in: %d %s, %d upstream requests", code, body, len(asked))
	}
}

// Codex itself signed in with an OpenAI API key (auth.json's auth_mode)
// rather than a ChatGPT account: a held key's native compaction relays
// as it always did — the sign-in is a key's, spending no account a
// gateway key's list governs.
func TestGatewayKeyCompactOnCodexAPIKey(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	s := New()
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"codex/spare@example.com"}}); err != nil {
		t.Fatal(err)
	}
	// Codex signs back in with an API key: no ChatGPT account behind the
	// sign-in, and no codex provider in magpie at all
	os.WriteFile(filepath.Join(os.Getenv("HOME"), ".codex", "auth.json"),
		[]byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`), 0o600)
	provider.ForgetAccounts()
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		asked = append(asked, strings.TrimPrefix(r.URL.Path, "/backend-api/codex")+" "+modelOf(b))
		io.WriteString(w, sse(
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"compaction","id":"cmp_openai","encrypted_content":"openai-own"}}`,
			`data: {"type":"response.completed","response":{"id":"resp_openai","status":"completed","output":[{"type":"compaction","id":"cmp_openai","encrypted_content":"openai-own"}]}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses/compact", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"x"}`))
	req.Header.Set("Authorization", "Bearer "+secrets[0])
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || strings.Join(asked, ",") != "/responses/compact gpt-5.5" {
		t.Fatalf("on Codex's API key: %d %s asked %v", rec.Code, rec.Body.String(), asked)
	}
}

// A gateway key held to some models (#882) holds Codex's own models too,
// asked for by their bare native names: the turn goes through routing,
// which names them codex/<model> as the catalog does, and one the key
// doesn't list is refused with nothing asked; one it does is served.
func TestGatewayKeyModelsHoldCodexOwnModel(t *testing.T) {
	codexSignedIn(t)
	var heads []http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		heads = append(heads, r.Header.Clone())
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"from `+r.Header.Get("chatgpt-account-id")+`"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	s := New()
	keys, secrets := newCaller(t, "Held")
	setModels := func(t *testing.T, id string, ms ...string) {
		t.Helper()
		if _, err := access.Update("models-key", access.Change{Key: id, Models: ms}); err != nil {
			t.Fatal(err)
		}
	}
	post := func(secret string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
		req.Header.Set("Authorization", "Bearer "+secret)
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	// the key's models are another provider's: Codex's own asked by its
	// bare name is not one of them, and is refused with nothing asked
	setModels(t, keys[0].ID, "relay/m1")
	code, body := post(secrets[0])
	var e struct {
		Error struct{ Message, Type string }
	}
	json.Unmarshal([]byte(body), &e)
	if code != 403 || e.Error.Type != "permission_error" || !strings.Contains(e.Error.Message, `"Held"`) || !strings.Contains(e.Error.Message, "may not use codex/gpt-5.5") || !strings.Contains(e.Error.Message, "relay/m1") {
		t.Fatalf("a native model the key doesn't list: %d %s", code, body)
	}
	if len(heads) != 0 {
		t.Fatalf("a native model the key doesn't list reached OpenAI: %v", heads)
	}
	// one it does list, by the name the catalog gives it: served
	setModels(t, keys[0].ID, "codex/gpt-5.5")
	code, body = post(secrets[0])
	if code != 200 || !strings.Contains(body, "from acct-1") {
		t.Fatalf("a native model the key lists: %d %s", code, body)
	}
	if len(heads) != 1 || heads[0].Get("chatgpt-account-id") != "acct-1" {
		t.Fatalf("asked %v", heads)
	}
}

// With the one account on and no cap, no pin, Codex's own model is
// relayed as it came for a key no hold is set on (a follow-up to #932:
// only a held key is served through its holds).
func TestCodexOwnModelRelayedForAFreeKey(t *testing.T) {
	codexSignedIn(t)
	var asked []string
	var heads []http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		asked = append(asked, strings.TrimPrefix(r.URL.Path, "/backend-api/codex")+" "+modelOf(b))
		heads = append(heads, r.Header.Clone())
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	s := New()
	_, secrets := newCaller(t, "Free")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
	req.Header.Set("Authorization", "Bearer "+secrets[0])
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") {
		t.Fatalf("a free key's own model: %d %s", rec.Code, rec.Body.String())
	}
	// relayed as it came: the path Codex sent, the bare model it asked
	// for, one request
	if len(asked) != 1 || asked[0] != "/responses gpt-5.5" || len(heads) != 1 {
		t.Fatalf("relayed %v", asked)
	}
}

func TestResetsIn(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	for body, want := range map[string]time.Duration{
		`{"error":{"type":"usage_limit_reached","resets_at":1003600,"resets_in_seconds":5}}`: time.Hour,
		`{"error":{"type":"usage_limit_reached","resets_in_seconds":90}}`:                    90 * time.Second,
		`{"error":{"resets_at":999000}}`:                                                     0,
		`{"error":{"message":"quota"}}`:                                                      0,
		`not json`:                                                                           0,
	} {
		if got := resetsIn([]byte(body), now); got != want {
			t.Errorf("%s: %v, want %v", body, got, want)
		}
	}
}

// A turn served by the pool of Codex's accounts goes on with what Codex
// says about it — a guardian review, the turn's metadata, Luna Reserve —
// as it would to Codex's own sign-in, signed by the pool's account and
// never by the client's; the Routing view and the usage log say it was a
// guardian review (碳碳双键: 为何 codex 桌面选的 5.6sol，但是请求记录…显示
// 的全是 gpt-6-luna).
func TestCodexPoolForwardsCodexHeaders(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var heads []http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		heads = append(heads, r.Header.Clone())
		if r.Header.Get("chatgpt-account-id") == "acct-1" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":7200}}`)
			return
		}
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	req.Header.Set("User-Agent", "codex_cli_rs/0.155.1")
	req.Header.Set("x-openai-subagent", "guardian")
	req.Header.Set("x-codex-turn-metadata", `{"turn_id":"t1"}`)
	req.Header.Set("x-openai-codex-luna-reserve", "true")
	req.Header.Set("x-codex-installation-id", "inst-1")
	req.Header.Set("x-codex-turn-state", "acct-1-state")
	req.Header.Set("x-oai-attestation", "device-proof")
	req.Header.Set("x-openai-actor-authorization", "Bearer someone")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(heads) != 2 {
		t.Fatalf("%d requests upstream", len(heads))
	}
	for i, h := range heads {
		for k, want := range map[string]string{
			"x-openai-subagent":       "guardian",
			"x-codex-turn-metadata":   `{"turn_id":"t1"}`,
			"x-codex-installation-id": "inst-1",
			"chatgpt-account-id":      "acct-" + string(rune('1'+i)),
		} {
			if got := h.Get(k); got != want {
				t.Errorf("request %d: %s %q, want %q", i, k, got, want)
			}
		}
		if h.Get("x-openai-codex-luna-reserve") != "" || h.Get("x-codex-turn-state") != "" {
			t.Errorf("request %d carries what holds for Codex's own account: %v", i, h)
		}
		if h.Get("Authorization") == "Bearer chatgpt-token" || h.Get("x-oai-attestation") != "" || h.Get("x-openai-actor-authorization") != "" {
			t.Errorf("request %d carries the client's sign-in: %v", i, h)
		}
	}
	if r := s.trace.routes[len(s.trace.routes)-1]; r.Kind != "guardian" {
		t.Errorf("route kind %q", r.Kind)
	}
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(provider.Path()), "usage.jsonl"))
	if !strings.Contains(string(b), `"kind":"guardian"`) {
		t.Errorf("usage log: %s", b)
	}
}

func TestCallKind(t *testing.T) {
	for want, h := range map[string]map[string]string{
		"":             {},
		"guardian":     {"x-openai-subagent": " guardian ", "x-openai-codex-luna-reserve": "true"},
		"thread_title": {"x-openai-subagent": "thread_title"},
		"memgen":       {"x-openai-memgen-request": "1"},
		"luna_reserve": {"x-openai-codex-luna-reserve": "true"},
	} {
		hh := http.Header{}
		for k, v := range h {
			hh.Set(k, v)
		}
		if got := callKind(hh); got != want {
			t.Errorf("%v: %q, want %q", h, got, want)
		}
	}
	for k, want := range map[string]bool{
		"x-openai-subagent": true, "X-Codex-Turn-Metadata": true, "X-Codex-Turn-State": false, "x-openai-codex-luna-reserve": false, "session_id": true, "Session_id": true,
		"Authorization": false, "chatgpt-account-id": false, "x-oai-attestation": false,
		"x-openai-actor-authorization": false, "User-Agent": false, "Cookie": false,
	} {
		if codexHeader(k) != want {
			t.Errorf("codexHeader(%q) = %v", k, !want)
		}
	}
}

// The ChatGPT backend serves a model only to a Codex that knows it: a turn
// signed by the pool says it comes from a Codex CLI at least as new as the
// one that sent it, in the User-Agent and the version header alike, so a
// model Codex CLI reaches on its own isn't refused through magpie
// (Discord: "The 'gpt-6.1-sol' model is not supported when using Codex
// with a ChatGPT account"). 0.161.0 is newer than any installed here or
// magpie's own fallback, so only the client's own version can pass.
func TestCodexPoolSaysTheClientsVersion(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var uas, vers []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ua := r.Header.Get("User-Agent")
		uas, vers = append(uas, ua), append(vers, r.Header.Get("version"))
		v, _, _ := strings.Cut(strings.TrimPrefix(ua, "codex_cli_rs/"), " ")
		if modelOf(b) == "gpt-6.1-sol" && (!strings.HasPrefix(ua, "codex_cli_rs/") || older(v, "0.161.0")) {
			w.WriteHeader(400)
			io.WriteString(w, `{"detail":"The 'gpt-6.1-sol' model is not supported when using Codex with a ChatGPT account."}`)
			return
		}
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6.1-sol"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-6.1-sol","stream":true,"input":"ping"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("User-Agent", "codex_cli_rs/0.161.0 (Mac OS 26.6.0; arm64) ghostty/1.2.0")
	req.Header.Set("version", "0.161.0")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") {
		t.Fatalf("status %d: %s (User-Agent %q, version %q)", rec.Code, rec.Body.String(), uas, vers)
	}
	for i := range uas {
		if !strings.HasPrefix(uas[i], "codex_cli_rs/0.161.0 (") || vers[i] != "0.161.0" {
			t.Errorf("request %d: User-Agent %q, version %q", i, uas[i], vers[i])
		}
	}
}

// older reports whether version a is before b ("0.159.0" before "0.161.0").
func older(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}
