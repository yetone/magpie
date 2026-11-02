package library

// What the router tells an agent of each server behind it
// (packinggoodsllc_07668 on Discord runs magpies that only share MCP
// servers, and an agent given several routers knew each by its name
// alone): the server's own title and first words, and its tools with the
// first clause of each description, as it last listed them. They are kept
// in a file of their own, so a router started again describes its servers
// before it has run any of them; they are what the servers said, not
// magpie's words, and no model writes them.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// hubCard is what the router last learnt of one server, for the server as
// it was then (Key: an edit or a sign-in since is another server to it).
type hubCard struct {
	Key   string `json:"key"`
	Title string `json:"title,omitempty"`
	Says  string `json:"says,omitempty"`
	Tools []Tool `json:"tools"`
}

// What a card keeps: a title and the server's own words cut short, at most
// maxCardTools tools, each description cut at maxCardDesc runes (the
// instructions take its first clause).
const (
	maxCardTitle = 80
	maxCardSays  = 300
	maxCardTools = 200
	maxCardDesc  = 200
)

// serverSays is what a server said of itself in initialize's result.
type serverSays struct{ Title, Says string }

// saysOf is what result says of its server: its title, and its description
// (serverInfo.description) or else its instructions' first line.
func saysOf(result json.RawMessage) serverSays {
	var r struct {
		Instructions string `json:"instructions"`
		ServerInfo   struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"serverInfo"`
	}
	if json.Unmarshal(result, &r) != nil {
		return serverSays{}
	}
	says := r.ServerInfo.Description
	if strings.TrimSpace(says) == "" {
		for l := range strings.SplitSeq(r.Instructions, "\n") {
			if strings.TrimSpace(l) != "" {
				says = l
				break
			}
		}
	}
	return serverSays{Title: clip(r.ServerInfo.Title, maxCardTitle), Says: clip(says, maxCardSays)}
}

func cardsPath() string { return filepath.Join(settings.Dir(), "mcp-hub-servers.json") }

// cards are the hub's cards by server name, read from path once.
var cards struct {
	sync.Mutex
	m    map[string]hubCard
	path string
}

// cardKey is s as the hub runs it, short.
func cardKey(s *Server) string {
	h := sha256.Sum256([]byte(hubKey(s)))
	return hex.EncodeToString(h[:8])
}

// loadCards reads the file when cards haven't been. A file that can't be
// read or parsed is no cards: each is what a server lists, which the next
// list writes again. Held under cards.
func loadCards() {
	p := cardsPath()
	if cards.m != nil && cards.path == p {
		return
	}
	cards.m, cards.path = map[string]hubCard{}, p
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &cards.m)
		if cards.m == nil {
			cards.m = map[string]hubCard{}
		}
	}
}

// cardOf is s's card, nil when there is none for s as it is now.
func cardOf(s *Server) *hubCard {
	cards.Lock()
	defer cards.Unlock()
	loadCards()
	c, ok := cards.m[s.Name]
	if !ok || c.Key != cardKey(s) {
		return nil
	}
	return &c
}

// noteCard keeps what s listed (ts) and said of itself, written to the
// file only when it changed; the cards of servers the library no longer
// has go with it.
func noteCard(s *Server, says serverSays, ts []map[string]json.RawMessage) {
	c := hubCard{Key: cardKey(s), Title: says.Title, Says: says.Says, Tools: []Tool{}}
	for _, t := range ts {
		var name, desc string
		if json.Unmarshal(t["name"], &name) != nil || name == "" {
			continue
		}
		json.Unmarshal(t["description"], &desc)
		c.Tools = append(c.Tools, Tool{Name: clip(name, maxToolName), Description: clip(desc, maxCardDesc)})
		if len(c.Tools) == maxCardTools {
			break
		}
	}
	// the library first: cards is taken under mu elsewhere (a sync)
	mu.Lock()
	l, err := load()
	mu.Unlock()
	cards.Lock()
	defer cards.Unlock()
	loadCards()
	if old, ok := cards.m[s.Name]; ok && old.Key == c.Key && old.Title == c.Title && old.Says == c.Says && slices.Equal(old.Tools, c.Tools) {
		return
	}
	cards.m[s.Name] = c
	if err == nil {
		for n := range cards.m {
			if l.server(n) == nil {
				delete(cards.m, n)
			}
		}
	}
	writeCards(cards.m)
}

// writeCards writes m through a temporary file, so a reader never sees
// half of it.
func writeCards(m map[string]hubCard) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	p := cardsPath()
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		os.Remove(tmp)
		return
	}
	if os.Rename(tmp, p) != nil {
		os.Remove(tmp)
	}
}

// hubServerOf is s as an agent is told of it: what the market says it is
// for, else what it says of itself; its title; and its tools but those
// turned off, as it last listed them.
func hubServerOf(s *Server) gateway.HubServer {
	out := gateway.HubServer{Name: s.Name, About: serverAbout(s)}
	c := cardOf(s)
	if c == nil {
		return out
	}
	out.Known = true
	if out.About == "" {
		out.About = c.Says
	}
	out.Title = c.Title
	for _, t := range c.Tools {
		if !slices.Contains(s.DisabledTools, t.Name) {
			out.Tools = append(out.Tools, gateway.HubToolNote{Name: t.Name, About: t.Description})
		}
	}
	return out
}
