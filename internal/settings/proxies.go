package settings

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// NamedProxy is a proxy the user keeps once, by name, and picks wherever a
// proxy can be chosen: Settings' own, plugin downloads', a provider's, a
// key's, an account's. Each of those holds the reference "@"+ID instead of
// an address, so one edit here applies to all of them. The address has no
// login in it; User and Pass are kept apart, in settings.json (0600) as
// GitHubToken is, and the page is never sent the password. This computer's
// own (KeepOwn), left out of a backup, as Proxy is.
type NamedProxy struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// URL is http://, https:// or socks5:// (host:port means http), with
	// no user and password in it.
	URL  string `json:"url"`
	User string `json:"user,omitempty"`
	Pass string `json:"pass,omitempty"`
}

// ProxyRefPrefix starts a proxy choice that names a NamedProxy by its id.
// No address starts with it, so it never collides with an inline value.
const ProxyRefPrefix = "@"

// IsProxyRef is whether a proxy choice names a saved proxy.
func IsProxyRef(choice string) bool {
	return strings.HasPrefix(strings.TrimSpace(choice), ProxyRefPrefix)
}

// ProxyRef is the choice that names the saved proxy id.
func ProxyRef(id string) string { return ProxyRefPrefix + id }

// ProxyID is the id a reference names, "" for a choice that isn't one.
func ProxyID(choice string) string {
	choice = strings.TrimSpace(choice)
	if !strings.HasPrefix(choice, ProxyRefPrefix) {
		return ""
	}
	return strings.TrimPrefix(choice, ProxyRefPrefix)
}

// Address is the proxy as a request takes it, with its login in it. Never
// shown or logged: netproxy.Redact is what a line of text may carry.
func (p NamedProxy) Address() (string, error) {
	u, err := url.Parse(withScheme(strings.TrimSpace(p.URL)))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("proxy %q has no usable address", p.Name)
	}
	if p.User != "" || p.Pass != "" {
		if p.Pass != "" {
			u.User = url.UserPassword(p.User, p.Pass)
		} else {
			u.User = url.User(p.User)
		}
	}
	return u.String(), nil
}

// ResolveProxy is the address a proxy choice stands for: a reference's
// saved proxy (its login in it), any other choice as it is. ok is false
// for a reference to a proxy that isn't there (deleted, or brought by a
// sync from a computer whose list this one doesn't have): the caller fails
// the request, as a choice never turns into a direct connection.
func ResolveProxy(choice string) (addr string, ok bool) {
	choice = strings.TrimSpace(choice)
	id := ProxyID(choice)
	if id == "" {
		return choice, true
	}
	for _, p := range Shared().Proxies {
		if p.ID == id {
			a, err := p.Address()
			return a, err == nil
		}
	}
	return choice, false
}

// NewProxyID is an id for a saved proxy.
func NewProxyID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return "px" + hex.EncodeToString(b)
}

