package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/artifact"
)

// pageUpload is a page's assets.upload, as runtime.js makes it: the
// file as a text/plain body from its opaque origin, its type in the url.
func pageUpload(h http.Handler, ref, tok, typ, body string, hdr ...string) (int, map[string]any) {
	r := httptest.NewRequest(http.MethodPost, ArtifactPagePath+ref+"/_api/upload?t="+tok+"&type="+strings.ReplaceAll(typ, "+", "%2B"), strings.NewReader(body))
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "127.0.0.1:3425"
	r.Header.Set("Origin", "null")
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("Sec-Fetch-Mode", "cors")
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			r.Host = hdr[i+1]
			continue
		}
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// blobGet is a page's <img src="/_blob/…">: a GET from its opaque origin.
func blobGet(h http.Handler, path string, hdr ...string) *httptest.ResponseRecorder {
	return pageGet(h, path, append([]string{"Origin", "null", "Sec-Fetch-Mode", "no-cors", "Sec-Fetch-Dest", "image"}, hdr...)...)
}

const assetPNG = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR0123456789"

// A page that declares assets uploads, lists and deletes them from its
// own origin, and shows them at /_blob/<id>, served as their type and
// sandboxed when opened as a document.
func TestArtifactPageAssets(t *testing.T) {
	h := artifacts(t)
	a := selfPage(t, `{"assets": {}, "db": {}}`, nil)
	tok := pageToken(t, h, ArtifactPagePath+a.ID+"/")
	code, up := pageUpload(h, a.ID, tok, "image/png", assetPNG)
	id, _ := up["id"].(string)
	if code != 200 || !artifact.ValidAssetID(id) || up["url"] != "/_blob/"+id || up["contentType"] != "image/png" || up["sizeBytes"] != float64(len(assetPNG)) {
		t.Fatalf("upload: %d %v", code, up)
	}
	code, svg := pageUpload(h, a.ID, tok, "image/svg+xml", `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><script>alert(2)</script><circle r="1"/></svg>`)
	if code != 200 {
		t.Fatalf("svg: %d %v", code, svg)
	}
	code, list := pageCall(h, a.ID, "assets", map[string]any{"t": tok, "op": "list"})
	items, _ := list["assets"].([]any)
	u, _ := list["usage"].(map[string]any)
	if code != 200 || len(items) != 2 || items[0].(map[string]any)["id"] != id || u["files"] != float64(2) || u["maxFiles"] != float64(artifact.MaxAssets) {
		t.Fatalf("list: %d %v", code, list)
	}

	w := blobGet(h, "/_blob/"+id)
	hd := w.Header()
	if w.Code != 200 || w.Body.String() != assetPNG || hd.Get("Content-Type") != "image/png" || hd.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(hd.Get("Content-Security-Policy"), "sandbox") || hd.Get("Access-Control-Allow-Origin") != "*" || hd.Get("ETag") != `"`+id+`"` {
		t.Fatalf("blob: %d %v %q", w.Code, hd, w.Body.String())
	}
	// a video seeks
	if w := blobGet(h, "/_blob/"+id, "Range", "bytes=0-3"); w.Code != 206 || w.Body.String() != assetPNG[:4] {
		t.Fatalf("a range: %d %q", w.Code, w.Body.String())
	}
	sid, _ := svg["id"].(string)
	if body := blobGet(h, "/_blob/"+sid).Body.String(); strings.Contains(body, "alert") || !strings.Contains(body, "<circle") {
		t.Fatalf("the svg served: %s", body)
	}
	// from another computer, or at another name, it isn't served
	r := httptest.NewRequest(http.MethodGet, "/_blob/"+id, nil)
	r.RemoteAddr, r.Host = "192.168.1.20:50001", "192.168.1.10:3425"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 || strings.Contains(w.Body.String(), "PNG") {
		t.Fatalf("a LAN read: %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/_blob/"+id, nil)
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "evil.example:3425"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 || strings.Contains(w.Body.String(), "PNG") {
		t.Fatalf("a rebound name read it: %d", w.Code)
	}
	if w := blobGet(h, "/_blob/../meta.json"); w.Code == 200 {
		t.Fatalf("a path as an id: %d", w.Code)
	}

	// delete by its url, then again
	code, del := pageCall(h, a.ID, "assets", map[string]any{"t": tok, "op": "delete", "ref": "/_blob/" + id})
	if code != 200 || del["deleted"] != true {
		t.Fatalf("delete: %d %v", code, del)
	}
	if _, del := pageCall(h, a.ID, "assets", map[string]any{"t": tok, "op": "delete", "ref": id}); del["deleted"] != false {
		t.Fatalf("a second delete: %v", del)
	}
	if w := blobGet(h, "/_blob/"+id); w.Code != 404 {
		t.Fatalf("a deleted blob: %d", w.Code)
	}
}

// What a page is refused, by the code the contract gives.
func TestArtifactPageAssetsRefused(t *testing.T) {
	h := artifacts(t)
	a := selfPage(t, `{"assets": {}}`, nil)
	tok := pageToken(t, h, ArtifactPagePath+a.ID+"/")
	for _, c := range []struct{ typ, body, code string }{
		{"text/html", "<p>", "unsupported_type"},
		{"text/plain;charset=utf-8", "hi", "unsupported_type"},
		{"image/png", "<svg onload=alert(1)>", "unsupported_type"},
		{"image/png", "", "invalid_request"},
		{"text/csv", "\xff\xfea\x00", "invalid_request"},
		{"image/svg+xml", "<svg>" + strings.Repeat(" ", artifact.MaxSVGAsset), "too_large"},
		{"image/png", strings.Repeat("x", artifact.MaxAsset+1), "too_large"},
	} {
		if code, out := pageUpload(h, a.ID, tok, c.typ, c.body); code == 200 || errCode(out) != c.code {
			t.Errorf("%s %.20q: %d %v, want %s", c.typ, c.body, code, out, c.code)
		}
	}
	if code, out := pageCall(h, a.ID, "assets", map[string]any{"t": tok, "op": "delete", "ref": "../meta.json"}); code == 200 || errCode(out) != "invalid_request" {
		t.Errorf("a path to delete: %d %v", code, out)
	}
	// a wrong token, a page that doesn't declare assets, another computer
	if code, out := pageUpload(h, a.ID, "nope", "image/png", assetPNG); code != 403 || errCode(out) != "not_granted" {
		t.Errorf("a wrong token: %d %v", code, out)
	}
	b := selfPage(t, `{"db": {}}`, nil)
	if code, out := pageUpload(h, b.ID, ptok(t, b.ID), "image/png", assetPNG); code != 403 || errCode(out) != "not_granted" {
		t.Errorf("undeclared: %d %v", code, out)
	}
	if code, out := pageCall(h, b.ID, "assets", map[string]any{"t": ptok(t, b.ID), "op": "list"}); code != 403 || errCode(out) != "not_granted" {
		t.Errorf("undeclared list: %d %v", code, out)
	}
	if code, _ := pageUpload(h, a.ID, tok, "image/png", assetPNG, "Host", "evil.example:3425"); code == 200 {
		t.Errorf("a rebound name uploaded: %d", code)
	}
	// a page uploads at most artifactUploadMost a minute
	c := selfPage(t, `{"assets": {}}`, nil)
	ctok := ptok(t, c.ID)
	for i := 1; i <= artifactUploadMost+1; i++ {
		code, out := pageUpload(h, c.ID, ctok, "text/plain", "x")
		if i <= artifactUploadMost && code != 200 || i > artifactUploadMost && (code != 429 || errCode(out) != "rate_limited") {
			t.Fatalf("upload %d: %d %v", i, code, out)
		}
	}
}

// The declaration: {} or {upload: "interact"|"admin"}, nothing else.
func TestAssetsDeclaration(t *testing.T) {
	for _, ok := range []string{`{"assets": {}}`, `{"assets": {"upload": "interact"}}`, `{"assets": {"upload": "admin"}}`} {
		if _, err := artifact.ParseCapabilities(json.RawMessage(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{`{"assets": {"upload": "view"}}`, `{"assets": {"upload": true}}`, `{"assets": []}`} {
		if _, err := artifact.ParseCapabilities(json.RawMessage(bad)); err == nil {
			t.Errorf("%s was taken", bad)
		}
	}
}
