package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// clientCache owns the marks of an explicitly opted-in Messages request.
// Message marks stay out of conversation hashes: a running process cannot
// move them in its existing history, and reports that gap on the reply.
type clientCache struct {
	system          []cachePoint
	tail            string
	count           int
	tools           int
	messages        int
	droppedSystem   int
	droppedMessages int
	systemMarks     int // the system's marks, as the request has them
	fresh           bool
	// pasted: the run is Claude Code's interactive mode, which is pasted
	// the words with none of their marks (claudetui's userPieces), and
	// keeps its own prompt caching (startRun): every mark but the tools'
	// is dropped
	pasted bool
	// print: the run is claude -p though the interactive mode is on
	// (Request.tuiRefused)
	print bool
	// kept: the run is claude -p for the marks' sake (keptByPrint)
	kept   bool
	report func()
}

// marksPrintAt is how much of a request's history its client's cache
// marks keep before claude -p is cheaper than the interactive mode for it
// (keptByPrint). The interactive mode is pasted the words with no marks,
// and Claude Code marks only the end of what it is pasted, so a fresh run
// writes everything after Claude Code's own prompt again, at 2x for an
// hour's cache (about 0.1·S + 2·M a call, S its prompt and M the marked
// history). -p is counted at about twice the five-hour allowance a token,
// but keeps the client's marks, so a call reads what the last one wrote
// and writes only what is new (about 2·(0.1·(S+M) + 2·D), D the new part).
// With S ≈ 69K tokens and D ≈ 5K the two meet at M ≈ 15–20K tokens, about
// 64KB of text (#5585, #5655).
const marksPrintAt = 64 << 10

// markedHistory is the bytes of req's messages up to the last part a
// client's cache mark ends (Part.cache): what a run that keeps the marks
// reads again instead of writing.
func markedHistory(req *Request) int {
	n, marked := 0, 0
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Data)
			if p.cache != "" {
				marked = n
			}
		}
	}
	return marked
}

// keptByPrint is whether req runs as claude -p though the interactive
// mode is on, for its client's cache marks: Claude Code's auto mode
// classifier, asked afresh each call with the transcript so far, marking
// more history than marksPrintAt. Pasted, each call wrote that history
// again and read only Claude Code's own prompt (#5585, #5655); a
// conversation's turns go on in their run, which keeps its own cache, and
// stay in the interactive mode.
func keptByPrint(req *Request) bool {
	return req.clientCache != nil && autoModeClassifier(req) && markedHistory(req) >= marksPrintAt
}

// keptAsPrint reports whether the run is claude -p for the marks' sake.
func (c *clientCache) keptAsPrint() bool {
	return c != nil && c.kept
}

func wantsClientCache(r *http.Request, from provider.Protocol) bool {
	return from == provider.Anthropic && r.Header.Get("X-Magpie-Cache-Marks") == "client"
}

// classifierClientCache reports whether a Messages body is Claude Code's
// auto mode classifier carrying cache marks of its own. Desktop's
// classifier marks its blocks but knows nothing of magpie's private header
// (#3835): its marks are kept like an opted-in client's, validated as
// theirs are. The bytes are looked at before the request is parsed, and
// only a classifier-shaped body carrying a cache_control field is parsed
// for its marks.
func classifierClientCache(from provider.Protocol, body []byte) bool {
	return from == provider.Anthropic &&
		bytes.Contains(body, []byte("cache_control")) &&
		autoModeClassifierAsk(body)
}

// selected reports the mapping as soon as run selection is known. Starting
// Claude Code or waiting for its first output must not hold keepalives.
func (c *clientCache) selected(fresh bool) {
	if c == nil {
		return
	}
	c.fresh = fresh
	c.pasted = claudeTUIOn() && !c.print
	if c.report != nil {
		c.report()
	}
}

// cacheReportTry scopes the report and selection hold to this candidate.
// A non-Claude try leaves no report from an earlier subscription try.
func cacheReportTry(hw *holdWriter, req *Request) {
	hw.w.Header().Del("X-Magpie-Cache-Marks-Dropped")
	hw.alive.cacheReportUntil = time.Time{}
	if req != nil {
		hw.alive.cacheReportUntil = time.Now().Add(2 * time.Second)
		cacheDropped(hw, req)
	}
}

func cacheReportReady(w http.ResponseWriter, req *Request) {
	cacheDropped(w, req)
	if hw, ok := w.(*holdWriter); ok {
		hw.mu.Lock()
		if hw.alive != nil {
			hw.alive.cacheReportUntil = time.Time{}
		}
		hw.mu.Unlock()
	}
}

