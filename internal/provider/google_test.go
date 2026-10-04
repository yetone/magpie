package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// fakeGoogle is Google's token endpoint and Code Assist, as far as magpie
// uses them.
type fakeGoogle struct {
	mu        sync.Mutex
	refreshes int
	loads     []map[string]any
	onboards  []map[string]any
	load      string // loadCodeAssist's reply
	onboard   string // onboardUser's reply
	quota     string // retrieveUserQuota's reply
	flags     string // listExperiments' reply
	models    string // fetchAvailableModels' reply
	summary   string // retrieveUserQuotaSummary's reply
	// fetchAvailableModels' status and reply for the body asked, in place
	// of models
	modelsFor func(body map[string]any) (int, string)
	fetches   []map[string]any
	exps      []map[string]any
	heads     map[string]http.Header
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.heads == nil {
		f.heads = map[string]http.Header{}
	}
	f.heads[r.URL.Path] = r.Header
	b, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(b, &body)
	switch {
	case r.URL.Path == "/token":
		f.refreshes++
		form, _ := url.ParseQuery(string(b))
		if form.Get("refresh_token") == "" || form.Get("grant_type") != "refresh_token" {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		io.WriteString(w, `{"access_token":"fresh-token","expires_in":3600}`)
	case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
		f.loads = append(f.loads, body)
		io.WriteString(w, f.load)
	case strings.HasSuffix(r.URL.Path, ":onboardUser"):
		f.onboards = append(f.onboards, body)
		io.WriteString(w, f.onboard)
	case strings.HasSuffix(r.URL.Path, ":retrieveUserQuota") && f.quota != "":
		io.WriteString(w, f.quota)
	case strings.HasSuffix(r.URL.Path, ":retrieveUserQuotaSummary") && f.summary != "":
		io.WriteString(w, f.summary)
	case strings.HasSuffix(r.URL.Path, ":fetchAvailableModels") && f.modelsFor != nil:
		f.fetches = append(f.fetches, body)
		code, reply := f.modelsFor(body)
		w.WriteHeader(code)
		io.WriteString(w, reply)
	case strings.HasSuffix(r.URL.Path, ":fetchAvailableModels") && f.models != "":
		io.WriteString(w, f.models)
	case strings.HasSuffix(r.URL.Path, ":listExperiments") && f.flags != "":
		f.exps = append(f.exps, body)
		io.WriteString(w, f.flags)
	case strings.HasSuffix(r.URL.Path, "latest-arm64-mac.yml"):
		io.WriteString(w, "version: 3.1.4\npath: x.zip\n")
	default:
		http.NotFound(w, r)
	}
}

func googleSandbox(t *testing.T, f *fakeGoogle) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows's home
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	oldToken, oldProd, oldDaily, oldVer, oldPoll := googleTokenURL, codeAssistProd, codeAssistDaily, antigravityVersionURL, onboardPoll
	googleTokenURL, codeAssistProd, codeAssistDaily = srv.URL+"/token", srv.URL+"/prod", srv.URL+"/daily"
	antigravityVersionURL, onboardPoll = srv.URL+"/latest-arm64-mac.yml", time.Millisecond
	resetGoogleState()
	t.Cleanup(func() {
		googleTokenURL, codeAssistProd, codeAssistDaily, antigravityVersionURL, onboardPoll = oldToken, oldProd, oldDaily, oldVer, oldPoll
		resetGoogleState()
	})
}

func resetGoogleState() {
	googleState.Lock()
	googleState.tokens = map[string]googleAuth{}
	googleState.projects = map[string]googleProject{}
	googleState.flags = map[string]geminiFlags{}
	googleState.Unlock()
	antigravityVer.Lock()
	antigravityVer.v, antigravityVer.at = "", time.Time{}
	antigravityVer.Unlock()
}

func writeGeminiLogin(t *testing.T, env string) {
	t.Helper()
	dir := geminiDir()
	os.MkdirAll(dir, 0o700)
	creds := `{"access_token":"old","refresh_token":"rt-own","expiry_date":1}`
	os.WriteFile(filepath.Join(dir, "oauth_creds.json"), []byte(creds), 0o600)
	os.WriteFile(filepath.Join(dir, "google_accounts.json"), []byte(`{"active":"me@example.com","old":[]}`), 0o600)
	if env != "" {
		os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600)
	}
}

