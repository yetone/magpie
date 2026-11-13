package usage

import (
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/probelog"
	"github.com/yetone/magpie/internal/provider"
)

// SourceProbe is Record.Source for a request magpie sent a provider on its
// own, outside any agent's turn (probelog): the editor's Test, Detect
// APIs, a model list's refresh. Its Kind says which (probelog.KindTest,
// KindDetect, KindList). Such a row is listed on the request log, told
// apart there, and counted nowhere: no total, cost, chart, group, budget,
// window hold or observer reads it. The log's index keeps it apart from
// the calls (logSnapshot.probes) so a reader of those never meets it.
const SourceProbe = "probe"

// SourceMCP is Record.Source for an agent's call to a tool of one of
// magpie's own MCP servers: the gateway's (image, vision, web search,
// plugin tools, the MCP hub) and the Magpie Image server an agent runs
// (ferock on Discord). Its Kind is KindMCP, Requested the tool's name as
// it was called, Host the server's, Provider and Model what the tool used
// when it used a model, Millis how long the call took and Error what a
// failed one said. Its arguments are never kept. It rides the probes'
// lane: listed on the request log, told apart there, counted nowhere, as
// the model requests the tool made are rows of their own that are.
const SourceMCP = "mcp"

// SourceSearch is Record.Source for one ask of a search API (Tavily,
// Brave, …) by a web search magpie ran (cybercaius on Discord: which
// search API each search used couldn't be seen). Its Kind is
// "web_search", Agent the agent the search was for, Requested the API's
// name, Host where it was asked, ProviderKeyID and ProviderKeyName the key
// (by a hash and masked), Millis how long it took, Status the API's when
// it refused the key, and Error what a failed one said ("found nothing"
// for an answer with no page). The query is never kept. It rides the
// probes' lane as an MCP call does: listed on the request log and counted
// nowhere, since it has no tokens and is no model's.
const SourceSearch = "search"

// KindMCP is an MCP tool call's Kind, the purpose the request log picks
// them by.
const KindMCP = "mcp"

// IsProbe is a request magpie sent on its own (SourceProbe), an MCP tool
// call (SourceMCP) or a search API's (SourceSearch): a row listed and
// counted nowhere.
func (r Record) IsProbe() bool {
	return r.Source == SourceProbe || r.Source == SourceMCP || r.Source == SourceSearch
}

// IsProbe is a row of such a request. A packed row keeps the row's Source,
// not its record's, so the request log asks the row.
func (r Row) IsProbe() bool {
	return r.Source == SourceProbe || r.Source == SourceMCP || r.Source == SourceSearch || r.Record.IsProbe()
}

// probeRows are the probes' blocks as the gateway rows' are: logBlockRows
// rows a block, frozen when full, the last reopened to take the next.
// A probe was a whole Record in a slice (952 B each): a log of 25k MCP tool
// calls held ~25 MB of them, and every append copied the slice.
type probeRows struct {
	blocks []*rowChunk
	tail   *rowChunk
}

// add takes a probe's record and where its line ends in the log, as a
// gateway row's Order is.
func (p *probeRows) add(r Record, off int64) {
	if p.tail == nil {
		if n := len(p.blocks); n > 0 && p.blocks[n-1].Count < logBlockRows {
			p.tail = p.blocks[n-1].writable()
			p.blocks = p.blocks[:n-1]
		} else {
			p.tail = &rowChunk{}
		}
	}
	p.tail.add(Row{Record: r, Source: r.Source}, "", off, false)
	if len(p.tail.Rows) == logBlockRows {
		p.blocks = append(p.blocks, p.tail.freeze())
		p.tail = nil
	}
}

// done is the blocks with the tail frozen in.
func (p *probeRows) done() []*rowChunk {
	if p.tail != nil {
		p.blocks = append(p.blocks, p.tail.freeze())
		p.tail = nil
	}
	return p.blocks
}

// eachProbe calls f with each probe made since since (all for zero) and
// where its line ends in the log.
func eachProbe(blocks []*rowChunk, since time.Time, f func(r Record, off int64)) {
	rows := chunkRows{}
	defer rows.release()
	for _, b := range blocks {
		if !since.IsZero() && b.Latest.Before(since) {
			continue
		}
		c := rows.of(b)
		for i := range c.Rows {
			row := c.row(i)
			if !since.IsZero() && row.Time.Before(since) {
				continue
			}
			r := row.Record
			r.Source = row.Source
			f(r, c.Rows[i].Order)
		}
	}
}

func init() {
	probelog.Sink = func(p probelog.Row) {
		r := Record{Time: p.Time, Agent: "magpie", Provider: p.Provider, ProviderAccount: p.Account,
			Host: p.Host, Model: p.Model, Requested: p.Model, Status: p.Status, Millis: p.Millis,
			Error: clipProbeError(p.Error), Endpoint: p.Endpoint, Kind: p.Kind, Source: SourceProbe}
		if quietProbe(r) {
			return
		}
		Append(r)
	}
}

// quietProbe is a request of magpie's own the request log leaves out: a
// model list that was answered. magpie asks for one by itself, at start-up,
// behind the panel and the Providers page, and on a schedule (a Remote
// magpie's every 10 minutes, RemoteMagpieEvery), so the Requests page
// filled with rows of 0 tokens that no agent sent (ferock on Discord). One
// that failed is listed, as is every Test and Detect APIs: a list that
// couldn't be fetched is why models are missing, and the row says why.
// Rows written before this are left out the same way as the log is read
// (logSnapshot.probes); the lines stay in the file, counted nowhere.
func quietProbe(r Record) bool {
	return r.Kind == probelog.KindList && r.Status >= 200 && r.Status < 400 && r.Error == ""
}

