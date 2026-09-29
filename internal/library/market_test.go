package library

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func registryEntry(t *testing.T, s string) *registryServer {
	t.Helper()
	var r registryServer
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func TestFromRegistry(t *testing.T) {
	// a remote one: its fixed header kept, the one with a secret asked for
	m, ok := fromRegistry(registryEntry(t, `{
		"name": "io.github.acme/weather-mcp-server", "description": "Weather",
		"repository": {"url": "https://github.com/acme/weather"},
		"remotes": [
			{"type": "sse", "url": "https://acme.dev/sse"},
			{"type": "streamable-http", "url": "https://acme.dev/mcp", "headers": [
				{"name": "X-Client", "value": "magpie"},
				{"name": "Authorization", "value": "Bearer {token}", "isRequired": true, "isSecret": true}
			]}
		]}`))
	if !ok || m.Name != "weather" || m.Title != "weather" || m.Publisher != "acme" || m.Homepage != "https://github.com/acme/weather" ||
		m.Icon != gh("acme") || m.Transport != "http" || m.server.URL != "https://acme.dev/mcp" || m.server.Headers["X-Client"] != "magpie" {
		t.Fatalf("remote: %+v %v", m, ok)
	}
	if len(m.Inputs) != 1 || m.Inputs[0].Key != "Authorization" || m.Inputs[0].format != "Bearer {}" || !m.Inputs[0].Secret {
		t.Fatalf("remote inputs: %+v", m.Inputs)
	}

	// a package: npm before pypi, its secret env asked for, a required
	// positional argument asked for, one with a value passed as it is
	m, ok = fromRegistry(registryEntry(t, `{
		"name": "com.example/files", "icons": [{"src": "http://example.com/i.png"}, {"src": "https://example.com/i.png"}],
		"packages": [
			{"registryType": "pypi", "identifier": "files-py"},
			{"registryType": "npm", "identifier": "@example/files", "transport": {"type": "stdio"},
			 "environmentVariables": [{"name": "FILES_KEY", "isSecret": true, "isRequired": true}, {"name": "OPTIONAL"}],
			 "packageArguments": [
				{"type": "named", "name": "--mode", "value": "ro"},
				{"type": "positional", "valueHint": "directory", "isRequired": true}
			 ]}
		]}`))
	if !ok || m.Runs != "npx" || m.Publisher != "example.com" || m.Icon != "https://example.com/i.png" ||
		!slices.Equal(m.server.Args, []string{"-y", "@example/files", "--mode", "ro"}) {
		t.Fatalf("npm: %+v %v", m, ok)
	}
	if len(m.Inputs) != 2 || m.Inputs[0].Key != "FILES_KEY" || m.Inputs[0].Where != "env" || m.Inputs[1].Where != "arg" || m.Inputs[1].Label != "directory" {
		t.Fatalf("npm inputs: %+v", m.Inputs)
	}

	// docker: its env passed through by name, the image last
	m, ok = fromRegistry(registryEntry(t, `{"name": "io.github.o/db", "packages": [
		{"registryType": "oci", "identifier": "ghcr.io/o/db:1", "environmentVariables": [{"name": "DB_URL", "isRequired": true}]}]}`))
	if !ok || m.Icon != gh("o") || !slices.Equal(m.server.Args, []string{"run", "-i", "--rm", "-e", "DB_URL", "ghcr.io/o/db:1"}) {
		t.Fatalf("oci: %+v %v", m, ok)
	}

	// what magpie can't start: a URL with a variable in it, a package over
	// another transport, a named argument it would have to make up
	for _, s := range []string{
		`{"name": "a/b", "remotes": [{"type": "streamable-http", "url": "https://{tenant}.acme.dev/mcp"}]}`,
		`{"name": "a/b", "packages": [{"registryType": "npm", "identifier": "x", "transport": {"type": "streamable-http"}}]}`,
		`{"name": "a/b", "packages": [{"registryType": "npm", "identifier": "x", "packageArguments": [{"type": "named", "name": "--db", "isRequired": true}]}]}`,
		`{"name": "a/b", "packages": [{"registryType": "nuget", "identifier": "x"}]}`,
	} {
		if m, ok := fromRegistry(registryEntry(t, s)); ok {
			t.Errorf("%s taken: %+v", s, m)
		}
	}
}

func TestShortNameAndKey(t *testing.T) {
	for in, want := range map[string]string{
		"io.github.upstash/context7-mcp":         "context7",
		"io.github.x/mcp-server-postgres":        "postgres",
		"com.example/Weather_MCP":                "weather",
		"io.github.x/mcp":                        "mcp",
		"io.github.x/" + strings.Repeat("a", 80): strings.Repeat("a", 64),
	} {
		if got := shortName(in); got != want {
			t.Errorf("shortName(%s) = %s, want %s", in, got, want)
		}
	}
	for want, s := range map[string]Server{
		"https://acme.dev/mcp":       {Transport: "http", URL: "https://ACME.dev/mcp/"},
		"npx @scope/pkg":             {Command: "npx", Args: []string{"-y", "@scope/pkg@latest", "--flag"}},
		"docker ghcr.io/o/db:1":      {Command: "docker", Args: []string{"run", "-i", "--rm", "ghcr.io/o/db:1"}},
		"uvx mcp-server-fetch":       {Command: "uvx", Args: []string{"mcp-server-fetch"}},
		"/usr/local/bin/some-server": {Command: "/usr/local/bin/some-server"},
	} {
		if got := serverKey(&s); got != want {
			t.Errorf("serverKey(%+v) = %s, want %s", s, got, want)
		}
	}
}

func TestMetaDescription(t *testing.T) {
	for page, want := range map[string]string{
		`<head><meta name="description" content="Turns &amp; checks PDFs &quot;fast&quot;"/></head>`: `Turns & checks PDFs "fast"`,
		`<meta name="description"  content="  spaced  ">`:                                            "spaced",
		`<meta property="og:description" content="not this one">`:                                    "",
		``: "",
	} {
		if got := metaDescription([]byte(page)); got != want {
			t.Errorf("%s: %q, want %q", page, got, want)
		}
	}
}

func TestIcons(t *testing.T) {
	github := featured[slices.IndexFunc(featured, func(m MarketServer) bool { return m.ID == "github" })]
	l := &Library{Icons: map[string]string{"npx @acme/tool": "https://acme.dev/icon.png"}}
	for _, c := range []struct {
		s    Server
		want string
	}{
		{github.server, github.Icon}, // the featured server it is, under any name
		{Server{Name: "mine", Transport: "http", URL: strings.ToUpper(github.server.URL)}, github.Icon},
		{Server{Name: "tool", Command: "npx", Args: []string{"-y", "@acme/tool"}}, "https://acme.dev/icon.png"}, // the one it was added from
		{Server{Name: "GitHub", Command: "node", Args: []string{"gh.js"}}, github.Icon},                         // named after one
		{Server{Name: "something", Command: "node", Args: []string{"x.js"}}, ""},                                // none made up
	} {
		if got := serverIcon(l, &c.s); got != c.want {
			t.Errorf("serverIcon(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
	for _, c := range []struct {
		s    Skill
		want string
	}{
		{Skill{Source: &Source{Kind: "github", Repo: "mattpocock/skills", Path: "grill-me"}}, gh("mattpocock")},
		{Skill{Source: &Source{Kind: "folder", Dir: "/x"}}, ""},
		{Skill{}, ""},
	} {
		if got := skillIcon(&c.s); got != c.want {
			t.Errorf("skillIcon(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestOffered(t *testing.T) {
	sandbox(t)
	for u, want := range map[string]bool{
		featured[0].Icon:                          true,
		gh("someone"):                             true,
		"https://github.com/a/b.png?size=96":      false, // not an owner's
		"http://github.com/someone.png?size=96":   false,
		"https://evil.example/tracker.png":        false,
		"https://github.com/someone.png?size=960": false,
	} {
		if got := offered(u); got != want {
			t.Errorf("offered(%s) = %v", u, got)
		}
	}
}

// skillsSh is skills.sh as the market reads it: its front page with the
// list in its data, and a page for each skill.
func skillsSh(t *testing.T) *atomic.Int64 {
	t.Helper()
	hits := new(atomic.Int64) // the pages are asked for at once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/":
			list := `[{"source":"o/r","skillId":"pdf","name":"pdf","installs":9},{"source":"not a repo","skillId":"x","name":"x"},{"source":"o/r","skillId":"","name":"y"}]`
			js, _ := json.Marshal(`{"initialSkills":` + list + `}`)
			w.Write([]byte(`<html><script>self.__next_f.push([1,` + string(js) + `])</script></html>`))
		case "/o/r/pdf":
			w.Write([]byte(`<meta name="description" content="Reads &amp; fills PDFs">`))
		case "/o/r/plain":
			w.Write([]byte(`<html>no description</html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	was := skillsShURL
	skillsShURL = srv.URL
	t.Cleanup(func() { skillsShURL = was })
	abouts.Lock()
	abouts.m, abouts.loaded = map[string]string{}, false
	abouts.Unlock()
	return hits
}

func TestSkillsShPages(t *testing.T) {
	sandbox(t)
	hits := skillsSh(t)
	list, err := fetchPopular()
	if err != nil || len(list) != 1 || list[0].Source != "o/r" || list[0].SkillID != "pdf" || list[0].Installs != 9 {
		t.Fatalf("popular: %+v %v", list, err)
	}

	got := SkillsAbout([]string{"o/r/pdf", "o/r/plain", "o/r/missing", "../../etc", "o/r"})
	if len(got) != 1 || got["o/r/pdf"] != "Reads & fills PDFs" {
		t.Fatalf("about: %v", got)
	}
	// known ones come from what was kept, on disk too
	n := hits.Load()
	abouts.Lock()
	abouts.m, abouts.loaded = map[string]string{}, false
	abouts.Unlock()
	if got := SkillsAbout([]string{"o/r/pdf"}); got["o/r/pdf"] != "Reads & fills PDFs" || hits.Load() != n {
		t.Fatalf("kept: %v, %d fetches", got, hits.Load()-n)
	}
}

func TestInstallFromRegistry(t *testing.T) {
	sandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search") != "acme" {
			t.Errorf("searched for %q", r.URL.RawQuery)
		}
		w.Write([]byte(`{"servers": [
			{"server": {"name": "io.github.acme/acme-mcp", "title": "Acme", "icons": [{"src": "https://acme.dev/logo.png"}],
			  "remotes": [{"type": "streamable-http", "url": "https://acme.dev/mcp",
			    "headers": [{"name": "Authorization", "value": "Bearer {key}", "isRequired": true, "isSecret": true}]}]}},
			{"server": {"name": "io.github.acme/old", "remotes": [{"type": "streamable-http", "url": "https://acme.dev/old"}]},
			 "_meta": {"io.modelcontextprotocol.registry/official": {"status": "deleted"}}}
		]}`))
	}))
	defer srv.Close()
	was := registryURL
	registryURL = srv.URL
	defer func() { registryURL = was }()

	list, err := MarketServers("acme")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(list, func(m MarketServer) bool { return m.ID == "io.github.acme/acme-mcp" })
	if i < 0 || slices.ContainsFunc(list, func(m MarketServer) bool { return m.ID == "io.github.acme/old" }) {
		t.Fatalf("found: %+v", list)
	}
	if list[i].Have != "" || !offered("https://acme.dev/logo.png") {
		t.Fatalf("before: %+v", list[i])
	}

	if _, err := InstallServer("io.github.acme/acme-mcp", nil, []string{"claude"}); err == nil || !strings.Contains(err.Error(), "Authorization") {
		t.Fatalf("installed without its key: %v", err)
	}
	ok(t)(InstallServer("io.github.acme/acme-mcp", map[string]string{"Authorization": " k-123 "}, []string{"claude"}))
	if _, err := InstallServer("io.github.acme/acme-mcp", map[string]string{"Authorization": "k"}, []string{"claude"}); err == nil {
		t.Fatal("installed twice")
	}

	l, err := loadLocked()
	if err != nil || len(l.MCP) != 1 {
		t.Fatalf("library: %+v %v", l, err)
	}
	s := l.MCP[0]
	if s.Name != "acme" || s.URL != "https://acme.dev/mcp" || s.Headers["Authorization"] != "Bearer k-123" || !slices.Equal(s.Agents, []string{"claude"}) {
		t.Fatalf("server: %+v", s)
	}
	// its icon stays with it, and stays one magpie will fetch, once the
	// market has forgotten it
	seenServers.Lock()
	clear(seenServers.m)
	seenServers.Unlock()
	if serverIcon(l, s) != "https://acme.dev/logo.png" || !offered("https://acme.dev/logo.png") {
		t.Fatalf("icon: %q", serverIcon(l, s))
	}
	v, err := Read(nil)
	if err != nil || len(v.Servers) != 1 || v.Servers[0].Icon != "https://acme.dev/logo.png" {
		t.Fatalf("view: %+v %v", v, err)
	}
	if list, _ := MarketServers("acme"); !slices.ContainsFunc(list, func(m MarketServer) bool { return m.Have == "acme" }) {
		t.Fatalf("not marked as added: %+v", list)
	}
}

func TestMagpieImageIsOptIn(t *testing.T) {
	sandbox(t)
	ok(t)(InstallServer("magpie-image", nil, nil))
	list, err := MarketServers("")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(list, func(m MarketServer) bool { return m.ID == "magpie-image" })
	if i < 0 || !list[i].OptIn || list[i].Have != "magpie-image" {
		t.Fatalf("market: %+v", list)
	}
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	s := l.server("magpie-image")
	if s == nil || len(s.Agents) != 0 || !filepath.IsAbs(s.Command) || strings.Join(s.Args, " ") != "mcp image" {
		t.Fatalf("added as %+v", s)
	}
}