// Gemini CLI's own sign-in is found, and its expired token is refreshed in
// memory: the file Gemini CLI keeps is left as it was.
func TestGeminiOwnLoginRefreshesInMemory(t *testing.T) {
	f := &fakeGoogle{}
	googleSandbox(t, f)
	writeGeminiLogin(t, "")
	before, _ := os.ReadFile(filepath.Join(geminiDir(), "oauth_creds.json"))
	g, ok := geminiOwnLogin()
	if !ok || g.user != "me@example.com" || !g.own {
		t.Fatalf("own login = %+v, %v", g, ok)
	}
	for i := 0; i < 2; i++ {
		tok, err := g.token(context.Background())
		if err != nil || tok != "fresh-token" {
			t.Fatalf("token = %q, %v", tok, err)
		}
	}
	if f.refreshes != 1 {
		t.Errorf("refreshed %d times, want once", f.refreshes)
	}
	after, _ := os.ReadFile(filepath.Join(geminiDir(), "oauth_creds.json"))
	if !bytes.Equal(before, after) {
		t.Error("Gemini CLI's oauth_creds.json was written")
	}
}

// Only a sign-in Gemini CLI's own OAuth client minted is Gemini CLI's: one
// minted for another client (Antigravity's) can't be refreshed as Gemini
// CLI's and isn't listed; one without an ID token is taken as Gemini CLI's.
func TestGeminiOwnLoginOnlyGeminiCLIsClient(t *testing.T) {
	idToken := func(claims map[string]any) string {
		b, _ := json.Marshal(claims)
		return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	}
	for _, c := range []struct {
		name, idToken string
		want          bool
	}{
		{"no id token", "", true},
		{"gemini cli", idToken(map[string]any{"aud": geminiApp.clientID, "azp": geminiApp.clientID}), true},
		{"antigravity", idToken(map[string]any{"aud": antigravityApp.clientID, "azp": antigravityApp.clientID}), false},
		{"antigravity aud only", idToken(map[string]any{"aud": antigravityApp.clientID}), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			googleSandbox(t, &fakeGoogle{})
			dir := geminiDir()
			os.MkdirAll(dir, 0o700)
			creds, _ := json.Marshal(map[string]any{"access_token": "old", "refresh_token": "rt-own", "expiry_date": 1, "id_token": c.idToken})
			os.WriteFile(filepath.Join(dir, "oauth_creds.json"), creds, 0o600)
			os.WriteFile(filepath.Join(dir, "google_accounts.json"), []byte(`{"active":"me@example.com","old":[]}`), 0o600)
			_, ok := geminiOwnLogin()
			if ok != c.want {
				t.Fatalf("own login found = %v, want %v", ok, c.want)
			}
			if n := len(googleLoginList("gemini")); n != map[bool]int{true: 1, false: 0}[c.want] {
				t.Errorf("gemini lists %d accounts", n)
			}
			if n := len(googleLoginList("antigravity")); n != 0 {
				t.Errorf("antigravity lists %d accounts", n)
			}
		})
	}
}

// Google turns individuals away from Gemini CLI's sign-in; the account
// then needs a project, and magpie says how to name one.
func TestGeminiNeedsAProject(t *testing.T) {
	f := &fakeGoogle{load: `{"allowedTiers":[{"id":"standard-tier","name":"Gemini Code Assist","userDefinedCloudaicompanionProject":true,"isDefault":true}],
		"ineligibleTiers":[{"reasonCode":"UNSUPPORTED_CLIENT","reasonMessage":"This client is no longer supported","tierId":"free-tier"}]}`}
	googleSandbox(t, f)
	writeGeminiLogin(t, "")
	g, _ := geminiOwnLogin()
	_, err := g.project(context.Background())
	if err == nil || !strings.Contains(err.Error(), "magpie accounts project gemini me@example.com") ||
		!strings.Contains(err.Error(), "no longer supported") || !strings.Contains(err.Error(), "~/.gemini/.env") ||
		!strings.Contains(err.Error(), "add it under Antigravity in magpie") {
		t.Fatalf("err = %v", err)
	}
	if len(f.onboards) != 0 {
		t.Error("onboarded without a project")
	}
}

