package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// desktopPicker is Claude Desktop's tIt (index.chunk-D3OyLXgG.js, 2.7032):
// whether the model id it was given gets a thinking-effort picker. IC is
// qS of the lowercased id; HFt's keys that have effort levels, and UFt.
func desktopPicker(id string) bool {
	e := strings.ToLower(id)
	t := regexp.MustCompile(`^arn:aws[a-z-]*:bedrock:[^/]+/`).ReplaceAllString(e, "")
	t = regexp.MustCompile(`^(?:[a-z][a-z0-9-]*\.)?anthropic\.`).ReplaceAllString(t, "")
	stripped := t != e || regexp.MustCompile(`^claude-(?:[a-z]+-)?\d`).MatchString(t)
	t = regexp.MustCompile(`\[[^\]]+\]$`).ReplaceAllString(t, "")
	if stripped {
		t = regexp.MustCompile(`-v\d+(?::\d+)?$`).ReplaceAllString(t, "")
	}
	t = regexp.MustCompile(`@\d{8}$`).ReplaceAllString(t, "")
	t = regexp.MustCompile(`-\d{8}$`).ReplaceAllString(t, "")
	switch t {
	case "claude-sonnet-4-6", "claude-sonnet-5", "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5":
		return true
	}
	return regexp.MustCompile(`^(?:claude-)?(?:fable|mythos)(?:-|$)`).MatchString(t)
}

// Desktop's small_fast pick (_$n): an id with haiku, sonnet or opus in it
func desktopSmallFast(id string) bool {
	for _, w := range []string{"haiku", "sonnet", "opus"} {
		if strings.Contains(id, w) {
			return true
		}
	}
	return false
}

// a model with reasoning levels is listed to Claude Desktop by an id it
// offers its effort picker for (ARNO: 接入claude desktop后无法设置思考强度):
// a Claude model by magpie-<n>.anthropic.<model>, any other by
// mythos-magpie-<n>, and so a routing group whatever model leads it (else
// Desktop names it from its catalog, "Opus 5.5", not by the group's name);
// one without levels as before. Each is kept by Desktop, served again by
// magpie, with or without [1m], and none but a Claude model becomes
// Desktop's small_fast pick.
func TestClaudeDesktopEffortIDs(t *testing.T) {
	levels := []string{"low", "medium", "high"}
	for _, c := range []struct {
		e      provider.Entry
		want   string // the id's form
		picker bool
	}{
		{provider.Entry{ID: "zai/glm-5.3", Model: "glm-5.3", Efforts: levels}, `^mythos-magpie-\d{10}$`, true},
		{provider.Entry{ID: "x/claude-sonnet-5-thinking", Model: "claude-sonnet-5-thinking", Efforts: levels}, `^mythos-magpie-\d{10}$`, true},
		{provider.Entry{ID: "or/anthropic/claude-opus-4-8", Model: "anthropic/claude-opus-4-8", Efforts: levels}, `^magpie-\d{10}\.anthropic\.claude-opus-4-8$`, true},
		{provider.Entry{ID: "x/Claude-Sonnet-4-6-20260101", Model: "Claude-Sonnet-4-6-20260101", Efforts: levels}, `^magpie-\d{10}\.anthropic\.claude-sonnet-4-6$`, true},
		{provider.Entry{ID: "x/claude-haiku-4-5", Model: "claude-haiku-4-5", Efforts: levels}, `^magpie-\d{10}\.anthropic\.claude-haiku-4-5$`, false},
		{provider.Entry{ID: "group/coding", Model: "claude-opus-5-5", Group: "coding", Efforts: levels}, `^mythos-magpie-\d{10}$`, true},
		{provider.Entry{ID: "zai/glm-4.5-air", Model: "glm-4.5-air"}, `^anthropic/magpie-\d{10}$`, false},
		{provider.Entry{ID: "x/claude-opus-4-8", Model: "claude-opus-4-8"}, `^x/claude-opus-4-8$`, false},
	} {
		id := claudeLooking(c.e)
		if !regexp.MustCompile(c.want).MatchString(id) {
			t.Errorf("%s listed as %s, want %s", c.e.ID, id, c.want)
		}
		if !desktopAccepts(id) {
			t.Errorf("%s: Desktop would drop %s", c.e.ID, id)
		}
		if desktopPicker(id) != c.picker {
			t.Errorf("%s as %s: picker %v", c.e.ID, id, !c.picker)
		}
		if strings.HasPrefix(id, desktopEffortAlias) && desktopSmallFast(id) {
			t.Errorf("%s: %s would be Desktop's small_fast model", c.e.ID, id)
		}
	}
	if desktopPicker("x/claude-opus-4-8") || desktopPicker(aliasFor("zai/glm-5.3")) {
		t.Fatal("the ids listed before had a picker")
	}

	setup(t, provider.Anthropic, &fake{})
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: "http://127.0.0.1:1",
		Models: []string{"m1", "claude-opus-4-8", "m2"}}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"fake/m1", "fake/claude-opus-4-8"} {
		if err := provider.SetModelEfforts(ref, []string{"low", "high"}); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("x-api-key", Token+"-claude-desktop")
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, req)
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range list.Data {
		got[DesktopCatalogID(m.ID)] = m.ID
	}
	for id, want := range map[string]string{
		"fake/m1":              desktopEffortAlias + aliasNumber("fake/m1"),
		"fake/claude-opus-4-8": "magpie-" + aliasNumber("fake/claude-opus-4-8") + ".anthropic.claude-opus-4-8",
		"fake/m2":              aliasFor("fake/m2"),
	} {
		if got[id] != want {
			t.Errorf("%s listed as %q, want %q (%s)", id, got[id], want, rec.Body)
		}
		for _, asked := range []string{want, want + "[1m]"} {
			if real, ok := aliased(asked); !ok || real != id {
				t.Errorf("aliased(%s) = %q %v", asked, real, ok)
			}
		}
	}
	for _, bad := range []string{"mythos-magpie-0000000000", "magpie-0000000000.anthropic.claude-opus-4-8", "magpie-12.anthropic.claude-opus-4-8", "mythos-5"} {
		if _, ok := aliased(bad); ok {
			t.Errorf("aliased(%s) resolved", bad)
		}
	}
}

// the effort picked in Claude Desktop reaches the model, fitted to its
// levels: Desktop's Claude Code sends thinking plus output_config.effort
func TestClaudeDesktopEffortReachesModel(t *testing.T) {
	f := &fake{reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	if err := provider.SetModelEfforts("fake/m1", []string{"low", "high"}); err != nil {
		t.Fatal(err)
	}
	alias := desktopEffortAlias + aliasNumber("fake/m1")
	for _, c := range []struct{ asked, sent string }{
		{"low", "low"}, {"high", "high"}, {"max", "high"}, {"xhigh", "high"},
	} {
		body := `{"model":"` + alias + `","max_tokens":32000,"stream":true,"thinking":{"type":"adaptive"},"output_config":{"effort":"` + c.asked + `"},` +
			`"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", Token+"-claude-desktop")
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.asked, rec.Code, rec.Body)
		}
		var sent struct {
			Model        string `json:"model"`
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		if err := json.Unmarshal(f.got, &sent); err != nil {
			t.Fatal(err)
		}
		if sent.Model != "m1" || sent.OutputConfig.Effort != c.sent {
			t.Errorf("%s: sent %s at %q, want m1 at %q", c.asked, sent.Model, sent.OutputConfig.Effort, c.sent)
		}
	}
}