// clipProbeError keeps an error's first 500 bytes, as a vendor's answer can
// be a whole page.
func clipProbeError(s string) string {
	const most = 500
	if len(s) <= most {
		return s
	}
	cut := most
	for cut > 0 && s[cut]&0xC0 == 0x80 { // not inside a character
		cut--
	}
	return s[:cut] + "…"
}

// probeChunk is the probes made since since as one chunk of the request
// log's rows, each in the order its line has in the log as a gateway
// row's is; nil for none.
// A provider renamed since is listed by the id it has now (Renames): the
// Requests page's provider list named a renamed one twice (cybercaius).
// One asked while a provider was being added, under its host, is listed
// by the id of the provider on that host (Renames.ProbeOf), and once no
// provider is on it, a deleted one's, by the id the log's calls to that
// host were made under (hosts, logSnapshot.hosts): cybercaius on Discord,
// a deleted provider's tests were listed under its host beside its id.
// A host no provider was on goes by its site (provider.SiteOf) the same
// way: CyberCaius again, Leee's model list asked at leee.baby before it
// was saved on another host of that site.
func probeChunk(probes []*rowChunk, since time.Time, renames provider.Renames, hosts map[string]string) *rowChunk {
	// the page polls every few seconds and the probes rarely change: the
	// chunk was made again each time, ~45 MB of two and a half minutes'
	// allocations with the page open (#2278). A frozen block is never
	// written again, so the same blocks, since, renames and hosts make
	// the same chunk, which no reader writes.
	probeMemo.Lock()
	m := probeMemo.last
	probeMemo.Unlock()
	if m != nil && m.since.Equal(since) && slices.Equal(m.probes, probes) && m.renames.Same(renames) && maps.Equal(m.hosts, hosts) {
		return m.c
	}
	c := makeProbeChunk(probes, since, renames, hosts)
	probeMemo.Lock()
	probeMemo.last = &probeMade{probes: slices.Clone(probes), since: since, renames: renames, hosts: maps.Clone(hosts), c: c}
	probeMemo.Unlock()
	return c
}

// probeMemo is the last chunk probeChunk made, and what it was made from.
var probeMemo struct {
	sync.Mutex
	last *probeMade
}

// releaseProbes lets the probe chunk go once the pages rest, with the
// rest of the reads' scratch: its rows are unpacked, ~5 MB of a day's
// probes on a real gateway, and were held for the process's life after
// the warm-up or a window's last poll. The next page read makes it again.
func releaseProbes() {
	probeMemo.Lock()
	probeMemo.last = nil
	probeMemo.Unlock()
}

type probeMade struct {
	probes  []*rowChunk
	since   time.Time
	renames provider.Renames
	hosts   map[string]string
	c       *rowChunk
}

func makeProbeChunk(probes []*rowChunk, since time.Time, renames provider.Renames, hosts map[string]string) *rowChunk {
	var c *rowChunk
	sites := sync.OnceValue(func() map[string]string { return sitesOf(hosts) })
	eachProbe(probes, since, func(r Record, off int64) {
		if c == nil {
			c = &rowChunk{}
		}
		if r.Source == SourceMCP {
			// a tool call names the provider its gateway call went to, by id
			if r.Provider != "" {
				r.Provider = renames.Of(r.Provider, r.Time)
			}
			c.add(Row{Record: r, Source: SourceMCP}, "", off, false)
			return
		}
		if r.Source == SourceSearch { // a search API is no provider
			c.add(Row{Record: r, Source: SourceSearch}, "", off, false)
			return
		}
		r.Provider = probeProvider(r, renames, hosts, sites)
		c.add(Row{Record: r, Source: SourceProbe}, "", off, false)
	})
	if c == nil {
		return nil
	}
	c.dict, c.shapeIDs = nil, nil
	return c
}

// sitesOf is hosts by site (provider.SiteOf): the provider the log's
// calls to that site went to, "" for a site two providers' calls went to.
func sitesOf(hosts map[string]string) map[string]string {
	out := map[string]string{}
	for h, id := range hosts {
		site := provider.SiteOf(h)
		if site == "" {
			continue
		}
		if to, ok := out[site]; ok && to != id {
			id = ""
		}
		out[site] = id
	}
	return out
}

// probeProvider is the provider a probe row is listed under: its id as it
// is now, and for one recorded under its host before the provider had an
// id, the provider saved on that host, or, with none on it now, the one
// the log's calls to that host went to; with neither, the same by the
// host's site. A host or site two providers were on, or none, stays the
// host.
func probeProvider(r Record, renames provider.Renames, hosts map[string]string, sites func() map[string]string) string {
	id := renames.ProbeOf(r.Provider, r.Time)
	if id != r.Provider || !strings.EqualFold(r.Provider, r.Host) || renames.OnHost(r.Host) {
		return id
	}
	if to := hosts[strings.ToLower(r.Host)]; to != "" {
		return renames.Of(to, r.Time)
	}
	if to, on := renames.OnSite(r.Host); on {
		if to != "" {
			return to
		}
	} else if site := provider.SiteOf(r.Host); site != "" {
		if to := sites()[site]; to != "" {
			return renames.Of(to, r.Time)
		}
	}
	return strings.ToLower(id) // one host, however it was typed
}
