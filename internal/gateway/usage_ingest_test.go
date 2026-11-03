package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/usage"
	"github.com/yetone/magpie/internal/usageapi"
)

type ingestReply struct {
	Accepted, Skipped int
	Records           []struct {
		Index       int
		RID, Status string
	}
	Error *struct {
		Message, Param string
		Index          *int
	}
}

func ingestServer(t *testing.T) func(from, body string, hdr ...string) (int, ingestReply, string) {
	t.Helper()
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"id":"anthropic","models":{"claude-opus-5-5":{"id":"claude-opus-5-5","cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	h := lanGuard(New().Handler())
	return func(from, body string, hdr ...string) (int, ingestReply, string) {
		r := httptest.NewRequest("POST", "/v1/magpie/usage/requests", strings.NewReader(body))
		r.RemoteAddr = from
		r.Host = "127.0.0.1:3425" // the address a client on this computer uses
		r.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(hdr); i += 2 {
			if hdr[i] == "Host" {
				r.Host = hdr[i+1]
				continue
			}
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out ingestReply
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Body.String()
	}
}

func reportedRecord(rid string, extra string) string {
	t := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	return `{"t":"` + t + `","agent":"my-app","provider":"claude","model":"claude-opus-5-5","in":1000000,"out":100000,"cache_read":1000000,"cache_write":0,"ms":1200,"ttft_ms":300,"status":200,"rid":"` + rid + `"` + extra + `}`
}

func reportedLines(t *testing.T) int {
	t.Helper()
	b, _ := os.ReadFile(usage.Path())
	return bytes.Count(b, []byte(`"source":"reported"`))
}

// POST /v1/magpie/usage/requests (#1874): this computer reports a call
// it made itself and it is counted, priced at magpie's prices, as
// reported; the same rid again is skipped; a call's own cost is kept; and
// the page and GET tell the reported ones apart and filter them.
func TestUsageIngest(t *testing.T) {
	do := ingestServer(t)
	c, out, b := do("127.0.0.1:5000", reportedRecord("r-1", ""))
	if c != 200 || out.Accepted != 1 || out.Skipped != 0 || len(out.Records) != 1 || out.Records[0].Status != "accepted" || out.Records[0].RID != "r-1" {
		t.Fatal(c, b)
	}
	c, out, b = do("127.0.0.1:5000", "["+reportedRecord("r-1", "")+","+reportedRecord("r-2", `,"cost":0.25`)+","+reportedRecord("r-2", "")+"]")
	if c != 200 || out.Accepted != 1 || out.Skipped != 2 || out.Records[0].Status != "skipped" || out.Records[1].Status != "accepted" || out.Records[2].Status != "skipped" {
		t.Fatal("again:", c, b)
	}
	if n := reportedLines(t); n != 2 {
		t.Fatal("lines:", n)
	}

	h := lanGuard(New().Handler())
	get := func(q string) usageapi.Ledger {
		r := httptest.NewRequest("GET", "/v1/magpie/usage/requests?period=all"+q, nil)
		r.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var l usageapi.Ledger
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &l) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return l
	}
	l := get("")
	priced := (1e6*4 + 1e5*20 + 1e6*0.2) / 1e6
	if l.Total != 2 || l.Reported.Calls != 2 || l.Through.Calls != 0 || math.Abs(l.Cost-(priced+0.25)) > 1e-9 {
		t.Fatalf("total %d, reported %+v, through %+v, cost %v", l.Total, l.Reported, l.Through, l.Cost)
	}
	for _, r := range l.Rows {
		if r.Source != usage.SourceReported || !r.Priced {
			t.Fatalf("row %+v", r.Row)
		}
		if want := map[string]float64{"r-1": priced, "r-2": 0.25}[r.RequestID]; math.Abs(r.Cost-want) > 1e-9 {
			t.Fatalf("%s cost %v, want %v", r.RequestID, r.Cost, want)
		}
	}
	if l := get("&via=reported"); l.Total != 2 {
		t.Fatal("via=reported:", l.Total)
	}
	if l := get("&via=through"); l.Total != 0 || l.Reported.Calls != 2 {
		t.Fatal("via=through:", l.Total, l.Reported)
	}
}

// A backfill (#1874): a full batch of a client's history, however old, is
// taken as dated, each record answered, and lands on its own day for the
// 7d, 30d and all views; sent again, it is skipped whole.
func TestUsageIngestBackfill(t *testing.T) {
	do := ingestServer(t)
	now := time.Now()
	ages := []time.Duration{3 * 365 * 24 * time.Hour, 20 * 24 * time.Hour, 3 * 24 * time.Hour}
	recs := make([]string, ingestMaxRecords)
	for i := range recs {
		at := now.Add(-ages[i%len(ages)]).UTC().Format(time.RFC3339Nano)
		recs[i] = fmt.Sprintf(`{"t":%q,"agent":"my-app","provider":"claude","model":"claude-opus-5-5","in":10,"out":5,"status":200,"rid":"old-%d"}`, at, i)
	}
	body := "[" + strings.Join(recs, ",") + "]"
	c, out, b := do("127.0.0.1:5000", body)
	if c != 200 || out.Accepted != ingestMaxRecords || len(out.Records) != ingestMaxRecords || out.Records[4999].Index != 4999 || out.Records[4999].Status != "accepted" {
		t.Fatalf("%d %.300s", c, b)
	}
	if c, out, b := do("127.0.0.1:5000", body); c != 200 || out.Accepted != 0 || out.Skipped != ingestMaxRecords {
		t.Fatalf("again: %d %.300s", c, b)
	}
	by := make([]int, len(ages))
	for i := range recs {
		by[i%len(ages)]++
	}
	want := map[usage.Period]int{usage.Today: 0, usage.Week: by[2], usage.Month: by[1] + by[2], usage.All: ingestMaxRecords}
	for p, n := range want {
		page := usage.QueryPage(p, usage.Filter{}, 0, 1)
		if page.Total != n || page.Reported.Calls != n {
			t.Errorf("%s: %d rows, %d reported; want %d", p, page.Total, page.Reported.Calls, n)
		}
		if s := usage.Summarize(p); s.Calls != n {
			t.Errorf("%s summary: %d calls; want %d", p, s.Calls, n)
		}
	}
}

// A reported call never touches a key's limit (#1874): not one posted with
// the key (the key only lets the post in), and not a line that names the
// key itself. Nor is it an answer Codex's sealed reasoning was given by.
func TestUsageIngestTouchesNoLimit(t *testing.T) {
	do := ingestServer(t)
	// both calls are stamped now, not a minute ago: a minute before 00:00:37
	// UTC is yesterday, outside the key's day, and the test went red on CI
	now := time.Now()
	if c, out, b := do("127.0.0.1:5000", reportedRecord("r-1", `,"session":"conv-1"`)); c != 200 || out.Accepted != 1 {
		t.Fatal(c, b)
	}
	usage.Append(usage.Record{Time: now, Agent: "my-app", Provider: "claude", Model: "claude-opus-5-5", Input: 5000, Output: 5000, Status: 200, RequestID: "r-2", Source: usage.SourceReported, CallerKeyID: "k1", Session: "conv-1"})
	st := budget.Of(access.Key{ID: "k1", Limit: &access.Limit{Period: "day", Tokens: 700}}, now)
	if st == nil || st.Calls != 0 || st.Tokens != 0 || st.Spent {
		t.Fatalf("a reported call counted against the key: %+v", st)
	}
	if a := loggedAnswers("conv-1"); len(a) != 0 {
		t.Fatalf("a reported call answered a Codex conversation: %+v", a)
	}
	// the gateway's own call of the key still counts
	usage.Append(usage.Record{Time: now, Agent: "my-app", Provider: "claude", Model: "claude-opus-5-5", Input: 500, Output: 500, Status: 200, CallerKeyID: "k1", Session: "conv-1"})
	if st := budget.Of(access.Key{ID: "k1", Limit: &access.Limit{Period: "day", Tokens: 700}}, now); st.Calls != 1 || !st.Spent {
		t.Fatalf("the key's own call: %+v", st)
	}
	if a := loggedAnswers("conv-1"); len(a) != 1 {
		t.Fatalf("the gateway's own answer: %+v", a)
	}
}

// Who may report: this computer; another only with an enabled key while
// shared; a browser page, from anywhere, only with a key, since corsGuard
// lets a page on localhost through keyless (an opaque origin it refuses);
// to a budget. Nothing refused is written.
func TestUsageIngestAccess(t *testing.T) {
	do := ingestServer(t)
	rec := reportedRecord("r-1", "")
	// not shared: another machine is refused outright
	if c, _, b := do("192.168.1.9:5000", rec); c != http.StatusForbidden {
		t.Fatal("not shared:", c, b)
	}
	_, secrets := newCaller(t, "Laptop")
	if c, _, b := do("192.168.1.9:5000", rec); c != http.StatusUnauthorized {
		t.Fatal("another machine with no key:", c, b)
	}
	if c, _, b := do("192.168.1.9:5000", rec, "Authorization", "Bearer sk-magpie-wrong-wrong-wrong"); c != http.StatusUnauthorized {
		t.Fatal("another machine with a wrong key:", c, b)
	}
	pages := [][]string{
		{"Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors"},
		{"Origin", "http://localhost:5173", "Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "cors"},
		{"Origin", "null", "Sec-Fetch-Mode", "cors"},
		{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "no-cors", "User-Agent", "Mozilla/5.0"},
	}
	for _, hdr := range pages {
		if c, _, b := do("127.0.0.1:5000", rec, hdr...); c != http.StatusForbidden && c != http.StatusUnauthorized {
			t.Fatal("a page with no key:", hdr, c, b)
		}
		// a rebound name, a page that thinks it is its own origin
		if c, _, b := do("127.0.0.1:5000", rec, append([]string{"Host", "rebound.example:3425"}, hdr...)...); c != http.StatusForbidden && c != http.StatusUnauthorized {
			t.Fatal("a rebound page:", hdr, c, b)
		}
	}
	if n := reportedLines(t); n != 0 {
		t.Fatal("a refused post wrote", n)
	}
	// with a key: another machine, and a page on this computer
	if c, out, b := do("192.168.1.9:5000", rec, "Authorization", "Bearer "+secrets[0]); c != 200 || out.Accepted != 1 {
		t.Fatal("another machine with the key:", c, b)
	}
	if c, out, b := do("127.0.0.1:5000", reportedRecord("r-2", ""), "Origin", "http://localhost:5173", "Authorization", "Bearer "+secrets[0]); c != 200 || out.Accepted != 1 {
		t.Fatal("a local page with the key:", c, b)
	}
	// a JSON body is required, so no form post can carry one
	if c, _, b := do("127.0.0.1:5000", rec, "Content-Type", "text/plain"); c != http.StatusUnsupportedMediaType {
		t.Fatal("text/plain:", c, b)
	}
	if n := reportedLines(t); n != 2 {
		t.Fatal("lines:", n)
	}
}

// A batch with one bad record answers 400 naming the record and its field,
// and writes none of them.
func TestUsageIngestValidation(t *testing.T) {
	do := ingestServer(t)
	good := reportedRecord("ok-1", "")
	future := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	cases := []struct {
		bad, field string
	}{
		{strings.Replace(reportedRecord("b", ""), `"in":1000000`, `"in":-5`, 1), "in"},
		{strings.Replace(reportedRecord("b", ""), `"out":100000`, `"out":1e12`, 1), "out"},
		{strings.Replace(reportedRecord("b", ""), `"cache_read":1000000`, `"cache_read":1.5`, 1), "cache_read"},
		{strings.Replace(reportedRecord("b", ""), `"in":1000000`, `"in":"10"`, 1), "in"},
		{strings.Replace(reportedRecord("b", ""), `,"rid":"b"`, ``, 1), "rid"},
		{strings.Replace(reportedRecord("b", ""), `"model":"claude-opus-5-5"`, `"model":""`, 1), "model"},
		{strings.Replace(reportedRecord("b", ""), `"status":200`, `"status":42`, 1), "status"},
		{strings.Replace(reportedRecord("b", ""), `"ms":1200`, `"ms":999999999999`, 1), "ms"},
		{reportedRecord("b", `,"cost":-1`), "cost"},
		{reportedRecord("b", `,"cost":1e9`), "cost"},
		{reportedRecord("b", `,"source":"through"`), "source"},
		{reportedRecord("b", `,"callerKeyId":"k1"`), "callerKeyId"},
		{`{"t":"` + future + `","agent":"a","provider":"p","model":"m","status":200,"rid":"b"}`, "t"},
		{`{"t":"yesterday","agent":"a","provider":"p","model":"m","status":200,"rid":"b"}`, "t"},
		{`{"t":"1999-12-31T23:59:59Z","agent":"a","provider":"p","model":"m","status":200,"rid":"b"}`, "t"},
		{`[1]`, "record"},
	}
	for _, c := range cases {
		code, out, b := do("127.0.0.1:5000", "["+good+","+c.bad+"]")
		if code != 400 || out.Error == nil || out.Error.Param != c.field || out.Error.Index == nil || *out.Error.Index != 1 || !strings.Contains(out.Error.Message, "record 1: "+c.field) {
			t.Errorf("%s: %d %s", c.bad, code, b)
		}
	}
	for _, body := range []string{"[]", "", "42", `[` + strings.Repeat(good+",", ingestMaxRecords) + good + `]`} {
		if code, out, b := do("127.0.0.1:5000", body); code != 400 || out.Error == nil || out.Error.Param != "body" {
			t.Errorf("body %.40q: %d %.200s", body, code, b)
		}
	}
	big := `[` + good + `,` + reportedRecord("big", `,"err":"`+strings.Repeat("x", ingestMaxBody)+`"`) + `]`
	if code, _, b := do("127.0.0.1:5000", big); code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over the limit: %d %.200s", code, b)
	}
	if n := reportedLines(t); n != 0 {
		t.Fatal("a refused batch wrote", n)
	}
	// the same batch, fixed, is taken whole
	if code, out, b := do("127.0.0.1:5000", "["+good+","+reportedRecord("ok-2", `,"cost":null,"effort":"high","session":"s1","err":"","reasoning":10,"cache_write_1h":0`)+"]"); code != 200 || out.Accepted != 2 {
		t.Fatal(code, b)
	}
}

// Posts at once, beside the gateway's own calls: each rid once, every line
// whole.
func TestUsageIngestConcurrent(t *testing.T) {
	do := ingestServer(t)
	const posts, each = 12, 30
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for p := range posts {
		wg.Add(2)
		go func() {
			defer wg.Done()
			var recs []string
			for i := range each {
				rid := fmt.Sprintf("shared-%d", i)
				if i%2 == 1 {
					rid = fmt.Sprintf("own-%d-%d", p, i)
				}
				recs = append(recs, reportedRecord(rid, ""))
			}
			c, out, b := do("127.0.0.1:5000", "["+strings.Join(recs, ",")+"]")
			if c != 200 || out.Accepted+out.Skipped != each {
				t.Error(c, b)
			}
			mu.Lock()
			accepted += out.Accepted
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			usage.Append(usage.Record{Time: time.Now().Add(-time.Hour), Agent: "claude", Provider: "claude", Model: "claude-opus-5-5", Input: 1, Output: 1, Status: 200})
		}()
	}
	wg.Wait()
	if want := each/2 + posts*each/2; accepted != want {
		t.Fatalf("accepted %d, want %d", accepted, want)
	}
	f, _ := os.Open(usage.Path())
	defer f.Close()
	b, _ := io.ReadAll(f)
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != accepted+posts {
		t.Fatalf("%d lines, want %d", len(lines), accepted+posts)
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("a torn line: %.200s", line)
		}
	}
}

// #2637 (tsy0716): a reported call carries first_text_ms and flow_ms, and
// its speed is told as a gateway call's is. The reporter's own record, a
// grok-4.7 reply that reasoned, tells its answer's speed from its first
// text; without first_text_ms it told none. A reply let go in a burst
// tells none, whether its flow_ms says so (HeldShare) or its answer came
// over 20 times faster than its reasoning before it (ThinkPace). The
// reporter's own record, all of it 403 ms after a 4.6 s first token, now
// tells none either (HeldWindowMs, pluviophile714 on Discord); the same
// record after a 0.3 s first token tells its answer's.
func TestUsageIngestTellsSpeed(t *testing.T) {
	do := ingestServer(t)
	repro := `{
  "t":"2026-10-17T00:24:29Z","agent":"my-app","provider":"xai","model":"grok-4.7","rid":"repro-1",
  "in":10696,"out":307,"reasoning":129,"ms":5035,"ttft_ms":4632,"first_text_ms":4700,"status":200}`
	at := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	// a reply that didn't reason, its 601 tokens let go in the last 109 ms
	// (its flow, scaled as the gateway scales it, 136 ms) of a 4 s window
	quick := strings.NewReplacer(`"ttft_ms":4632,"first_text_ms":4700`, `"ttft_ms":232,"first_text_ms":300`, `"ms":5035`, `"ms":635`, "repro-1", "quick-1").Replace(repro)
	burst := `{"t":"` + at + `","agent":"my-app","provider":"xai","model":"grok-4.7","rid":"burst-1","in":1000,"out":601,"ms":5035,"ttft_ms":1000,"flow_ms":136,"status":200}`
	// the same reply, streamed as it was written
	even := strings.Replace(strings.Replace(burst, `"flow_ms":136`, `"flow_ms":4000`, 1), "burst-1", "even-1", 1)
	// 22 reasoning tokens over 4.4 s, then 528 answer tokens in 335 ms
	held := `{"t":"` + at + `","agent":"my-app","provider":"xai","model":"grok-4.7","rid":"held-1","in":1000,"out":550,"reasoning":22,"ms":5035,"ttft_ms":300,"first_text_ms":4700,"status":200}`
	c, out, b := do("127.0.0.1:5000", "["+repro+","+quick+","+burst+","+even+","+held+"]")
	if c != 200 || out.Accepted != 5 {
		t.Fatal(c, b)
	}

	h := lanGuard(New().Handler())
	r := httptest.NewRequest("GET", "/v1/magpie/usage/requests?period=all&via=reported", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var l usageapi.Ledger
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &l) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	speed := map[string][2]int64{}
	for _, row := range l.Rows {
		n, w := row.Row.Decode()
		speed[row.RequestID] = [2]int64{int64(n), w}
	}
	want := map[string][2]int64{
		"repro-1": {0, 0},                 // held: all of it under half a second after 4.6 s
		"quick-1": {307 - 129, 635 - 300}, // its answer, from its first text
		"burst-1": {0, 0},                 // held: its content came in under a quarter of its window
		"even-1":  {601, 5035 - 1000},
		"held-1":  {0, 0}, // held: its answer came over 20 times its reasoning's pace
	}
	for rid, w := range want {
		if speed[rid] != w {
			t.Errorf("%s: told %d tokens over %d ms, want %d over %d", rid, speed[rid][0], speed[rid][1], w[0], w[1])
		}
	}
	// the page's totals count the two that tell a speed
	if got := l.Reported; got.DecodeN != 2 || got.DecodeOut != 178+601 || got.DecodeMs != 335+4035 {
		t.Errorf("reported totals: %d timed, %d tokens over %d ms", got.DecodeN, got.DecodeOut, got.DecodeMs)
	}
}

// first_text_ms and flow_ms are checked as ttft_ms is, and against the
// call they are in: first text within ms and never before the first
// content; flow within ms as the gateway scales it.
func TestUsageIngestTimesChecked(t *testing.T) {
	do := ingestServer(t)
	good := reportedRecord("ok-1", "")
	cases := []struct{ extra, field string }{
		{`,"first_text_ms":1201`, "first_text_ms"}, // after the call's 1200 ms
		{`,"first_text_ms":299`, "first_text_ms"},  // before its 300 ms first content
		{`,"first_text_ms":-1`, "first_text_ms"},
		{`,"first_text_ms":"400"`, "first_text_ms"},
		{`,"first_text_ms":400.5`, "first_text_ms"},
		{`,"flow_ms":1502`, "flow_ms"}, // over 1200×10/8
		{`,"flow_ms":-1`, "flow_ms"},
		{`,"flow_ms":1e12`, "flow_ms"},
	}
	for _, c := range cases {
		code, out, b := do("127.0.0.1:5000", "["+good+","+reportedRecord("b", c.extra)+"]")
		if code != 400 || out.Error == nil || out.Error.Param != c.field {
			t.Errorf("%s: %d %s", c.extra, code, b)
		}
	}
	if n := reportedLines(t); n != 0 {
		t.Fatal("a refused batch wrote", n)
	}
	// the edges are taken, and so is either left out or null
	ok := []string{`,"first_text_ms":300,"flow_ms":1501`, `,"first_text_ms":1200,"flow_ms":0`, `,"first_text_ms":null,"flow_ms":null`, `,"reasoning":10`}
	for i, extra := range ok {
		if code, out, b := do("127.0.0.1:5000", reportedRecord(fmt.Sprintf("ok-%d", i), extra)); code != 200 || out.Accepted != 1 {
			t.Errorf("%s: %d %s", extra, code, b)
		}
	}
}
