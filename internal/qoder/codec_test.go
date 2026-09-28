package qoder

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestCodecRoundTrip checks the fiddliest reverse-engineered piece: a request
// body survives encode-to-wire then decode-from-wire unchanged, over every
// length so the partial final 6-bit group, the "$" padding and the
// outer-thirds swap are all exercised.
func TestCodecRoundTrip(t *testing.T) {
	for n := 0; n < 200; n++ {
		plain := make([]byte, n)
		if _, err := rand.Read(plain); err != nil {
			t.Fatal(err)
		}
		wire := EncodeRequestBody(plain)
		if got := DecodeRequestBody(wire); !bytes.Equal(got, plain) {
			t.Fatalf("round trip failed at len %d", n)
		}
		if len(wire)%4 != 0 {
			t.Fatalf("wire length %d not a multiple of 4", len(wire))
		}
	}
}

// capturePath is CLIProxyAPI's vendored live capture of a real Qoder request,
// if that reference repo is checked out beside magpie.
const capturePath = `D:\Project\github\CLIProxyAPI\oauth-service\qorder\cipher_QQTEST.txt`

// TestBodyDecodeRealCapture decodes that real captured request — the strongest
// offline proof the ported codec matches Qoder's wire format. A wrong alphabet
// or swap yields bytes that are neither mostly printable nor full of the
// request's JSON markers. It skips when the capture isn't on disk.
func TestBodyDecodeRealCapture(t *testing.T) {
	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Skipf("live capture not available: %v", err)
	}
	// The capture is dumped with a "<n>\t" record prefix per line and the
	// body split across lines; rebuild it exactly as the reference test does.
	rePrefix := regexp.MustCompile(`^\d+\t`)
	var sb strings.Builder
	for _, ln := range strings.Split(string(raw), "\n") {
		sb.WriteString(rePrefix.ReplaceAllString(ln, ""))
	}
	body := strings.ReplaceAll(strings.TrimSpace(sb.String()), `\n`, "")
	if n := strings.Count(body, "$"); n != 1 {
		t.Skipf("capture layout differs (%d '$' chars); not the file this test targets", n)
	}
	plain := BodyDecode(body)
	if len(plain) == 0 {
		t.Fatal("decoded nothing from the capture")
	}
	printable := 0
	for _, b := range plain {
		if b >= 32 && b < 127 || b == '\n' || b == '\r' || b == '\t' {
			printable++
		}
	}
	ratio := float64(printable) / float64(len(plain))
	if ratio < 0.98 {
		t.Fatalf("decoded printable ratio %.3f < 0.98 — codec does not match the live capture", ratio)
	}
	text := string(plain)
	for _, kw := range []string{`"business"`, `"parameters"`, `"stage":"start"`, `"messages"`, "123_QQTEST"} {
		if !strings.Contains(text, kw) {
			t.Fatalf("missing marker %q in the decoded live request", kw)
		}
	}
}

// TestCosyHeadersStructure checks the request envelope is well formed: the
// Authorization is "Bearer COSY.<payload>.<md5 hex>", the payload base64
// decodes to the documented JSON, and the token is never in the clear.
func TestCosyHeadersStructure(t *testing.T) {
	u := &User{UID: "uid-1", Name: "N", Email: "e@x", Token: "jt-abc"}
	ts := int64(1700000000)
	h, err := BuildCosyHeaders(APIHost+ChatPath, u, `{"messages":[]}`, ts)
	if err != nil {
		t.Fatal(err)
	}
	auth := h["Authorization"]
	if !strings.HasPrefix(auth, "Bearer COSY.") {
		t.Fatalf("bad Authorization: %q", auth)
	}
	parts := strings.Split(strings.TrimPrefix(auth, "Bearer COSY."), ".")
	if len(parts) != 2 || len(parts[1]) != 32 {
		t.Fatalf("expected payload.<md5hex>, got %q", auth)
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("payload not base64: %v", err)
	}
	var p map[string]any
	if json.Unmarshal(decoded, &p) != nil {
		t.Fatalf("payload not JSON: %q", decoded)
	}
	if p["version"] != "v1" || p["cosyVersion"] != "1.1.49" {
		t.Fatalf("unexpected payload fields: %v", p)
	}
	if strings.Contains(string(decoded), "jt-abc") {
		t.Fatal("token leaked into the payload")
	}
	if h["Cosy-User"] != "uid-1" {
		t.Fatalf("Cosy-User wrong: %q", h["Cosy-User"])
	}
	if h["Cosy-Date"] != "1700000000" {
		t.Fatalf("Cosy-Date wrong: %q", h["Cosy-Date"])
	}
}
