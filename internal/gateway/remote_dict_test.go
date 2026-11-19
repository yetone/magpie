package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

func freshDicts(t *testing.T) {
	oldSent, oldHeld := sentDicts, heldDicts
	sentDicts, heldDicts = newSentBodies(), newKeptBodies()
	t.Cleanup(func() { sentDicts, heldDicts = oldSent, oldHeld })
}

// agentChat is a long agent conversation as it grows, turn by turn: real
// source files read by tools, the model's words, and one screenshot,
// every request the whole of it with the cache mark on its last message,
// as Claude Code sends one.
type agentChat struct {
	sys      string
	messages []map[string]any
	files    [][]byte
	rnd      *rand.Rand
}

func newAgentChat(t testing.TB, seed uint64) *agentChat {
	t.Helper()
	names, _ := filepath.Glob("*.go")
	slices.Sort(names)
	c := &agentChat{rnd: rand.New(rand.NewPCG(seed, seed+1)), sys: strings.Repeat("You are an agent working in a Go repository. Follow the user's instructions. ", 200)}
	for _, n := range names {
		if b, err := os.ReadFile(n); err == nil && len(b) > 2000 {
			c.files = append(c.files, b)
		}
	}
	if len(c.files) < 20 {
		t.Fatalf("only %d source files to read", len(c.files))
	}
	c.rnd.Shuffle(len(c.files), func(i, j int) { c.files[i], c.files[j] = c.files[j], c.files[i] })
	return c
}

// turn adds a tool call and its result (a source file, or the screenshot
// on turn 5), and is the request then.
func (c *agentChat) turn(i int) []byte {
	id := fmt.Sprintf("toolu_%d_%d", c.rnd.Uint32(), i)
	var result any
	if i == 5 {
		img := make([]byte, 300<<10) // a PNG or JPEG is as good as random to a compressor
		for j := range img {
			img[j] = byte(c.rnd.Uint32())
		}
		result = []any{map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(img)}}}
	} else {
		f := c.files[i%len(c.files)]
		result = string(f[:min(len(f), 40<<10)])
	}
	c.messages = append(c.messages,
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": fmt.Sprintf("Let me look at the next file (step %d).", i)},
			map[string]any{"type": "tool_use", "id": id, "name": "Read", "input": map[string]any{"file_path": fmt.Sprintf("/repo/f%d.go", i)}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": result}}})
	// the cache mark moves to the last message, as Claude Code's does
	for _, m := range c.messages {
		if parts, ok := m["content"].([]any); ok {
			for _, p := range parts {
				delete(p.(map[string]any), "cache_control")
			}
		}
	}
	last := c.messages[len(c.messages)-1]["content"].([]any)
	last[len(last)-1].(map[string]any)["cache_control"] = map[string]any{"type": "ephemeral"}
	msgs := append([]map[string]any{{"role": "user", "content": "Read the gateway's files and tell me how routing works."}}, c.messages...)
	b, _ := json.Marshal(map[string]any{"model": "office/fake/m1", "max_tokens": 32000, "stream": false, "system": c.sys, "messages": msgs,
		"metadata": map[string]any{"user_id": fmt.Sprintf("session_%d", c.rnd.Uint32())}})
	return b
}

// A long agent conversation, sent to a remote magpie turn by turn: what
// went on the wire as it was, as zstd, and as "dcz" against the body
// before, with two conversations' turns interleaved as two agents send
// them. The numbers go in the commit.
func TestDictSavingOnALongConversation(t *testing.T) {
	freshDicts(t)
	p := provider.Provider{ID: "office", Preset: provider.RemoteMagpiePreset, Key: "rk", Chat: "http://office:3425/v1"}
	a, b := newAgentChat(t, 1), newAgentChat(t, 2)
	var plain, zst, dcz int
	var took time.Duration
	const turns = 60
	for i := range turns {
		for k, c := range []*agentChat{a, b} {
			if k == 1 && i%3 != 0 {
				continue // the second agent sends a turn a third as often
			}
			body := c.turn(i)
			req := httptest.NewRequest("POST", "http://office:3425/v1/messages", nil)
			start := time.Now()
			d, sent := sendDict(p, req, body)
			took += time.Since(start)
			wire, _ := io.ReadAll(req.Body)
			if d == nil {
				t.Fatalf("turn %d: no dictSend", i)
			}
			if i > 0 && d.enc != "dcz" {
				t.Fatalf("turn %d of %d went %q", i, k, d.enc)
			}
			// the other magpie decodes it and keeps it
			if d.enc == "dcz" {
				r := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(wire))
				r.Header = req.Header.Clone()
				dict := heldDicts.get("s", mustSum(t, r.Header.Get(availableDictHeader)))
				rd, err := dczReader(bytes.NewReader(wire), dict)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(rd)
				if err != nil || !bytes.Equal(got, body) {
					t.Fatalf("turn %d decoded %d bytes, %v", i, len(got), err)
				}
			}
			sum := heldDicts.put("s", body)
			res := &http.Response{StatusCode: 200, Header: http.Header{KeptBodyHeader: {sfBinary(sum)}}, Body: http.NoBody}
			d.after(req, res, nil, nil)
			plain += len(body)
			zst += len(zstdEncode(body))
			dcz += sent
			if i == turns-1 && k == 0 {
				t.Logf("last request %d bytes plain, %d zstd, %d dcz", len(body), len(zstdEncode(body)), sent)
			}
		}
	}
	t.Logf("%d requests, %d bytes plain, %d zstd (%.2f%%), %d dcz (%.3f%%), dcz encoding %v in all",
		turns+turns/3, plain, zst, 100*float64(zst)/float64(plain), dcz, 100*float64(dcz)/float64(plain), took.Round(time.Millisecond))
	if dcz*20 > plain {
		t.Fatalf("dcz sent %d of %d bytes", dcz, plain)
	}
}

