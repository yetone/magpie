package library

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestAddress(t *testing.T) {
	for q, want := range map[string]string{
		"https://github.com/crystaldba/postgres-mcp":        "github.com/crystaldba/postgres-mcp",
		"https://www.GitHub.com/CrystalDBA/postgres-mcp/":   "github.com/crystaldba/postgres-mcp",
		"https://github.com/crystaldba/postgres-mcp.git":    "github.com/crystaldba/postgres-mcp",
		"git@github.com:crystaldba/postgres-mcp.git":        "github.com/crystaldba/postgres-mcp",
		"github.com/crystaldba/postgres-mcp?tab=readme#use": "github.com/crystaldba/postgres-mcp",
		"crystaldba/postgres-mcp":                           "crystaldba/postgres-mcp",
		"https://mcp.notion.com/mcp":                        "mcp.notion.com/mcp",
		"https://www.npmjs.com/package/@playwright/mcp":     "@playwright/mcp",
		"@playwright/mcp@latest":                            "@playwright/mcp",
		"https://pypi.org/project/mcp-server-fetch/":        "mcp-server-fetch",
		"ghcr.io/eszetael/postgres-mcp-hardened:0.1.10":     "ghcr.io/eszetael/postgres-mcp-hardened",
		"https://hub.docker.com/r/crystaldba/postgres-mcp":  "crystaldba/postgres-mcp",
		"docker.io/crystaldba/postgres-mcp:latest":          "crystaldba/postgres-mcp",
		"https://user@example.com/mcp":                      "example.com/mcp",
		"io.github.crystaldba/postgres-mcp":                 "io.github.crystaldba/postgres-mcp",
		"www.example.com":                                   "example.com",
		"postgres":                                          "", // words, searched for by name
		"postgres mcp":                                      "",
		"github postgres/mysql servers":                     "",
		"":                                                  "",
	} {
		if got := address(q); got != want {
			t.Errorf("address(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestAddrScore(t *testing.T) {
	repo := addrsOf("https://github.com/crystaldba/postgres-mcp")
	sub := addrsOf("https://github.com/modelcontextprotocol/servers/tree/main/src/fetch")
	owner := addrsOf("https://github.com/crystaldba")
	for _, c := range []struct {
		q     string
		addrs []string
		want  int
	}{
		{"github.com/crystaldba/postgres-mcp", repo, 3},
		{"crystaldba/postgres-mcp", repo, 3}, // on any host
		{"github.com/crystaldba/postgres-mcp/blob/main/readme.md", repo, 1},
		{"github.com/crystaldba/postgres", repo, 0}, // a path's part isn't it
		{"gitlab.com/crystaldba/postgres-mcp", repo, 0},
		{"github.com/modelcontextprotocol/servers", sub, 2},
		{"github.com/crystaldba/other", owner, 0}, // a page that is the owner's isn't every repository's
		{"github.com/crystaldba", repo, 2},
	} {
		if got := addrScore(c.q, c.addrs); got != c.want {
			t.Errorf("addrScore(%q, %v) = %d, want %d", c.q, c.addrs, got, c.want)
		}
	}
	if got := addrTerms("github.com/crystaldba/postgres-mcp"); !slices.Equal(got, []string{"postgres-mcp", "crystaldba"}) {
		t.Errorf("terms: %v", got)
	}
	if got := addrTerms("@playwright/mcp"); !slices.Equal(got, []string{"mcp", "playwright"}) {
		t.Errorf("npm terms: %v", got)
	}
	if got := addrTerms("mcp.context7.com/mcp"); !slices.Equal(got, []string{"context7", "mcp"}) {
		t.Errorf("site terms: %v", got)
	}
}

// fakeRegistry answers a search like the MCP Registry: by a part of the
// name, any case. It records what was searched for.
func fakeRegistry(t *testing.T, servers ...string) *[]string {
	t.Helper()
	var mu sync.Mutex
	var searched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("search")
		mu.Lock()
		searched = append(searched, q)
		mu.Unlock()
		out := `{"servers": [`
		n := 0
		for _, s := range servers {
			var e struct{ Name string }
			if err := json.Unmarshal([]byte(s), &e); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.ToLower(e.Name), strings.ToLower(q)) {
				if n > 0 {
					out += ","
				}
				out += `{"server": ` + s + `}`
				n++
			}
		}
		w.Write([]byte(out + `]}`))
	}))
	t.Cleanup(srv.Close)
	was := registryURL
	registryURL = srv.URL
	t.Cleanup(func() { registryURL = was })
	return &searched
}

