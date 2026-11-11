package netproxy

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func savedVia(t *testing.T, tr http.RoundTripper, choice string) (string, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(With(context.Background(), choice), http.MethodGet, "http://vendor.example/v1/models", nil)
	res, err := tr.RoundTrip(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b), nil
}

// A request whose choice names a saved proxy goes through it, with the
// login kept beside the address; editing the proxy moves the next request
// to the new address, with no restart and no other place told; a deleted
// one fails the request, which never goes direct.
func TestSavedProxyChoice(t *testing.T) {
	clearProxyEnv(t)
	first := fakeHTTPProxy(t, loginUser, loginPass)
	second := fakeHTTPProxy(t, "other", "secret")
	save := func(ps ...settings.NamedProxy) {
		t.Helper()
		if err := settings.Save(settings.Settings{Proxies: ps}); err != nil {
			t.Fatal(err)
		}
	}
	save(settings.NamedProxy{ID: "pxa", Name: "A", URL: first.URL, User: loginUser, Pass: loginPass})
	base := &http.Transport{Proxy: Func}
	tr := Dispatch(base)
	t.Cleanup(base.CloseIdleConnections)

	if got, err := savedVia(t, tr, "@pxa"); err != nil || got != "carried http://vendor.example/v1/models" {
		t.Fatalf("through the saved proxy: %q, %v", got, err)
	}
	// the same name, another address and login: the next request follows
	save(settings.NamedProxy{ID: "pxa", Name: "A", URL: second.URL, User: "other", Pass: "secret"})
	if got, err := savedVia(t, tr, "@pxa"); err != nil || got != "carried http://vendor.example/v1/models" {
		t.Fatalf("after the edit: %q, %v", got, err)
	}
	// a wrong password is refused by the proxy, so the login is the saved one
	save(settings.NamedProxy{ID: "pxa", Name: "A", URL: second.URL, User: "other", Pass: "wrong"})
	if got, err := savedVia(t, tr, "@pxa"); err == nil && !strings.Contains(got, "refused") {
		t.Fatalf("a wrong saved password was carried: %q", got)
	}
	// deleted: the request fails, it doesn't leave direct
	save()
	if got, err := savedVia(t, tr, "@pxa"); err == nil {
		t.Fatalf("a request naming a deleted proxy was carried: %q", got)
	}
	if _, err := Parse("@pxa"); err == nil {
		t.Fatal("Parse accepted a deleted proxy")
	}
}

// Settings' own proxy may name a saved one: every request without its own
// choice takes it, and a CLI magpie starts gets its address in *_PROXY; a
// deleted one points a CLI nowhere rather than direct.
func TestSavedProxyAsTheGlobalOne(t *testing.T) {
	clearProxyEnv(t)
	p := fakeHTTPProxy(t, loginUser, loginPass)
	if err := settings.Save(settings.Settings{
		Proxy:   "@pxg",
		Proxies: []settings.NamedProxy{{ID: "pxg", Name: "G", URL: p.URL, User: loginUser, Pass: loginPass}},
	}); err != nil {
		t.Fatal(err)
	}
	if body, code := throughSaved(t, "http://vendor.example/v1/models"); code != 200 || body != "carried http://vendor.example/v1/models" {
		t.Fatalf("through the global saved proxy: %d %q", code, body)
	}
	env := func() string {
		for _, e := range EnvWith("", []string{}) {
			if k, v, _ := strings.Cut(e, "="); k == "HTTPS_PROXY" {
				return v
			}
		}
		return ""
	}
	if eu, err := url.Parse(env()); err != nil || eu.Host != strings.TrimPrefix(p.URL, "http://") {
		t.Fatalf("a CLI's HTTPS_PROXY is %q, %v", env(), err)
	}
	// what Settings shows names neither part of the login nor the reference
	assertHiddenFor(t, "@pxg")
	// the proxy deleted while the choice stays: nothing goes direct
	if err := settings.Save(settings.Settings{Proxy: "@pxg"}); err == nil {
		t.Fatal("saved a choice naming a proxy that isn't there")
	}
	cur := settings.Load()
	cur.Proxies = nil
	cur.Proxy = ""
	if err := settings.Save(cur); err != nil {
		t.Fatal(err)
	}
}

func assertHiddenFor(t *testing.T, choice string) {
	t.Helper()
	shown, _ := Describe()
	for _, secret := range []string{loginUser, loginPass, loginUserEnc, loginPassEnc, "p@ss", "p%40ss", "corp"} {
		if strings.Contains(shown, secret) || strings.Contains(Redact(choice), secret) {
			t.Fatalf("shows %q: %q / %q", secret, shown, Redact(choice))
		}
	}
}

// A dangling reference (a proxy a sync carried a choice for, which this
// computer doesn't have) points a CLI at nothing, and a request to nowhere.
func TestDanglingProxyReferenceFailsClosed(t *testing.T) {
	clearProxyEnv(t)
	got := EnvWith("@nothere", []string{})
	found := false
	for _, e := range got {
		if k, v, _ := strings.Cut(e, "="); strings.EqualFold(k, "HTTPS_PROXY") {
			found = true
			if v != DeadProxy {
				t.Fatalf("HTTPS_PROXY = %q, want the dead proxy", v)
			}
		}
	}
	if !found {
		t.Fatalf("no HTTPS_PROXY in %v: a CLI would go direct", got)
	}
	req, _ := http.NewRequestWithContext(With(context.Background(), "@nothere"), http.MethodGet, "http://vendor.example/", nil)
	if u, err := Func(req); err == nil {
		t.Fatalf("Func gave %v, nil for a reference to nothing", u)
	}
}
