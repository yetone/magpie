package gui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
)

func TestSettingsSaveKeepsCorruptFile(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/api/settings", `{"theme":"light"}`},
		{"/api/settings/quota-left", `{"on":true}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			h := t.TempDir()
			t.Setenv("HOME", h)
			t.Setenv("USERPROFILE", h)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
			t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
			t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
			for _, v := range agentenv.Vars {
				t.Setenv(v, "")
			}
			if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
				t.Fatal(err)
			}
			const token = "SYNTHETIC_PRIVATE_TOKEN"
			original := []byte(`{"theme":"dark","githubToken":"` + token + `"`)
			if err := os.WriteFile(settings.Path(), original, 0o600); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("save returned %d, want a settings error", rec.Code)
			}
			if strings.Contains(rec.Body.String(), token) {
				t.Error("the error response exposed the settings contents")
			}
			if after, err := os.ReadFile(settings.Path()); err != nil || !bytes.Equal(after, original) {
				t.Errorf("the request overwrote the corrupt settings: %v", err)
			}
		})
	}
}

// What the models' page keeps in settings — names, levels, whether a model
// takes images — outlives a save of the Settings page, which never sends it.
func TestSettingsSaveKeepsModelChoices(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	if err := settings.Save(settings.Settings{
		ModelNames:   map[string]string{"p/m": "Mine"},
		ModelEfforts: map[string][]string{"p/m": {"low"}},
		ModelImages:  map[string]bool{"p/m": true},
	}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"theme":"dark","lang":"en"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s := settings.Load()
	if s.Theme != "dark" || s.ModelNames["p/m"] != "Mine" || len(s.ModelEfforts["p/m"]) != 1 || !s.ModelImages["p/m"] {
		t.Fatalf("theme %q names %v efforts %v images %v", s.Theme, s.ModelNames, s.ModelEfforts, s.ModelImages)
	}
}

// Everything in settings the Settings page doesn't send — the models hidden
// from an agent one by one among it — outlives a save of that page: each
// field prefsKeep leaves out is set here, and must be as it was after a
// save of the theme alone. A field added to settings is either sent by the
// page or has to be set here, and kept by the handler.
func TestSettingsSaveKeepsWhatItDoesNotSend(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	js, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	_, fn, _ := strings.Cut(string(js), "\nfunction prefsKeep(s) {")
	fn, _, _ = strings.Cut(fn, "\n}\n")
	sent := map[string]bool{}
	for _, m := range regexp.MustCompile(`(\w+):`).FindAllStringSubmatch(fn, -1) {
		sent[m[1]] = true
	}
	if !sent["theme"] || !sent["proxy"] {
		t.Fatalf("prefsKeep not read from app.js: %v", sent)
	}
	one, two := 1.0, 2.0
	was := settings.Settings{
		AgentOrder:           []string{"codex"},
		AgentsHidden:         []string{"goose"},
		AgentsShown:          []string{"pi"},
		AgentsHere:           []string{"codex"},
		Visible:              map[string][]string{"claude": {"anthropic"}},
		HiddenModels:         map[string][]string{"claude": {"p/m"}, "codex": {"p/n", "group/g"}},
		DisabledModels:       []string{"*fable*", "p/m"},
		PickedModels:         map[string][]string{"opencode": {"p/m"}},
		OwnModelsHidden:      []string{"dsh"},
		OrderedModels:        map[string][]string{"codex": {"group/g", "p/n"}},
		FastPicks:            map[string][]string{"codex": {"p/n"}},
		UltraPicks:           map[string][]string{"codex": {"p/n"}},
		AgentEfforts:         map[string]string{"cursor-local": "high"},
		PluginCheckins:       map[string]bool{"fakeco": true},
		PluginOptions:        map[string]map[string]bool{"fakeco": {"auto": true}},
		CheckinOff:           []string{"workbuddy|u1"},
		ModelNames:           map[string]string{"p/m": "Mine"},
		ModelEfforts:         map[string][]string{"p/m": {"low"}},
		ModelReasonless:      map[string]bool{"p/n": true},
		ModelImages:          map[string]bool{"p/m": true},
		ModelOutputs:         map[string]int{"p/m": 131072},
		ModelCompacts:        map[string]int{"p/*": 500000},
		CompactAt:            400000,
		AgentCompacts:        map[string]settings.AgentCompact{"claude": {At: 600000}},
		ModelPrices:          map[string]settings.ModelPrice{"p/m": {Input: &one, Output: &two}},
		ModelWires:           map[string]string{"p/m": "vendor-c/m"},
		ModelAPIs:            map[string]string{"p/m": "anthropic"},
		ModelSameAs:          map[string]string{"p/m": "deepseek-v4.1-flash"},
		ModelIDs:             map[string]string{"p/flash": "deepseek-v4.1-flash-1017"},
		UsageAliases:         map[string]string{"deepseek-v4.1-flash": "deepseek-flash"},
		UsageCleared:         []settings.UsageClear{{Provider: "p", Model: "m", At: 1761600000000}},
		ModelKinds:           map[string]string{"p/m": "video"},
		ModelHeaders:         map[string]map[string]string{"p/m": {"X-Endpoint": "ep-1"}},
		ModelSystemPrompts:   map[string]string{"p/m": "Answer in Chinese."},
		RedactRules:          []redact.Rule{{Kind: "prefix", Prefix: "oc_sk_"}},
		RedactActions:        map[string]string{"API_KEY": "block", "EMAIL": "warn"},
		RedactAllow:          []string{"team@kelvarn.io"},
		LAN:                  true,
		LANKey:               "sk-lan",
		LANKeyID:             "lan-key-id",
		Port:                 3591,
		CORSOrigins:          []string{"http://localhost:3000"},
		AllowIPs:             []string{"203.0.113.7"},
		FetchPrivate:         []string{"nas.lan"},
		GitHubToken:          "ghp_kept",
		RequestArchive:       true,
		RequestArchiveMaxMB:  64,
		GatewayConversations: true,
		QuotaLeft:            true,
		UsageOrder:           []string{"kimi", "codex"},
		PanelUsageHidden:     []string{"deepseek"},
		UsageAgentsHidden:    []string{"curl"},
		UsageCardsHidden:     []string{"stepfun|balance"},
		ClientAgents:         map[string]string{"hermes-codex-thread-bridge": "codex"},
		PlainNames:           true,
		PlainOwnNames:        true,
		BareModelIDs:         true,
		CodexAgentsV1:        true,
		CodexTitles:          "off",
		CodexDescriptions:    "fake/m1",
		CodexPredictions:     "off",
		CodexAutoReview:      "fake/m1",
		CodexSubagentModel:   "codex/gpt-6-astra",
		CodexServiceTier:     "priority",
		DesktopSubagentModel: "fake/m2",
		DshCompactModel:      "fake/m1",
		ChinaMirror:          true,
		NPMRegistry:          "https://npm.corp.example",
		CodexAutoReset:       []string{"me@example.com"},
		CodexAutoResetOnly:   map[string]string{"me@example.com": "expiring"},
		CodexResetLead:       180,
		CodexNoCredits:       []string{"me@example.com"},
		CodexCreditsLast:     []string{"two@example.com"},
		CodexCreditsFirst:    []string{"three@example.com"},
		CodexWarmAtOf:        map[string]string{"me@example.com": "09:00"},
		TextSize:             125,
		UpdateSkip:           "0.1.500",
		UpdateMirror:         "https://mirror.example/",
		Window:               []int{900, 700},
		WindowMaximised:      true,
		WindowAt:             []int{-1440, 120},
		FullContext:          true,
		DesktopLongest:       true,
		DesktopIsolated:      true,
		GuideHidden:          true,
		GatewayMode:          "off",
		QuotaReads:           "asked",
		SearchAll:            true,
		SearchCount:          9,
		NoAccountDiscovery:   true,
		RedactModel:          true,
		RedactModelName:      "astrlink-guard",
		RedactModelScore:     map[string]float64{"astrlink-guard": 0.9},
		RedactRouteTo:        "fake/m1",
	}
	was.RedactModelOff, was.RedactModelAuto = true, true
	was.DesktopSubagentEffort = "high"
	was.ReplyModel = settings.ReplyMember // memberModel, its mirror, is kept with it
	was.CodexAsyncCompact, was.CodexAsyncCompactPercent = true, 70
	was.ClaudeRunsMost = 3
	was.RequestArchiveLocal, was.RequestArchiveLocalMB = true, 256
	was.ProxyCustom = "http://me:secret@proxy.example:8080"
	was.PluginProxyCustom = "http://me:secret@plugins.example:8080"
	was.PanelShortcut = "Ctrl+Alt+K"
	was.SearcherThen = []string{"fake/m1", "fake/m2"} // set in its own row
	was.RouterNote = "Los Angeles"                    // the Library's servers/hub-note
	was.RouterAllTools = true                         // the Library's servers/hub-tools
	was.BusyLoad = 150                                // the Routing page's busy-load
	was.MTLS = true                                   // Network and sharing's settings/mtls
	// the Routing groups page's defaults for every group (groups/defaults)
	was.GroupSink, was.GroupFirstToken, was.GroupPatience = true, 30, 120
	if err := settings.Save(was); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetGatewayConversations(true); err != nil {
		t.Fatal(err)
	}
	// only SetSessionReads writes the agents whose sessions are read
	if err := settings.SetSessionReads(func(*settings.SessionReads) *settings.SessionReads {
		return &settings.SessionReads{Asked: true, Agents: map[string]bool{"claude": true, "codex": false}}
	}); err != nil {
		t.Fatal(err)
	}
	was = settings.Load()
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"theme":"dark"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	now := settings.Load()
	if now.Theme != "dark" {
		t.Fatalf("theme %q", now.Theme)
	}
	wv, nv, typ := reflect.ValueOf(was), reflect.ValueOf(now), reflect.TypeOf(was)
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if sent[name] {
			continue
		}
		if wv.Field(i).IsZero() {
			t.Errorf("%s: not sent by the Settings page; set it in this test and keep it in POST /api/settings", typ.Field(i).Name)
			continue
		}
		if !reflect.DeepEqual(wv.Field(i).Interface(), nv.Field(i).Interface()) {
			t.Errorf("%s: %v after a save of the Settings page, was %v", typ.Field(i).Name, nv.Field(i).Interface(), wv.Field(i).Interface())
		}
	}
}

// The Update pill hidden for one version stays hidden for it through a save
// of the Settings page, which doesn't send it, and comes back once cleared.
func TestUpdateSkip(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	post := func(path, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	post("/api/settings/update-skip", `{"version":"0.1.501"}`)
	if s := settings.Load(); s.UpdateSkip != "0.1.501" {
		t.Fatalf("skip %q", s.UpdateSkip)
	}
	post("/api/settings", `{"theme":"dark","noUpdatePill":true}`)
	if s := settings.Load(); s.UpdateSkip != "0.1.501" || !s.NoUpdatePill || s.Theme != "dark" {
		t.Fatalf("after a save: skip %q pill off %v theme %q", s.UpdateSkip, s.NoUpdatePill, s.Theme)
	}
	post("/api/settings/update-skip", `{"version":""}`)
	if s := settings.Load(); s.UpdateSkip != "" || !s.NoUpdatePill {
		t.Fatalf("cleared: skip %q pill off %v", s.UpdateSkip, s.NoUpdatePill)
	}
}
