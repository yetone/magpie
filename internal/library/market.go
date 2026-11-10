package library

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/iconproxy"
	"github.com/yetone/magpie/internal/lazyre"
)

// The market is where the page finds MCP servers and skills to add in one
// click: servers magpie picked and checked itself, then the MCP Registry's;
// skills from skills.sh, which counts how often each is installed.

// ---- servers ----------------------------------------------------------------

// Input is something a server needs from the user before it can start: a
// key in its environment, a header it sends, or an argument.
type Input struct {
	Key         string `json:"key"`
	Where       string `json:"where"` // env, header or arg
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Required    bool   `json:"required,omitempty"`
	format      string // a header's value around the input, "Bearer {}"
}

// MarketServer is a server the market offers.
type MarketServer struct {
	ID          string `json:"id"`
	Name        string `json:"name"` // what the library calls it
	Title       string `json:"title"`
	Description string `json:"description"`
	Publisher   string `json:"publisher,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	Transport   string `json:"transport"`
	// Runs is what starts a local one: npx, uvx or docker
	Runs string `json:"runs,omitempty"`
	// SignIn is a remote one that has the agent sign in, in the browser,
	// the first time it's used
	SignIn   bool `json:"signIn,omitempty"`
	Featured bool `json:"featured,omitempty"`
	// OptIn is one no agent is given unless it is picked for it
	OptIn bool `json:"optIn,omitempty"`
	// Unready is why one of magpie's own can't answer yet (nothing it
	// needs is set up, or Settings turned it off), from the GUI, which
	// knows: its tools still answer, with the same words
	Unready string  `json:"unready,omitempty"`
	Inputs  []Input `json:"inputs"`
	// Have is the library's server that is this one
	Have string `json:"have,omitempty"`
	// Match is one found at the address searched for: its repository, page,
	// endpoint or package is it
	Match  bool `json:"match,omitempty"`
	server Server
	addrs  []string // where it is, normalised (normAddr): matched by an address
}

func gh(owner string) string { return "https://github.com/" + owner + ".png?size=96" }

func remoteServer(id, title, publisher, icon, u, desc string, signIn bool, inputs ...Input) MarketServer {
	return MarketServer{ID: id, Name: id, Title: title, Publisher: publisher, Icon: icon, Description: desc, Transport: "http",
		SignIn: signIn, Featured: true, Inputs: inputs, server: Server{Name: id, Transport: "http", URL: u}}
}

func localServer(id, title, publisher, icon, homepage, desc, command string, args []string, inputs ...Input) MarketServer {
	return MarketServer{ID: id, Name: id, Title: title, Publisher: publisher, Icon: icon, Homepage: homepage, Description: desc,
		Transport: "stdio", Runs: command, Featured: true, Inputs: inputs, server: Server{Name: id, Transport: "stdio", Command: command, Args: args}}
}

func secretEnv(key, label, where string) Input {
	return Input{Key: key, Where: "env", Label: label, Description: where, Secret: true, Required: true}
}

// featured are the servers magpie offers first. Every one was checked: its
// package is published, its endpoint answers.
var featured = []MarketServer{
	selfServer(),
	browserServer(),
	searchServer(),
	visionServer(),
	memoryServer(),
	remoteServer("context7", "Context7", "Upstash", gh("upstash"), "https://mcp.context7.com/mcp",
		"Up-to-date documentation and code examples for any library, straight into the prompt.", false,
		Input{Key: "CONTEXT7_API_KEY", Where: "header", Label: "API key", Description: "Optional — raises the rate limit. From context7.com/dashboard", Secret: true, format: "{}"}),
	localServer("playwright", "Playwright", "Microsoft", "https://playwright.dev/img/playwright-logo.svg", "https://github.com/microsoft/playwright-mcp",
		"Drive a real browser: open pages, click, type and read them through the accessibility tree.", "npx", []string{"-y", "@playwright/mcp@latest"}),
	localServer("chrome-devtools", "Chrome DevTools", "Google", gh("ChromeDevTools"), "https://github.com/ChromeDevTools/chrome-devtools-mcp",
		"Control and inspect a live Chrome: console, network, performance traces and screenshots.", "npx", []string{"-y", "chrome-devtools-mcp@latest"}),
	remoteServer("github", "GitHub", "GitHub", gh("github"), "https://api.githubcopilot.com/mcp/",
		"Repositories, issues, pull requests, Actions and code search on GitHub.", false,
		Input{Key: "Authorization", Where: "header", Label: "Personal access token", Description: "github.com/settings/personal-access-tokens", Placeholder: "github_pat_…", Secret: true, Required: true, format: "Bearer {}"}),
	remoteServer("deepwiki", "DeepWiki", "Cognition", gh("CognitionAI"), "https://mcp.deepwiki.com/mcp",
		"Ask questions about any public GitHub repository and read its generated wiki.", false),
	remoteServer("exa", "Exa", "Exa", gh("exa-labs"), "https://mcp.exa.ai/mcp",
		"Web search and code search built for agents, with clean page contents.", false),
	remoteServer("anysearch", "AnySearch", "AnySearch", gh("anysearch-ai"), "https://api.anysearch.com/mcp",
		"Real-time web search, vertical searches (finance, papers, code, security and more) and full pages as Markdown.", false,
		Input{Key: "Authorization", Where: "header", Label: "API key", Description: "Optional — raises the rate limit. From anysearch.com/console/api-keys", Placeholder: "as_sk_…", Secret: true, format: "Bearer {}"}),
	localServer("filesystem", "Filesystem", "Model Context Protocol", gh("modelcontextprotocol"), "https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem",
		"Read, write and search files, limited to the folders you allow.", "npx", []string{"-y", "@modelcontextprotocol/server-filesystem"},
		Input{Key: "dir", Where: "arg", Label: "Folder", Description: "The folder it may read and write", Placeholder: "~/code", Required: true}),
	localServer("fetch", "Fetch", "Model Context Protocol", gh("modelcontextprotocol"), "https://github.com/modelcontextprotocol/servers/tree/main/src/fetch",
		"Fetch a web page and hand it over as Markdown.", "uvx", []string{"mcp-server-fetch"}),
	localServer("memory", "Memory", "Model Context Protocol", gh("modelcontextprotocol"), "https://github.com/modelcontextprotocol/servers/tree/main/src/memory",
		"A knowledge graph the agent keeps across conversations.", "npx", []string{"-y", "@modelcontextprotocol/server-memory"}),
	localServer("sequential-thinking", "Sequential Thinking", "Model Context Protocol", gh("modelcontextprotocol"), "https://github.com/modelcontextprotocol/servers/tree/main/src/sequentialthinking",
		"Think a problem through step by step, revising and branching as it goes.", "npx", []string{"-y", "@modelcontextprotocol/server-sequential-thinking"}),
	localServer("git", "Git", "Model Context Protocol", gh("modelcontextprotocol"), "https://github.com/modelcontextprotocol/servers/tree/main/src/git",
		"Read, search and change Git repositories.", "uvx", []string{"mcp-server-git"}),
	localServer("time", "Time", "Model Context Protocol", gh("modelcontextprotocol"), "https://github.com/modelcontextprotocol/servers/tree/main/src/time",
		"The current time anywhere, and conversions between time zones.", "uvx", []string{"mcp-server-time"}),
	remoteServer("sentry", "Sentry", "Sentry", gh("getsentry"), "https://mcp.sentry.dev/mcp",
		"Issues, errors and traces from Sentry, and Seer's analysis of them.", true),
	remoteServer("linear", "Linear", "Linear", gh("linear"), "https://mcp.linear.app/mcp",
		"Find, create and update Linear issues, projects and comments.", true),
	remoteServer("notion", "Notion", "Notion", gh("makenotion"), "https://mcp.notion.com/mcp",
		"Search, read and write pages and databases in your Notion workspace.", true),
	remoteServer("atlassian", "Atlassian", "Atlassian", gh("atlassian"), "https://mcp.atlassian.com/v1/mcp",
		"Jira issues and Confluence pages from Atlassian Cloud.", true),
	remoteServer("supabase", "Supabase", "Supabase", gh("supabase"), "https://mcp.supabase.com/mcp",
		"Manage Supabase projects: tables, SQL, migrations, logs and edge functions.", true),
	remoteServer("stripe", "Stripe", "Stripe", gh("stripe"), "https://mcp.stripe.com",
		"Customers, payments, subscriptions and Stripe's documentation.", true),
	remoteServer("vercel", "Vercel", "Vercel", gh("vercel"), "https://mcp.vercel.com",
		"Projects, deployments and logs on Vercel, and its documentation.", true),
	remoteServer("neon", "Neon", "Neon", gh("neondatabase"), "https://mcp.neon.tech/mcp",
		"Serverless Postgres on Neon: projects, branches and SQL.", true),
	remoteServer("cloudflare-docs", "Cloudflare Docs", "Cloudflare", gh("cloudflare"), "https://docs.mcp.cloudflare.com/mcp",
		"Search Cloudflare's documentation.", false),
	remoteServer("microsoft-learn", "Microsoft Learn", "Microsoft", gh("microsoft"), "https://learn.microsoft.com/api/mcp",
		"Official Microsoft and Azure documentation and code samples.", false),
	remoteServer("huggingface", "Hugging Face", "Hugging Face", gh("huggingface"), "https://huggingface.co/mcp",
		"Search models, datasets, Spaces and papers on the Hugging Face Hub.", false),
	localServer("brave-search", "Brave Search", "Brave", gh("brave"), "https://github.com/brave/brave-search-mcp-server",
		"Web, news, image and local search from Brave's independent index.", "npx", []string{"-y", "@brave/brave-search-mcp-server"},
		secretEnv("BRAVE_API_KEY", "API key", "brave.com/search/api")),
	localServer("firecrawl", "Firecrawl", "Firecrawl", gh("firecrawl"), "https://github.com/firecrawl/firecrawl-mcp-server",
		"Scrape, crawl and search the web into clean Markdown.", "npx", []string{"-y", "firecrawl-mcp"},
		secretEnv("FIRECRAWL_API_KEY", "API key", "firecrawl.dev/app/api-keys")),
	localServer("tavily", "Tavily", "Tavily", gh("tavily-ai"), "https://github.com/tavily-ai/tavily-mcp",
		"Search and extract from the web with Tavily.", "npx", []string{"-y", "tavily-mcp"},
		secretEnv("TAVILY_API_KEY", "API key", "app.tavily.com")),
}

// magpieCommand stands for this magpie's own binary in a featured server's
// command, which InstallServer puts in when it is added, and in a library
// carried to another computer, which reads it as its own (self_here.go).
const magpieCommand = "magpie"

// selfServerName is the name magpie's own image generation server is written
// under, in every agent's config.
const selfServerName = "magpie-image"

// selfServer is magpie's own image generation server: generate_image makes
// an image with the model Settings → Models → Images → Image generation names, saved
// in the project. It costs what the model does, so it is given only to the
// agents picked for it.
func selfServer() MarketServer {
	m := localServer(selfServerName, "Magpie Image", "Magpie", "https://usemagpie.ai/favicon.png", "https://usemagpie.ai",
		"Generate and edit images with the image model set in Magpie (Settings → Models → Images), and make short videos with a video model set up in Magpie (Grok Imagine, Seedance), saved in the project.", magpieCommand, []string{"mcp", "image"})
	m.OptIn = true
	return m
}

// browserServerName is the name Magpie Browser's server is written under.
const browserServerName = "magpie-browser"

// browserServer is Magpie Browser: the agent uses the user's own browser,
// its tabs signed in to the sites they use, through the extension magpie
// sets up (gateway/browser.go). It acts as the user wherever they are
// signed in, so it is given only to the agents picked for it.
func browserServer() MarketServer {
	m := localServer(browserServerName, "Magpie Browser", "Magpie", "https://usemagpie.ai/favicon.png", "https://usemagpie.ai",
		"Use your own browser: your tabs, signed in to the sites you use. Open pages, click, type, upload, read and take screenshots, through the Magpie Browser extension.", magpieCommand, []string{"mcp", "browser"})
	m.OptIn = true
	return m
}

// searchServerName is the name Magpie Web Search's server is written
// under, the name the gateway's own search server goes by (ownMCP).
const searchServerName = "magpie-web-search"

// searchServer is Magpie Web Search: magpie's web search (Settings ›
// Models › Web search) as a web_search and a web_fetch tool, through
// `magpie mcp search`, which passes each call to the gateway's
// SearchMCPPath. Settings named it, by its address only, and the Library
// didn't have it (lutra_fs on Discord). A search costs what the searcher
// or the search API it asks charges, and an agent mostly has a search of
// its own, so it is given only to the agents picked for it.
func searchServer() MarketServer {
	m := localServer(searchServerName, "Magpie Web Search", "Magpie", "https://usemagpie.ai/favicon.png", "https://usemagpie.ai",
		"Search the web with the search set in Magpie (Settings → Models → Web search), and read a page by its URL.", magpieCommand, []string{"mcp", "search"})
	m.OptIn = true
	return m
}

// visionServerName is the name Magpie Image Recognition's server is
// written under, the name the gateway's own image recognition server goes
// by (ownMCP).
const visionServerName = "magpie-vision"

// visionServer is Magpie Image Recognition: magpie's image recognition
// (Settings › Models › Images › Image recognition) as an analyze_image
// tool, through `magpie mcp vision`, which passes each call to the
// gateway's VisionMCPPath. Settings named it by its address only, and the
// Library had image generation and search but not it (matrix0487 on
// Discord, #4653). A look costs what the model that sees charges, and
// most agents' models see for themselves, so it is given only to the
// agents picked for it.
func visionServer() MarketServer {
	m := localServer(visionServerName, "Magpie Image Recognition", "Magpie", "https://usemagpie.ai/favicon.png", "https://usemagpie.ai",
		"Read an image (a file, a URL, a screenshot) with the image recognition model set in Magpie (Settings → Models → Images), for a model that can't see.", magpieCommand, []string{"mcp", "vision"})
	m.OptIn = true
	return m
}

// memoryServerName is the name Magpie Memory's server is written under,
// the name the gateway's own memory server goes by (ownMCP).
const memoryServerName = "magpie-memory"

// memoryServer is Magpie Memory: what is known about the user, kept by
// magpie for every agent, as memory_search, memory_save and memory_forget,
// through `magpie mcp memory`, which passes each call to the gateway's
// MemoryMCPPath with the folder the agent started it in. It is there to be
// shared and costs nothing, so, unlike magpie's other servers, it is given
// to every agent that can take it.
func memoryServer() MarketServer {
	return localServer(memoryServerName, "Magpie Memory", "Magpie", "https://usemagpie.ai/favicon.png", "https://usemagpie.ai",
		"Memory shared by every agent: your preferences, rules and what each project needs, kept in Magpie. Agents search it before asking again, and save what you ask them to remember.", magpieCommand, []string{"mcp", "memory"})
}

// selfCommand is the server's command and env for this magpie: its binary
// by full path, and the gateway's address when it isn't the default one.
func selfCommand(s *Server) {
	if s.Command != magpieCommand {
		return
	}
	if exe, err := selfExe(); err == nil {
		s.Command = exe
	}
	if a := os.Getenv("MAGPIE_ADDR"); a != "" {
		if s.Env == nil {
			s.Env = map[string]string{}
		}
		s.Env["MAGPIE_ADDR"] = a
	}
}

// homepages are the remote servers' pages, where their endpoints aren't.
var homepages = map[string]string{
	"context7":        "https://context7.com",
	"github":          "https://github.com/github/github-mcp-server",
	"deepwiki":        "https://deepwiki.com",
	"exa":             "https://docs.exa.ai/reference/exa-mcp",
	"anysearch":       "https://github.com/anysearch-ai/anysearch-mcp-server",
	"sentry":          "https://docs.sentry.io/product/sentry-mcp/",
	"linear":          "https://linear.app/docs/mcp",
	"notion":          "https://developers.notion.com/docs/mcp",
	"atlassian":       "https://www.atlassian.com/platform/remote-mcp-server",
	"supabase":        "https://supabase.com/docs/guides/getting-started/mcp",
	"stripe":          "https://docs.stripe.com/mcp",
	"vercel":          "https://vercel.com/docs/mcp/vercel-mcp",
	"neon":            "https://neon.com/docs/ai/neon-mcp-server",
	"cloudflare-docs": "https://github.com/cloudflare/mcp-server-cloudflare",
	"microsoft-learn": "https://github.com/MicrosoftDocs/mcp",
	"huggingface":     "https://huggingface.co/settings/mcp",
}

func init() {
	for i := range featured {
		m := &featured[i]
		if m.Homepage == "" {
			m.Homepage = homepages[m.ID]
		}
		if m.Inputs == nil {
			m.Inputs = []Input{}
		}
		m.addrs = addrsOf(m.Homepage, m.server.URL, packageOf(&m.server))
	}
}

// registryURL is the MCP Registry's; a variable for tests.
var registryURL = "https://registry.modelcontextprotocol.io/v0/servers"

//go:embed market_servers.json
var serversSnapshot []byte

// popularServers are the registry's servers whose repositories have the
// most stars on GitHub, most first, offered after the featured ones before
// anything is searched. The registry lists tens of thousands, by name, so
// they come from a crawl of it shipped with magpie (market_servers.json).
// One a featured server already is, or two that start the same thing, is
// offered once.
var popularServers = sync.OnceValue(func() []MarketServer { return readPopular(serversSnapshot) })

func readPopular(b []byte) []MarketServer {
	var snap []registryServer
	if json.Unmarshal(b, &snap) != nil {
		return nil
	}
	out := []MarketServer{}
	seen := map[string]bool{}
	for _, m := range featured {
		seen[serverKey(&m.server)] = true
	}
	for i := range snap {
		m, ok := fromRegistry(&snap[i])
		if !ok || seen[serverKey(&m.server)] {
			continue
		}
		seen[serverKey(&m.server)] = true
		out = append(out, m)
	}
	return out
}

// seenServers are the registry's servers the page was last shown, for one to
// be installed by its id.
var seenServers = struct {
	sync.Mutex
	m map[string]MarketServer
}{m: map[string]MarketServer{}}

// MarketServers is what the market shows for a search: the featured servers
// that match it, then the registry's, then the popular ones that do; with no
// search, the featured and every popular one. A search that is an address —
// a repository, a page, an endpoint, a package — finds the ones at it.
func MarketServers(q string) ([]MarketServer, error) {
	raw := strings.TrimSpace(q)
	q = strings.ToLower(raw)
	addr := address(q)
	var out []MarketServer
	seen := map[string]bool{}
	add := func(list []MarketServer) {
		for _, f := range list {
			if k := serverKey(&f.server); !seen[k] {
				seen[k] = true
				out = append(out, f)
			}
		}
	}
	pick := func(list []MarketServer) []MarketServer {
		var got []MarketServer
		for _, m := range list {
			switch {
			case addr != "":
				if addrScore(addr, m.addrs) > 0 {
					m.Match = true
					got = append(got, m)
				}
			case q == "" || strings.Contains(strings.ToLower(m.Title+" "+m.ID+" "+m.Publisher+" "+m.Description+" "+packageOf(&m.server)), q):
				got = append(got, m)
			}
		}
		return got
	}
	add(pick(featured))
	var err error
	switch {
	case q == "":
		add(popularServers())
	case addr != "":
		add(pick(popularServers()))
		var found []MarketServer
		found, err = searchAt(raw, addr)
		add(found)
	default:
		// the registry's, ranked by name first; then the popular ones
		// whose words have it
		found, ferr := searchWords(q)
		err = ferr
		add(found)
		add(pick(popularServers()))
	}
	if out == nil {
		out = []MarketServer{}
	}
	if l, lerr := loadLocked(); lerr == nil {
		for i := range out {
			out[i].Have = l.haveServer(&out[i].server)
		}
	}
	return out, err
}

// serverKey is what a server is, whatever it's called: its URL, or the
// command and the package it starts.
func serverKey(s *Server) string {
	if s.Remote() {
		return strings.TrimSuffix(strings.ToLower(s.URL), "/")
	}
	if len(s.Args) == 2 && s.Args[0] == "mcp" && (s.Args[1] == "image" || s.Args[1] == "browser" || s.Args[1] == "search" || s.Args[1] == "vision" || s.Args[1] == "memory") {
		return "magpie mcp " + s.Args[1] // magpie's own, wherever its binary is
	}
	for _, a := range s.Args {
		if a == "" || strings.HasPrefix(a, "-") || a == "run" {
			continue
		}
		if i := strings.LastIndex(a, "@"); i > 0 { // @scope/pkg@latest → @scope/pkg
			a = a[:i]
		}
		return s.Command + " " + a
	}
	return s.Command
}

func (l *Library) haveServer(s *Server) string {
	k := serverKey(s)
	for _, x := range l.MCP {
		if serverKey(x) == k {
			return x.Name
		}
	}
	return ""
}

type registryServer struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	WebsiteURL  string `json:"websiteUrl"`
	Repository  struct {
		URL string `json:"url"`
	} `json:"repository"`
	Icons []struct {
		Src string `json:"src"`
	} `json:"icons"`
	Remotes []struct {
		Type    string        `json:"type"`
		URL     string        `json:"url"`
		Headers []registryVar `json:"headers"`
	} `json:"remotes"`
	Packages []struct {
		RegistryType string                `json:"registryType"`
		Identifier   string                `json:"identifier"`
		Transport    struct{ Type string } `json:"transport"`
		Env          []registryVar         `json:"environmentVariables"`
		Arguments    []struct {
			Type        string `json:"type"`
			Name        string `json:"name"`
			Value       string `json:"value"`
			Default     string `json:"default"`
			ValueHint   string `json:"valueHint"`
			Description string `json:"description"`
			IsRequired  bool   `json:"isRequired"`
		} `json:"packageArguments"`
	} `json:"packages"`
}

type registryVar struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
	IsRequired  bool   `json:"isRequired"`
	IsSecret    bool   `json:"isSecret"`
}

var placeholderRe = lazyre.New(`\{[^{}]*\}`)

// listed is an active server the registry lists, as the market
// offers it.
type listed struct {
	m MarketServer
	r registryServer
}

// fetchRegistry is what the registry lists for a search, which it matches
// against servers' names; the same server once.
func fetchRegistry(q string) ([]listed, error) {
	u := registryURL + "?" + url.Values{"search": {q}, "limit": {"100"}, "version": {"latest"}}.Encode()
	var body struct {
		Servers []struct {
			Server registryServer `json:"server"`
			Meta   map[string]struct {
				Status string `json:"status"`
			} `json:"_meta"`
		} `json:"servers"`
	}
	if err := getJSONBy(registryClient, u, &body); err != nil {
		return nil, &SearchError{Source: "the MCP Registry", Err: err}
	}
	var list []listed
	seen := map[string]bool{}
	for _, e := range body.Servers {
		if st := e.Meta["io.modelcontextprotocol.registry/official"].Status; st != "" && st != "active" {
			continue
		}
		m, ok := fromRegistry(&e.Server)
		if !ok || seen[serverKey(&m.server)] {
			continue
		}
		seen[serverKey(&m.server)] = true
		list = append(list, listed{m, e.Server})
	}
	return list, nil
}

type scored struct {
	m     MarketServer
	score int
}

func searchRegistry(q string) ([]MarketServer, error) {
	found, err := fetchRegistry(q)
	if err != nil {
		return nil, err
	}
	var list []scored
	for _, e := range found {
		list = append(list, scored{e.m, rank(&e.r, e.m, q)})
	}
	return offer(list), nil
}

// searchRegistryAt finds the registry's servers at an address. The registry
// searches names only, so it's asked for the names the address suggests (a
// repository's, its owner's), and what it lists is kept when its repository,
// page, endpoint or package is the address. When none is, what it lists for
// the first name is offered, as a search for that name would.
func searchRegistryAt(addr string) ([]MarketServer, error) {
	var matched, byName []scored
	seen := map[string]bool{}
	for i, term := range addrTerms(addr) {
		found, err := fetchRegistry(term)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			break
		}
		for _, e := range found {
			if i == 0 {
				byName = append(byName, scored{e.m, rank(&e.r, e.m, term)})
			}
			k := serverKey(&e.m.server)
			if seen[k] {
				continue
			}
			if sc := addrScore(addr, e.m.addrs); sc > 0 {
				seen[k] = true
				e.m.Match = true
				matched = append(matched, scored{e.m, sc*1000 + rank(&e.r, e.m, term)})
			}
		}
	}
	if len(matched) > 0 {
		return offer(matched), nil
	}
	return offer(byName), nil
}

// offer is the best of a search's servers, best first, kept for one to be
// installed by its id.
func offer(list []scored) []MarketServer {
	sort.SliceStable(list, func(i, j int) bool { return list[i].score > list[j].score })
	out := []MarketServer{}
	seenServers.Lock()
	defer seenServers.Unlock()
	if len(seenServers.m) > 2000 {
		clear(seenServers.m)
	}
	for i, s := range list {
		if i == 100 {
			break
		}
		seenServers.m[s.m.ID] = s.m
		out = append(out, s.m)
	}
	return out
}

// rank puts first what's called what was searched for, then what says it
// is, with an icon and a repository; the registry lists them by name.
func rank(r *registryServer, m MarketServer, q string) int {
	s := 0
	short := strings.ToLower(r.Name[strings.LastIndex(r.Name, "/")+1:])
	switch {
	case m.Name == q || strings.ToLower(r.Title) == q || short == q:
		s += 100
	case strings.Contains(short, q):
		s += 40
	}
	if strings.Contains(strings.ToLower(r.Title), q) {
		s += 20
	}
	if strings.Contains(strings.ToLower(r.Description), q) {
		s += 10
	}
	if len(r.Icons) > 0 {
		s += 8
	}
	if r.Repository.URL != "" {
		s += 5
	}
	if strings.HasPrefix(r.Name, "ai.smithery/") { // wrappers around others' servers
		s -= 60
	}
	return s
}

// fromRegistry is what a registry entry becomes in the library: its remote
// when it has one, else the package it's published as.
func fromRegistry(r *registryServer) (MarketServer, bool) {
	m := MarketServer{ID: r.Name, Name: shortName(r.Name), Title: r.Title, Description: r.Description, Homepage: r.WebsiteURL, Inputs: []Input{}}
	m.addrs = addrsOf(r.Name, r.WebsiteURL, r.Repository.URL)
	for _, rm := range r.Remotes {
		m.addrs = append(m.addrs, addrsOf(rm.URL)...)
	}
	for _, p := range r.Packages {
		m.addrs = append(m.addrs, addrsOf(p.Identifier)...)
	}
	if m.Title == "" {
		m.Title = m.Name
	}
	if m.Homepage == "" {
		m.Homepage = r.Repository.URL
	}
	owner := githubOwner(r.Repository.URL)
	if owner == "" {
		if rest, ok := strings.CutPrefix(r.Name, "io.github."); ok {
			owner, _, _ = strings.Cut(rest, "/")
		}
	}
	switch {
	case owner != "":
		m.Publisher = owner
	default:
		ns, _, _ := strings.Cut(r.Name, "/")
		parts := strings.Split(ns, ".")
		slices.Reverse(parts)
		m.Publisher = strings.Join(parts, ".")
	}
	for _, ic := range r.Icons {
		if strings.HasPrefix(ic.Src, "https://") {
			m.Icon = ic.Src
			break
		}
	}
	if m.Icon == "" && owner != "" {
		m.Icon = gh(owner)
	}
	s := &m.server
	s.Name = m.Name
	for _, want := range []string{"streamable-http", "sse"} {
		for _, rm := range r.Remotes {
			if rm.Type != want || strings.Contains(rm.URL, "{") || !strings.HasPrefix(rm.URL, "https://") {
				continue
			}
			s.Transport, s.URL = map[string]string{"streamable-http": "http", "sse": "sse"}[want], rm.URL
			for _, h := range rm.Headers {
				switch {
				case h.Value != "" && !placeholderRe.MatchString(h.Value):
					if s.Headers == nil {
						s.Headers = map[string]string{}
					}
					s.Headers[h.Name] = h.Value
				case h.IsRequired || h.IsSecret:
					f := "{}"
					if h.Value != "" {
						f = placeholderRe.ReplaceAllString(h.Value, "{}")
					}
					m.Inputs = append(m.Inputs, Input{Key: h.Name, Where: "header", Label: h.Name, Description: h.Description,
						Secret: h.IsSecret, Required: h.IsRequired, format: f})
				}
			}
			m.Transport = s.Transport
			return m, true
		}
	}
	for _, want := range []string{"npm", "pypi", "oci"} {
		for _, p := range r.Packages {
			if p.RegistryType != want || p.Identifier == "" || (p.Transport.Type != "" && p.Transport.Type != "stdio") {
				continue
			}
			s.Transport, m.Transport = "stdio", "stdio"
			switch want {
			case "npm":
				s.Command, s.Args = "npx", []string{"-y", p.Identifier}
			case "pypi":
				s.Command, s.Args = "uvx", []string{p.Identifier}
			case "oci":
				s.Command, s.Args = "docker", []string{"run", "-i", "--rm"}
			}
			m.Runs = s.Command
			for _, e := range p.Env {
				if !e.IsRequired && !e.IsSecret {
					continue
				}
				m.Inputs = append(m.Inputs, Input{Key: e.Name, Where: "env", Label: e.Name, Description: e.Description, Secret: e.IsSecret, Required: e.IsRequired})
				if want == "oci" {
					s.Args = append(s.Args, "-e", e.Name)
				}
			}
			if want == "oci" {
				s.Args = append(s.Args, p.Identifier)
			}
			for i, a := range p.Arguments {
				v := a.Value
				if v == "" {
					v = a.Default
				}
				if placeholderRe.MatchString(v) {
					v = ""
				}
				switch {
				case v != "" && a.Type == "named":
					s.Args = append(s.Args, a.Name, v)
				case v != "":
					s.Args = append(s.Args, v)
				case a.IsRequired && a.Type != "named":
					label := a.ValueHint
					if label == "" {
						label = fmt.Sprintf("Argument %d", i+1)
					}
					m.Inputs = append(m.Inputs, Input{Key: fmt.Sprintf("arg%d", i), Where: "arg", Label: label, Description: a.Description, Required: true})
				case a.IsRequired:
					return MarketServer{}, false // a named argument magpie can't fill
				}
			}
			return m, true
		}
	}
	return MarketServer{}, false
}

// shortName is a registry name as the library would call it:
// io.github.upstash/context7-mcp is context7.
func shortName(n string) string {
	n = strings.ToLower(n[strings.LastIndex(n, "/")+1:])
	orig := n
	for _, p := range []string{"mcp-server-", "server-", "mcp-"} {
		n = strings.TrimPrefix(n, p)
	}
	for _, s := range []string{"-mcp-server", "-mcp", "-server", "_mcp", ".mcp"} {
		n = strings.TrimSuffix(n, s)
	}
	if n == "" {
		n = orig
	}
	n = strings.Trim(unsafe.ReplaceAllString(n, "-"), "-_")
	if len(n) > 64 {
		n = n[:64]
	}
	if n == "" || !nameRe.MatchString(n) {
		n = "server"
	}
	return n
}

func lastPart(p string) string { return p[strings.LastIndex(p, "/")+1:] }

func githubOwner(u string) string {
	p, err := url.Parse(u)
	if err != nil || (p.Host != "github.com" && p.Host != "www.github.com") {
		return ""
	}
	owner, _, _ := strings.Cut(strings.Trim(p.Path, "/"), "/")
	return owner
}

func marketServer(id string) (MarketServer, bool) {
	if i := slices.IndexFunc(featured, func(m MarketServer) bool { return m.ID == id }); i >= 0 {
		return featured[i], true
	}
	list := popularServers()
	if i := slices.IndexFunc(list, func(m MarketServer) bool { return m.ID == id }); i >= 0 {
		return list[i], true
	}
	seenServers.Lock()
	defer seenServers.Unlock()
	m, ok := seenServers.m[id]
	return m, ok
}

// InstallServer adds a market server to the library, filled in with values
// (by input key), and gives it to the agents named — to every agent that can
// have it when none are.
func InstallServer(id string, values map[string]string, agents []string) (*Result, error) {
	m, ok := marketServer(id)
	if !ok {
		return nil, fmt.Errorf("the market has no server %s; search for it again", id)
	}
	s := m.server
	s.Args = slices.Clone(s.Args)
	s.Env, s.Headers = maps.Clone(s.Env), maps.Clone(s.Headers)
	selfCommand(&s)
	for _, in := range m.Inputs {
		v := strings.TrimSpace(values[in.Key])
		if v == "" {
			if in.Required {
				return nil, fmt.Errorf("%s needs %s", m.Title, in.Label)
			}
			continue
		}
		switch in.Where {
		case "env":
			if s.Env == nil {
				s.Env = map[string]string{}
			}
			s.Env[in.Key] = v
		case "header":
			if s.Headers == nil {
				s.Headers = map[string]string{}
			}
			f := in.format
			if f == "" {
				f = "{}"
			}
			s.Headers[in.Key] = strings.Replace(f, "{}", v, 1)
		case "arg":
			s.Args = append(s.Args, expand(v))
		}
	}
	if agents == nil && m.OptIn {
		agents = []string{}
	}
	if agents == nil {
		for _, t := range Targets() {
			if t.MCP != nil && t.MCP.supports(&s) == nil {
				agents = append(agents, t.Agent.ID)
			}
		}
	}
	s.Agents = agents
	if err := s.check(); err != nil {
		return nil, err
	}
	return change(func(l *Library) error {
		if have := l.haveServer(&s); have != "" {
			return fmt.Errorf("the library has it already, as %s", have)
		}
		base := s.Name
		for i := 2; l.server(s.Name) != nil; i++ {
			s.Name = fmt.Sprintf("%s-%d", base, i)
		}
		s.Agents = slices.Sorted(slices.Values(s.Agents))
		l.MCP = append(l.MCP, &s)
		if m.Icon != "" {
			if l.Icons == nil {
				l.Icons = map[string]string{}
			}
			l.Icons[serverKey(&s)] = m.Icon
		}
		return nil
	})
}

// serverIcon is a server's own icon, where the market knows it: the
// featured server it is, the one it was added from, or the featured one it
// is named after. None is made up for the rest.
func serverIcon(l *Library, s *Server) string {
	k := serverKey(s)
	for _, m := range featured {
		if serverKey(&m.server) == k {
			return m.Icon
		}
	}
	if u := l.Icons[k]; u != "" {
		return u
	}
	for _, m := range featured {
		if strings.EqualFold(m.ID, s.Name) {
			return m.Icon
		}
	}
	return ""
}

// serverAbout is what a server is for, where the market says: the
// featured or registry server it is, or the featured one it is named
// after; "" for the rest.
func serverAbout(s *Server) string {
	k := serverKey(s)
	for _, m := range featured {
		if serverKey(&m.server) == k {
			return m.Description
		}
	}
	for _, m := range popularServers() {
		if serverKey(&m.server) == k {
			return m.Description
		}
	}
	seenServers.Lock()
	for _, m := range seenServers.m {
		if serverKey(&m.server) == k {
			seenServers.Unlock()
			return m.Description
		}
	}
	seenServers.Unlock()
	for _, m := range featured {
		if strings.EqualFold(m.ID, s.Name) {
			return m.Description
		}
	}
	return ""
}

// skillIcon is its repository owner's avatar, for a skill from GitHub.
func skillIcon(s *Skill) string {
	if s.Source == nil || s.Source.Kind != "github" {
		return ""
	}
	owner, _, ok := strings.Cut(s.Source.Repo, "/")
	if !ok || owner == "" {
		return ""
	}
	return gh(owner)
}

// ---- skills -----------------------------------------------------------------

// MarketSkill is a skill the market offers, from a GitHub repository.
type MarketSkill struct {
	ID       string `json:"id"`     // owner/repo/skill
	Source   string `json:"source"` // owner/repo
	SkillID  string `json:"skillId"`
	Name     string `json:"name"`
	Installs int    `json:"installs"`
	Official bool   `json:"official,omitempty"`
	// Featured is magpie's own, not one skills.sh lists
	Featured bool   `json:"featured,omitempty"`
	Icon     string `json:"icon"`
	// Description is known once it has been fetched
	Description string `json:"description,omitempty"`
	Have        string `json:"have,omitempty"`
}

// featuredSkills are magpie's own skills, first in the market: in
// magpie-community/plugins, beside its plugins. magpie-setup has an agent
// set magpie up as the user asks, through its commands (Leyuu_5 on X: so
// many features that an AI could do the setting up); magpie-quota reads
// what magpie quota tells (ttmouse on X).
var featuredSkills = []MarketSkill{{
	Source: "magpie-community/plugins", SkillID: "magpie-setup", Name: "magpie-setup", Official: true, Featured: true,
	Description: "Set magpie up as you ask, through its own commands: add keys and subscriptions, point agents at models, build routing groups with fallbacks and time-of-day rules, give devices gateway keys with budgets, and share MCP servers and instructions with every agent.",
}, {
	Source: "magpie-community/plugins", SkillID: "magpie-quota", Name: "magpie-quota", Official: true, Featured: true,
	Description: "Check how much is left of the user's AI subscriptions, coding plans and API key balances through magpie: each usage window's percent used and when it resets, each key's balance, and wait until a subscription has allowance again.",
}}

type skillsShEntry struct {
	Source   string `json:"source"`
	SkillID  string `json:"skillId"`
	Name     string `json:"name"`
	Installs int    `json:"installs"`
	Official bool   `json:"isOfficial,omitempty"`
}

// skillsShURL is skills.sh; a variable for tests.
var skillsShURL = "https://www.skills.sh"

//go:embed market_skills.json
var skillsSnapshot []byte

var repoRe = lazyre.New(`^[\w.-]+/[\w.-]+$`)

var popular = struct {
	sync.Mutex
	list []skillsShEntry
	at   time.Time
}{}

func marketCache(name string) string {
	d, err := appdir.SystemCache()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "market", name)
}

// popularSkills are skills.sh's most installed, read off its front page,
// then the ones trending there that aren't among them; kept for six hours,
// and on disk for when it can't be reached, else what magpie shipped with.
func popularSkills() []skillsShEntry {
	popular.Lock()
	defer popular.Unlock()
	if popular.list != nil && time.Since(popular.at) < 6*time.Hour {
		return popular.list
	}
	cache := marketCache("skills.json")
	if list, err := fetchPopular(); err == nil && len(list) > 0 {
		popular.list, popular.at = list, time.Now()
		if b, err := json.Marshal(list); err == nil && cache != "" {
			_ = os.MkdirAll(filepath.Dir(cache), 0o755)
			_ = os.WriteFile(cache, b, 0o644)
		}
		return list
	}
	var list []skillsShEntry
	if b, err := os.ReadFile(cache); err != nil || json.Unmarshal(b, &list) != nil || len(list) == 0 {
		_ = json.Unmarshal(skillsSnapshot, &list)
	}
	popular.list, popular.at = list, time.Now().Add(-6*time.Hour+5*time.Minute) // try again in a while
	return list
}

// fetchPopular is skills.sh's front page's list, its 600 most installed,
// then its trending page's that aren't on it. A trending skill's count is
// of its recent installs, not all of them, so it isn't told.
func fetchPopular() ([]skillsShEntry, error) {
	list, err := skillsShList("/")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, e := range list {
		seen[e.Source+"/"+e.SkillID] = true
	}
	if more, err := skillsShList("/trending"); err == nil {
		for _, e := range more {
			if !seen[e.Source+"/"+e.SkillID] {
				seen[e.Source+"/"+e.SkillID] = true
				e.Installs = 0
				list = append(list, e)
			}
		}
	}
	return list, nil
}

// skillsShList is the list of skills a skills.sh page has in its data.
func skillsShList(path string) ([]skillsShEntry, error) {
	b, err := get(skillsShURL+path, "text/html", 8<<20)
	if err != nil {
		return nil, err
	}
	// the list is in the page's data as JSON inside a JS string, its quotes escaped
	const key = `initialSkills\":`
	i := strings.Index(string(b), key)
	if i < 0 {
		return nil, errors.New("skills.sh's page has no list of skills")
	}
	s := strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(string(b[i+len(key):]))
	var list []skillsShEntry
	if err := json.NewDecoder(strings.NewReader(s)).Decode(&list); err != nil {
		return nil, fmt.Errorf("skills.sh's list: %w", err)
	}
	return slices.DeleteFunc(list, func(e skillsShEntry) bool { return !repoRe.MatchString(e.Source) || e.SkillID == "" }), nil
}

