package library

import (
	"regexp"
	"slices"
	"strings"
)

// A search in the market can be an address: a server's repository
// (https://github.com/crystaldba/postgres-mcp, or just crystaldba/postgres-mcp),
// its page, its endpoint (https://mcp.notion.com/mcp) or its package
// (@playwright/mcp, npmjs.com/package/…, pypi.org/project/…, ghcr.io/…). It
// finds the servers at that address, whatever they are called.

// codeHosts keep repositories and images under owner/name, so an address
// there says the names to look for.
var codeHosts = map[string]bool{
	"github.com": true, "gitlab.com": true, "bitbucket.org": true, "codeberg.org": true, "gitee.com": true,
	"ghcr.io": true, "docker.io": true, "quay.io": true,
}

var (
	schemeRe    = regexp.MustCompile(`^[a-z][a-z0-9+.-]*://`)
	ownerRepoRe = regexp.MustCompile(`^@?[a-z0-9][a-z0-9_.-]*/[a-z0-9_.-]+$`)
	hostRe      = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)+(:\d+)?$`)
)

// normAddr is an address as it's compared: lower case, with no scheme, user,
// www., query, fragment, trailing slash or .git; a package's page is the
// package (npmjs.com/package/x is x), and an image has no tag.
func normAddr(s string) string {
	a := strings.ToLower(strings.TrimSpace(s))
	if a == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(a, "git@"); ok { // git@github.com:o/r.git
		a = strings.Replace(rest, ":", "/", 1)
	}
	if schemeRe.MatchString(a) {
		a = schemeRe.ReplaceAllString(a, "")
		host, rest, ok := strings.Cut(a, "/")
		if _, h, found := strings.Cut(host, "@"); found {
			host = h // user@host
		}
		if a = host; ok {
			a += "/" + rest
		}
	}
	if i := strings.IndexAny(a, "?#"); i >= 0 {
		a = a[:i]
	}
	a = strings.TrimPrefix(a, "www.")
	a = strings.TrimRight(a, "/")
	a = strings.TrimSuffix(a, ".git")
	for _, p := range []string{"npmjs.com/package/", "npmjs.org/package/", "pypi.org/project/", "hub.docker.com/r/", "hub.docker.com/_/", "docker.io/library/", "docker.io/", "registry.hub.docker.com/r/"} {
		if rest, ok := strings.CutPrefix(a, p); ok {
			a = rest
			break
		}
	}
	// a package's version or an image's tag: @scope/pkg@1.2, pkg@latest, img:tag
	last := a[strings.LastIndex(a, "/")+1:]
	if i := strings.LastIndex(last, "@"); i > 0 {
		a = a[:len(a)-len(last)+i]
		last = last[:i]
	}
	if i := strings.LastIndex(last, ":"); i > 0 && strings.Contains(a, "/") {
		a = a[:len(a)-len(last)+i]
	}
	return strings.TrimRight(a, "/")
}

// address is a search read as an address, normalised; "" when it reads as
// words to look for.
func address(q string) string {
	q = strings.TrimSpace(q)
	if q == "" || strings.ContainsAny(q, " \t\n") {
		return ""
	}
	low := strings.ToLower(q)
	a := normAddr(q)
	if a == "" {
		return ""
	}
	host, _, hasPath := strings.Cut(a, "/")
	switch {
	case schemeRe.MatchString(low) || strings.HasPrefix(low, "git@"):
	case strings.HasPrefix(low, "www."):
	case hasPath && hostRe.MatchString(host): // github.com/o/r, ghcr.io/o/i
	case ownerRepoRe.MatchString(a): // o/r, @scope/pkg
	default:
		return ""
	}
	return a
}

// addrsOf are a server's addresses, normalised, the empty ones left out.
func addrsOf(list ...string) []string {
	var out []string
	for _, s := range list {
		if a := normAddr(s); a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// packageOf is the package a local server starts: @playwright/mcp for
// npx -y @playwright/mcp@latest, the image for docker run.
func packageOf(s *Server) string {
	if s.Remote() || len(s.Args) == 0 {
		return ""
	}
	if s.Command == "docker" {
		return s.Args[len(s.Args)-1]
	}
	for _, a := range s.Args {
		if a == "" || strings.HasPrefix(a, "-") || a == "run" {
			continue
		}
		if i := strings.LastIndex(a, "@"); i > 0 {
			a = a[:i]
		}
		return a
	}
	return ""
}

// hostOf is an address's host, when it has one.
func hostOf(a string) string {
	host, _, _ := strings.Cut(a, "/")
	if strings.HasPrefix(a, "@") || !strings.Contains(host, ".") {
		return ""
	}
	return host
}

// addrScore is how well an address searched for is one of a server's: 3 it
// is, 2 it is under it (a repository for a server in one of its folders),
// 1 the server's is under it (a file in its repository); 0 neither. One
// with no host (owner/repo) is matched on any host.
func addrScore(q string, addrs []string) int {
	best := 0
	for _, e := range addrs {
		cands := []string{e}
		if hostOf(q) == "" && hostOf(e) != "" {
			_, rest, _ := strings.Cut(e, "/")
			cands = append(cands, rest)
		}
		for _, c := range cands {
			if c == "" {
				continue
			}
			depth := strings.Count(c, "/")
			if hostOf(c) == "" {
				depth++ // owner/repo is as deep as github.com/owner/repo
			}
			switch {
			case c == q:
				best = max(best, 3)
			case strings.HasPrefix(c, q+"/"):
				best = max(best, 2)
			case depth >= 2 && strings.HasPrefix(q, c+"/"): // not under a bare host or owner
				best = max(best, 1)
			}
		}
	}
	return best
}

// addrTerms are the names the registry is asked for to find the servers at
// an address: a repository's or package's name and then its owner's, or a
// site's name (context7 for mcp.context7.com/mcp).
func addrTerms(a string) []string {
	var terms []string
	add := func(t string) {
		t = strings.TrimPrefix(t, "@")
		if len(t) >= 2 && !slices.Contains(terms, t) && len(terms) < 2 {
			terms = append(terms, t)
		}
	}
	host := hostOf(a)
	path := a
	if host != "" {
		_, path, _ = strings.Cut(a, "/")
	}
	parts := strings.Split(path, "/")
	switch {
	case host == "" || codeHosts[host]:
		if len(parts) >= 2 {
			add(parts[1])
		}
		add(parts[0])
	default:
		add(siteName(host))
		add(parts[len(parts)-1])
	}
	return terms
}

// siteName is what a host is called: example for mcp.example.com.
func siteName(host string) string {
	host, _, _ = strings.Cut(host, ":")
	labels := strings.Split(host, ".")
	if len(labels) > 1 {
		labels = labels[:len(labels)-1] // the top-level domain
	}
	for i := len(labels) - 1; i >= 0; i-- {
		switch labels[i] {
		case "www", "mcp", "api", "app", "docs", "server", "co", "com":
			continue
		}
		return labels[i]
	}
	return ""
}

// Custom is a server to add by hand at an address nothing the market lists
// is at: a remote one at an endpoint, or one named for its repository whose
// command is filled in.
type Custom struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	URL       string   `json:"url,omitempty"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
}

