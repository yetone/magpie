package library

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/lazyre"
)

// A server the MCP Registry doesn't list can still be at the address
// searched for: a Python one in a repository's folder, as
// https://github.com/volcengine/mcp-server#subdirectory=server/mcp_server_askecho_search_infinity
// (CyberCaius on Discord), or a package on PyPI. Its pyproject.toml says
// the program it installs, and its README the environment it reads, so it
// is offered filled in: uvx --from git+https://github.com/o/r#subdirectory=p
// <program>, with the README's keys to fill in.

// repoAt is a folder of a GitHub repository a search names.
type repoAt struct {
	Repo string // owner/name, as written
	Ref  string // "" for the default branch
	Path string // the folder, "" for the top
}

var ghRepoRe = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+$`)

// parseRepoAt reads a GitHub address as a repository's folder:
// github.com/o/r, with #subdirectory=p (pip's and uv's way), /tree/ref/p,
// /blob/ref/p/file, or git+https://github.com/o/r@ref; the case kept.
func parseRepoAt(q string) (repoAt, bool) {
	q = strings.TrimSpace(q)
	q = strings.TrimPrefix(q, "git+")
	frag := ""
	if i := strings.Index(q, "#"); i >= 0 {
		q, frag = q[:i], q[i+1:]
	}
	if i := strings.Index(q, "?"); i >= 0 {
		q = q[:i]
	}
	for _, p := range []string{"https://", "http://", "ssh://git@", "git@"} {
		q = strings.TrimPrefix(q, p)
	}
	q = strings.TrimPrefix(q, "www.")
	host, rest, ok := strings.Cut(q, "/")
	if host == "github.com:" || strings.HasPrefix(host, "github.com:") { // git@github.com:o/r
		host, rest, ok = "github.com", strings.TrimPrefix(q, "github.com:"), true
	}
	if !ok || !strings.EqualFold(host, "github.com") {
		return repoAt{}, false
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 {
		return repoAt{}, false
	}
	var a repoAt
	name := strings.TrimSuffix(parts[1], ".git")
	if n, ref, found := strings.Cut(name, "@"); found {
		name, a.Ref = strings.TrimSuffix(n, ".git"), ref
	}
	a.Repo = parts[0] + "/" + name
	if !ghRepoRe.MatchString(a.Repo) {
		return repoAt{}, false
	}
	if len(parts) >= 4 && (parts[2] == "tree" || parts[2] == "blob") {
		a.Ref = parts[3]
		sub := parts[4:]
		if parts[2] == "blob" && len(sub) > 0 {
			sub = sub[:len(sub)-1] // the file's folder
		}
		a.Path = strings.Join(sub, "/")
	}
	if vals, err := url.ParseQuery(frag); err == nil && vals.Get("subdirectory") != "" {
		a.Path = vals.Get("subdirectory")
	}
	a.Path = strings.Trim(a.Path, "/")
	if strings.Contains(a.Path, "..") {
		return repoAt{}, false
	}
	return a, true
}

// gitURL is the repository's folder as uv installs from it.
func (a repoAt) gitURL() string {
	u := "git+https://github.com/" + a.Repo
	if a.Ref != "" {
		u += "@" + a.Ref
	}
	if a.Path != "" {
		u += "#subdirectory=" + a.Path
	}
	return u
}

func (a repoAt) file(name string) string {
	if a.Path == "" {
		return name
	}
	return a.Path + "/" + name
}

type pyProject struct {
	Project struct {
		Name        string            `toml:"name"`
		Description string            `toml:"description"`
		Scripts     map[string]string `toml:"scripts"`
	} `toml:"project"`
}

// program is the one a project installs that runs its server: the only
// one, the one named as the project, or the one that says mcp.
func (p *pyProject) program() string {
	names := slices.Sorted(func(yield func(string) bool) {
		for k := range p.Project.Scripts {
			if !yield(k) {
				return
			}
		}
	})
	switch {
	case len(names) == 0:
		return ""
	case len(names) == 1:
		return names[0]
	case slices.Contains(names, p.Project.Name):
		return p.Project.Name
	}
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), "mcp") {
			return n
		}
	}
	return names[0]
}

// fromRepo is the Python server in a repository's folder, from its
// pyproject.toml and README; false when the folder has no project with a
// program to run.
func fromRepo(a repoAt) (MarketServer, bool) {
	b, err := get(rawURL(a.Repo, a.Ref, a.file("pyproject.toml")), "text/plain", 1<<20)
	if err != nil {
		return MarketServer{}, false
	}
	var p pyProject
	if toml.Unmarshal(b, &p) != nil {
		return MarketServer{}, false
	}
	prog := p.program()
	if prog == "" {
		return MarketServer{}, false
	}
	readme, _ := get(rawURL(a.Repo, a.Ref, a.file("README.md")), "text/plain", 2<<20)
	owner, _, _ := strings.Cut(a.Repo, "/")
	home := "https://github.com/" + a.Repo
	if a.Path != "" {
		ref := a.Ref
		if ref == "" {
			ref = "HEAD"
		}
		home += "/tree/" + ref + "/" + a.Path
	}
	name := p.Project.Name
	if name == "" {
		name = prog
	}
	m := MarketServer{ID: "github.com/" + a.Repo + pathPart(a), Name: shortName(name), Title: name, Description: p.Project.Description,
		Publisher: owner, Icon: gh(owner), Homepage: home, Transport: "stdio", Runs: "uvx", Match: true,
		Inputs: envInputs(readme)}
	m.server = Server{Name: m.Name, Transport: "stdio", Command: "uvx", Args: []string{"--from", a.gitURL(), prog}}
	return m, true
}

func pathPart(a repoAt) string {
	s := ""
	if a.Ref != "" {
		s += "@" + a.Ref
	}
	if a.Path != "" {
		s += "#subdirectory=" + a.Path
	}
	return s
}

// pypiURL is PyPI's JSON API; a variable for tests.
var pypiURL = "https://pypi.org/pypi"

var pypiNameRe = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// fromPyPI is the server a PyPI package of that name runs, uvx <name>,
// with what its description (its README) says it reads from the
// environment; false when PyPI has no such package.
func fromPyPI(name string) (MarketServer, bool) {
	if !pypiNameRe.MatchString(name) {
		return MarketServer{}, false
	}
	var body struct {
		Info struct {
			Name        string            `json:"name"`
			Summary     string            `json:"summary"`
			Description string            `json:"description"`
			HomePage    string            `json:"home_page"`
			ProjectURLs map[string]string `json:"project_urls"`
		} `json:"info"`
	}
	if getJSON(pypiURL+"/"+url.PathEscape(name)+"/json", &body) != nil || body.Info.Name == "" {
		return MarketServer{}, false
	}
	i := body.Info
	home := i.HomePage
	for _, k := range []string{"Repository", "Source", "Homepage", "Source Code", "Code"} {
		if home == "" && i.ProjectURLs[k] != "" {
			home = i.ProjectURLs[k]
		}
	}
	if home == "" {
		home = "https://pypi.org/project/" + i.Name + "/"
	}
	m := MarketServer{ID: "pypi.org/project/" + strings.ToLower(i.Name), Name: shortName(i.Name), Title: i.Name, Description: i.Summary,
		Homepage: home, Transport: "stdio", Runs: "uvx", Match: true, Inputs: envInputs([]byte(i.Description))}
	if o := githubOwner(home); o != "" {
		m.Publisher, m.Icon = o, gh(o)
	}
	m.server = Server{Name: m.Name, Transport: "stdio", Command: "uvx", Args: []string{i.Name}}
	m.addrs = addrsOf(i.Name, "pypi.org/project/"+i.Name)
	return m, true
}

var (
	envBlockRe = lazyre.New(`"env"\s*:\s*\{`)
	envKeyRe   = lazyre.New(`^[A-Z][A-Z0-9_]*$`)
	secretRe   = lazyre.New(`KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH`)
	fenceRe    = lazyre.New("^\\s*(```|~~~)")
)