// wireDecoded is a hop's body as the other magpie reads it: a "dcz" one
// against whichever caller's kept body it names.
func wireDecoded(h http.Header, b []byte) []byte {
	var rd io.Reader
	switch h.Get("Content-Encoding") {
	case "zstd":
		d, err := zstd.NewReader(bytes.NewReader(b))
		if err != nil {
			return b
		}
		defer d.Close()
		rd = d
	case "dcz":
		sum, _ := parseSFBinary(h.Get(availableDictHeader))
		var dict []byte
		heldDicts.mu.Lock()
		for _, m := range heldDicts.scopes {
			if el := m[sum]; el != nil {
				dict = el.Value.(*heldDict).body
			}
		}
		heldDicts.mu.Unlock()
		d, err := dczReader(bytes.NewReader(b), dict)
		if err != nil {
			return b
		}
		defer d.Close()
		rd = d
	default:
		return b
	}
	out, err := io.ReadAll(rd)
	if err != nil {
		return b
	}
	return out
}

func mustSum(t *testing.T, v string) bodySum {
	t.Helper()
	s, ok := parseSFBinary(v)
	if !ok {
		t.Fatalf("not a byte sequence: %q", v)
	}
	return s
}

// twoMagpies is a gateway that is both computers' (as the remote turn
// tests have it), the hop between them logged as it went on the wire.
func twoMagpies(t *testing.T) (h http.Handler, vendor, hop *hopLog) {
	t.Helper()
	freshRemoteTurns(t)
	freshDicts(t)
	vendor, hop = &hopLog{}, &hopLog{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vendor.add(r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"msg_%d","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"answer %d"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`, vendor.n(), vendor.n())
	}))
	t.Cleanup(up.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	h = New().Handler()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b := hop.add(r)
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "rk", Models: []string{"fake/m1"},
		Chat: remote.URL + "/v1", Responses: remote.URL + "/v1", Anthropic: remote.URL}); err != nil {
		t.Fatal(err)
	}
	return h, vendor, hop
}

func askMessages(t *testing.T, h http.Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("User-Agent", "claude-cli/2.1.0 (external, cli)")
	h.ServeHTTP(rec, req)
	return rec
}