// #lc on Discord: https://github.com/crystaldba/postgres-mcp pasted into
// Discover found nothing — the registry searches names, and featured ones
// were matched on their words. An address finds what is at it.
func TestMarketFindsByAddress(t *testing.T) {
	sandbox(t)
	searched := fakeRegistry(t,
		`{"name": "io.github.someone/postgres-mcp", "repository": {"url": "https://github.com/someone/postgres-mcp"}, "packages": [{"registryType": "npm", "identifier": "pg-mcp"}]}`,
		`{"name": "io.github.crystaldba/postgres-pro", "title": "Postgres Pro", "repository": {"url": "https://github.com/crystaldba/postgres-mcp.git"},
		  "packages": [{"registryType": "pypi", "identifier": "postgres-mcp"}]}`,
		`{"name": "com.acme/db", "remotes": [{"type": "streamable-http", "url": "https://mcp.acme.dev/mcp"}]}`,
	)

	// the repository: found by its owner's name, where its own isn't it
	list, err := MarketServers("https://github.com/CrystalDBA/postgres-mcp/")
	if err != nil || len(list) != 1 || list[0].ID != "io.github.crystaldba/postgres-pro" || !list[0].Match {
		t.Fatalf("by repository: %+v %v", list, err)
	}
	if !slices.Equal(*searched, []string{"postgres-mcp", "crystaldba"}) {
		t.Fatalf("searched for %q", *searched)
	}
	if c := CustomAt("https://github.com/crystaldba/postgres-mcp", list); c != nil {
		t.Fatalf("offered to add by hand what was found: %+v", c)
	}
	// a featured one by its endpoint, its repository and its package
	for _, q := range []string{"https://mcp.notion.com/mcp/", "https://github.com/microsoft/playwright-mcp", "npmjs.com/package/@playwright/mcp", "https://github.com/modelcontextprotocol/servers/tree/main/src/fetch"} {
		list, _ := MarketServers(q)
		if len(list) == 0 || !list[0].Featured || !list[0].Match {
			t.Errorf("%s: %+v", q, list)
		}
	}
	// a registry one by its endpoint
	if list, _ := MarketServers("https://mcp.acme.dev/mcp"); len(list) != 1 || list[0].ID != "com.acme/db" {
		t.Errorf("by endpoint: %+v", list)
	}
	// what nothing is at: the servers called by its name, and one to add by hand
	list, err = MarketServers("https://github.com/nobody/postgres-mcp")
	if err != nil || len(list) != 1 || list[0].ID != "io.github.someone/postgres-mcp" || list[0].Match {
		t.Fatalf("by its name: %+v %v", list, err)
	}
	if c := CustomAt("https://github.com/nobody/postgres-mcp", list); c == nil || c.Name != "postgres" || c.Transport != "stdio" || c.URL != "" {
		t.Fatalf("by hand: %+v", c)
	}
	if c := CustomAt("https://mcp.nowhere.dev/mcp", nil); c == nil || c.Transport != "http" || c.URL != "https://mcp.nowhere.dev/mcp" || c.Name != "nowhere" {
		t.Fatalf("an endpoint by hand: %+v", c)
	}
	if c := CustomAt("https://pypi.org/project/mcp-server-nowhere/", nil); c == nil || c.Command != "uvx" || !slices.Equal(c.Args, []string{"mcp-server-nowhere"}) || c.Name != "nowhere" {
		t.Fatalf("a package by hand: %+v", c)
	}
	if c := CustomAt("postgres", nil); c != nil {
		t.Fatalf("words to add by hand: %+v", c)
	}
	// words are searched for as they were
	*searched = nil
	if list, _ := MarketServers("postgres"); !slices.ContainsFunc(list, func(m MarketServer) bool { return m.ID == "neon" }) || !slices.Equal(*searched, []string{"postgres"}) {
		t.Fatalf("by words: %+v %q", list, *searched)
	}
}
