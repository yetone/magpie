package sessions

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

// TestCCUsageDecodedAsJSONUnmarshal holds the in-place usage decoder to
// json.Unmarshal: the same fields, or the same refusal, for each shape a
// usage object can take, and for objects made at random from them.
func TestCCUsageDecodedAsJSONUnmarshal(t *testing.T) {
	cases := []string{
		`{"input_tokens":3,"cache_creation_input_tokens":1200,"cache_read_input_tokens":45000,"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":1000},"output_tokens":512,"service_tier":"standard","server_tool_use":{"web_search_requests":0}}`,
		`{}`, `{"input_tokens":null}`, `{"cache_creation":null}`, `{"cache_creation":{}}`,
		`{"Input_Tokens":5}`, `{"INPUT_TOKENS":5,"input_tokens":6}`, `{"cache_creation":{"Ephemeral_1h_input_tokens":4}}`,
		`{"input_tokens":1.5}`, `{"input_tokens":1e3}`, `{"input_tokens":"5"}`, `{"input_tokens":true}`,
		`{"input_tokens":-7}`, `{"input_tokens":99999999999999999999}`, `{"input_tokens":1,"input_tokens":2}`,
		`{"cache_creation":{"ephemeral_5m_input_tokens":1},"cache_creation":{"ephemeral_1h_input_tokens":2}}`,
		`{"cache_creation":{"ephemeral_5m_input_tokens":1},"cache_creation":null}`,
		`{"cache_creation":[1]}`, `{"cache_creation":5}`, `{"cache_creation":{"ephemeral_5m_input_tokens":"x"}}`,
		`{"input\u005ftokens":9}`, `{"x":{"input_tokens":4},"output_tokens":2}`, `{"input_tokens":3,}`, `{"input_tokens":3`,
		`{"input_tokens":0, "output_tokens" : 4 }`, `{"ſerver":1,"output_toKens":3}`, `[]`, `null`, ``,
	}
	r := rand.New(rand.NewSource(1))
	keys := []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "cache_creation", "Output_tokens", "service_tier", "x"}
	vals := []string{"0", "1", "123456", "-1", "null", "1.0", `"s"`, "true", `{"ephemeral_5m_input_tokens":7}`, `{"ephemeral_1h_input_tokens":8,"z":1}`, "[]"}
	for range 3000 {
		var parts []string
		for range r.Intn(6) {
			parts = append(parts, fmt.Sprintf("%q:%s", keys[r.Intn(len(keys))], vals[r.Intn(len(vals))]))
		}
		cases = append(cases, "{"+strings.Join(parts, ",")+"}")
	}
	for _, c := range cases {
		var want, got ccUsageFields
		werr := json.Unmarshal([]byte(c), &want)
		gerr := ccDecodeUsage([]byte(c), &got)
		if (werr == nil) != (gerr == nil) {
			t.Fatalf("%s: json.Unmarshal %v, own %v", c, werr, gerr)
		}
		if werr != nil {
			continue
		}
		w, _ := json.Marshal(want)
		g, _ := json.Marshal(got)
		if string(w) != string(g) {
			t.Fatalf("%s:\n got %s\nwant %s", c, g, w)
		}
	}
}

// TestCCUsageReadInPlace: a reply's usage, as Claude Code writes it, is
// read without an allocation.
func TestCCUsageReadInPlace(t *testing.T) {
	b := []byte(`{"input_tokens":3,"cache_creation_input_tokens":1200,"cache_read_input_tokens":45000,"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":1000},"output_tokens":512,"service_tier":"standard"}`)
	var u ccUsageFields
	if _, ok := ccUsageInPlace(b); !ok {
		t.Fatal("Claude Code's usage was left to json.Unmarshal")
	}
	n := testing.AllocsPerRun(100, func() { ccDecodeUsage(b, &u) })
	// the cache_creation it points at
	if n > 1 {
		t.Fatalf("a usage read made %v allocations, want at most 1", n)
	}
	if u.Input != 3 || u.Output != 512 || u.CacheRead != 45000 || u.CacheWrite != 1200 || u.Creation == nil || u.Creation.Ephemeral1h != 1000 {
		t.Fatalf("%+v %+v", u, u.Creation)
	}
}

// TestClaudePromptContentNotCopied: a prompt read whole reads its content
// where the line holds it; a pasted megabyte was copied for each line.
func TestClaudePromptContentNotCopied(t *testing.T) {
	big := strings.Repeat("pasted words ", 80000)
	line := fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":%q},"uuid":"u1","timestamp":"2026-09-20T12:00:00Z","cwd":"/w","sessionId":"s1"}`, "fix this: "+big)
	b := []byte(line)
	s := &state{}
	claudeLine(s, b, true)
	if !strings.HasPrefix(s.Title, "fix this: pasted words") || s.Cwd != "/w" || s.ID != "s1" {
		t.Fatalf("title %q cwd %q id %q", s.Title, s.Cwd, s.ID)
	}
	prompts := 0
	for _, e := range s.Days {
		prompts += e.v.Prompts
	}
	if prompts != 1 {
		t.Fatalf("%d prompts", prompts)
	}
	var l ccLine
	content, err := ccDecodeLess(b, &l)
	at := strings.Index(line, `"fix this`)
	if err != nil || l.Type != "user" || l.UUID != "u1" || len(content) != len(big)+len("fix this: ")+2 || &content[0] != &b[at] {
		t.Fatalf("err %v type %q: the content given is not the line's own bytes (%d)", err, l.Type, len(content))
	}
	runtime.GC()
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	before := ms.TotalAlloc
	claudeFull(&state{}, b, true)
	runtime.ReadMemStats(&ms)
	// the prompt's text once, not the line again
	made := ms.TotalAlloc - before
	t.Logf("a prompt line of %d bytes made %d bytes", len(b), made)
	if made > uint64(len(b))*3/2 {
		t.Fatalf("a prompt line of %d bytes made %d bytes", len(b), made)
	}
}