// envInputs are the environment variables a README's configuration
// examples give the server ("env": {…} in an mcpServers block), in the
// order it gives them. Each says what the README's prose says about it.
// One alone is required; of several none is, since a README often has
// the user pick one way to sign in of two (an API key, or an access key
// and secret).
func envInputs(readme []byte) []Input {
	var keys []string
	vals := map[string]string{}
	for _, loc := range envBlockRe.FindAllIndex(readme, -1) {
		dec := json.NewDecoder(bytes.NewReader(readme[loc[1]-1:]))
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			continue
		}
		td := json.NewDecoder(bytes.NewReader(raw))
		if _, err := td.Token(); err != nil { // {
			continue
		}
		for td.More() {
			k, err := td.Token()
			if err != nil {
				break
			}
			var v any
			if td.Decode(&v) != nil {
				break
			}
			key, _ := k.(string)
			if !envKeyRe.MatchString(key) || slices.Contains(keys, key) {
				continue
			}
			keys = append(keys, key)
			if s, ok := v.(string); ok {
				vals[key] = s
			}
		}
	}
	about := proseAbout(readme, keys)
	ins := []Input{}
	for _, k := range keys {
		in := Input{Key: k, Where: "env", Label: k, Description: about[k], Secret: secretRe.MatchString(k), Required: len(keys) == 1}
		if v := vals[k]; v != "" && !in.Secret && !placeholderRe.MatchString(v) && !strings.ContainsAny(v, "<>") && !strings.HasPrefix(strings.ToLower(v), "your") {
			in.Placeholder = v
		}
		ins = append(ins, in)
	}
	return ins
}