// the vendor's request is the agent's: the same messages, whatever way it
// went between the magpies
func sameMessages(t *testing.T, vendor []byte, sent []byte) {
	t.Helper()
	var v, s struct {
		Messages json.RawMessage `json:"messages"`
	}
	json.Unmarshal(vendor, &v)
	json.Unmarshal(sent, &s)
	var vm, sm any
	json.Unmarshal(v.Messages, &vm)
	json.Unmarshal(s.Messages, &sm)
	vb, _ := json.Marshal(vm)
	sb, _ := json.Marshal(sm)
	if len(sb) < 1000 || !bytes.Equal(vb, sb) {
		t.Fatalf("the vendor was sent other messages: %d bytes for %d", len(vb), len(sb))
	}
}

// A long conversation through a remote magpie (gaubeebangeel on X): every
// turn went to the other magpie whole. Now the first goes whole, offered
// kept, and each after it as "dcz" against the one before, a few percent
// of it, and the vendor behind still gets the conversation whole.
func TestRemoteMagpieBodyGoesAgainstTheOneBefore(t *testing.T) {
	h, vendor, hop := twoMagpies(t)
	c := newAgentChat(t, 7)
	for i := range 8 {
		body := c.turn(i)
		if rec := askMessages(t, h, body); rec.Code != 200 || !strings.Contains(rec.Body.String(), "answer") {
			t.Fatalf("turn %d: %d %s", i, rec.Code, rec.Body.String())
		}
		if hop.n() != i+1 || vendor.n() != i+1 {
			t.Fatalf("turn %d: the other magpie asked %d times, the vendor %d", i, hop.n(), vendor.n())
		}
		hd := hop.header(i)
		if hd.Get(KeepBodyHeader) == "" {
			t.Fatalf("turn %d not offered kept: %v", i, hd)
		}
		wire := len(hop.wire(i))
		if i == 0 {
			if hd.Get("Content-Encoding") != "" {
				t.Fatalf("turn 0 went %q to a magpie not yet known to keep bodies", hd.Get("Content-Encoding"))
			}
		} else if hd.Get("Content-Encoding") != "dcz" || hd.Get(availableDictHeader) == "" || i != 5 && wire*4 > len(body) {
			// turn 5's screenshot is new, and as large compressed
			t.Fatalf("turn %d went %q, %d bytes on the wire for %d", i, hd.Get("Content-Encoding"), wire, len(body))
		}
		sameMessages(t, []byte(vendor.raw(i)), body)
		for k := range vendor.header(i) {
			if strings.HasPrefix(strings.ToLower(k), "x-magpie-") || strings.EqualFold(k, availableDictHeader) {
				t.Fatalf("vendor sent %s", k)
			}
		}
		if ce := vendor.header(i).Get("Content-Encoding"); ce != "" {
			t.Fatalf("vendor sent Content-Encoding %q", ce)
		}
	}
}

// The other magpie restarted, and holds no body: the "dcz" turn is
// answered with DictMissHeader before any vendor is asked, and goes again
// whole; the agent never sees it.
func TestRemoteMagpieLostDictionaryResendsWhole(t *testing.T) {
	h, vendor, hop := twoMagpies(t)
	c := newAgentChat(t, 8)
	if rec := askMessages(t, h, c.turn(0)); rec.Code != 200 {
		t.Fatalf("turn 0: %d %s", rec.Code, rec.Body.String())
	}
	heldDicts = newKeptBodies() // a restart
	body := c.turn(1)
	if rec := askMessages(t, h, body); rec.Code != 200 || !strings.Contains(rec.Body.String(), "answer") {
		t.Fatalf("turn 1: %d %s", rec.Code, rec.Body.String())
	}
	if hop.n() != 3 || vendor.n() != 2 {
		t.Fatalf("the other magpie asked %d times, the vendor %d", hop.n(), vendor.n())
	}
	if hop.header(1).Get("Content-Encoding") != "dcz" || hop.header(2).Get("Content-Encoding") != "" || !json.Valid(hop.wire(2)) {
		t.Fatalf("turn 1 went %q then %q, %d bytes", hop.header(1).Get("Content-Encoding"), hop.header(2).Get("Content-Encoding"), len(hop.wire(2)))
	}
	sameMessages(t, []byte(vendor.raw(1)), body)
	// kept again from the whole one: the next turn is a delta again
	if rec := askMessages(t, h, c.turn(2)); rec.Code != 200 {
		t.Fatalf("turn 2: %d", rec.Code)
	}
	if hop.header(3).Get("Content-Encoding") != "dcz" {
		t.Fatalf("turn 2 went %q", hop.header(3).Get("Content-Encoding"))
	}
}