func searchSkillsSh(q string) ([]skillsShEntry, error) {
	var body struct {
		Skills []skillsShEntry `json:"skills"`
	}
	if err := getJSON(skillsShURL+"/api/search?"+url.Values{"q": {q}, "limit": {"200"}}.Encode(), &body); err != nil {
		return nil, &SearchError{Source: "skills.sh", Err: err}
	}
	return slices.DeleteFunc(body.Skills, func(e skillsShEntry) bool { return !repoRe.MatchString(e.Source) || e.SkillID == "" }), nil
}

// MarketSkills is what the market shows for a search: magpie's own skills
// that match it, the most installed skills that do, then what skills.sh
// finds for it.
func MarketSkills(q string) ([]MarketSkill, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	var list []skillsShEntry
	var err error
	for _, e := range popularSkills() {
		if q == "" || strings.Contains(strings.ToLower(e.Name+" "+e.Source+" "+e.SkillID), q) {
			list = append(list, e)
		}
	}
	if q == "" {
		list = byRepo(list)
	}
	if len(q) >= 2 {
		var found []skillsShEntry
		found, err = searchSkillsSh(q)
		for _, f := range found {
			if !slices.ContainsFunc(list, func(e skillsShEntry) bool { return e.Source == f.Source && e.SkillID == f.SkillID }) {
				list = append(list, f)
			}
		}
	}
	about := cachedAbout()
	l, _ := loadLocked()
	out := []MarketSkill{}
	for _, f := range featuredSkills {
		if q != "" && !strings.Contains(strings.ToLower(f.Name+" "+f.Source+" "+f.Description), q) {
			continue
		}
		owner, _, _ := strings.Cut(f.Source, "/")
		f.ID, f.Icon = f.Source+"/"+f.SkillID, gh(owner)
		if l != nil {
			f.Have = l.haveSkill(f.Source, f.SkillID, f.Name)
		}
		// once skills.sh lists it too, its count of installs is told, and
		// it isn't shown twice
		list = slices.DeleteFunc(list, func(e skillsShEntry) bool {
			if e.Source == f.Source && e.SkillID == f.SkillID {
				f.Installs = e.Installs
				return true
			}
			return false
		})
		out = append(out, f)
	}
	for _, e := range list {
		owner, _, _ := strings.Cut(e.Source, "/")
		ms := MarketSkill{ID: e.Source + "/" + e.SkillID, Source: e.Source, SkillID: e.SkillID, Name: e.Name, Installs: e.Installs,
			Official: e.Official, Icon: gh(owner)}
		ms.Description = about[ms.ID]
		if l != nil {
			ms.Have = l.haveSkill(e.Source, e.SkillID, e.Name)
		}
		out = append(out, ms)
	}
	return out, err
}