// With GOOGLE_CLOUD_PROJECT in Gemini CLI's .env the account is onboarded
// to it, as Gemini CLI does.
func TestGeminiProjectFromEnv(t *testing.T) {
	f := &fakeGoogle{
		load:    `{"allowedTiers":[{"id":"standard-tier","name":"Gemini Code Assist","userDefinedCloudaicompanionProject":true,"isDefault":true}]}`,
		onboard: `{"done":true,"response":{"cloudaicompanionProject":{"id":"my-proj","name":"x"}}}`,
	}
	googleSandbox(t, f)
	writeGeminiLogin(t, "# comment\nGOOGLE_CLOUD_PROJECT=\"my-proj\"\n")
	g, _ := geminiOwnLogin()
	p, err := g.project(context.Background())
	if err != nil || p.id != "my-proj" || p.plan != "Gemini Code Assist" {
		t.Fatalf("project = %+v, %v", p, err)
	}
	if f.loads[0]["cloudaicompanionProject"] != "my-proj" || f.onboards[0]["tierId"] != "standard-tier" {
		t.Errorf("load %v onboard %v", f.loads[0], f.onboards[0])
	}
	if ua := f.heads["/prod/v1internal:loadCodeAssist"].Get("User-Agent"); !strings.HasPrefix(ua, "GeminiCLI/") {
		t.Errorf("User-Agent = %q", ua)
	}
}

// An account named in magpie gets its project from `magpie accounts project`.
func TestSetGoogleProject(t *testing.T) {
	f := &fakeGoogle{load: `{"currentTier":{"id":"standard-tier","name":"Gemini Code Assist Standard"}}`}
	googleSandbox(t, f)
	auth := googleAuth{AccessToken: "a", RefreshToken: "rt-2", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	if err := addGoogleLogin("gemini", "work@example.com", "", auth); err != nil {
		t.Fatal(err)
	}
	ls := googleLogins("gemini")
	if len(ls) != 1 {
		t.Fatalf("logins = %+v", ls)
	}
	if _, err := ls[0].acct.project(context.Background()); err == nil {
		t.Fatal("a project was found for an account with none")
	}
	if err := SetGoogleProject("gemini", "work@example.com", " proj-7 "); err != nil {
		t.Fatal(err)
	}
	p, err := googleLogins("gemini")[0].acct.project(context.Background())
	if err != nil || p.id != "proj-7" || p.plan != "Gemini Code Assist Standard" {
		t.Fatalf("project = %+v, %v", p, err)
	}
	if err := SetGoogleProject("gemini", "nobody@example.com", "x"); err == nil {
		t.Error("set a project on an account that isn't there")
	}
}

// A Gemini CLI account lists the models Gemini CLI 0.61 offers it — one
// Flash and one Flash Lite, the latest where Code Assist's experiments
// roll it out to the account, previews where its quota has one — and each
// goes to Code Assist by the id the CLI sends.
func TestGeminiModelsAsTheCLIOffersThem(t *testing.T) {
	ids := func(ms []catalog.Model) string {
		var s []string
		for _, m := range ms {
			if m.Name == "" {
				t.Errorf("%s has no name", m.ID)
			}
			s = append(s, m.ID)
		}
		return strings.Join(s, " ")
	}
	sent := func(p Provider, model string) (string, string) {
		body := `{"model":"` + model + `","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`
		req, _ := http.NewRequest("POST", p.Base(CodeAssist)+"/v1internal:streamGenerateContent?alt=sse", strings.NewReader(body))
		if err := p.Sign(context.Background(), req, CodeAssist, []byte(body)); err != nil {
			t.Fatal(err)
		}
		var env map[string]any
		b, _ := io.ReadAll(req.Body)
		json.Unmarshal(b, &env)
		return env["model"].(string), req.Header.Get("User-Agent")
	}
	quota := `{"buckets":[{"modelId":"gemini-2.5-pro","remainingFraction":1},{"modelId":"gemini-3-flash","remainingFraction":0.5},
		{"modelId":"gemini-3-pro-preview","remainingFraction":1},{"modelId":"gemini-3.1-flash-lite","remainingFraction":1}]}`
	for _, c := range []struct {
		name, flags, quota, models string
		wire                       map[string]string
	}{
		{"no experiments", "", quota,
			"gemini-3-pro-preview gemini-3-flash-preview gemini-3.5-flash gemini-3.1-flash-lite gemini-2.5-pro gemini-2.5-flash gemini-2.5-flash-lite",
			map[string]string{"gemini-3.5-flash": "gemini-3-flash", "gemini-3.1-flash-lite": "gemini-3.1-flash-lite", "gemini-2.5-pro": "gemini-2.5-pro",
				"gemini-3.8-flash": "gemini-3-flash", "gemini-3.5-flash-lite": "gemini-3.1-flash-lite"}},
		{"latest rolled out", `{"flags":[{"flagId":45842815,"boolValue":true},{"flagId":45827489,"boolValue":true},{"flagId":45760185,"boolValue":true}]}`, quota,
			"gemini-3.1-pro-preview gemini-3-flash-preview gemini-3.8-flash gemini-3.5-flash-lite gemini-2.5-pro gemini-2.5-flash-lite",
			map[string]string{"gemini-3.8-flash": "gemini-3.8-flash", "gemini-3.5-flash": "gemini-3.8-flash", "gemini-3-flash": "gemini-3.8-flash",
				"gemini-3.1-flash-lite": "gemini-3.5-flash-lite", "gemini-3.5-flash-lite": "gemini-3.5-flash-lite"}},
		{"no preview, no pro", `{"flags":[{"flagId":45768879,"boolValue":true}]}`, `{"buckets":[{"modelId":"gemini-2.5-flash","remainingFraction":1}]}`,
			"gemini-3.5-flash gemini-3.1-flash-lite gemini-2.5-flash gemini-2.5-flash-lite", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeGoogle{load: `{"currentTier":{"id":"standard-tier","name":"Gemini Code Assist Standard"},"cloudaicompanionProject":"proj"}`,
				quota: c.quota, flags: c.flags}
			googleSandbox(t, f)
			auth := googleAuth{AccessToken: "a", RefreshToken: "rt-m", Expiry: time.Now().Add(time.Hour).UnixMilli()}
			if err := addGoogleLogin("gemini", "work@example.com", "", auth); err != nil {
				t.Fatal(err)
			}
			p, _ := googleAccountOf("gemini")
			if _, err := p.Fetch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := ids(p.Available()); got != c.models {
				t.Errorf("models = %s\nwant     %s", got, c.models)
			}
			for asked, want := range c.wire {
				if got, ua := sent(p, asked); got != want || !strings.Contains(ua, "GeminiCLI/0.61.0/"+asked+" ") {
					t.Errorf("%s went as %s (User-Agent %q), want %s", asked, got, ua, want)
				}
			}
			if c.flags != "" {
				meta, _ := f.exps[0]["metadata"].(map[string]any)
				if len(f.exps) != 1 || f.exps[0]["project"] != "proj" || meta["duetProject"] != "proj" || meta["ideVersion"] != "0.61.0" {
					t.Errorf("experiments asked %d times: %v", len(f.exps), f.exps)
				}
			}
			// the quota's gemini-3-flash is the Flash the CLI shows
			if c.quota == quota {
				flash := map[bool]string{false: "gemini-3.5-flash", true: "gemini-3.8-flash"}[c.flags != ""]
				found := false
				for _, w := range googleLogins("gemini")[0].acct.quota(context.Background(), "").Windows {
					found = found || w.Model == flash && w.Used == 50
				}
				if !found {
					t.Errorf("no %s quota window", flash)
				}
			}
		})
	}
	// Antigravity's ids go as they are
	out, _, _ := codeAssistEnvelope("antigravity", []byte(`{"model":"gemini-3.5-flash","request":{}}`), "p", geminiFlags{})
	if !strings.Contains(string(out), `"model":"gemini-3.5-flash"`) {
		t.Errorf("antigravity envelope = %s", out)
	}
}

