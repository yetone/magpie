package redact

import (
	"strings"
	"testing"
)

// An image a Mac saved at twice its size is named like an address
// (…@2x.jpg), and was masked as one, so Pi could no longer read the file
// (#3029, the reporter's path). A file's name stays; an address beside it
// is still masked.
func TestFileNameIsNoEmail(t *testing.T) {
	o := Options{Secrets: true, Personal: true}
	for _, s := range []string{
		"/Users/vdxz/Pictures/261020_14-16-11@2x.jpg",
		"read assets/icon@3x.png and logo@2x.PNG",
		"open ~/Desktop/scan@1x.pdf",
	} {
		if out, n := Mask(s, o); n != 0 || out != s {
			t.Errorf("%q masked: %q", s, out)
		}
	}
	s := `{"role":"user","content":"look at /Users/vdxz/Pictures/261020_14-16-11@2x.jpg and mail rina.k@fastmail.com"}`
	out, n := MaskJSON([]byte(s), o)
	if n != 1 || !strings.Contains(string(out), "261020_14-16-11@2x.jpg") || strings.Contains(string(out), "rina.k@fastmail.com") {
		t.Fatalf("%d masked: %s", n, out)
	}

	// nor does the local model's EMAIL take one
	f := &fakeModel{values: map[string]string{"261020_14-16-11@2x.jpg": "private_email"}}
	if out, n := Mask("/Users/vdxz/Pictures/261020_14-16-11@2x.jpg", Options{Personal: true, Model: f.find}); n != 0 {
		t.Fatalf("model's: %d masked: %q", n, out)
	}
}

// Image names as apps ship them (willz on Discord: image@2x.png), other
// files named so (intro@2x.mp4), and a scale before an extension that is a
// domain too (icon@2x.zip) are no address either. An address on a real
// domain still is: .ai, .io, and .zip, .mov and .md with no scale.
func TestMoreFileNamesAreNoEmail(t *testing.T) {
	o := Options{Secrets: true, Personal: true}
	for _, s := range []string{
		"file@2x.png", "Default@2x~iphone.png", "icon@3x.webp", "logo@2x.jpg",
		"screenshot@1.5x.png", "Assets/AppIcon@2x.PNG", "banner@final.pdf",
		"intro@2x.mp4", "strings@base.json", "icon@2x.zip", "intro@1.5x.mov",
		`"src": "img/hero@2x.avif"`, "![shot](./docs/shot@2x.png)",
	} {
		in := "see " + s + " here"
		if out, n := Mask(in, o); out != in || n != 0 {
			t.Errorf("%q: %d masked: %q", s, n, out)
		}
	}
	// as a request carries them: a tool's result listing an asset folder
	body := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"AppIcon@2x.png\nintro@2x.mp4\nicon@2x.zip\nowner: x@company.ai"}]}]}`
	got, n := MaskJSON([]byte(body), o)
	if n != 1 || !strings.Contains(string(got), `AppIcon@2x.png\nintro@2x.mp4\nicon@2x.zip\nowner: {{EMAIL_`) {
		t.Errorf("request: %d masked: %s", n, got)
	}
	for _, s := range []string{
		"a.b@acme-corp.com", "x@company.ai", "dev@startup.io", "me@notes.md",
		"ops@files.zip", "jane@studio.mov", "sales@2x.com", "hi@png.dev",
	} {
		in := "mail " + s + " now"
		if out, n := Mask(in, o); n != 1 || strings.Contains(unmarked(out), s) {
			t.Errorf("%q: %d masked: %q", s, n, out)
		}
	}
}

// A file named after an address is the address's all the same: the
// extension that made #3029's @2x.jpg a file's name doesn't make
// alice.smith@acme.com.pdf no one's. Only a host with no domain before
// its extension (@2x.jpg, @final.pdf, @base.json) is a file's alone, by
// the rules and by the local model's EMAIL.
func TestEmailInAFileNameIsMasked(t *testing.T) {
	o := Options{Secrets: true, Personal: true}
	for _, c := range []struct{ in, file, email string }{
		{"attach /Users/me/Invoices/alice.smith@acme.com.pdf", "alice.smith@acme.com.pdf", "alice.smith@acme.com"},
		{"open export-rina.k@fastmail.com.png", "export-rina.k@fastmail.com.png", "rina.k@fastmail.com"},
		{"see john@gmail.com.jpg", "john@gmail.com.jpg", "john@gmail.com"},
		{"unpack ops@corp.co.uk.tar.gz now", "ops@corp.co.uk.tar.gz", "ops@corp.co.uk"},
		{`{"path":"scans/jane@studio.io.heic"}`, "jane@studio.io.heic", "jane@studio.io"},
	} {
		if out, n := Mask(c.in, o); n != 1 || strings.Contains(unmarked(out), c.email) {
			t.Errorf("%q: %d masked: %q", c.in, n, out)
		}
		f := &fakeModel{values: map[string]string{c.file: "private_email"}}
		if out, n := Mask(c.in, Options{Personal: true, Model: f.find}); n != 1 || strings.Contains(unmarked(out), c.email) {
			t.Errorf("model's %q: %d masked: %q", c.file, n, out)
		}
	}
	for _, s := range []string{"logo@2x.jpg", "icon@3x.png", "banner@final.pdf", "strings@base.json", "icon@2x.zip", "intro@1.5x.mov.mp4"} {
		f := &fakeModel{values: map[string]string{s: "private_email"}}
		in := "see " + s + " here"
		if out, n := Mask(in, Options{Secrets: true, Personal: true, Model: f.find}); n != 0 || out != in {
			t.Errorf("%q: %d masked: %q", s, n, out)
		}
	}
}