func validProxyID(id string) bool {
	if id == "" || len(id) > 40 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// checkProxies refuses a list of saved proxies that couldn't be used.
func checkProxies(ps []NamedProxy) error {
	seen := map[string]bool{}
	names := map[string]bool{}
	for i := range ps {
		p := &ps[i]
		p.Name, p.URL = strings.TrimSpace(p.Name), strings.TrimSpace(p.URL)
		if !validProxyID(p.ID) {
			return fmt.Errorf("a saved proxy's id is lower-case letters, digits, - and _, not %q", p.ID)
		}
		if seen[p.ID] {
			return fmt.Errorf("two saved proxies have the id %q", p.ID)
		}
		seen[p.ID] = true
		if p.Name == "" {
			return fmt.Errorf("a saved proxy needs a name")
		}
		k := strings.ToLower(p.Name)
		if names[k] {
			return fmt.Errorf("two saved proxies are named %q", p.Name)
		}
		names[k] = true
		if p.URL == "direct" || strings.HasPrefix(p.URL, ProxyRefPrefix) {
			return fmt.Errorf("proxy %q: type its address, like http://127.0.0.1:7890", p.Name)
		}
		if err := checkAddress(p.URL); err != nil {
			return fmt.Errorf("proxy %q: %w", p.Name, err)
		}
		if u, err := url.Parse(withScheme(p.URL)); err != nil || u.User != nil {
			return fmt.Errorf("proxy %q: the username and password go in their own fields, not in the address", p.Name)
		}
	}
	return nil
}

func withScheme(raw string) string {
	if !strings.Contains(raw, "://") {
		return "http://" + raw
	}
	return raw
}

func checkAddress(p string) error {
	u, err := url.Parse(withScheme(p))
	if err != nil || u.Host == "" || !slices.Contains([]string{"http", "https", "socks5", "socks5h"}, u.Scheme) {
		return fmt.Errorf("proxy must look like http://127.0.0.1:7890 or socks5://127.0.0.1:1080")
	}
	return nil
}

// CheckProxyIn is CheckProxy for a choice made beside the list ps (a
// Settings being saved holds its own).
func CheckProxyIn(p string, ps []NamedProxy) error {
	p = strings.TrimSpace(p)
	if IsProxyRef(p) {
		id := ProxyID(p)
		if !slices.ContainsFunc(ps, func(n NamedProxy) bool { return n.ID == id }) {
			return fmt.Errorf("the saved proxy %q isn't there", id)
		}
		return nil
	}
	return CheckProxy(p)
}

// ProxyLabel is how a saved proxy is named in a message: its name, else the
// name it had (ProxyNames), else its id.
func ProxyLabel(id string) string {
	s := Shared()
	for _, p := range s.Proxies {
		if p.ID == id {
			return p.Name
		}
	}
	if n := s.ProxyNames[id]; n != "" {
		return n
	}
	return id
}

func rememberProxyNames(m map[string]string, ps []NamedProxy) map[string]string {
	for _, p := range ps {
		if m == nil {
			m = map[string]string{}
		}
		m[p.ID] = p.Name
	}
	return m
}

// WithoutProxyPasswords is ps with each password left out: what goes
// where credentials don't.
func WithoutProxyPasswords(ps []NamedProxy) []NamedProxy {
	out := slices.Clone(ps)
	for i := range out {
		out[i].Pass = ""
	}
	return out
}

// MergeProxies is cur with the proxies another computer sent put in: one
// with the same id takes the sent name, address and login (its password
// too when keys, else it keeps cur's); one cur lacks is added, its name
// made unique here. A proxy only cur has stays: a sync never takes away
// what a choice here may name.
func MergeProxies(cur, in []NamedProxy, keys bool) []NamedProxy {
	out := slices.Clone(cur)
	taken := map[string]bool{}
	for _, p := range out {
		taken[strings.ToLower(p.Name)] = true
	}
	for _, n := range in {
		if i := slices.IndexFunc(out, func(p NamedProxy) bool { return p.ID == n.ID }); i >= 0 {
			old := out[i]
			if !keys {
				n.Pass = old.Pass
			}
			if !strings.EqualFold(old.Name, n.Name) && taken[strings.ToLower(n.Name)] {
				n.Name = old.Name
			}
			out[i] = n
			continue
		}
		if !keys {
			n.Pass = ""
		}
		base, k := n.Name, 2
		for taken[strings.ToLower(n.Name)] {
			n.Name = fmt.Sprintf("%s %d", base, k)
			k++
		}
		taken[strings.ToLower(n.Name)] = true
		out = append(out, n)
	}
	return out
}

// RestoreProxies puts the proxies another computer sent into this one's
// (MergeProxies), where a restore or a sync brings them.
func RestoreProxies(in []NamedProxy, keys bool) error {
	s := Load()
	merged := MergeProxies(s.Proxies, in, keys)
	if slices.Equal(merged, s.Proxies) {
		return nil
	}
	s.Proxies = merged
	return Save(s)
}