// CustomAt is the server to add by hand for a search, when it's an address
// and none of what was found is at it.
func CustomAt(q string, found []MarketServer) *Custom {
	a := address(q)
	if a == "" || slices.ContainsFunc(found, func(m MarketServer) bool { return m.Match }) {
		return nil
	}
	q = strings.TrimSpace(q)
	low := strings.ToLower(q)
	host := hostOf(a)
	parts := strings.Split(a, "/")
	name := parts[len(parts)-1]
	switch {
	case strings.Contains(low, "npmjs.com/package/") || strings.Contains(low, "npmjs.org/package/") || strings.HasPrefix(a, "@"):
		return &Custom{Name: shortName(name), Transport: "stdio", Command: "npx", Args: []string{"-y", a}}
	case strings.Contains(low, "pypi.org/project/"):
		return &Custom{Name: shortName(name), Transport: "stdio", Command: "uvx", Args: []string{a}}
	case host != "" && !codeHosts[host] && (strings.HasPrefix(low, "https://") || strings.HasPrefix(low, "http://")):
		return &Custom{Name: shortName(siteName(host)), Transport: "http", URL: q}
	case host != "" && codeHosts[host] && len(parts) >= 3:
		name = parts[2]
	case host == "" && len(parts) >= 2:
		name = parts[1]
	}
	return &Custom{Name: shortName(name), Transport: "stdio"}
}
