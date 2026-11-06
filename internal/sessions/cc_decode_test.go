package sessions

import (
	"encoding/json"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

// ccDecode reads a reply line as json.Unmarshal of it whole did, less its
// content: strings escaped or not, bytes that aren't UTF-8, a field that
// holds no string, a line with no content or with content first.
func TestCCDecodeAsUnmarshal(t *testing.T) {
	big := strings.Repeat(`{"type":"text","text":"a \"quoted\" line\n"},`, 2000)
	lines := []string{
		`{"parentUuid":"p","isSidechain":false,"userType":"external","cwd":"/Users/x/w","sessionId":"s-1","version":"2.1.5","gitBranch":"main","message":{"model":"claude-opus","id":"msg_1","type":"message","role":"assistant","content":[` + big + `{"type":"text","text":"end"}],"stop_reason":null,"usage":{"input_tokens":3,"cache_creation_input_tokens":10,"cache_read_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":10},"output_tokens":7,"service_tier":"standard"}},"requestId":"req_1","type":"assistant","uuid":"u-1","timestamp":"2026-11-05T10:00:00.123Z","entrypoint":"cli","effort":"high"}`,
		`{"type":"assistant","cwd":"C:\\Users\\x \"q\"","sessionId":"\u00e9t\u00e9","requestId":"r\/2","message":{"content":"plain","id":"m","model":"m"},"uuid":"u","timestamp":"2026-11-05T10:00:00Z"}`,
		"{\"type\":\"assistant\",\"cwd\":\"bad \xff\xfe utf8\",\"sessionId\":\"ok\",\"message\":{\"id\":\"m\",\"content\":[]},\"timestamp\":\"2026-11-05T10:00:00Z\"}",
		`{"type":"assistant","cwd":42,"sessionId":null,"requestId":["x"],"error":{"a":1},"effort":true,"perTurnEffort":null,"message":{"id":"m","model":"m","usage":{"input_tokens":1}},"timestamp":"2026-11-05T10:00:00Z"}`,
		`{"message":{"content":[{"type":"tool_use","input":{"cwd":"no"}}],"id":"m"},"type":"assistant","cwd":"after","timestamp":"t"}`,
		`{"type":"assistant","isApiErrorMessage":true,"error":"rate_limit","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error"}]}}`,
		`{"type":"assistant","cwd":"","sessionId":"\u2028\t","message":{"content":null}}`,
		`not json`,
		``,
	}
	for _, line := range lines {
		var want, got ccCall
		werr := json.Unmarshal([]byte(line), &want)
		gerr := ccDecode([]byte(line), &got)
		if (werr == nil) != (gerr == nil) || !reflect.DeepEqual(got, want) {
			t.Fatalf("%.80s:\n got %+v %v\nwant %+v %v", line, got, gerr, want, werr)
		}
	}
}

// A reply line is decoded without a copy of it or of its content, and
// with no decoder of its own for each string field: a first start's read
// of a 21 GB history made ~140 MB of those that nothing kept (#2278).
func TestCCDecodeAllocations(t *testing.T) {
	content := `[{"type":"thinking","thinking":"` + strings.Repeat("x", 40<<10) + `","signature":"s"},{"type":"text","text":"done"}]`
	line := []byte(`{"parentUuid":"0b6e","isSidechain":false,"userType":"external","cwd":"/Users/someone/workspace/projects/app","sessionId":"5f2a9c1e-8d1b-4a5e-9c33-2f1e0a7b6c4d","version":"2.1.5","gitBranch":"main","message":{"model":"claude-opus-4-1","id":"msg_01ABCDEF","type":"message","role":"assistant","content":` + content + `,"stop_reason":null,"usage":{"input_tokens":3,"cache_creation_input_tokens":10,"cache_read_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":10},"output_tokens":7,"service_tier":"standard"}},"requestId":"req_011CABCDEF","type":"assistant","uuid":"8c1d2e3f-0000-4000-8000-000000000001","timestamp":"2026-11-05T10:00:00.123Z","entrypoint":"cli","effort":"high"}`)
	var l ccCall
	if err := ccDecode(line, &l); err != nil || l.Cwd != "/Users/someone/workspace/projects/app" || l.Message.Usage == nil || l.Message.Usage.Output != 7 {
		t.Fatalf("%+v %v", l, err)
	}
	runtime.GC()
	defer debug.SetGCPercent(debug.SetGCPercent(-1)) // a collection empties the pool
	var ms runtime.MemStats
	least := uint64(1 << 62)
	for range 5 {
		runtime.ReadMemStats(&ms)
		before := ms.TotalAlloc
		for range 50 {
			var l ccCall
			ccDecode(line, &l)
		}
		runtime.ReadMemStats(&ms)
		least = min(least, (ms.TotalAlloc-before)/50)
	}
	// ~900 bytes: the strings and usage it keeps, and json.Unmarshal's own;
	// a decoder for each string field made ~1.7 KB, a copy of the line 52 KB
	if least > 1300 {
		t.Fatalf("a %d-byte reply line made %d bytes, want at most 1300", len(line), least)
	}
}