type cachePoint struct {
	End int
	TTL string
}

func cacheTTL(cc map[string]string) string {
	if cc["type"] != "ephemeral" {
		return ""
	}
	if cc["ttl"] == "" {
		return "5m"
	}
	return cc["ttl"]
}

// askedCacheTTL is the longest TTL a Messages request's own cache marks
// ask for, on its tools, its system, its messages or the request itself,
// "" for one that marks nothing. A Claude bridge run started for it keeps
// its cache that long (runTTLFor): the client's Claude Code marks as its
// user picked, an hour only when asked (Linka, a Docker magpie's relay
// writing an hour's cache for a Mac set to 5m).
func askedCacheTTL(a *aRequest) string {
	ttl := ""
	take := func(raw json.RawMessage) {
		if len(raw) == 0 || ttl == "1h" {
			return
		}
		var cc map[string]string
		if json.Unmarshal(raw, &cc) != nil {
			return
		}
		switch t := cacheTTL(cc); t {
		case "5m", "1h":
			if ttl == "" || t == "1h" {
				ttl = t
			}
		}
	}
	take(a.CacheControl)
	for _, t := range a.Tools {
		take(t.CacheControl)
	}
	if bytes.Contains(a.System, cacheControlField) {
		var system []struct {
			CacheControl json.RawMessage `json:"cache_control"`
		}
		if json.Unmarshal(a.System, &system) == nil {
			for _, b := range system {
				take(b.CacheControl)
			}
		}
	}
	for _, m := range a.Messages {
		if ttl == "1h" {
			break
		}
		if !bytes.Contains(m.Content, cacheControlField) {
			continue
		}
		var blocks []struct {
			CacheControl json.RawMessage `json:"cache_control"`
		}
		if json.Unmarshal(m.Content, &blocks) == nil {
			for _, b := range blocks {
				take(b.CacheControl)
			}
		}
	}
	return ttl
}

func renderedCacheBlock(b aBlock) bool {
	switch b.Type {
	case "text":
		return b.Text != ""
	case "thinking":
		return b.Thinking != ""
	case "redacted_thinking":
		return false
	case "image":
		return b.Source != nil && (b.Source.Data != "" || b.Source.URL != "")
	}
	return true
}

func cacheMark(ttl string) map[string]string {
	return map[string]string{"type": "ephemeral", "ttl": ttl}
}

// partMark is the mark of a client's own a rebuilt block keeps (#4571):
// the ttl the parse read off the block, or none.
func partMark(ttl string) map[string]string {
	if ttl == "" {
		return nil
	}
	return cacheMark(ttl)
}

func shorterTTL(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if a == "5m" || b == "5m" {
		return "5m"
	}
	return "1h"
}

