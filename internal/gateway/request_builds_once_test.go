package gateway

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// A request builds the providers once, however many times it looks a
// model up on its way: each build reads every agent's sign-in, every
// plugin's and the settings, and three builds a request kept magpie at
// 100–350% CPU under seven agents at once (#2502). The next request,
// within a second and nothing written, builds none: one request after
// another built the providers again each time. After a write it builds
// once more. What a request within that second is judged by is when its
// build was asked, not when the request began: on a loaded machine a
// request can begin within the second what was built is held for and
// reach the build after the hold has let it go, and that build is right
// (#2965).
func TestRequestBuildsProvidersOnce(t *testing.T) {
	plugin.UseCached(nil)
	setup(t, provider.Chat, &fake{t: t, reply: sse(`{"choices":[{"delta":{"content":"hi"}}]}`, "[DONE]")})
	var asked time.Time // when the first build was asked for
	for i, want := range []int64{1, 0, 1} {
		if i == 2 {
			provider.Changed() // a provider saved
		}
		n := provider.AllBuilt.Load()
		code, body := post(t, "/v1/chat/completions", `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		got := provider.AllBuilt.Load() - n
		at := time.Unix(0, provider.AllBuiltAt.Load())
		if i == 0 {
			asked = at
		}
		if i == 1 && got == 1 && at.Sub(asked) >= time.Second {
			// the request began within the second the built providers are
			// held for, but its build was asked after the hold: built
			// again, rightly (a loaded machine)
			want = 1
		}
		if got != want {
			t.Fatalf("request %d built the providers %d times, want %d", i+1, got, want)
		}
	}
}
