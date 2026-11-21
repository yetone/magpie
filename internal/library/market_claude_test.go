package library

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/source"
)

// claudeForget empties the market's memory, before the test and after it.
func claudeForget(t *testing.T) {
	forget := func() {
		claudeMarket.Lock()
		claudeMarket.list, claudeMarket.at = nil, time.Time{}
		claudeMarket.Unlock()
	}
	forget()
	t.Cleanup(forget)
}

func claudeByName(list []ClaudeMarketPlugin) map[string]ClaudeMarketPlugin {
	m := map[string]ClaudeMarketPlugin{}
	for _, p := range list {
		m[p.Name] = p
	}
	return m
}

// The shipped list is anthropics/claude-plugins-official's
// marketplace.json as it was (blob 49691e49, 317 plugins), and reads into
// what the library's install can fetch.
func TestClaudeMarketReadsTheOfficialMarketplace(t *testing.T) {
	list, err := parseClaudeMarket(claudeSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	// 317 less Xcode's (a command to run) and twelve language servers
	if len(list) != 304 {
		t.Fatalf("%d plugins, want 304", len(list))
	}
	by := claudeByName(list)
	for _, gone := range []string{"xcode", "gopls-lsp", "pyright-lsp"} {
		if _, ok := by[gone]; ok {
			t.Errorf("%s listed: the library can't install it", gone)
		}
	}
	// a plugin in the marketplace's own folder
	sdk := by["agent-sdk-dev"]
	if sdk.Author != "Anthropic" || sdk.Category != "development" || sdk.Marketplace != "claude-plugins-official" ||
		sdk.Source != "anthropics/claude-plugins-official" ||
		sdk.Repo != "https://github.com/anthropics/claude-plugins-official/tree/main/plugins/agent-sdk-dev" ||
		sdk.Icon != "https://github.com/anthropics.png?size=96" || sdk.Description == "" {
		t.Errorf("agent-sdk-dev = %+v", sdk)
	}
	// one in a repository of its own
	if f := by["figma"]; f.Repo != "https://github.com/figma/mcp-server-guide" || f.Icon != "https://github.com/figma.png?size=96" || f.Source != "anthropics/claude-plugins-official" {
		t.Errorf("figma = %+v", f)
	}
	// one in a folder of another repository, at its ref
	if c := by["42crunch-api-security-testing"]; c.Repo != "https://github.com/42Crunch-AI/claude-plugins/tree/v1.5.5/plugins/api-security-testing" {
		t.Errorf("42crunch = %+v", c)
	}
	// the name it shows
	if by["airwallex-agentos"].Title != "Airwallex" {
		t.Errorf("airwallex-agentos's title = %q", by["airwallex-agentos"].Title)
	}
	// a name over 40 is listed by the name the install keeps it by
	if o := by["oracle-ai-data-platform-workbench-databr"]; o.Title != "oracle-ai-data-platform-workbench-databricks-migrator" {
		t.Errorf("a long name: %+v", o)
	}
}

func TestClaudeMarketLeavesOutWhatItCantFetch(t *testing.T) {
	b := []byte(`{"name":"m","plugins":[
		{"name":"ok","source":"./plugins/ok","description":"  two\n words  ","tags":["a"],"keywords":["a","b"]},
		{"name":"out","source":"../elsewhere"},
		{"name":"cmd","source":{"source":"command","command":"x"}},
		{"name":"npm","source":{"source":"npm","package":"x"}},
		{"name":"lsp","source":"./plugins/lsp","lspServers":{}},
		{"name":"bad name","source":"./plugins/x"},
		{"name":"ok","source":"./plugins/again"},
		{"name":"site","source":{"source":"url","url":"https://gitlab.com/o/r.git"},"homepage":"https://github.com/someone"},
		{"name":"plain","source":"./p","homepage":"http://x.dev"}]}`)
	list, err := parseClaudeMarket(b)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range list {
		names = append(names, p.Name)
	}
	if got := strings.Join(names, " "); got != "ok bad-name site plain" {
		t.Fatalf("listed %q", got)
	}
	by := claudeByName(list)
	if ok := by["ok"]; ok.Description != "two words" || strings.Join(ok.Tags, ",") != "a,b" || ok.Marketplace != "m" {
		t.Errorf("ok = %+v", ok)
	}
	// no GitHub page of its code: its owner's picture from its site
	if s := by["site"]; s.Repo != "" || s.Icon != "https://github.com/someone.png?size=96" {
		t.Errorf("site = %+v", s)
	}
	if by["bad-name"].Title != "bad name" {
		t.Errorf("bad name = %+v", by["bad-name"])
	}
	if by["plain"].Homepage != "" {
		t.Errorf("an http homepage kept: %+v", by["plain"])
	}
	for _, bad := range []string{``, `{}`, `{"name":"m","plugins":[]}`, `{"plugins":[{"name":"a","source":"./a"}]}`} {
		if _, err := parseClaudeMarket([]byte(bad)); err == nil {
			t.Errorf("%q read without an error", bad)
		}
	}
}

// The list is GitHub's when it answers, kept six hours and on disk; when
// it doesn't, the last one on disk, else the shipped one.
func TestClaudeMarketRefreshes(t *testing.T) {
	claudeForget(t)
	piCacheHere(t)
	fresh := bytes.Replace(claudeSnapshot, []byte(`"name": "agent-sdk-dev"`), []byte(`"name": "agent-sdk-dev-2"`), 1)
	if bytes.Equal(fresh, claudeSnapshot) {
		t.Fatal("the fixture's agent-sdk-dev entry wasn't found")
	}
	var down atomic.Bool
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if down.Load() {
			http.Error(w, "no", 500)
			return
		}
		w.Write(fresh)
	}))
	defer srv.Close()
	old := claudeMarketURL
	claudeMarketURL = srv.URL + "/marketplace.json"
	t.Cleanup(func() { claudeMarketURL = old })

	has := func(list []ClaudeMarketPlugin, name string) bool { _, ok := claudeByName(list)[name]; return ok }
	list, err := MarketClaudePlugins("")
	if err != nil || !has(list, "agent-sdk-dev-2") {
		t.Fatalf("fresh list: %d plugins, %v", len(list), err)
	}
	if b, _ := os.ReadFile(marketCache("claude-plugins.json")); !bytes.Equal(b, fresh) {
		t.Fatal("the fresh list wasn't kept on disk")
	}
	// within six hours, GitHub isn't asked again
	n := asked.Load()
	if _, err := MarketClaudePlugins("figma"); err != nil || asked.Load() != n {
		t.Fatalf("asked again within six hours (%v)", err)
	}

	// GitHub down: the list on disk
	down.Store(true)
	claudeForget(t)
	list, err = MarketClaudePlugins("")
	if err != nil || !has(list, "agent-sdk-dev-2") {
		t.Fatalf("GitHub down: %d plugins, %v, want the list on disk", len(list), err)
	}
	// and it's asked again a while later, not on every search
	n = asked.Load()
	MarketClaudePlugins("x")
	if asked.Load() != n {
		t.Fatal("GitHub asked again at once after failing")
	}

	// nothing on disk either: the shipped list
	claudeForget(t)
	os.Remove(marketCache("claude-plugins.json"))
	list, err = MarketClaudePlugins("")
	if err != nil || !has(list, "agent-sdk-dev") || has(list, "agent-sdk-dev-2") || len(list) != 304 {
		t.Fatalf("nothing on disk: %d plugins, %v, want the shipped list", len(list), err)
	}

	// a list on disk that doesn't read: the shipped one
	claudeForget(t)
	os.WriteFile(marketCache("claude-plugins.json"), []byte("{half"), 0o644)
	if list, err = MarketClaudePlugins(""); err != nil || len(list) != 304 {
		t.Fatalf("a half-written list on disk: %d plugins, %v", len(list), err)
	}
}

