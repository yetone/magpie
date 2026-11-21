package library

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/source"
)

// The Claude plugins market: the plugins of Claude Code's own official
// marketplace, anthropics/claude-plugins-official, the one Claude Code
// adds by itself (claudeOfficial), as the Pi packages tab has Pi's. Its
// list is the marketplace's .claude-plugin/marketplace.json read whole:
// kept six hours in memory, on disk for when GitHub can't be reached,
// else the copy magpie shipped with (market_claude.json, that file's
// bytes as they were). With the 「国内镜像」 switch, jsDelivr's copy of the
// same file is asked first (source.ChinaFirst).
//
// A plugin is installed from the marketplace by the library's own
// plugin install (InstallPlugins with the marketplace's repository and
// the plugin's name), so its skills, MCP servers, hooks and subagents go
// to the agents picked, as one installed from its address does.

// claudeMarketRepo is the marketplace's repository, what an install
// takes; claudeMarketURL its list. Variables for tests.
var (
	claudeMarketRepo = "anthropics/claude-plugins-official"
	claudeMarketURL  = "https://raw.githubusercontent.com/anthropics/claude-plugins-official/main/.claude-plugin/marketplace.json"
)

//go:embed market_claude.json
var claudeSnapshot []byte

// ClaudeMarketPlugin is one plugin the market offers.
type ClaudeMarketPlugin struct {
	// Name is the plugin's name in the marketplace, the one the library
	// keeps it by once installed
	Name string `json:"name"`
	// Title is the name it shows, when the marketplace gives one
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Category    string   `json:"category,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Version     string   `json:"version,omitempty"`
	// Marketplace is the marketplace's name, and Source what the install
	// takes to reach it: its repository
	Marketplace string `json:"marketplace"`
	Source      string `json:"source"`
	// Repo is the page of the plugin's own code, Homepage its site
	Repo     string `json:"repo,omitempty"`
	Homepage string `json:"homepage,omitempty"`
	Icon     string `json:"icon,omitempty"`
}

var claudeMarket = struct {
	sync.Mutex
	list []ClaudeMarketPlugin
	at   time.Time
}{}

// claudeMarketList is the marketplace's plugins: fetched again after six
// hours, else the last list on disk, else the one magpie shipped with.
func claudeMarketList() ([]ClaudeMarketPlugin, error) {
	claudeMarket.Lock()
	defer claudeMarket.Unlock()
	if claudeMarket.list != nil && time.Since(claudeMarket.at) < 6*time.Hour {
		return claudeMarket.list, nil
	}
	cache := marketCache("claude-plugins.json")
	b, err := fetchClaudeMarket()
	if err == nil {
		var list []ClaudeMarketPlugin
		if list, err = parseClaudeMarket(b); err == nil {
			claudeMarket.list, claudeMarket.at = list, time.Now()
			if cache != "" {
				_ = os.MkdirAll(filepath.Dir(cache), 0o755)
				_ = os.WriteFile(cache, b, 0o644)
			}
			return list, nil
		}
	}
	list, cerr := parseClaudeMarket(readOr(cache))
	if cerr != nil {
		list, cerr = parseClaudeMarket(claudeSnapshot)
	}
	if cerr != nil {
		return nil, errors.Join(err, cerr)
	}
	claudeMarket.list, claudeMarket.at = list, time.Now().Add(-6*time.Hour+5*time.Minute) // try again in a while
	return list, nil
}

func readOr(p string) []byte {
	if p == "" {
		return nil
	}
	b, _ := os.ReadFile(p)
	return b
}

// fetchClaudeMarket is the marketplace's file: jsDelivr's copy first with
// the 「国内镜像」 switch on, then GitHub's.
func fetchClaudeMarket() ([]byte, error) {
	var err error
	for _, u := range []string{source.ChinaFirst(claudeMarketURL), claudeMarketURL} {
		if u == "" {
			continue
		}
		var b []byte
		if b, err = get(u, "application/json", 8<<20); err == nil {
			return b, nil
		}
	}
	return nil, &SearchError{Source: "GitHub", Err: err}
}

// parseClaudeMarket reads a marketplace.json into what the market lists:
// the plugins the library's install can fetch. Left out are one whose
// source is a command to run (Xcode's), one whose source leaves the
// marketplace's folder or isn't a repository magpie fetches, and a
// language server's (lspServers), which no agent but Claude Code takes.
func parseClaudeMarket(b []byte) ([]ClaudeMarketPlugin, error) {
	var mk struct {
		Name     string `json:"name"`
		Metadata struct {
			PluginRoot string `json:"pluginRoot"`
		} `json:"metadata"`
		Plugins []map[string]json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(stripBOM(b), &mk); err != nil {
		return nil, err
	}
	if strings.TrimSpace(mk.Name) == "" {
		return nil, errors.New("the marketplace has no name")
	}
	str := func(e map[string]json.RawMessage, k string) string {
		var s string
		_ = json.Unmarshal(e[k], &s)
		return strings.Join(strings.Fields(s), " ")
	}
	out := []ClaudeMarketPlugin{}
	seen := map[string]bool{}
	for _, e := range mk.Plugins {
		// the name the install finds it by, and keeps it by (findPlugins):
		// the marketplace's, made a slug, cut to 40
		given := str(e, "name")
		name := pluginSlug(given)
		if name == "" || seen[name] {
			continue
		}
		if _, lsp := e["lspServers"]; lsp {
			continue
		}
		p := ClaudeMarketPlugin{Name: name, Title: str(e, "displayName"), Description: str(e, "description"),
			Category: str(e, "category"), Version: str(e, "version"), Marketplace: mk.Name, Source: claudeMarketRepo}
		if p.Title == "" && given != name {
			p.Title = given
		}
		if p.Title == name {
			p.Title = ""
		}
		if r := []rune(p.Description); len(r) > 280 {
			p.Description = strings.TrimSpace(string(r[:277])) + "…"
		}
		var author struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(e["author"], &author)
		p.Author = strings.TrimSpace(author.Name)
		for _, k := range []string{"keywords", "tags"} {
			var tags []string
			_ = json.Unmarshal(e[k], &tags)
			for _, t := range tags {
				if t = strings.TrimSpace(t); t != "" && !slices.Contains(p.Tags, t) {
					p.Tags = append(p.Tags, t)
				}
			}
		}
		if h := str(e, "homepage"); strings.HasPrefix(h, "https://") {
			p.Homepage = h
		}
		// where its code is, as findPlugins reads the entry
		var rel string
		var src map[string]any
		switch {
		case json.Unmarshal(e["source"], &rel) == nil && rel != "":
			if _, ok := inside("/m", filepath.Join(mk.Metadata.PluginRoot, rel)); !ok {
				continue
			}
			p.Repo = "https://github.com/" + claudeMarketRepo + "/tree/main/" + strings.Trim(filepath.ToSlash(filepath.Clean(filepath.Join(mk.Metadata.PluginRoot, rel))), "/")
		case json.Unmarshal(e["source"], &src) == nil && src != nil:
			s, why := marketSource(src)
			if why != "" {
				continue
			}
			if s.Kind == "github" {
				p.Repo = "https://github.com/" + s.Repo
				if path := strings.Trim(plStr(src["path"]), "/"); path != "" {
					ref := s.Ref
					if ref == "" {
						ref = "HEAD"
					}
					p.Repo += "/tree/" + ref + "/" + path
				}
			}
		default:
			continue
		}
		owner := githubOwner(p.Repo)
		if owner == "" {
			owner = githubOwner(p.Homepage)
		}
		if owner != "" {
			p.Icon = gh(owner)
		}
		seen[name] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("the marketplace lists no plugins")
	}
	return out, nil
}

// MarketClaudePlugins is what the market shows for a search: the
// marketplace's plugins whose name, title, description, author, category
// or tags have it, in the marketplace's order. A list read from disk or
// the shipped copy because GitHub didn't answer comes with no error: it
// is the marketplace as it was, not a search cut short.
func MarketClaudePlugins(q string) ([]ClaudeMarketPlugin, error) {
	list, err := claudeMarketList()
	if err != nil {
		return []ClaudeMarketPlugin{}, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	out := []ClaudeMarketPlugin{}
	for _, p := range list {
		if q == "" || strings.Contains(strings.ToLower(strings.Join(append([]string{p.Name, p.Title, p.Description, p.Author, p.Category}, p.Tags...), " ")), q) {
			out = append(out, p)
		}
	}
	return out, nil
}
