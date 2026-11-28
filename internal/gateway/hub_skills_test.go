package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
)

// skillsHub is fakeHub with the Library's skills for another computer.
type skillsHub struct {
	*fakeHub
	skills map[string][]RemoteSkill // by agent
	files  map[string]RemoteSkillFiles
	err    error // RemoteSkills's: a Library it couldn't read
}

func (f *skillsHub) RemoteSkills(agent string) ([]RemoteSkill, error) { return f.skills[agent], f.err }

func (f *skillsHub) RemoteSkill(agent, name string) (RemoteSkill, RemoteSkillFiles, bool) {
	for _, k := range f.skills[agent] {
		if k.Name == name {
			return k, f.files[name], true
		}
	}
	return RemoteSkill{}, RemoteSkillFiles{}, false
}

func newSkillsHub() *skillsHub {
	deploy := RemoteSkill{Name: "deploy", About: "Ships the site to production"}
	notes := RemoteSkill{Name: "release-notes", About: "Writes the release notes from the commits"}
	manual := RemoteSkill{Name: "wipe", About: "Wipes the cache", Manual: true}
	return &skillsHub{
		fakeHub: &fakeHub{on: true, tools: map[string][]HubTool{"": {{Server: "notes", Tool: "find", Spec: hubSpec("find")}}}},
		skills: map[string][]RemoteSkill{
			"":       {deploy, notes, manual},
			"claude": {deploy},
		},
		files: map[string]RemoteSkillFiles{
			"deploy": {Files: map[string][]byte{
				"SKILL.md":        []byte("---\nname: deploy\ndescription: Ships the site to production\n---\nRun scripts/ship.sh.\n"),
				"scripts/ship.sh": []byte("#!/bin/sh\necho shipped\n"),
				"assets/logo.png": {0x89, 'P', 'N', 'G', 0, 0},
				"docs/big.txt":    []byte(strings.Repeat("a", hubSkillFileMax+500)),
			}, Exec: []string{"scripts/ship.sh"}, Left: []string{"model.bin"}},
		},
	}
}

// hubSkillsReply is a hub reply's instructions, tools and text.
type hubSkillsReply struct {
	Result struct {
		Instructions string
		Tools        []struct {
			Name string
			Meta map[string]any `json:"_meta"`
		}
		Content []struct{ Text string }
		IsError bool
	}
	Error *struct{ Message string }
}

func readHubSkills(t *testing.T, w *httptest.ResponseRecorder) hubSkillsReply {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	var m hubSkillsReply
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("%v: %s", err, w.Body)
	}
	return m
}

const skillsInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`

func (m hubSkillsReply) lists(name string) bool {
	return slices.ContainsFunc(m.Result.Tools, func(t struct {
		Name string
		Meta map[string]any `json:"_meta"`
	}) bool {
		return t.Name == name
	})
}

func (m hubSkillsReply) text() string {
	var b strings.Builder
	for _, c := range m.Result.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// An agent on another computer with no magpie (Michaelfpqc on X: a
// magpie in Docker serving it models and its router) is given the
// Library's skills by the router: its instructions name those its model
// may start, get_skill is listed with the agent's own tools (not behind
// its tool search), and gives the list, a skill's SKILL.md with its other
// files named, and each file: text whole up to a bound, not a binary one,
// no path outside the skill's files. At /hub/<agent> it is the agent's
// skills.
func TestHubGivesSkillsToAnotherComputer(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	f := newSkillsHub()
	withHub(t, f)
	h := lanGuard(New().Handler())
	_, secrets := newCaller(t, "Laptop")
	key := []string{"Authorization", "Bearer " + secrets[0]}
	const there = "172.17.0.1:40000"

	init := readHubSkills(t, hubPostAt(h, HubMCPAll, there, skillsInit, key...))
	say := init.Result.Instructions
	if !strings.Contains(say, "get_skill") || !strings.Contains(say, "deploy (Ships the site to production)") || !strings.Contains(say, "release-notes") {
		t.Fatalf("instructions name no skill: %q", say)
	}
	if strings.Contains(say, "wipe") {
		t.Fatalf("a skill the model doesn't start itself is in the instructions: %q", say)
	}
	if n := runeLen(say); n > hubSayMax+hubSkillsSayMax+1 {
		t.Fatalf("instructions are %d characters", n)
	}
	list := readHubSkills(t, hubPostAt(h, HubMCPAll, there, toolsList, key...))
	i := slices.IndexFunc(list.Result.Tools, func(t struct {
		Name string
		Meta map[string]any `json:"_meta"`
	}) bool {
		return t.Name == hubSkillTool
	})
	if i < 0 || list.Result.Tools[i].Meta[hubAlwaysClaude] != true {
		t.Fatalf("get_skill isn't listed, always loaded: %+v", list.Result.Tools)
	}
	if !list.lists("notes__find") {
		t.Fatalf("the agent's own tools went: %+v", list.Result.Tools)
	}

	get := func(path, args string) hubSkillsReply {
		t.Helper()
		return readHubSkills(t, hubPostAt(h, path, there, toolsCall(hubSkillTool, args), key...))
	}
	if m := get(HubMCPAll, `{}`); m.Result.IsError || !strings.Contains(m.text(), "- deploy: Ships the site") || !strings.Contains(m.text(), "- wipe: Wipes the cache (used only when the user asks for it)") {
		t.Fatalf("the list: %+v", m)
	}
	m := get(HubMCPAll, `{"name":"deploy"}`)
	if txt := m.text(); m.Result.IsError || !strings.Contains(txt, "Run scripts/ship.sh.") || !strings.Contains(txt, "scripts/ship.sh (23 bytes, runs)") || !strings.Contains(txt, "Too big to give: model.bin") || strings.Contains(txt, "SKILL.md (") {
		t.Fatalf("SKILL.md: %+v", m)
	}
	if m := get(HubMCPAll, `{"name":"deploy","file":"./scripts/ship.sh"}`); m.Result.IsError || m.text() != "#!/bin/sh\necho shipped\n" {
		t.Fatalf("a file: %+v", m)
	}
	if m := get(HubMCPAll, `{"name":"deploy","file":"docs/big.txt"}`); m.Result.IsError || !strings.Contains(m.text(), "only the first 100000") {
		t.Fatalf("a big file: %d %+v", len(m.text()), m.Result.IsError)
	}
	for file, want := range map[string]string{
		"assets/logo.png":     "is not text",
		"model.bin":           "too big",
		"../../etc/passwd":    "has no file etc/passwd",
		"/etc/passwd":         "has no file etc/passwd",
		"..\\..\\etc\\passwd": "has no file etc/passwd",
	} {
		if m := get(HubMCPAll, fmt.Sprintf(`{"name":"deploy","file":%q}`, file)); !m.Result.IsError || !strings.Contains(m.text(), want) {
			t.Errorf("file %s: %+v", file, m)
		}
	}
	if m := get(HubMCPAll, `{"name":"deplo"}`); !m.Result.IsError || !strings.Contains(m.text(), "close to it: deploy") {
		t.Fatalf("a near name: %+v", m)
	}
	// the agent's own address: its skills
	if m := get(HubMCPPath+"claude", `{"name":"release-notes"}`); !m.Result.IsError {
		t.Fatalf("claude given a skill it isn't: %+v", m)
	}
	if m := get(HubMCPPath+"claude", `{"name":"deploy"}`); m.Result.IsError {
		t.Fatalf("claude's skill: %+v", m)
	}
	// an agent given no skill isn't listed the tool
	if m := readHubSkills(t, hubPost(h, "codex", there, toolsList, key...)); m.lists(hubSkillTool) {
		t.Fatalf("listed get_skill with no skill to give: %+v", m.Result.Tools)
	}
	// a key for MCP servers only is held to no model: given them
	keys, _ := access.List()
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{access.NoModels}}); err != nil {
		t.Fatal(err)
	}
	if m := get(HubMCPAll, `{"name":"deploy"}`); m.Result.IsError {
		t.Fatalf("a key for MCP only: %+v", m)
	}
}

// The skills go to an agent on another computer with its gateway key, and
// nothing else: not without a key (no Origin, a null one, a foreign site,
// a rebound Host, through a proxy here), not to a web page with the key,
// another magpie's router (it borrows the Library as a whole), an agent
// on this computer (the Library links them into its folder), a key held
// to models, a budget or some MCP servers, or with the router off.
func TestHubSkillsOnlyWithAKeyFromThere(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	f := newSkillsHub()
	withHub(t, f)
	h := lanGuard(New().Handler())
	call := toolsCall(hubSkillTool, `{"name":"deploy"}`)
	const there = "192.0.2.7:4000"
	leaked := func(w *httptest.ResponseRecorder) bool {
		return strings.Contains(w.Body.String(), "Run scripts/ship.sh") || strings.Contains(w.Body.String(), "Ships the site")
	}
	refused := func(what string, w *httptest.ResponseRecorder, want ...int) {
		t.Helper()
		if leaked(w) || !slices.Contains(want, w.Code) {
			t.Errorf("%s: %d %s, want %v", what, w.Code, w.Body, want)
		}
	}
	refused("not shared", hubPostAt(h, HubMCPAll, there, call), http.StatusForbidden)
	keys, secrets := newCaller(t, "Laptop", "Held", "Narrow")
	key := []string{"Authorization", "Bearer " + secrets[0]}
	for _, body := range []string{call, skillsInit, toolsList} {
		refused("no key, no Origin", hubPostAt(h, HubMCPAll, there, body), http.StatusUnauthorized)
		refused("a wrong key", hubPostAt(h, HubMCPAll, there, body, "Authorization", "Bearer sk-magpie-wrong"), http.StatusUnauthorized)
		for _, origin := range []string{"null", "https://evil.example", "chrome-extension://abcdefghijklmnop", "file://"} {
			hdr := []string{"Origin", origin, "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors"}
			refused("no key, Origin "+origin, hubPostAt(h, HubMCPAll, there, body, hdr...), http.StatusUnauthorized, http.StatusForbidden)
			refused("Origin "+origin+" from here", hubPostAt(h, HubMCPAll, here, body, hdr...), http.StatusForbidden)
		}
		for _, addr := range []string{here, there} {
			for _, marks := range [][]string{
				{"Origin", "http://rebind.attacker.example:3425", "Sec-Fetch-Site", "same-origin"},
				{"Sec-Fetch-Site", "same-origin"},
				{"User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 Safari/605.1.15"},
				{},
			} {
				r := httptest.NewRequest(http.MethodPost, HubMCPAll, strings.NewReader(body))
				r.RemoteAddr, r.Host = addr, "rebind.attacker.example:3425"
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Accept", "application/json, text/event-stream")
				for i := 0; i+1 < len(marks); i += 2 {
					r.Header.Set(marks[i], marks[i+1])
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				// with none of a browser's marks, from here, it is an agent
				// here calling by a name of its own: let by, given no skill
				if leaked(rec) {
					t.Errorf("a rebound Host from %s %v: %d %s", addr, marks, rec.Code, rec.Body)
				}
			}
		}
		refused("through a proxy here, no key", hubPostAt(h, HubMCPAll, here, body, "X-Forwarded-For", "203.0.113.9"), http.StatusUnauthorized, http.StatusForbidden)
	}
	// these reach the router, and are given no skill
	notGiven := func(what string, addr string, hdr ...string) {
		t.Helper()
		for _, body := range []string{call, skillsInit, toolsList} {
			w := hubPostAt(h, HubMCPAll, addr, body, hdr...)
			if leaked(w) || strings.Contains(w.Body.String(), `"name":"get_skill"`) {
				t.Errorf("%s: given skills: %d %s", what, w.Code, w.Body)
			}
		}
	}
	notGiven("an agent here", here)
	notGiven("an agent here with a key", here, key...)
	notGiven("a page with the key", there, append([]string{"Origin", "http://192.0.2.7:3430", "Sec-Fetch-Site", "same-origin"}, key...)...)
	notGiven("a browser's agent with the key", there, append([]string{"User-Agent", "Mozilla/5.0"}, key...)...)
	notGiven("another magpie's router", there, append([]string{RouterHeader, "1"}, key...)...)
	if _, err := access.Update("models-key", access.Change{Key: keys[1].ID, Models: []string{"openai/gpt-5"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("mcp-key", access.Change{Key: keys[2].ID, MCP: []string{"notes"}}); err != nil {
		t.Fatal(err)
	}
	for i, who := range []string{"a key held to models", "a key held to some MCP servers"} {
		k := []string{"Authorization", "Bearer " + secrets[i+1]}
		notGiven(who, there, k...)
		if m := readHubSkills(t, hubPostAt(h, HubMCPAll, there, call, k...)); !m.Result.IsError || !strings.Contains(m.text(), "is held to") {
			t.Errorf("%s isn't told why: %+v", who, m)
		}
		if m := readHubSkills(t, hubPostAt(h, HubMCPAll, there, skillsInit, k...)); !strings.Contains(m.Result.Instructions, hubSkillsHeld) {
			t.Errorf("%s isn't told why in the instructions: %q", who, m.Result.Instructions)
		}
	}
	// the router off: none
	f.mu.Lock()
	f.on = false
	f.mu.Unlock()
	notGiven("the router off", there, key...)
	// and on, the key from there is given them (the test's control)
	f.mu.Lock()
	f.on = true
	f.mu.Unlock()
	if !leaked(hubPostAt(h, HubMCPAll, there, call, key...)) {
		t.Fatal("not given with its key from there")
	}
	if !leaked(hubPostAt(h, HubMCPAll, here, call, "X-Forwarded-For", "203.0.113.9", "Authorization", "Bearer "+secrets[0])) {
		t.Fatal("not given with its key through a proxy here")
	}
}
