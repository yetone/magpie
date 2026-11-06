package library

// A Remote magpie's MCP router is a server in the Library of its own
// (treeman_26897 on Discord: with another magpie as a provider, its MCP
// servers took adding that magpie again under Library → MCP). Once the
// other magpie, its models listed, says its router has a server to give
// (provider/remote_hub.go), the library adds a server named for the
// provider, at that magpie's gateway.HubMCPAll with the provider's key.
// It is then a server as any other: switched on per agent, its tools
// turned off one by one, behind the MCP hub as <name>__<tool>, or written
// into an agent as its own entry with the hub off.
//
//   - It is offered once (BridgesOffered): a user who removes the server
//     doesn't get it back until the provider is removed and added again.
//     One the user switched off for an agent stays off.
//   - A server the user has at that magpie's router already (its host and
//     the router's path) is the one: none is added beside it.
//   - It goes with the provider: once providers.json no longer has it, the
//     server is taken out. A provider turned off keeps its server. A
//     providers.json that can't be read takes nothing out. The provider's
//     address and key, changed, change the server's.
//   - Edited to another address or key, it is the user's own from then on.
//   - It is kept to this computer (Local): the key is this computer's.
//   - Added with the hub on, it is on for the agents given the hub's entry;
//     with the hub off, on for none until the user switches it on.
//   - Two magpies that have each other as Remote magpies don't ask each
//     other round: a router asks another's with gateway.RouterHeader, and
//     a request with it isn't given a server of this kind
//     (gateway.FromRouter).

import (
	"log"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// remoteHubs are provider's, a test's fake in its place.
var remoteHubs = provider.RemoteHubs

func init() {
	provider.RemoteHubAsk = bridgeWanted
	provider.RemoteHubFound = func(id string) {
		if changed, err := adoptBridge(id); err != nil {
			log.Printf("library: giving a Remote magpie's MCP router a server: %v", err)
		} else if changed {
			if _, err := Sync(); err != nil {
				log.Printf("library: syncing after a Remote magpie's MCP router: %v", err)
			}
		}
	}
}

// bridgeURL is the address of the router of the magpie at root.
func bridgeURL(root string) string { return strings.TrimRight(root, "/") + gateway.HubMCPAll }

// bridgeHeaders are the headers its router is asked with.
func bridgeHeaders(key string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + key}
}

// routerAt is whether u is a magpie's router at host (scheme and host).
func routerAt(u, host string) bool {
	p, err := url.Parse(u)
	return err == nil && strings.EqualFold(p.Scheme+"://"+p.Host, host) && strings.HasPrefix(p.Path, gateway.HubMCPAll)
}

// originOf is u's scheme and host.
func originOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Scheme + "://" + p.Host
}

// bridgeWanted is whether the Remote magpie id would be given a server,
// were it to say it serves its router: it wasn't offered one yet.
func bridgeWanted(id string) bool {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	return err == nil && !slices.Contains(l.BridgesOffered, id)
}

// adoptBridge gives the router of the Remote magpie id a server, it having
// said it serves one, saving the library when it changed. It doesn't
// sync.
func adoptBridge(id string) (bool, error) {
	hs, err := remoteHubs()
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(hs, func(h provider.RemoteHub) bool { return h.ID == id })
	if i < 0 {
		return false, nil // not a provider (yet): one being tried in the editor
	}
	h := hs[i]
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return false, err
	}
	if slices.Contains(l.BridgesOffered, id) {
		return false, nil
	}
	l.BridgesOffered = append(l.BridgesOffered, id)
	host := originOf(bridgeURL(h.Root))
	if !slices.ContainsFunc(l.MCP, func(s *Server) bool { return s.Bridge == id || (s.Remote() && routerAt(s.URL, host)) }) {
		l.MCP = append(l.MCP, &Server{Name: l.bridgeName(h), Transport: "http", URL: bridgeURL(h.Root),
			Headers: bridgeHeaders(h.Key), Bridge: id, Local: true, Agents: l.hubAgents()})
	}
	return true, l.save()
}

// adoptBridges brings the library's Remote magpie servers in line with the
// providers: one whose provider is gone is taken out, and its offer with
// it; the others' address and key are the provider's now. It asks no
// magpie; true when it changed the library.
func (l *Library) adoptBridges() bool {
	if len(l.BridgesOffered) == 0 && !slices.ContainsFunc(l.MCP, func(s *Server) bool { return s.Bridge != "" }) {
		return false
	}
	hs, err := remoteHubs()
	if err != nil {
		return false // unknown: nothing taken out
	}
	by := map[string]provider.RemoteHub{}
	for _, h := range hs {
		by[h.ID] = h
	}
	changed := false
	if n := len(l.BridgesOffered); n > 0 {
		l.BridgesOffered = slices.DeleteFunc(l.BridgesOffered, func(id string) bool { _, ok := by[id]; return !ok })
		changed = len(l.BridgesOffered) != n
		if len(l.BridgesOffered) == 0 {
			l.BridgesOffered = nil
		}
	}
	if n := len(l.MCP); n > 0 {
		l.MCP = slices.DeleteFunc(l.MCP, func(s *Server) bool { _, ok := by[s.Bridge]; return s.Bridge != "" && !ok })
		changed = changed || len(l.MCP) != n
	}
	for _, s := range l.MCP {
		h, ok := by[s.Bridge]
		if s.Bridge == "" || !ok {
			continue
		}
		if u := bridgeURL(h.Root); s.URL != u {
			s.URL, changed = u, true
		}
		if want := bridgeHeaders(h.Key); !sameHeaders(s.Headers, want) {
			s.Headers, changed = want, true
		}
	}
	return changed
}

// sameHeaders is whether a and b are the same headers.
func sameHeaders(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// bridgeName is a name no server has, from the provider's name (its id
// when the name gives none): never the hub's own entry's.
func (l *Library) bridgeName(h provider.RemoteHub) string {
	base := slug(h.Name)
	if base == "" {
		base = slug(h.ID)
	}
	if base == "" {
		base = "remote-magpie"
	}
	if base == HubEntry {
		base = "magpie-remote"
	}
	name := base
	for i := 2; l.server(name) != nil; i++ {
		name = base + "-" + strconv.Itoa(i)
	}
	return name
}

// slug is s in a server name's characters, lower case, at most 56 of them.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
		if b.Len() >= 56 {
			break
		}
	}
	return strings.Trim(b.String(), "-_")
}

// keptBridge is s, edited from old, still the Remote magpie's server: its
// address and key as they were. Edited to others, it is the user's own,
// and on this computer still, the key being this computer's.
func keptBridge(old *Server, s *Server) {
	if old.Bridge == "" || s.Bridge != "" {
		return
	}
	s.Local = true
	if s.URL == old.URL && sameHeaders(s.Headers, old.Headers) {
		s.Bridge = old.Bridge
	}
}
