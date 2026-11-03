package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/masking"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// wangbinyq on Discord: clicking a block of the context window card said
// its text was gone, as magpie keeps only the last 10 requests' bodies in
// memory, with the request archive on. The archive's copy is read instead,
// the reply beside it, masked as the copy in memory is; a request the
// archive has no copy of says why.
func TestContextTextFromTheArchive(t *testing.T) {
	h := archiveHome(t, fakeArchive{objs: map[string][]byte{}})
	archiveBucket = func() (archiveStore, bool) { return nil, false }
	unmask := gwmw.Mask(masking.Masker{})
	t.Cleanup(unmask)
	prev := served.Swap(nil)
	t.Cleanup(func() { served.Store(prev) })
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true, RequestArchiveLocal: true}); err != nil {
		t.Fatal(err)
	}
	lastCall.Lock()
	lastCall.c = archivedCopy{}
	lastCall.Unlock()

	const phone = "13800138000"
	req := `{"model":"m","system":"You are the release agent.","messages":[{"role":"user","content":"call me at ` + phone + ` once it ships"}]}`
	res := `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"I will call ` + phone + ` when it ships."}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":9}}`
	obj, _ := json.Marshal(gateway.Archived{ID: "2026-10-02/101010-00000000000000aa", Call: gateway.Call{Model: "p/m", Status: 200, From: provider.Anthropic},
		Request:  gateway.ArchivePart{Method: "POST", Path: "/v1/messages", Body: req, Size: int64(len(req))},
		Response: gateway.ArchivePart{Status: 200, Body: res, Size: int64(len(res))}})
	if err := gateway.LocalArchive().Put(context.Background(), archiveObject, obj); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 2, 10, 10, 10, 0, time.Local)
	route := func(id int64, archive string) []byte {
		r := ctxRoute(id, "claude", "s1", 3000, 1000, 200000, 0)
		r.Time, r.Archive = day, archive
		b, _ := json.Marshal(r)
		return append(b, '\n')
	}
	os.MkdirAll(gateway.HistoryDir(), 0o700)
	lines := append(route(41, "2026-10-02/101010-00000000000000aa"), route(42, "")...)
	lines = append(lines, route(43, "2026-10-02/101010-00000000000000bb")...)
	os.WriteFile(filepath.Join(gateway.HistoryDir(), "2026-10-02.jsonl"), lines, 0o600)

	get := func(url string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		return rec.Code, rec.Body.String()
	}
	code, body := get("/api/context/text?id=41&kind=chat&item=0&day=2026-10-02")
	var pt gateway.PromptText
	json.Unmarshal([]byte(body), &pt)
	if code != 200 || pt.From != "archive" || len(pt.Items) != 1 || !strings.Contains(body, "once it ships") || strings.Contains(body, phone) {
		t.Fatalf("text: %d %s", code, body)
	}
	if code, body := get("/api/context/text?id=41&kind=system&item=0&day=2026-10-02"); code != 200 || !strings.Contains(body, "You are the release agent.") {
		t.Fatalf("system: %d %s", code, body)
	}
	code, body = get("/api/context/reply?id=41&day=2026-10-02")
	var rp gateway.PromptReply
	json.Unmarshal([]byte(body), &rp)
	if code != 200 || rp.From != "archive" || len(rp.Pieces) != 1 || !strings.Contains(body, "when it ships") || strings.Contains(body, phone) {
		t.Fatalf("reply: %d %s", code, body)
	}
	// no copy named for it: the archive was off
	for _, url := range []string{"/api/context/text?id=42&kind=chat&day=2026-10-02", "/api/context/reply?id=42&day=2026-10-02", "/api/context/text?id=99&kind=chat"} {
		if code, body := get(url); code != http.StatusNotFound || !strings.Contains(body, `"archive":"off"`) {
			t.Fatalf("%s: %d %s", url, code, body)
		}
	}
	// one named and not there
	if code, body := get("/api/context/text?id=43&kind=chat&day=2026-10-02"); code != http.StatusNotFound || !strings.Contains(body, `"archive":"missing"`) {
		t.Fatalf("missing: %d %s", code, body)
	}
	// with masking wanted and the masker failing, nothing is shown: one
	// served last serves, whatever another test left registered
	broken := gwmw.Mask(failingMasker{})
	defer broken()
	if code, body := get("/api/context/reply?id=41&day=2026-10-02"); code != http.StatusNotFound || !strings.Contains(body, `"archive":"unread"`) || strings.Contains(body, phone) {
		t.Fatalf("unmasked: %d %s", code, body)
	}
}

// failingMasker is a masker that can't mask: Safely takes its panic as a
// failure
type failingMasker struct{}

func (failingMasker) JSON([]byte) ([]byte, int)                                  { panic("can't mask") }
func (failingMasker) Text(string) (string, int)                                  { panic("can't mask") }
func (failingMasker) Restore(s string, _ bool) string                            { return s }
func (failingMasker) Writer(w http.ResponseWriter) (http.ResponseWriter, func()) { return w, func() {} }