// Antigravity's account is onboarded on the daily endpoint, asking until
// it is done, and its requests carry what Antigravity sends.
func TestAntigravityProjectAndEnvelope(t *testing.T) {
	f := &fakeGoogle{
		load:    `{"allowedTiers":[{"id":"free-tier","name":"Antigravity","isDefault":true}]}`,
		onboard: `{"done":true,"response":{"cloudaicompanionProject":"ag-proj"}}`,
	}
	googleSandbox(t, f)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-ag", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	if err := addGoogleLogin("antigravity", "ag@example.com", "", auth); err != nil {
		t.Fatal(err)
	}
	p, ok := googleAccountOf("antigravity")
	if !ok || p.Account == nil || p.Base(CodeAssist) != codeAssistDaily {
		t.Fatalf("provider = %+v", p)
	}
	if s := p.Speaks(); len(s) != 1 || s[0] != CodeAssist {
		t.Fatalf("speaks %v", s)
	}
	body := `{"model":"claude-sonnet-4-6","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`
	req, _ := http.NewRequest("POST", p.Base(CodeAssist)+"/v1internal:streamGenerateContent?alt=sse", strings.NewReader(body))
	if err := p.Sign(context.Background(), req, CodeAssist, []byte(body)); err != nil {
		t.Fatal(err)
	}
	if f.heads["/prod/v1internal:loadCodeAssist"] == nil || f.heads["/daily/v1internal:onboardUser"] == nil {
		t.Errorf("asked %v", f.heads)
	}
	if f.onboards[0]["tier_id"] != "free-tier" {
		t.Errorf("onboard = %v", f.onboards[0])
	}
	var env map[string]any
	b, _ := io.ReadAll(req.Body)
	json.Unmarshal(b, &env)
	r, _ := env["request"].(map[string]any)
	if env["project"] != "ag-proj" || env["userAgent"] != "antigravity" || env["requestType"] != "agent" ||
		!strings.HasPrefix(env["requestId"].(string), "agent-") || r["sessionId"] == "" || r["sessionId"] == nil {
		t.Errorf("envelope = %s", b)
	}
	if req.Header.Get("Authorization") != "Bearer tok" || !strings.HasPrefix(req.Header.Get("User-Agent"), "antigravity/hub/3.1.4 ") {
		t.Errorf("headers = %v", req.Header)
	}
	// the same conversation keeps its session
	req2, _ := http.NewRequest("POST", "http://x", nil)
	p.Sign(context.Background(), req2, CodeAssist, []byte(body))
	b2, _ := io.ReadAll(req2.Body)
	var env2 map[string]any
	json.Unmarshal(b2, &env2)
	if env2["request"].(map[string]any)["sessionId"] != r["sessionId"] {
		t.Error("session changed between turns")
	}
}