// A magpie from before this never says it keeps a body: it is sent every
// one as it was, uncompressed, as before.
func TestRemoteMagpieFromBeforeIsSentBodiesAsTheyWere(t *testing.T) {
	freshRemoteTurns(t)
	freshDicts(t)
	hop := &hopLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hop.add(r)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "rk", Models: []string{"fake/m1"}, Anthropic: srv.URL}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	c := newAgentChat(t, 9)
	for i := range 3 {
		body := c.turn(i)
		if rec := askMessages(t, h, body); rec.Code != 200 {
			t.Fatalf("turn %d: %d %s", i, rec.Code, rec.Body.String())
		}
		if ce := hop.header(i).Get("Content-Encoding"); ce != "" || !json.Valid(hop.wire(i)) {
			t.Fatalf("turn %d went %q", i, ce)
		}
	}
}

// keptRequest is a magpie's request to h, from addr with key id.
func keptRequest(body []byte, key, addr string, header map[string]string) *http.Request {
	r := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(body))
	r.RemoteAddr = addr
	r.Header.Set("User-Agent", "magpie/0.1.9999")
	for k, v := range header {
		r.Header.Set(k, v)
	}
	return r.WithContext(access.WithIdentity(r.Context(), access.Identity{KeyID: key}))
}

// A body kept for one caller is no other's: another key, or the same key
// from another computer, naming it is answered as a dictionary not held,
// and nothing is decoded against it. A body that doesn't decode, or
// decodes to another than the digest names, goes no further either.
func TestKeptBodyIsOnlyItsCallers(t *testing.T) {
	freshDicts(t)
	reached := 0
	var got []byte
	h := keptBodyRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		got, _ = io.ReadAll(r.Body)
		if r.Header.Get(availableDictHeader) != "" || r.Header.Get(KeepBodyHeader) != "" || r.Header.Get("Content-Encoding") != "" {
			t.Errorf("passed on with %v", r.Header)
		}
	}))
	c := newAgentChat(t, 3)
	first := c.turn(0)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, keptRequest(first, "keyA", "10.0.0.2:1", map[string]string{KeepBodyHeader: "dcz"}))
	sum := sha256.Sum256(first)
	if rec.Header().Get(KeptBodyHeader) != sfBinary(sum) || reached != 1 {
		t.Fatalf("not kept: %v", rec.Header())
	}
	next := c.turn(1)
	z, err := dczEncode(next, first)
	if err != nil {
		t.Fatal(err)
	}
	dz := map[string]string{"Content-Encoding": "dcz", availableDictHeader: sfBinary(sum), BodyDigestHeader: sfBinary(sha256.Sum256(next))}
	for _, tc := range []struct {
		name, key, addr string
		body            []byte
		header          map[string]string
	}{
		{"another key", "keyB", "10.0.0.2:1", z, dz},
		{"no key", "", "10.0.0.2:1", z, dz},
		{"another computer", "keyA", "10.0.0.3:1", z, dz},
		{"a corrupt body", "keyA", "10.0.0.2:1", append(slices.Clone(z[:len(z)/2]), 0xff, 0xff, 0xff), dz},
		{"a flipped byte", "keyA", "10.0.0.2:1", flipped(t, z, first, next), dz},
		{"another digest", "keyA", "10.0.0.2:1", z, map[string]string{"Content-Encoding": "dcz", availableDictHeader: sfBinary(sum), BodyDigestHeader: sfBinary(sha256.Sum256(first))}},
		{"no digest", "keyA", "10.0.0.2:1", z, map[string]string{"Content-Encoding": "dcz", availableDictHeader: sfBinary(sum)}},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, keptRequest(tc.body, tc.key, tc.addr, tc.header))
		if rec.Code != 400 || rec.Header().Get(DictMissHeader) == "" || reached != 1 {
			t.Fatalf("%s: %d %v, reached %d", tc.name, rec.Code, rec.Header(), reached)
		}
	}
	// its own caller's is decoded, as it was sent
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, keptRequest(z, "keyA", "10.0.0.2:9", dz))
	if rec.Code != 200 || reached != 2 || !bytes.Equal(got, next) {
		t.Fatalf("own caller: %d, reached %d, %d bytes", rec.Code, reached, len(got))
	}
	// a client that isn't a magpie gets nothing kept, and a "dcz" body is
	// one it can't send
	rec = httptest.NewRecorder()
	r := keptRequest(first, "keyA", "10.0.0.2:1", map[string]string{KeepBodyHeader: "dcz"})
	r.Header.Set("User-Agent", "curl/8")
	h.ServeHTTP(rec, r)
	if rec.Header().Get(KeptBodyHeader) != "" {
		t.Fatalf("kept a plain client's body")
	}
}