// byRepo is the most installed of each repository first, then each one's
// second, and so on, for the first page not to be one author's; all of
// them are there for the reader to go on to.
func byRepo(list []skillsShEntry) []skillsShEntry {
	nth := map[string]int{}
	rounds := [][]skillsShEntry{}
	for _, e := range list {
		n := nth[e.Source]
		nth[e.Source]++
		if n == len(rounds) {
			rounds = append(rounds, nil)
		}
		rounds[n] = append(rounds[n], e)
	}
	return slices.Concat(rounds...)
}

func (l *Library) haveSkill(source, id, name string) string {
	for _, s := range l.Skills {
		if s.Source != nil && s.Source.Kind == "github" && strings.EqualFold(s.Source.Repo, source) &&
			(s.Name == id || s.Name == name || lastPart(s.Source.Path) == id) {
			return s.Name
		}
	}
	// one taken in from an agent's folder (npx skills puts them in
	// ~/.agents/skills) has no repository to match: it's had by its name,
	// unless it is known to come from another repository. One by that name
	// from another repository is another skill, added beside it
	// (skill_beside.go)
	for _, n := range []string{id, name} {
		if s := l.skill(n); s != nil && (s.Source == nil || s.Source.Kind == "folder") && (s.From == "" || strings.EqualFold(s.From, source)) {
			return s.Name
		}
	}
	return ""
}

