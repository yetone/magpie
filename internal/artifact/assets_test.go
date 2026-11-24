package artifact

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func assetPage(t *testing.T) *Artifact {
	t.Helper()
	a, err := PublishPage(Publish{Page: []byte("<!doctype html><p>gallery")})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

// An asset is the artifact's: uploaded, listed oldest first with the
// budget, found by its id alone, kept by a publish, and deleted for good.
func TestAssetsUploadListDelete(t *testing.T) {
	sandbox(t)
	a := assetPage(t)
	one, err := UploadAsset(a.ID, png, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !ValidAssetID(one.ID) || one.Size != int64(len(png)) || one.ContentType != "image/png" {
		t.Fatalf("uploaded %+v", one)
	}
	two, err := UploadAsset(a.ID, []byte("a,b\n1,2\n"), "text/csv")
	if err != nil {
		t.Fatal(err)
	}
	list, u, err := Assets(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != one.ID || list[1].ID != two.ID || u.Files != 2 || u.Bytes != one.Size+two.Size || u.MaxFiles != MaxAssets || u.MaxBytes != MaxAssetBytes {
		t.Fatalf("listed %+v %+v", list, u)
	}
	// a publish keeps them
	if _, err := PublishPage(Publish{ID: a.ID, Page: []byte("<!doctype html><p>two"), Base: 1}); err != nil {
		t.Fatal(err)
	}
	blobs.Clear() // found again from the folders, as after a restart
	p, got, err := BlobFile(one.ID)
	if err != nil || got.ContentType != "image/png" {
		t.Fatalf("found %q %+v %v", p, got, err)
	}
	if b, _ := os.ReadFile(p); !bytes.Equal(b, png) {
		t.Fatalf("the blob holds %q", b)
	}
	// another artifact neither lists nor deletes it
	b := assetPage(t)
	if del, err := DeleteAsset(b.ID, one.ID); err != nil || del {
		t.Fatalf("another artifact's delete: %v %v", del, err)
	}
	if del, err := DeleteAsset(a.ID, one.ID); err != nil || !del {
		t.Fatalf("delete: %v %v", del, err)
	}
	if del, err := DeleteAsset(a.ID, one.ID); err != nil || del {
		t.Fatalf("a second delete: %v %v", del, err)
	}
	if _, _, err := BlobFile(one.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted asset is still served: %v", err)
	}
	if _, err := os.Stat(assetPath(a.ID, one.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("its file stays: %v", err)
	}
	if _, err := DeleteAsset(a.ID, "../meta.json"); !errors.Is(err, ErrAssetRequest) {
		t.Fatalf("a path as an id: %v", err)
	}
	// deleting the artifact takes them with it
	if err := Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := BlobFile(two.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an asset outlives its artifact: %v", err)
	}
	if _, err := UploadAsset(a.ID, png, "image/png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an upload to a deleted artifact: %v", err)
	}
}

// What an asset may be, as the contract has it.
func TestAssetChecks(t *testing.T) {
	cases := []struct {
		name, t string
		body    []byte
		want    error
	}{
		{"png", "image/png", png, nil},
		{"a parameter", "text/plain; charset=utf-8", []byte("hi"), ErrAssetType},
		{"an alias", "application/javascript", []byte("1"), ErrAssetType},
		{"html", "text/html", []byte("<p>"), ErrAssetType},
		{"empty", "image/png", nil, ErrAssetRequest},
		{"markup as png", "image/png", []byte("  <svg onload=alert(1)>"), ErrAssetType},
		{"markup as pdf", "application/pdf", []byte("\xef\xbb\xbf<html>"), ErrAssetType},
		{"utf-16 text", "text/plain", []byte("\xff\xfeh\x00i\x00"), ErrAssetRequest},
		{"text with a BOM", "text/markdown", []byte("\xef\xbb\xbf# hi"), nil},
		{"markup in text/plain", "text/plain", []byte("<p>hi"), nil},
		{"markup as css", "text/css", []byte("<style>p{}</style>"), ErrAssetType},
		{"binary as js", "text/javascript", []byte("a\x00b"), ErrAssetType},
		{"js", "text/javascript", []byte("export const a = 1 < 2"), nil},
		{"a big svg", "image/svg+xml", append([]byte("<svg>"), bytes.Repeat([]byte(" "), MaxSVGAsset)...), ErrTooLarge},
		{"a big png", "image/png", make([]byte, MaxAsset+1), ErrTooLarge},
		{"html as svg", "image/svg+xml", []byte("<html><body/></html>"), ErrAssetType},
		{"broken svg", "image/svg+xml", []byte("<svg><g></svg>"), ErrAssetType},
	}
	for _, c := range cases {
		_, err := CheckAsset(c.body, c.t)
		if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	if AssetLimit("text/css") != MaxCodeAsset || AssetLimit("video/mp4") != MaxAsset {
		t.Fatal("the limits by type")
	}
}

// An SVG is stored with nothing in it that runs or reaches out.
func TestSanitizeSVG(t *testing.T) {
	in := `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY x "y">]><!-- note -->
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 10 10" onload="alert(1)">
<script>alert(2)</script><style>@import url(https://evil)</style>
<foreignObject><div xmlns="http://www.w3.org/1999/xhtml">x</div></foreignObject>
<a xlink:href="java&#9;script:alert(3)"><text>link &amp; &lt;b&gt;</text></a>
<a href="https://example.com/"><circle r="4" fill="url(#g)" ONCLICK="x()"/></a>
<image href="data:image/svg+xml;base64,PHN2Zz4="/><image href="pic.png"/>
<use href="#c"/><use href="https://evil/sprite.svg#a"/><use xlink:href="other.svg#b"/>
<rect style="fill:url(data:image/png;base64,AAAA)"/>
<animate attributeName="href" to="javascript:alert(4)"/><set attributeName="x"/>
<linearGradient id="g"><stop offset="0"/></linearGradient>
</svg>`
	out, err := SanitizeSVG([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, gone := range []string{"alert", "<script", "<style", "evil", "foreignObject", "<div", "onload", "ONCLICK", "data:", "other.svg", "<animate", "<set", "<!--", "DOCTYPE", "<?xml", "ENTITY"} {
		if strings.Contains(s, gone) {
			t.Errorf("%q is left: %s", gone, s)
		}
	}
	for _, kept := range []string{`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 10 10">`, `link &amp; &lt;b&gt;`, `href="https://example.com/"`, `fill="url(#g)"`, `<use href="#c"></use>`, `<image href="pic.png"></image>`, `<linearGradient id="g"><stop offset="0"></stop></linearGradient>`} {
		if !strings.Contains(s, kept) {
			t.Errorf("%q is gone: %s", kept, s)
		}
	}
	// what it writes is an SVG it keeps as it is
	again, err := SanitizeSVG(out)
	if err != nil || string(again) != s {
		t.Fatalf("a second pass: %v\n%s\n%s", err, s, again)
	}
	for _, bad := range []string{"", "<svg>", "<svg></svg><svg></svg>", "<g/>", `<svg><x:a xmlns:x="u"></a></svg>`} {
		if _, err := SanitizeSVG([]byte(bad)); !errors.Is(err, ErrAssetType) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// The budget holds: an upload past it is refused, and leaves no file.
func TestAssetQuota(t *testing.T) {
	sandbox(t)
	a := assetPage(t)
	list := make([]Asset, MaxAssets)
	for i := range list {
		list[i] = Asset{ID: newAssetID(), ContentType: "image/png", Size: 1}
	}
	if err := writeAssets(a.ID, list); err != nil {
		t.Fatal(err)
	}
	if _, err := UploadAsset(a.ID, png, "image/png"); !errors.Is(err, ErrAssetQuota) {
		t.Fatalf("an asset past the count: %v", err)
	}
	if err := writeAssets(a.ID, []Asset{{ID: newAssetID(), ContentType: "video/mp4", Size: MaxAssetBytes - 4}}); err != nil {
		t.Fatal(err)
	}
	if _, err := UploadAsset(a.ID, png, "image/png"); !errors.Is(err, ErrAssetQuota) {
		t.Fatalf("an asset past the bytes: %v", err)
	}
	ents, _ := os.ReadDir(filepath.Join(folder(a.ID), assetsDir))
	if len(ents) != 0 {
		t.Fatalf("a refused upload left %d files", len(ents))
	}
	if _, err := UploadAsset(a.ID, []byte("abc"), "text/plain"); err != nil {
		t.Fatalf("an asset that fits: %v", err)
	}
}
