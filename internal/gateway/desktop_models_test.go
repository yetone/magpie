package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Claude Desktop's model menu lists the models picked for it on the Agents
// page (蓝猫): /v1/models, asked with its key, holds those and no other,
// each named as the agents' lists name it — the Provider in model names
// setting, so the menu reads like Codex's (#1746) — and two of one name
// stay apart.
func TestClaudeDesktopPickedModels(t *testing.T) {
	setup(t, provider.Anthropic, &fake{})
	for _, p := range []provider.Provider{
		{ID: "opencode-go", Name: "OpenCode Go", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4.1-flash", "glm-5"}},
		{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4.1-flash"}},
		{ID: "grok-2", Name: "Grok", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"grok-4.6"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for ref, name := range map[string]string{"opencode-go/deepseek-v4.1-flash": "DeepSeek V4.1 Flash", "relay/deepseek-v4.1-flash": "DeepSeek V4.1 Flash", "grok-2/grok-4.6": "grok-4.6"} {
		if err := provider.SetModelName(ref, name); err != nil {
			t.Fatal(err)
		}
	}
	list := func() map[string]desktopRow {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/models?limit=1000", nil)
		req.Header.Set("x-api-key", TokenFor("claude-desktop"))
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		var out struct {
			Data []desktopRow `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		got := map[string]desktopRow{}
		for _, d := range out.Data {
			if d.Description != "" {
				t.Errorf("%s has a description: %q", d.DisplayName, d.Description)
			}
			got[DesktopCatalogID(d.ID)] = d
		}
		return got
	}
	keys := func(m map[string]desktopRow) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}

	// both DeepSeeks shown: each with its provider's after it; grok-4.6, a
	// name the user gave that already says its provider's, without it
	all := list()
	if all["opencode-go/deepseek-v4.1-flash"].DisplayName != "DeepSeek V4.1 Flash · OpenCode Go" ||
		all["relay/deepseek-v4.1-flash"].DisplayName != "DeepSeek V4.1 Flash · Relay" || all["grok-2/grok-4.6"].DisplayName != "grok-4.6" {
		t.Fatalf("both shown: %v", all)
	}

	// three picked, the other DeepSeek, the routing group magpie found for
	// the two and GLM left out: only those three, the DeepSeek still with
	// its provider after it
	hide := []string{"relay/deepseek-v4.1-flash", "opencode-go/glm-5"}
	for id := range all {
		if provider.IsGroupRef(id) {
			hide = append(hide, id)
		}
	}
	if err := provider.SetHiddenModels("claude-desktop", hide); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.SetHiddenModels("claude-desktop", nil) })
	got := list()
	if want := []string{"fake/m1", "grok-2/grok-4.6", "opencode-go/deepseek-v4.1-flash"}; !slices.Equal(keys(got), want) {
		t.Fatalf("Desktop lists %v, want %v", keys(got), want)
	}
	if n := got["opencode-go/deepseek-v4.1-flash"].DisplayName; n != "DeepSeek V4.1 Flash · OpenCode Go" {
		t.Fatalf("named %q, want the name with its provider after it", n)
	}
	// another agent's list is its own
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var other struct{ Data []struct{ ID string } }
	json.Unmarshal(rec.Body.Bytes(), &other)
	if len(other.Data) != len(all) {
		t.Fatalf("another agent's list: %s", rec.Body)
	}
}

// Claude Desktop's menu names a model as every agent's list does, by the
// Provider in model names setting (#1746, marsxxl): on, the name with its
// provider's after it, as Codex's list has it; own, a name the user gave
// as they wrote it and a vendor's with its provider's; off, plain names —
// and two of one name keep their provider's, two that still read the same
// (one provider's, named alike twice) the whole id.
func TestClaudeDesktopNamesFollowSuffixMode(t *testing.T) {
	fresh(t)
	for _, p := range []provider.Provider{
		{ID: "opencode-go", Name: "OpenCode Go", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4.1-flash"}},
		{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4.1-flash"}},
		{ID: "grok-2", Name: "Grok", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"grok-4.6"}},
		{ID: "kimi", Name: "Kimi", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"kimi-k3"}},
		{ID: "twin", Name: "Twinco", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x", "x2"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for ref, name := range map[string]string{
		"opencode-go/deepseek-v4.1-flash": "DeepSeek V4.1 Flash",
		"relay/deepseek-v4.1-flash":       "DeepSeek V4.1 Flash",
		"kimi/kimi-k3":                    "Nightly",
		"twin/x":                          "Twin",
		"twin/x2":                         "Twin",
	} {
		if err := provider.SetModelName(ref, name); err != nil {
			t.Fatal(err)
		}
	}
	list := func() map[string]string {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/models", nil)
		req.Header.Set("x-api-key", TokenFor("claude-desktop"))
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		var out struct {
			Data []desktopRow `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, d := range out.Data {
			got[DesktopCatalogID(d.ID)] = d.DisplayName
		}
		return got
	}
	for _, c := range []struct {
		mode string
		want map[string]string
	}{
		{provider.SuffixOn, map[string]string{
			"opencode-go/deepseek-v4.1-flash": "DeepSeek V4.1 Flash · OpenCode Go",
			"relay/deepseek-v4.1-flash":       "DeepSeek V4.1 Flash · Relay",
			"grok-2/grok-4.6":                 "grok-4.6 · Grok",
			"kimi/kimi-k3":                    "Nightly · Kimi",
			"twin/x":                          "Twin · Twinco (twin/x)",
			"twin/x2":                         "Twin · Twinco (twin/x2)",
		}},
		{provider.SuffixOwn, map[string]string{
			"opencode-go/deepseek-v4.1-flash": "DeepSeek V4.1 Flash · OpenCode Go",
			"relay/deepseek-v4.1-flash":       "DeepSeek V4.1 Flash · Relay",
			"grok-2/grok-4.6":                 "grok-4.6 · Grok",
			"kimi/kimi-k3":                    "Nightly",
			"twin/x":                          "Twin · Twinco (twin/x)",
			"twin/x2":                         "Twin · Twinco (twin/x2)",
		}},
		{provider.SuffixOff, map[string]string{
			"opencode-go/deepseek-v4.1-flash": "DeepSeek V4.1 Flash · OpenCode Go",
			"relay/deepseek-v4.1-flash":       "DeepSeek V4.1 Flash · Relay",
			"grok-2/grok-4.6":                 "grok-4.6",
			"kimi/kimi-k3":                    "Nightly",
			"twin/x":                          "Twin · Twinco (twin/x)",
			"twin/x2":                         "Twin · Twinco (twin/x2)",
		}},
	} {
		if err := provider.SetSuffixMode(c.mode); err != nil {
			t.Fatal(err)
		}
		got := list()
		for id, want := range c.want {
			if got[id] != want {
				t.Fatalf("%s: %s named %q, want %q (all: %v)", c.mode, id, got[id], want, got)
			}
		}
	}
}

// A Claude Desktop build that has no mythos word (the Store's 1.6608.2,
// sublatesublate-design) keeps a gateway model only when its id reads as a
// sonnet|opus|haiku tier or has claude, sonnet, opus, haiku or anthropic in
// it, so every reasoning-level model listed by mythos-magpie-<n> vanished
// from its menu. To a build whose User-Agent names no 2.x, a
// reasoning-level model is listed by its plain alias, which every build
// keeps; one that says 2.x keeps the picker alias.
func TestClaudeDesktopOldBuildModels(t *testing.T) {
	setup(t, provider.Anthropic, &fake{})
	if err := provider.Save(provider.Provider{ID: "opencode-go", Name: "OpenCode Go", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("opencode-go/deepseek-v4.1-flash", []string{"low", "medium", "high"}); err != nil {
		t.Fatal(err)
	}
	// an old build's own filter (its app.asar, 1.6608.2)
	oldBuildKeeps := func(id string) bool {
		l := strings.ToLower(id)
		if regexp.MustCompile(`^(sonnet|opus|haiku)(-[\d.]+)?$`).MatchString(l) {
			return true
		}
		for _, w := range []string{"claude", "sonnet", "opus", "haiku", "anthropic"} {
			if strings.Contains(l, w) {
				return true
			}
		}
		return false
	}
	list := func(ua string) map[string]string {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/models", nil)
		req.Header.Set("x-api-key", TokenFor("claude-desktop"))
		if ua != "" {
			req.Header.Set("User-Agent", ua)
		}
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		var out struct {
			Data []desktopRow `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, d := range out.Data {
			got[DesktopCatalogID(d.ID)] = d.ID
		}
		return got
	}
	const oldUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Claude/1.6608.2 Chrome/140.0.0.0 Electron/37.2.0 Safari/537.36"

	// the old build keeps every model it is shown
	for _, ua := range []string{oldUA} {
		for catalog, id := range list(ua) {
			if !oldBuildKeeps(id) {
				t.Errorf("User-Agent %q: %s is listed as %s, which the build drops", ua, catalog, id)
			}
		}
	}

	// a 2.x build is still offered the effort picker, and so is a request
	// that names no build (the Code tab's Claude Code, known by its key)
	for _, ua := range []string{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Claude/2.7032.0 Chrome/140.0.0.0 Electron/37.2.0 Safari/537.36", "claude-cli/2.1.295 (external, claude-desktop)", ""} {
		if id := list(ua)["opencode-go/deepseek-v4.1-flash"]; !regexp.MustCompile(`^mythos-magpie-\d{10}$`).MatchString(id) {
			t.Errorf("User-Agent %q: deepseek-v4.1-flash is listed as %q, want the mythos picker alias", ua, id)
		}
	}
}

// desktopKnowsMythos, by the version a User-Agent names: 2.x takes the
// mythos effort alias, an older one does not, and a request naming no
// build is listed as before.
func TestDesktopKnowsMythos(t *testing.T) {
	for _, c := range []struct {
		ua   string
		want bool
	}{
		{"Mozilla/5.0 … Claude/2.7032.0 Chrome/… Safari/537.36", true},
		{"Mozilla/5.0 … Claude/2 Chrome/…", true},
		{"Mozilla/5.0 … Claude/1.6608.2 Chrome/… Safari/537.36", false},
		{"Mozilla/5.0 … Claude/3.0.0 Chrome/… Safari/537.36", true},
		// no build named (the Code tab's Claude Code, known by its key):
		// listed as before, by the picker alias
		{"claude-cli/2.1.295 (external, claude-desktop)", true},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36", true},
		{"", true},
	} {
		if got := desktopKnowsMythos(c.ua); got != c.want {
			t.Errorf("desktopKnowsMythos(%q) = %v, want %v", c.ua, got, c.want)
		}
	}
}

// desktopNames: the agents' list naming (provider.Labels, the Provider in
// model names setting) with the whole catalog id after two entries that
// still read the same — Desktop's menu shows the name alone, so two models
// of one provider named alike stay apart there.
func TestDesktopNames(t *testing.T) {
	fresh(t)
	es := []provider.Entry{
		{ID: "a/x", Name: "X", Provider: &provider.Provider{ID: "a", Name: "A"}},
		{ID: "b/x", Name: "x", Provider: &provider.Provider{ID: "b", Name: "B"}},
		{ID: "c/y", Name: "Y", Provider: &provider.Provider{ID: "c", Name: "C"}},
		{ID: "d/z", Name: "Z", Provider: &provider.Provider{ID: "d", Name: "D"}},
		{ID: "d/z2", Name: "Z", Provider: &provider.Provider{ID: "d", Name: "D"}},
	}
	if got, want := desktopNames("claude-desktop", es), []string{"X · A", "x · B", "Y · C", "Z · D (d/z)", "Z · D (d/z2)"}; !slices.Equal(got, want) {
		t.Fatalf("desktopNames = %q, want %q", got, want)
	}
	if err := provider.SetSuffixMode(provider.SuffixOff); err != nil {
		t.Fatal(err)
	}
	if got, want := desktopNames("claude-desktop", es), []string{"X · A", "x · B", "Y", "Z · D (d/z)", "Z · D (d/z2)"}; !slices.Equal(got, want) {
		t.Fatalf("desktopNames (names plain) = %q, want %q", got, want)
	}
}

// Claude Desktop names a Claude model with reasoning levels after its own
// catalog ("Opus 5.5"), not by display_name, so four relays' claude-opus-5-5
// read alike and its menu told them apart only by the id's tail,
// "Opus 5.5 (…7922287072.anthropic.claude-opus-5-5)" (4 v5 on Discord:
// 老林夕, 新林夕, 百德 and chatflowsai, Provider in model names on). Such
// a row says its provider in the description Desktop shows under the
// name, by the same setting the name follows: on, every one; off, only
// two of one name, as display_name keeps their provider then. Its id stays
// as it was, and a row Desktop names by display_name says nothing more.
func TestClaudeDesktopCatalogNamedSaysProvider(t *testing.T) {
	setup(t, provider.Anthropic, &fake{})
	levels := []string{"low", "medium", "high", "max"}
	for _, p := range []provider.Provider{
		{ID: "laolin", Name: "老林夕", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-opus-5-5"}},
		{ID: "xinlin", Name: "新林夕", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-opus-5-5"}},
		{ID: "baide", Name: "百德", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-opus-5-5"}},
		{ID: "chatflowsai", Name: "chatflowsai", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-opus-5-5", "claude-sonnet-5-5", "glm-5.3"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		for _, m := range p.Models {
			if err := provider.SetModelEfforts(p.ID+"/"+m, levels); err != nil {
				t.Fatal(err)
			}
		}
	}
	list := func() map[string]desktopRow {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/models?limit=1000", nil)
		req.Header.Set("x-api-key", TokenFor("claude-desktop"))
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		var out struct {
			Data []desktopRow `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		got := map[string]desktopRow{}
		for _, d := range out.Data {
			got[DesktopCatalogID(strings.TrimSuffix(d.ID, "[1m]"))] = d
		}
		return got
	}
	opus := map[string]string{"laolin/claude-opus-5-5": "老林夕", "xinlin/claude-opus-5-5": "新林夕", "baide/claude-opus-5-5": "百德", "chatflowsai/claude-opus-5-5": "chatflowsai"}
	ids := map[string]string{}
	for _, c := range []struct {
		mode   string
		sonnet string // what the lone Sonnet 5.5 says
	}{
		{provider.SuffixOn, "chatflowsai"},
		{provider.SuffixOwn, "chatflowsai"},
		{provider.SuffixOff, ""},
	} {
		if err := provider.SetSuffixMode(c.mode); err != nil {
			t.Fatal(err)
		}
		got := list()
		for ref, by := range opus {
			r := got[ref]
			if !regexp.MustCompile(`^magpie-\d{10}\.anthropic\.claude-opus-5-5$`).MatchString(r.ID) {
				t.Fatalf("%s: %s listed as %q, not one Desktop names after its catalog", c.mode, ref, r.ID)
			}
			if r.Description != by {
				t.Errorf("%s: %s described %q, want %q", c.mode, ref, r.Description, by)
			}
			if ids[ref] == "" {
				ids[ref] = r.ID
			} else if ids[ref] != r.ID {
				t.Errorf("%s: %s's id moved from %s to %s", c.mode, ref, ids[ref], r.ID)
			}
		}
		if r := got["chatflowsai/claude-sonnet-5-5"]; r.Description != c.sonnet {
			t.Errorf("%s: the lone Sonnet 5.5 described %q, want %q", c.mode, r.Description, c.sonnet)
		}
		// named by display_name (mythos-magpie-<n>), which says it already
		if r := got["chatflowsai/glm-5.3"]; r.ID == "" || r.Description != "" {
			t.Errorf("%s: glm-5.3 (%s) described %q, want nothing", c.mode, r.ID, r.Description)
		}
	}

	// a 1M row listed by its "[1m]" id says both
	if err := provider.SetSuffixMode(provider.SuffixOn); err != nil {
		t.Fatal(err)
	}
	now, err := provider.Find("baide")
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.SetContext(*now, "claude-opus-5-5", 1_000_000); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.DesktopLongest = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	r := list()["baide/claude-opus-5-5"]
	if r.ID != ids["baide/claude-opus-5-5"]+"[1m]" || r.Description != "1M context window · 百德" {
		t.Errorf("1M: listed as %s, described %q", r.ID, r.Description)
	}
}