var abouts = struct {
	sync.Mutex
	m      map[string]string
	loaded bool
}{m: map[string]string{}}

func cachedAbout() map[string]string {
	abouts.Lock()
	defer abouts.Unlock()
	if !abouts.loaded {
		abouts.loaded = true
		if b, err := os.ReadFile(marketCache("about.json")); err == nil {
			_ = json.Unmarshal(b, &abouts.m)
		}
	}
	return maps.Clone(abouts.m)
}

// SkillsAbout is what each skill says of itself, by id (owner/repo/skill),
// from its SKILL.md; kept on disk once known.
func SkillsAbout(ids []string) map[string]string {
	have := cachedAbout()
	out := map[string]string{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 6)
	for _, id := range ids {
		if d, ok := have[id]; ok {
			out[id] = d
			continue
		}
		if strings.Count(id, "/") != 2 || !repoRe.MatchString(id[:strings.LastIndex(id, "/")]) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// the skill's page on skills.sh says what it's for — its
			// download API gives the SKILL.md itself, but only 60 an hour
			b, err := get(skillsShURL+"/"+id[:strings.LastIndex(id, "/")+1]+url.PathEscape(id[strings.LastIndex(id, "/")+1:]), "text/html", 4<<20)
			if err != nil {
				return
			}
			if d := metaDescription(b); d != "" {
				mu.Lock()
				out[id] = d
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	abouts.Lock()
	changed := false
	for id, d := range out {
		if _, ok := abouts.m[id]; !ok {
			abouts.m[id], changed = d, true
		}
	}
	if changed {
		if b, err := json.Marshal(abouts.m); err == nil {
			p := marketCache("about.json")
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, b, 0o644)
		}
	}
	abouts.Unlock()
	return out
}

// InstallMarketSkill adds a skill from the market, found in its repository,
// and gives it to the agents named — to every agent that can have skills
// when none are.
func InstallMarketSkill(source, id string, agents []string) (*Result, error) {
	if !repoRe.MatchString(source) {
		return nil, fmt.Errorf("%s isn't a GitHub repository", source)
	}
	// only this skill's folder, when the repository is too big to
	// download whole (#2794)
	p, err := cachedProbeFor(source, id)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(p.Candidates, func(c Candidate) bool { return c.Name == id })
	if i < 0 {
		i = slices.IndexFunc(p.Candidates, func(c Candidate) bool { return lastPart(c.Path) == id })
	}
	if i < 0 {
		return nil, fmt.Errorf("%s has no skill %s any more", source, id)
	}
	if agents == nil {
		for _, t := range Targets() {
			if t.Skills != "" {
				agents = append(agents, t.Agent.ID)
			}
		}
	}
	// the market showed this one skill: the repository's others are
	// offered as new by a check, beside it
	path := p.Candidates[i].Path
	return installChange(func(l *Library, in *installed) error { return installFrom(l, p, []string{path}, agents, false, in) })
}

// ---- icons ------------------------------------------------------------------

// Icon is a market icon, fetched once and kept on disk: only an icon the
// market gave out is fetched, so the page can't have magpie fetch anything else.
func Icon(u string) ([]byte, string, error) {
	sum := sha256.Sum256([]byte(u))
	file := marketCache(filepath.Join("icons", hex.EncodeToString(sum[:16])))
	if b, err := os.ReadFile(file); err == nil {
		if ct, err := os.ReadFile(file + ".type"); err == nil {
			return b, string(ct), nil
		}
	}
	if !offered(u) {
		return nil, "", errors.New("not an icon of the market's")
	}
	b, ct, err := fetchImage(u)
	if err != nil {
		return nil, "", err
	}
	if file != "" {
		_ = os.MkdirAll(filepath.Dir(file), 0o755)
		if os.WriteFile(file, b, 0o644) == nil {
			_ = os.WriteFile(file+".type", []byte(ct), 0o644)
		}
	}
	return b, ct, nil
}

func offered(u string) bool {
	if !strings.HasPrefix(u, "https://") {
		return false
	}
	if slices.ContainsFunc(featured, func(m MarketServer) bool { return m.Icon == u }) || slices.ContainsFunc(popularServers(), func(m MarketServer) bool { return m.Icon == u }) {
		return true
	}
	if rest, ok := strings.CutPrefix(u, "https://github.com/"); ok && strings.HasSuffix(rest, ".png?size=96") && !strings.Contains(strings.TrimSuffix(rest, ".png?size=96"), "/") {
		return true // an owner's avatar
	}
	if l, err := loadLocked(); err == nil && slices.Contains(slices.Collect(maps.Values(l.Icons)), u) {
		return true // one a server was added with
	}
	seenServers.Lock()
	defer seenServers.Unlock()
	for _, m := range seenServers.m {
		if m.Icon == u {
			return true
		}
	}
	return false
}

// fetchImage downloads an icon through usemagpie.ai's icon proxy, so the
// host a server's author picked never sees the user's IP.
func fetchImage(u string) ([]byte, string, error) {
	b, err := get(iconproxy.URL(u), "image/*", 1<<20)
	if err != nil {
		return nil, "", err
	}
	ct := http.DetectContentType(b)
	if strings.Contains(string(b[:min(len(b), 512)]), "<svg") {
		ct = "image/svg+xml"
	}
	if !strings.HasPrefix(ct, "image/") {
		return nil, "", fmt.Errorf("%s isn't an image", u)
	}
	return b, ct, nil
}

// ---- fetching ---------------------------------------------------------------

var client = &http.Client{Timeout: 20 * time.Second}

// registryTimeout is the MCP Registry's own budget: its search answers in
// 13-37 s often enough (9 of 33 tries past 13 s, 6 past 20 s, the slowest
// 37.3 s - measured in #3043) that the market's 20 s client emptied the
// search with "context deadline exceeded". Its asks go with a longer one;
// skills.sh and the icons keep the short one.
var registryTimeout = 60 * time.Second

// registryClient is the client the registry is asked with, a variable for
// tests.
var registryClient = &http.Client{Timeout: registryTimeout}

// SearchError is a source the market searches that didn't answer; what
// the market has of its own is still listed beside it (#1589).
type SearchError struct {
	Source string // skills.sh, the MCP Registry
	Err    error
}

func (e *SearchError) Error() string { return "couldn't search " + e.Source + ": " + e.Err.Error() }
func (e *SearchError) Unwrap() error { return e.Err }

// retryPause is how long a 503 is waited out before the one more try; a
// variable for tests.
var retryPause = time.Second

var metaDescRe = lazyre.New(`<meta\s+name="description"\s+content="([^"]*)"`)

// metaDescription is what a page's <meta name="description"> says.
func metaDescription(page []byte) string {
	m := metaDescRe.FindSubmatch(page)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(string(m[1])))
}

func get(u, accept string, limit int64) ([]byte, error) {
	return getBy(client, u, accept, limit)
}

// getBy is get with the client: the registry's own budget (#3043).
func getBy(c *http.Client, u, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	// skills.sh answers a client it doesn't know with a page of its own
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 magpie")
	req.Header.Set("Accept", accept)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	// a 503 is a server starting cold or busy for a moment (skills.sh's
	// search answered one, and 200 right after, #1589): it's asked once more
	if resp.StatusCode == http.StatusServiceUnavailable {
		resp.Body.Close()
		time.Sleep(retryPause)
		if resp, err = c.Do(req); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s answered %s", resp.Request.URL.Host, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is too large", u)
	}
	return b, nil
}

func getJSON(u string, v any) error {
	b, err := get(u, "application/json", 8<<20)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// getJSONBy is getJSON with the client (the registry's own budget, #3043).
func getJSONBy(c *http.Client, u string, v any) error {
	b, err := getBy(c, u, "application/json", 8<<20)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// loadLocked reads the library, waiting for a change being made.
func loadLocked() (*Library, error) {
	mu.Lock()
	defer mu.Unlock()
	return load()
}
