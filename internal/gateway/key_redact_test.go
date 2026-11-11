package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/settings"
)

// y0ngb1n on X: each gateway key says whether Settings › Privacy's masking
// applies to its requests. Off sends them as written, on masks them even
// where the settings mask nothing, and follow, like a request with no
// key, does what the settings say.
func TestKeyRedactSetting(t *testing.T) {
	_, seen, _ := maskOnceSetup(t) // masking on, the word "acmecorp"
	keys, secrets := newCaller(t, "Trusted", "External", "Plain")
	for i, mode := range []string{"off", "on", "follow"} {
		if _, err := access.Update("redact-key", access.Change{Key: keys[i].ID, Redact: mode}); err != nil {
			t.Fatal(err)
		}
	}
	gw := lanServer(t, New())
	send := func(secret, model string) string {
		t.Helper()
		body := `{"model":"` + model + `","messages":[{"role":"user","content":"a note about acmecorp"}]}`
		req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("%d %s", res.StatusCode, b)
		}
		got, _ := seen()
		return got
	}
	for i, c := range []struct {
		name   string
		masked bool
	}{{"off", false}, {"on", true}, {"follow", true}} {
		if got := send(secrets[i], "fake/m1"); strings.Contains(got, "acmecorp") == c.masked {
			t.Errorf("key %s: the vendor was sent %s", c.name, got)
		}
	}

	// off through a Remote magpie: this one masks nothing and says nothing
	// was masked, so the remote masks by its own settings
	if got := send(secrets[0], "office/fake/m1"); strings.Contains(got, "acmecorp") {
		t.Errorf("an off key's request told the remote it was masked: %s", got)
	}

	// the settings mask nothing: off and follow send as written, on masks
	// secrets and personal data by the defaults
	if err := settings.Save(settings.Settings{LAN: true}); err != nil {
		t.Fatal(err)
	}
	const mail = "write to jo@quill.dev"
	for i, c := range []struct {
		name   string
		masked bool
	}{{"off", false}, {"on", true}, {"follow", false}} {
		body := `{"model":"fake/m1","messages":[{"role":"user","content":"` + mail + `"}]}`
		req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+secrets[i])
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		got, _ := seen()
		if strings.Contains(got, "jo@quill.dev") == c.masked {
			t.Errorf("settings off, key %s: the vendor was sent %s", c.name, got)
		}
	}
}

// A key's setting holds on every route that masks what goes to a vendor,
// not only chat: a web search's query is masked through maskedQuery, and
// a native call through maskTextHits.
func TestKeyRedactEveryRoute(t *testing.T) {
	fresh(t)
	if err := settings.Save(settings.Settings{Redact: true, RedactWords: []string{"acmecorp"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { settings.Save(settings.Settings{}) })
	scanCount(t)
	for _, c := range []struct {
		mode   string
		masked bool
	}{{"", true}, {"on", true}, {"off", false}} {
		ctx := access.WithIdentity(context.Background(), access.Identity{KeyID: "k", Redact: c.mode})
		q, err := maskedQuery(ctx, "news about acmecorp", nil)
		if err != nil || strings.Contains(q, "acmecorp") != !c.masked {
			t.Errorf("search, key %q: %q %v", c.mode, q, err)
		}
		if _, got, _, err := maskTextHits(ctx, "news about acmecorp"); err != nil || strings.Contains(got, "acmecorp") != !c.masked {
			t.Errorf("text, key %q: %q %v", c.mode, got, err)
		}
	}
	// no key: the settings
	q, _ := maskedQuery(context.Background(), "news about acmecorp", nil)
	if strings.Contains(q, "acmecorp") {
		t.Errorf("a request with no key went unmasked: %q", q)
	}
}