// With the 「国内镜像」 switch, jsDelivr's copy of the very file is asked
// first, and GitHub's after it when jsDelivr fails.
func TestClaudeMarketAsksTheMirrorFirst(t *testing.T) {
	if got := source.ChinaFirst(claudeMarketURL); got != "" {
		t.Fatalf("switch off: mirror %q", got)
	}
	was := source.China
	source.China = func() bool { return true }
	t.Cleanup(func() { source.China = was })
	if got := source.ChinaFirst(claudeMarketURL); got != "https://cdn.jsdelivr.net/gh/anthropics/claude-plugins-official@main/.claude-plugin/marketplace.json" {
		t.Fatalf("switch on: mirror %q", got)
	}
}

func TestClaudeMarketSearch(t *testing.T) {
	claudeForget(t)
	piCacheHere(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(claudeSnapshot) }))
	defer srv.Close()
	old := claudeMarketURL
	claudeMarketURL = srv.URL
	t.Cleanup(func() { claudeMarketURL = old })
	for q, want := range map[string]string{
		"FIGMA":      "figma",             // its name, any case
		"Airwallex":  "airwallex-agentos", // the name it shows
		"database":   "",                  // a category: several
		"zzz-nobody": "-",
	} {
		list, err := MarketClaudePlugins(q)
		if err != nil {
			t.Fatal(err)
		}
		switch want {
		case "-":
			if len(list) != 0 {
				t.Errorf("%q found %d", q, len(list))
			}
		case "":
			if len(list) < 30 {
				t.Errorf("%q found %d, want the category's", q, len(list))
			}
		default:
			if _, ok := claudeByName(list)[want]; !ok {
				t.Errorf("%q didn't find %s", q, want)
			}
		}
	}
}