// readClientCache validates the request in Anthropic's tools, system,
// messages order before the bridge starts or claims any process.
func readClientCache(a aRequest) (*clientCache, error) {
	c := &clientCache{}
	floor := ""
	mark := func(raw json.RawMessage, path string) (string, error) {
		if len(raw) == 0 || string(raw) == "null" {
			return "", nil
		}
		var cc map[string]string
		if json.Unmarshal(raw, &cc) != nil || cc["type"] != "ephemeral" || (cc["ttl"] != "" && cc["ttl"] != "5m" && cc["ttl"] != "1h") {
			return "", fmt.Errorf("cache_control must have type ephemeral and ttl 5m or 1h")
		}
		ttl := cacheTTL(cc)
		c.count++
		if floor == "5m" && ttl == "1h" {
			return "", fmt.Errorf("%s.ttl: a ttl='1h' cache_control block must not come after a ttl='5m' cache_control block. Note that blocks are processed in the following order: `tools`, `system`, `messages`.", path)
		}
		floor = ttl
		return ttl, nil
	}
	for i, t := range a.Tools {
		if ttl, err := mark(t.CacheControl, fmt.Sprintf("tools.%d.cache_control", i)); err != nil {
			return nil, err
		} else if ttl != "" {
			c.tools++
		}
	}
	var system []json.RawMessage
	_ = json.Unmarshal(a.System, &system)
	cut := len(stringOrText(a.System)) - len(withoutBillingHeader(stringOrText(a.System)))
	lastEnd := cut
	for i, raw := range system {
		var b struct {
			CacheControl json.RawMessage `json:"cache_control"`
		}
		_ = json.Unmarshal(raw, &b)
		ttl, err := mark(b.CacheControl, fmt.Sprintf("system.%d.cache_control", i))
		if err != nil {
			return nil, err
		}
		if ttl != "" {
			c.systemMarks++
			prefix, _ := json.Marshal(system[:i+1])
			end := len(stringOrText(prefix))
			if end <= lastEnd {
				c.droppedSystem++
			} else {
				c.system = append(c.system, cachePoint{end, ttl})
				lastEnd = end
			}
		}
	}
	for i, m := range a.Messages {
		var blocks []struct {
			CacheControl json.RawMessage `json:"cache_control"`
		}
		_ = json.Unmarshal(m.Content, &blocks)
		for j, b := range blocks {
			ttl, err := mark(b.CacheControl, fmt.Sprintf("messages.%d.content.%d.cache_control", i, j))
			if err != nil {
				return nil, err
			}
			if ttl == "" {
				continue
			}
			if i == len(a.Messages)-1 && j == len(blocks)-1 {
				c.tail = ttl
			} else {
				c.messages++
			}
		}
	}
	if len(a.CacheControl) > 0 && string(a.CacheControl) != "null" {
		// Both forms count toward the request limit, even when the bridge
		// represents their shared boundary with one request-level mark.
		var cc map[string]string
		_ = json.Unmarshal(a.CacheControl, &cc)
		if c.tail != "" && cacheTTL(cc) != c.tail {
			return nil, fmt.Errorf("automatic cache_control and the last block must have the same ttl")
		}
		ttl, err := mark(a.CacheControl, "cache_control")
		if err != nil {
			return nil, err
		}
		c.tail = ttl
	}
	if c.count > 4 {
		return nil, fmt.Errorf("A maximum of 4 blocks with cache_control may be provided. Found %d.", c.count)
	}
	return c, nil
}

// cacheIdentity adds no bytes for ordinary requests, preserving their keys.
func cacheIdentity(h hash.Hash, req *Request) {
	if c := req.clientCache; c != nil {
		fmt.Fprintf(h, "\x00client-cache:%v", c.system)
	}
	// a run started with the caller's instructions as its system prompt
	// was never told Claude Code's, and one started with Claude Code's has
	// them in its first message: neither goes on with the other's turns
	if req.leanPrompt {
		fmt.Fprint(h, "\x00lean-prompt")
	}
}

func cacheDropped(w http.ResponseWriter, req *Request) {
	c := req.clientCache
	if c == nil {
		return
	}
	var fields []string
	if c.tools > 0 {
		fields = append(fields, fmt.Sprintf("tools:%d", c.tools))
	}
	system, messages := c.droppedSystem, c.messages
	if c.fresh {
		messages = c.droppedMessages
	}
	if c.pasted {
		system, messages = c.systemMarks, c.messages
		if c.tail != "" {
			messages++
		}
	}
	if system > 0 {
		fields = append(fields, fmt.Sprintf("system:%d", system))
	}
	if messages > 0 {
		fields = append(fields, fmt.Sprintf("messages:%d", messages))
	}
	if len(fields) > 0 {
		value := strings.Join(fields, ",")
		if hw, ok := w.(*holdWriter); ok {
			hw.note("X-Magpie-Cache-Marks-Dropped", value)
		} else {
			w.Header().Set("X-Magpie-Cache-Marks-Dropped", value)
		}
	}
}

// cachePrefix is immutable after launch and survives shelving. A new tail
// must fit the marks already stored in Claude Code's first user message.
type cachePrefix struct {
	count int
	floor string
	// ttl is how long the run's Claude Code keeps its cache, as its
	// first message's marks (claudeRunTTL); "" is a session saved before
	// it was kept, an hour's
	ttl string
}

func (p cachePrefix) runTTL() string {
	if p.ttl == "" {
		return "1h"
	}
	return p.ttl
}

func (p cachePrefix) accepts(tail string) bool {
	return tail == "" || (p.count < 4 && !(p.floor == "5m" && tail == "1h"))
}

func (r *Request) cacheTail() string {
	if r.clientCache != nil {
		return r.clientCache.tail
	}
	return ""
}

func promptCachePrefix(blocks []map[string]any) cachePrefix {
	var p cachePrefix
	for _, b := range blocks {
		if cc, ok := b["cache_control"].(map[string]string); ok {
			p.count++
			p.floor = shorterTTL(p.floor, cacheTTL(cc))
		}
	}
	return p
}
