package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
)

func TestCaptureRequestBodyLimit(t *testing.T) {
	body := []byte(strings.Repeat("x", callBodyLimit+17))
	got, truncated := captureRequestBody(body)
	if len(got) != callBodyLimit || !truncated {
		t.Fatalf("captured %d bytes, truncated=%v", len(got), truncated)
	}
	for _, n := range []int{0, 5, callBodyLimit} {
		in := []byte(strings.Repeat("y", n))
		if got, truncated := captureRequestBody(in); got != string(in) || truncated {
			t.Fatalf("%d bytes captured as %d, truncated=%v", n, len(got), truncated)
		}
	}
	// copied once: the capture is all a request's capture allocates
	if n := allocated(func() { captureRequestBody(body) }); n > callBodyLimit*5/4 {
		t.Fatalf("capturing %d bytes allocated %d", callBodyLimit, n)
	}
	// and it is a copy: the body's memory is not the capture's
	body[0] = 'z'
	if got[0] != 'x' {
		t.Fatal("the capture reads the body's memory")
	}
}

func TestCaptureResponseWriterPreservesResponse(t *testing.T) {
	r := httptest.NewRecorder()
	w := &captureResponseWriter{ResponseWriter: r}
	_, _ = w.Write([]byte(`{"ok":true}`))
	if got := w.body.text(); got != `{"ok":true}` {
		t.Fatalf("capture = %q", got)
	}
	if got := r.Body.String(); got != `{"ok":true}` {
		t.Fatalf("response = %q", got)
	}
}

// The bodies the OTLP export carries lose every secret of magpie's own
// whether masking is on for the vendor or not, since what is sent to the
// collector is kept nowhere (#195). A masking rule of the user's own is
// left behind: mask gates it on Mask secrets, so forcing the secrets on
// here would switch their rule on where the vendor side left it off.
func TestBodyForExportTakesOnlyMagpiesOwnSecrets(t *testing.T) {
	fresh(t)
	if err := settings.Save(settings.Settings{Redact: true,
		RedactRules: []redact.Rule{{Kind: "RELAY", Prefix: "rz_"}}}); err != nil {
		t.Fatal(err)
	}
	// a key of this test's own: another test's, or another case's, may
	// already have masked its own, and a value masked once is masked again
	// wherever a request has it (known.go), whatever the options say
	const key = "rz_ExportKey1234567"
	// a secret magpie's own rules do know, to pin that the export still
	// takes it out: the point is which rules apply, not that nothing does
	const mine = "ghp_" + "Expo0rt0Token0AbCdEfGhIjKlMnOpQrSt"
	body := `{"choices":[{"message":{"content":"using ` + key + ` and ` + mine + `"}}]}`
	if got := bodyForExport(body, false); strings.Contains(got, mine) {
		t.Errorf("exported body keeps a secret of magpie's own: %s", got)
	}
	if got := bodyForExport(body, false); !strings.Contains(got, key) {
		t.Errorf("exported body: %s", got)
	}
	// masking off, the user's rule is off with it, as the vendor side has it,
	// and no rule of magpie's knows rz_, so the key is what is kept
	if err := settings.Save(settings.Settings{
		RedactRules: []redact.Rule{{Kind: "RELAY", Prefix: "rz_"}}}); err != nil {
		t.Fatal(err)
	}
	if got := bodyForExport(body, false); !strings.Contains(got, key) {
		t.Errorf("exported body with masking off took the rule's match out: %s", got)
	}
}
