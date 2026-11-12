package gui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// An app's import link from a page in a browser is opened on the computer
// magpie runs on, where the app's database "added" reads is, not left to the
// viewing browser, which may be another machine's (Jealousy_Du on X).
func TestWebOpensAppLinkOnTheHost(t *testing.T) {
	sandboxHome(t)
	oldLink, oldOpen := appLink, openOnHost
	t.Cleanup(func() { appLink, openOnHost = oldLink, oldOpen })
	appLink = func(link string, open func(string) error) error { return open(link) }
	var got []string
	openOnHost = func(u string) error {
		if strings.HasPrefix(u, "http") {
			return nil
		}
		got = append(got, u)
		return nil
	}
	link := agent.CindyScheme + "v=1&data=x"
	rec := httptest.NewRecorder()
	webPageHandler(t, webHost{}, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/open", strings.NewReader(`{"url":"`+link+`"}`)))
	if rec.Code != http.StatusNoContent || len(got) != 1 || got[0] != link {
		t.Fatalf("open: %d %s, opened on the host: %v", rec.Code, rec.Body, got)
	}
}

func TestOpenOnHostLeavesWebLinksToThePage(t *testing.T) {
	if err := webHost.OpenURL(webHost{}, "https://example.invalid/x"); err != nil {
		t.Fatal(err)
	}
}