// An Antigravity account's windows are one a model, each naming its family
// (01huadalang on Discord: several accounts, every level of every model,
// read as bloat), so the GUI can show one figure a family; the windows
// themselves, and the model each counts for routing, stay as they were.
func TestAntigravityQuotaFamilies(t *testing.T) {
	f := &fakeGoogle{
		load:    `{"allowedTiers":[{"id":"free-tier","name":"Antigravity","isDefault":true}]}`,
		onboard: `{"done":true,"response":{"cloudaicompanionProject":"ag-proj"}}`,
		models: `{"models":{
			"gemini-3.1-pro-high":{"displayName":"Gemini 3.1 Pro (High)","quotaInfo":{"remainingFraction":0.4,"resetTime":"2099-01-01T00:00:00Z"}},
			"gemini-3.1-pro-low":{"displayName":"Gemini 3.1 Pro (Low)","quotaInfo":{"remainingFraction":1}},
			"gemini-3.7-flash-medium":{"displayName":"Gemini 3.7 Flash (Medium)","quotaInfo":{}},
			"claude-opus-4-6-thinking":{"displayName":"Claude Opus 4.6 (Thinking)","quotaInfo":{"remainingFraction":0.75}},
			"claude-sonnet-4-6":{"quotaInfo":{"remainingFraction":0.9}},
			"gpt-oss-120b-medium":{"displayName":"GPT-OSS 120B (Medium)","quotaInfo":{"remainingFraction":1}},
			"tab_flash_lite_preview":{"quotaInfo":{"remainingFraction":1}}}}`,
	}
	googleSandbox(t, f)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-ag", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	if err := addGoogleLogin("antigravity", "ag@example.com", "", auth); err != nil {
		t.Fatal(err)
	}
	q := googleLogins("antigravity")[0].acct.quota(context.Background(), "")
	var got []string
	for _, w := range q.Windows {
		got = append(got, fmt.Sprintf("%s|%s|%s|%.0f", w.Model, w.Name, w.Family, w.Used))
	}
	want := []string{
		"claude-opus-4-6-thinking|Claude Opus 4.6 (Thinking)|Claude|25",
		"claude-sonnet-4-6|claude-sonnet-4-6|Claude|10",
		"gemini-3.1-pro-high|Gemini 3.1 Pro (High)|Gemini|60",
		"gemini-3.1-pro-low|Gemini 3.1 Pro (Low)|Gemini|0",
		"gemini-3.7-flash-medium|Gemini 3.7 Flash (Medium)|Gemini|100",
		"gpt-oss-120b-medium|GPT-OSS 120B (Medium)|GPT-OSS|0",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("windows\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	b, _ := json.Marshal(q.Windows[0])
	if !strings.Contains(string(b), `"family":"Claude"`) {
		t.Errorf("json %s", b)
	}
	// a family Antigravity may add later goes by its name's first word
	for _, m := range []catalog.Model{{ID: "gemini-3-flash"}, {ID: "claude-x", Name: "Other"}, {ID: "kimi-k2", Name: "Kimi K2"}, {ID: "glm-5"}} {
		fam := antigravityVendor(m)
		if want := map[string]string{"gemini-3-flash": "Gemini", "claude-x": "Claude", "kimi-k2": "Kimi", "glm-5": "glm"}[m.ID]; fam != want {
			t.Errorf("family of %s = %q, want %q", m.ID, fam, want)
		}
	}
}

// An Antigravity account's models draw on a quota a group, each with a
// 5-hour and a weekly window (a user on Discord: 这3个模型都是一样的，没必
// 要分开…多加个7day 条就好); fetchAvailableModels tells only the 5-hour one,
// model by model. The groups' windows, from retrieveUserQuotaSummary, come
// after the models', each model naming its group, and a page of text shows
// the groups' windows in place of the models'.
func TestAntigravityQuotaPools(t *testing.T) {
	f := &fakeGoogle{
		load:    `{"allowedTiers":[{"id":"free-tier","name":"Antigravity","isDefault":true}]}`,
		onboard: `{"done":true,"response":{"cloudaicompanionProject":"ag-proj"}}`,
		models: `{"models":{
			"gemini-3.1-pro-high":{"displayName":"Gemini 3.1 Pro (High)","quotaInfo":{"remainingFraction":0.9545545,"resetTime":"2099-01-01T05:00:00Z"}},
			"gemini-3-flash":{"displayName":"Gemini 3 Flash","quotaInfo":{"remainingFraction":0.9545545,"resetTime":"2099-01-01T05:00:00Z"}},
			"claude-opus-4-6-thinking":{"displayName":"Claude Opus 4.6 (Thinking)","quotaInfo":{"remainingFraction":0.7}},
			"gpt-oss-120b-medium":{"displayName":"GPT-OSS 120B (Medium)","quotaInfo":{"remainingFraction":0.7}}}}`,
		// as Antigravity answers (Antigravity-Manager#3185)
		summary: `{"groups":[
			{"displayName":"Gemini Models","description":"Models within this group: Gemini Flash, Gemini Pro","buckets":[
				{"bucketId":"gemini-weekly","window":"weekly","remainingFraction":0.75,"resetTime":"2099-01-07T00:00:00Z","displayName":"Weekly Limit"},
				{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.9545545,"resetTime":"2099-01-01T05:00:00Z","displayName":"Five Hour Limit"}]},
			{"displayName":"Claude and GPT models","description":"Models within this group: Claude Opus, Claude Sonnet, GPT-OSS","buckets":[
				{"bucketId":"3p-weekly","window":"weekly","resetTime":"2099-01-06T00:00:00Z"},
				{"bucketId":"3p-5h","window":"5h","remainingFraction":0.7,"resetTime":"2099-01-01T03:00:00Z"},
				{"bucketId":"3p-x","window":"5h","remainingFraction":1,"disabled":true}]}]}`,
	}
	googleSandbox(t, f)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-ag", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	if err := addGoogleLogin("antigravity", "ag@example.com", "", auth); err != nil {
		t.Fatal(err)
	}
	q := googleLogins("antigravity")[0].acct.quota(context.Background(), "")
	line := func(w QuotaWindow) string {
		at := ""
		if w.ResetsAt != nil {
			at = w.ResetsAt.UTC().Format("01-02T15")
		}
		return fmt.Sprintf("%s|%s|%s|%s|%.0f|%s|%v", w.Model, w.Name, w.Family, w.Pool, w.Used, at, w.Aside)
	}
	var got []string
	for _, w := range q.Windows {
		got = append(got, line(w))
	}
	want := []string{
		"claude-opus-4-6-thinking|Claude Opus 4.6 (Thinking)|Claude|Claude & GPT|30||false",
		"gemini-3-flash|Gemini 3 Flash|Gemini|Gemini|5|01-01T05|false",
		"gemini-3.1-pro-high|Gemini 3.1 Pro (High)|Gemini|Gemini|5|01-01T05|false",
		"gpt-oss-120b-medium|GPT-OSS 120B (Medium)|GPT-OSS|Claude & GPT|30||false",
		"|7 days||Gemini|25|01-07T00|true",
		"|5 hours||Gemini|5|01-01T05|true",
		"|7 days||Claude & GPT|100|01-06T00|true", // none left: no fraction
		"|5 hours||Claude & GPT|30|01-01T03|true",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("windows\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	b, _ := json.Marshal(q.Windows[4])
	if !strings.Contains(string(b), `"pool":"Gemini"`) {
		t.Errorf("json %s", b)
	}
	// routing reads the models' windows as it did; the pools' are aside
	if a := allowanceOf(q.Windows, time.Now()); len(a) != 4 {
		t.Errorf("allowance has %d limits, want the 4 models'", len(a))
	}
	// a model not enabled leaves its pool's windows as they were
	kept := chosenWindows(q.Windows, map[string]bool{"gemini-3.1-pro-high": true}, nil)
	if len(kept) != 5 {
		t.Errorf("chosen windows: %d, want 1 model's and 4 pools'", len(kept))
	}
	if len(chosenWindows(q.Windows, map[string]bool{}, nil)) != len(q.Windows) {
		t.Error("no model enabled: every window is kept")
	}
	got = nil
	for _, w := range PooledWindows(q.Windows) {
		got = append(got, w.Name)
	}
	if strings.Join(got, ",") != "Gemini · 7 days,Gemini · 5 hours,Claude & GPT · 7 days,Claude & GPT · 5 hours" {
		t.Errorf("pooled = %v", got)
	}

	// no summary to be had: the models' windows alone, as before
	f.mu.Lock()
	f.summary = ""
	f.mu.Unlock()
	q = googleLogins("antigravity")[0].acct.quota(context.Background(), "")
	if len(q.Windows) != 4 || q.Windows[0].Pool != "" || len(PooledWindows(q.Windows)) != 4 {
		t.Errorf("without a summary: %+v", q.Windows)
	}

	// a summary with a group's week and not its 5 hours (#745): the
	// group's models aren't put in it, so their 5 hours stay beside its week
	f.mu.Lock()
	f.summary = `{"groups":[
		{"displayName":"Gemini Models","description":"Models within this group: Gemini Flash, Gemini Pro","buckets":[
			{"bucketId":"gemini-weekly","window":"weekly","remainingFraction":0.75,"resetTime":"2099-01-07T00:00:00Z"},
			{"bucketId":"gemini-5h","window":"5h","remainingFraction":1,"disabled":true}]}]}`
	f.mu.Unlock()
	q = googleLogins("antigravity")[0].acct.quota(context.Background(), "")
	got = nil
	for _, w := range PooledWindows(q.Windows) {
		got = append(got, w.Name+"|"+w.Pool)
	}
	if strings.Join(got, ",") != "Claude Opus 4.6 (Thinking)|,Gemini 3 Flash|,Gemini 3.1 Pro (High)|,GPT-OSS 120B (Medium)|,Gemini · 7 days|Gemini" {
		t.Errorf("week alone: pooled = %v", got)
	}
}

// #745 (werldl517-cyber): a pool's windows stand in for its models' only
// for the spans the pool has; with only its week, a model's 5 hours stay.
func TestPooledWindowsPartialAggregateKeepsModelFallback(t *testing.T) {
	ws := []QuotaWindow{
		{Name: "Gemini 3 Flash", Model: "gemini-3-flash", Pool: "Gemini", Span: 5 * time.Hour, Used: 40},
		{Name: "7 days", Pool: "Gemini", Span: 7 * 24 * time.Hour, Aside: true, Used: 80},
	}
	got := PooledWindows(ws)
	for _, w := range got {
		t.Logf("name=%q model=%q pool=%q span=%s used=%.0f aside=%v", w.Name, w.Model, w.Pool, w.Span, w.Used, w.Aside)
	}
	if len(got) != 2 {
		t.Fatalf("got %d windows, want the 5h model fallback plus the 7d pool aggregate", len(got))
	}

	model := func(id string, span time.Duration) QuotaWindow {
		return QuotaWindow{Name: id, Model: id, Pool: "Gemini", Span: span}
	}
	five := QuotaWindow{Name: "5 hours", Pool: "Gemini", Span: 5 * time.Hour, Aside: true}
	week := QuotaWindow{Name: "7 days", Pool: "Gemini", Span: 7 * 24 * time.Hour, Aside: true}
	for _, c := range []struct {
		name string
		ws   []QuotaWindow
		want string
	}{
		{"5 hours and week", []QuotaWindow{model("a", 0), model("b", 0), week, five}, "Gemini · 7 days,Gemini · 5 hours"},
		{"week alone", []QuotaWindow{model("a", 0), model("b", 5*time.Hour), week}, "a,b,Gemini · 7 days"},
		{"5 hours alone", []QuotaWindow{model("a", 0), model("b", 0), five}, "Gemini · 5 hours"},
		{"no pool windows", []QuotaWindow{model("a", 0), model("b", 0)}, "a,b"},
	} {
		var names []string
		for _, w := range PooledWindows(c.ws) {
			names = append(names, w.Name)
		}
		if strings.Join(names, ",") != c.want {
			t.Errorf("%s: %v, want %s", c.name, names, c.want)
		}
	}
}

// Antigravity's list is taken as Antigravity gives it (0000FF on Discord:
// Claude Opus 5.5 and Sonnet 5.5 in Antigravity, not in magpie): in its
// picker's order, so a model it adds comes where it puts it, not after
// older ones by id; with the context, output and images it says, so a
// model it adds isn't given those of a model of that name elsewhere
// (Anthropic's 1M for Claude), nor said to see when it doesn't.
func TestAntigravityModelsAsItListsThem(t *testing.T) {
	f := &fakeGoogle{
		load:    `{"allowedTiers":[{"id":"free-tier","name":"Antigravity","isDefault":true}]}`,
		onboard: `{"done":true,"response":{"cloudaicompanionProject":"ag-proj"}}`,
		models: `{"models":{
			"claude-opus-4-6-thinking":{"displayName":"Claude Opus 4.6 (Thinking)","maxTokens":250000,"maxOutputTokens":64000,"supportsImages":true,"quotaInfo":{"remainingFraction":1}},
			"claude-opus-5-5":{"displayName":"Claude Opus 5.5","maxTokens":250000,"maxOutputTokens":64000,"supportsImages":true,"quotaInfo":{"remainingFraction":1}},
			"gemini-3.8-flash-high":{"displayName":"Gemini 3.8 Flash (High)","maxTokens":1048576,"maxOutputTokens":65536,"supportsImages":true,"quotaInfo":{"remainingFraction":1}},
			"gemini-3.8-flash-low":{"displayName":"Gemini 3.8 Flash (Low)","maxTokens":1048576,"maxOutputTokens":65536,"supportsImages":true,"quotaInfo":{"remainingFraction":1}},
			"gemini-3.1-flash-lite":{"displayName":"Gemini 3.1 Flash Lite","maxTokens":1048576,"maxOutputTokens":65535,"quotaInfo":{"remainingFraction":1}},
			"text-only-x":{"displayName":"Text Only","maxTokens":131072,"supportsImages":false,"quotaInfo":{"remainingFraction":1}}},
			"agentModelSorts":[{"displayName":"Recommended","groups":[{"modelIds":["gemini-3.8-flash-high","gemini-3.8-flash-low","claude-opus-5-5","claude-opus-4-6-thinking","text-only-x"]}]}]}`,
	}
	googleSandbox(t, f)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-ag", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	if err := addGoogleLogin("antigravity", "ag@example.com", "", auth); err != nil {
		t.Fatal(err)
	}
	ms, err := googleLogins("antigravity")[0].acct.models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range ms {
		got = append(got, fmt.Sprintf("%s|%d|%d|%v", m.ID, m.Context, m.Output, m.Images))
	}
	want := []string{
		"gemini-3.8-flash-high|1048576|65536|true",
		"gemini-3.8-flash-low|1048576|65536|true",
		"claude-opus-5-5|250000|64000|true",
		"claude-opus-4-6-thinking|250000|64000|true",
		"text-only-x|131072|0|false",
		"gemini-3.1-flash-lite|1048576|65535|true", // not in the picker: after, and it says nothing of images
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("models\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// what magpie offers: the family first, Claude 5.5 before 4.6, Antigravity's 250k kept
	var offered []string
	for _, m := range collapseAntigravityModels(catalog.Decorate(ms, []catalog.Model{{ID: "claude-opus-5-5", Context: 1000000, Output: 128000}})) {
		offered = append(offered, fmt.Sprintf("%s|%d", m.ID, m.Context))
	}
	if w := "gemini-3.8-flash|1048576 claude-opus-5-5|250000 claude-opus-4-6-thinking|250000 text-only-x|131072 gemini-3.1-flash-lite|1048576"; strings.Join(offered, " ") != w {
		t.Errorf("offered %v\nwant %s", offered, w)
	}
}
