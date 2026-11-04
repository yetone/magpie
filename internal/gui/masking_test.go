package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/masking"
	"github.com/yetone/magpie/internal/maskrec"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
)

// Settings → Privacy reads what masking swapped: the summary, the records
// newest first, one record, and a value only while the masker remembers
// it; a placeholder it never made says nothing.
func TestMaskingRecordsAPI(t *testing.T) {
	const key = "sk-proj-abcdEFGH1234ijklMNOP5678qrst"
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	defer gwmw.Mask(masking.Masker{})()
	m, _ := gwmw.Served()
	sent, hits := m.(gwmw.Teller).TextHits("my key is " + key)
	if len(hits) != 1 || strings.Contains(sent, key) {
		t.Fatalf("masked %q %+v", sent, hits)
	}
	p := hits[0].Placeholder
	rec := maskrec.Add(maskrec.Record{What: "chat", Agent: "claude", Provider: "anthropic", N: 1,
		Values: []maskrec.Value{{Placeholder: p, Kind: hits[0].Kind, By: hits[0].By, Head: hits[0].Head, Len: hits[0].Len}},
		Snips:  map[string]string{p: sent}})
	other := maskrec.Add(maskrec.Record{What: "search", N: 1, Values: []maskrec.Value{{Placeholder: "{{EMAIL_00000000}}", Kind: "EMAIL", By: redact.ByRule}}})

	srv := webPageHandler(t, webHost{func() {}}, nil)
	get := func(path string, into any) int {
		rw := httptest.NewRecorder()
		srv.ServeHTTP(rw, httptest.NewRequest("GET", path, nil))
		if into != nil && rw.Code == http.StatusOK {
			if err := json.Unmarshal(rw.Body.Bytes(), into); err != nil {
				t.Fatalf("%s: %v %s", path, err, rw.Body)
			}
		}
		return rw.Code
	}
	var sum maskrec.Summary
	if get("/api/masking/summary?days=7", &sum); sum.Requests < 2 || len(sum.Days) != 7 {
		t.Fatalf("summary %+v", sum)
	}
	var page struct {
		Records []maskedRecord `json:"records"`
		More    bool           `json:"more"`
	}
	page.Records, page.More = nil, false
	if get("/api/masking/records?limit=1", &page); len(page.Records) != 1 || !page.More || page.Records[0].Key != other.Key {
		t.Fatalf("first page %+v", page)
	}
	page.Records, page.More = nil, false
	if get("/api/masking/records?limit=5&before="+strconv.FormatInt(other.Key, 10), &page); len(page.Records) == 0 || page.Records[0].Key != rec.Key || !page.Records[0].Values[0].Known {
		t.Fatalf("next page %+v", page)
	}
	page.Records, page.More = nil, false
	if get("/api/masking/records?kind=EMAIL", &page); len(page.Records) == 0 || page.Records[0].Key != other.Key || page.Records[0].Values[0].Known {
		t.Fatalf("by kind %+v", page)
	}
	var one maskedRecord
	if get("/api/masking/record?key="+strconv.FormatInt(rec.Key, 10), &one); one.Snips[p] != sent || !one.Values[0].Known {
		t.Fatalf("record %+v", one)
	}
	// the key goes as a string: a JS number holds a nanosecond key only
	// to 256ns, and the rounded key found no record
	for _, path := range []string{"/api/masking/record?key=" + strconv.FormatInt(rec.Key, 10), "/api/masking/records?limit=1"} {
		rw := httptest.NewRecorder()
		srv.ServeHTTP(rw, httptest.NewRequest("GET", path, nil))
		if !strings.Contains(rw.Body.String(), `"key":"`) {
			t.Fatalf("%s: key not a string: %s", path, rw.Body)
		}
	}
	if code := get("/api/masking/record?key=1", nil); code != http.StatusNotFound {
		t.Fatalf("no such record: %d", code)
	}
	var v struct{ Value string }
	if get("/api/masking/value?p="+url.QueryEscape(p), &v); v.Value != key {
		t.Fatalf("value %q", v.Value)
	}
	if code := get("/api/masking/value?p="+url.QueryEscape("{{EMAIL_00000000}}"), nil); code != http.StatusNotFound {
		t.Fatalf("a placeholder it never made: %d", code)
	}

	// the request as it went, and the values to read it as it came
	body := `{"messages":[{"role":"user","content":"` + sent + `"}]}`
	whole := maskrec.Add(maskrec.Record{What: "chat", N: 1, Sent: body,
		Values: []maskrec.Value{{Placeholder: p, Kind: hits[0].Kind, By: hits[0].By, Len: hits[0].Len}}})
	var got struct{ Body string }
	if get("/api/masking/sent?key="+strconv.FormatInt(whole.Key, 10), &got); got.Body != body {
		t.Fatalf("sent %q", got.Body)
	}
	if code := get("/api/masking/sent?key="+strconv.FormatInt(rec.Key, 10), nil); code != http.StatusNotFound {
		t.Fatalf("a record kept without its body: %d", code)
	}
	rw := httptest.NewRecorder()
	srv.ServeHTTP(rw, httptest.NewRequest("POST", "/api/masking/values", strings.NewReader(`{"ps":["`+p+`","{{EMAIL_00000000}}"]}`)))
	var vs struct{ Values map[string]string }
	if err := json.Unmarshal(rw.Body.Bytes(), &vs); err != nil || len(vs.Values) != 1 || vs.Values[p] != key || rw.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("values %d %s %v", rw.Code, rw.Body, rw.Header())
	}

	// Clear takes every record and its text, by POST alone; what a
	// placeholder stood for stays with the masker for answers to come
	// (maxyou2090 on Discord)
	rw = httptest.NewRecorder()
	srv.ServeHTTP(rw, httptest.NewRequest("GET", "/api/masking/clear", nil))
	if rw.Code == http.StatusOK {
		t.Fatalf("GET clear: %d", rw.Code)
	}
	if get("/api/masking/records?limit=5", &page); len(page.Records) == 0 {
		t.Fatal("a GET cleared the records")
	}
	rw = httptest.NewRecorder()
	srv.ServeHTTP(rw, httptest.NewRequest("POST", "/api/masking/clear", nil))
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), `"cleared":true`) {
		t.Fatalf("clear: %d %s", rw.Code, rw.Body)
	}
	page.Records, page.More = nil, false
	if get("/api/masking/records?limit=5", &page); len(page.Records) != 0 || page.More {
		t.Fatalf("records after clear %+v", page)
	}
	sum = maskrec.Summary{}
	if get("/api/masking/summary?days=30", &sum); sum.Requests != 0 {
		t.Fatalf("summary after clear %+v", sum)
	}
	for _, path := range []string{"/api/masking/record?key=" + strconv.FormatInt(rec.Key, 10), "/api/masking/sent?key=" + strconv.FormatInt(whole.Key, 10)} {
		if code := get(path, nil); code != http.StatusNotFound {
			t.Fatalf("%s after clear: %d", path, code)
		}
	}
	if get("/api/masking/value?p="+url.QueryEscape(p), &v); v.Value != key {
		t.Fatalf("value after clear %q", v.Value)
	}
}

// Try it under Settings › Privacy shows what a request would send: with
// Keep a key's form on, a made-up key of the same form, not a
// placeholder.
func TestMaskingPreviewKeepsKeyForm(t *testing.T) {
	const key = "sk_w3axCByTQgK1PxqPhfKTrJOLBqz6sbZv"
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	srv := webPageHandler(t, webHost{func() {}}, nil)
	preview := func() string {
		rw := httptest.NewRecorder()
		srv.ServeHTTP(rw, httptest.NewRequest("POST", "/api/masking/preview", strings.NewReader(`{"text":"用密钥 `+key+` 部署可以吗，或用密钥 OPENAI_API_KEY=`+key+`"}`)))
		var out struct{ Text string }
		if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
			t.Fatalf("%v %s", err, rw.Body)
		}
		return out.Text
	}
	for _, shape := range []bool{false, true} {
		if err := settings.Save(settings.Settings{Redact: true, RedactShape: shape}); err != nil {
			t.Fatal(err)
		}
		got := preview()
		if strings.Contains(got, key) || strings.Contains(got, "{{") == shape || shape && (!strings.Contains(got, "=sk_") || !strings.Contains(got, "用密钥 sk_")) {
			t.Errorf("keep form %v: %q", shape, got)
		}
	}
}