// proseAbout is, for each key, the first line outside a code block of the
// README that names it, cut short.
func proseAbout(readme []byte, keys []string) map[string]string {
	out := map[string]string{}
	in := false
	sc := bufio.NewScanner(bytes.NewReader(readme))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if fenceRe.MatchString(line) {
			in = !in
			continue
		}
		if in {
			continue
		}
		for _, k := range keys {
			if out[k] == "" && strings.Contains(line, k) {
				t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#-*>|"))
				if r := []rune(t); len(r) > 200 {
					t = string(r[:200]) + "…"
				}
				out[k] = t
			}
		}
	}
	return out
}

// keep has a server found outside the registry installable by its id, as
// offer does the registry's.
func keep(m MarketServer) MarketServer {
	seenServers.Lock()
	defer seenServers.Unlock()
	seenServers.m[m.ID] = m
	return m
}

func matched(list []MarketServer) bool {
	return slices.ContainsFunc(list, func(m MarketServer) bool { return m.Match })
}

// searchAt finds the servers at an address. A repository's folder is read
// first, since the registry lists no server by its folder; a repository is
// read when nothing the registry lists is at it, and a PyPI page asks PyPI.
func searchAt(raw, addr string) ([]MarketServer, error) {
	ra, isRepo := parseRepoAt(raw)
	if isRepo && ra.Path != "" {
		if m, ok := fromRepo(ra); ok {
			return []MarketServer{keep(m)}, nil
		}
	}
	found, err := searchRegistryAt(addr)
	if matched(found) {
		return found, err
	}
	var m MarketServer
	ok := false
	switch {
	case isRepo:
		m, ok = fromRepo(ra)
	case strings.Contains(strings.ToLower(raw), "pypi.org/project/"):
		m, ok = fromPyPI(lastPart(addr))
	}
	if ok {
		return append([]MarketServer{keep(m)}, found...), nil
	}
	return found, err
}

// searchWords is the registry's search, and, for a search that could be a
// server's package on PyPI (one word naming mcp with a - _ or . in it, so
// not PyPI's mcp, the SDK), that package when the
// registry lists nothing by that name: the registry doesn't list every
// server PyPI has (mcp-server-askecho-search-infinity).
func searchWords(q string) ([]MarketServer, error) {
	if !strings.Contains(q, "mcp") || !strings.ContainsAny(q, "-_.") || !pypiNameRe.MatchString(q) {
		return searchRegistry(q)
	}
	type res struct {
		m  MarketServer
		ok bool
	}
	ch := make(chan res, 1)
	go func() {
		m, ok := fromPyPI(q)
		ch <- res{m, ok}
	}()
	found, err := searchRegistry(q)
	p := <-ch
	if !p.ok || slices.ContainsFunc(found, func(m MarketServer) bool {
		return strings.EqualFold(packageOf(&m.server), p.m.Title) || m.Name == p.m.Name
	}) {
		return found, err
	}
	p.m.Match = false // a name, not an address
	return append([]MarketServer{keep(p.m)}, found...), err
}