// flipped is z with one byte from its middle on flipped where the flip
// changes what it decodes to: a bit zstd doesn't read (a table entry no
// symbol uses) decodes to the same body, which is rightly passed on, so
// which byte that is depends on the package's sources newAgentChat reads.
func flipped(t *testing.T, z, dict, want []byte) []byte {
	t.Helper()
	for i := len(z) / 2; i < len(z); i++ {
		b := slices.Clone(z)
		b[i] ^= 0x40
		r, err := dczReader(bytes.NewReader(b), dict)
		if err != nil {
			return b
		}
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil || !bytes.Equal(got, want) {
			return b
		}
	}
	t.Fatal("no byte past the middle changes the body")
	return nil
}

// A magpie keeps no more than dictPerScope bodies of a caller, nor more
// than its bounds in bytes, the least lately used going first.
func TestKeptBodiesBounded(t *testing.T) {
	k := newKeptBodies()
	body := func(i, n int) []byte { return bytes.Repeat([]byte{byte(i)}, n) }
	for i := range dictPerScope + 3 {
		k.put("a", body(i, 1000))
	}
	if n := len(k.scopes["a"]); n != dictPerScope {
		t.Fatalf("%d kept", n)
	}
	if k.get("a", sha256.Sum256(body(0, 1000))) != nil || k.get("a", sha256.Sum256(body(dictPerScope+2, 1000))) == nil {
		t.Fatal("the oldest kept, or the newest not")
	}
	for i := range 4 {
		k.put("b", body(i, dictScopeBytes/3))
	}
	if k.size > dictServerBytes || len(k.scopes["b"]) > 3 {
		t.Fatalf("%d bytes, %d of b's", k.size, len(k.scopes["b"]))
	}
	for i := range 20 {
		k.put(fmt.Sprintf("c%d", i), body(i, dictScopeBytes/4))
	}
	if k.size > dictServerBytes {
		t.Fatalf("%d bytes kept", k.size)
	}
	// and gone after dictTTL
	now := time.Now()
	k.now = func() time.Time { return now.Add(dictTTL + time.Minute) }
	if k.get("a", sha256.Sum256(body(dictPerScope+2, 1000))) != nil {
		t.Fatal("kept past its time")
	}
	// this side's too
	s := newSentBodies()
	for i := range 40 {
		b := body(i, dictClientBytes/8)
		s.put(fmt.Sprintf("r%d", i%5), &sentDict{sum: sha256.Sum256(b), body: b, when: time.Now().Add(time.Duration(i) * time.Second)})
	}
	if s.size > dictClientBytes {
		t.Fatalf("this side kept %d bytes", s.size)
	}
}
