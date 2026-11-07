package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// nginx502 is the page an nginx in front of a relay serves when what it
// proxies to gave up on a request, as chenxiang0010's relay did for an
// 8.2 MB Claude Code turn.
const nginx502 = "<html>\r\n<head><title>502 Bad Gateway</title></head>\r\n<body>\r\n<center><h1>502 Bad Gateway</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"

// bigMessages is a Claude Code turn of about mib MiB of text, to model.
func bigMessages(model string, mib int, stream bool) string {
	s := "false"
	if stream {
		s = "true"
	}
	return `{"model":"` + model + `","stream":` + s + `,"max_tokens":10,"messages":[{"role":"user","content":"` + strings.Repeat("word ", mib<<20/5) + `"}]}`
}

// A relay whose proxy answers a very large request with its own 502 page
// refused it for its size: the client is told the request is too large
// for the provider and to compact, once, not handed a 502 it asks again
// with the same body (chenxiang0010 on Discord: "Retrying … attempt 4/9",
// and every --resume of the session the same).
func TestBigBodyProxyPageIsByteCap(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, page := range []struct{ name, ctype, body string }{
			{"nginx", "text/html", nginx502},
			{"empty", "", ""},
		} {
			name := page.name
			if stream {
				name += "/stream"
			}
			t.Run(name, func(t *testing.T) {
				fresh(t)
				up := &capped{proto: provider.Chat, most: 1 << 20, status: 502, ctype: page.ctype, body: page.body}
				cappedOn(t, up)
				code, body := postMessages(t, bigMessages("fake/m1", 5, stream))
				if code != 413 || !strings.Contains(body, "request_too_large") {
					t.Fatalf("a proxy's page for a 5 MB request: %d %.600s", code, body)
				}
				for _, want := range []string{"the request is 5.0 MB", "1017dp answered", "/compact"} {
					if !strings.Contains(body, want) {
						t.Errorf("the error lacks %q: %.600s", want, body)
					}
				}
				if n := len(up.calls()); n != 1 {
					t.Fatalf("the same body was sent %d times", n)
				}
			})
		}
	}
}

// A route's next member may take what one member's proxy turned away.
func TestBigBodyProxyPageTriesAnotherMember(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{502, "text/html", nginx502}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"group/g","messages":[{"role":"user","content":"` + strings.Repeat("word ", 5<<20/5) + `"}]}`
	code, out := postAs(t, New(), "", body)
	if code != 200 || !strings.Contains(out, "hello") || a.n != 1 || b.n != 1 {
		t.Fatalf("the next member wasn't asked: %d %.300s (a %d, b %d)", code, out, a.n, b.n)
	}
}

// A small request's page is the relay's own trouble, not its size, and
// a vendor's own words are never a page.
func TestPageOverSize(t *testing.T) {
	for _, c := range []struct {
		status, sent int
		body         string
		want         bool
	}{
		{502, 8 << 20, nginx502, true},
		{413, 8 << 20, nginx413, true},
		{504, 8 << 20, "", true},
		{524, 8 << 20, "<!DOCTYPE html><html><head><title>A timeout occurred</title></head></html>", true},
		{502, 64 << 10, nginx502, false},
		{502, 8 << 20, `{"error":{"message":"upstream error"}}`, false},
		{429, 8 << 20, nginx502, false},
		{401, 8 << 20, "", false},
		{400, 8 << 20, nginx502, false},
	} {
		if got := pageOverSize(c.status, []byte(c.body), c.sent); got != c.want {
			t.Errorf("%d %d %.40q: %v", c.status, c.sent, c.body, got)
		}
	}
}
