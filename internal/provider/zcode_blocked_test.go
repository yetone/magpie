package provider

import (
	"net/http"
	"strings"
	"testing"
)

// zcodeBlockPage is the page zcode.z.ai answers a Start Plan request it
// turns away with (#425).
const zcodeBlockPage = `<!DOCTYPE html><html><head><title>405</title></head><body>
<h1>Sorry, your request has been blocked due to unusual activity.</h1>
<a href="https://errors.aliyun.com/error/405?code=blocked">errors.aliyun.com</a></body></html>`

// The Start Plan's block is said for what it is (#425): sent as ZCode's
// app sends it, it is the network blocking the address or ZCode checking
// something new, and what to use instead; not only the firewall. A Coding Plan account,
// and any other provider, keep the generic hint.
func TestZCodeStartBlockedSaid(t *testing.T) {
	say := func(p Provider, status int, body string) string {
		return p.Explain(p.Name+": "+APIError([]byte(body), "405 Method Not Allowed"), status, []byte(body))
	}
	start := zcodeProvider("trial@example.com", "Start Plan", zcodeKey{JWT: "a.b.c"})
	if got, want := say(start, http.StatusMethodNotAllowed, zcodeBlockPage), "ZCode: 405 Method Not Allowed — "+ZCodeStartBlockedHint; got != want {
		t.Fatalf("Start Plan block:\n got %q\nwant %q", got, want)
	}
	if got := say(start, http.StatusMethodNotAllowed, `{"code":3012,"msg":"method not allowed"}`); !strings.HasSuffix(got, " — "+ZCodeStartBlockedHint) || strings.Contains(got, BlockedHint) {
		t.Fatalf("Start Plan 3012: %q", got)
	}
	if got := say(start, http.StatusTooManyRequests, `{"code":1302,"msg":"rate limited"}`); strings.Contains(got, ZCodeStartBlockedHint) {
		t.Fatalf("a rate limit got the block hint: %q", got)
	}
	if !EdgeBlocked([]byte(say(start, http.StatusMethodNotAllowed, zcodeBlockPage))) {
		t.Fatal("the Start Plan's hint, relayed, isn't taken as a block")
	}

	plan := zcodeProvider("pro@example.com", "GLM Coding Plan", zcodeKey{Key: "id.secret", Base: "https://api.z.ai/api/anthropic"})
	if got, want := say(plan, http.StatusMethodNotAllowed, zcodeBlockPage), "ZCode: 405 Method Not Allowed — "+BlockedHint; got != want {
		t.Fatalf("Coding Plan block:\n got %q\nwant %q", got, want)
	}
	other := Provider{ID: "za", Name: "ZA"}
	if got, want := say(other, http.StatusMethodNotAllowed, zcodeBlockPage), "ZA: 405 Method Not Allowed — "+BlockedHint; got != want {
		t.Fatalf("another provider:\n got %q\nwant %q", got, want)
	}
}
