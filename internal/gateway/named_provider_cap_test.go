package gateway

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Anotiaww on X: one magpie asked another's "claude/claude-opus-5-5"; with
// the Claude account at its usage cap and no fallback set on the provider,
// the request was answered by a routing group of the user's that has that
// model in it (a WorkBuddy member), with nothing said. A request that
// names the provider is that provider's: past its cap it is the cap's
// 429, on every protocol, streaming or not. A
// fallback the user set on the provider still takes it.
func TestNamedProviderCappedRefuses(t *testing.T) {
	bodies := map[string]string{
		"/v1/chat/completions": `{"model":%q,"stream":%v,"messages":[{"role":"user","content":"hi"}]}`,
		"/v1/responses":        `{"model":%q,"stream":%v,"input":"hi"}`,
		"/v1/messages":         `{"model":%q,"stream":%v,"max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`,
	}
	for path, shape := range bodies {
		for _, model := range []string{"codex/gpt-5.5"} {
			for _, stream := range []bool{false, true} {
				for _, fallback := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s-%s-%v-fallback=%v", path, model, stream, fallback), func(t *testing.T) {
						codexSignedIn(t)
						b := newResetBackend(t)
						capUsage(t, map[string]float64{"me@example.com": 99})
						if err := provider.SetAccountCap("codex", "me@example.com", 98); err != nil {
							t.Fatal(err)
						}
						a := &scripted{replies: []reply{{200, "", chatSSEReply("from the group")}}}
						f := &scripted{replies: []reply{{200, "", chatSSEReply("from the fallback")}}}
						scriptedOn(t, "a", provider.Chat, a)
						scriptedOn(t, "f", provider.Chat, f)
						if err := provider.SaveGroup(provider.Group{Name: "claude-opus-5.5", Members: []string{"codex/gpt-5.5", "a/m"}}); err != nil {
							t.Fatal(err)
						}
						if fallback {
							p, err := provider.Find("codex")
							if err != nil {
								t.Fatal(err)
							}
							p.Fallback = []string{"f/m"}
							if err := provider.Save(*p); err != nil {
								t.Fatal(err)
							}
						}
						code, body := postTo(t, New(), path, fmt.Sprintf(shape, model, stream))
						tried, _ := b.seen()
						if tried != "" || a.n != 0 {
							t.Fatalf("%d %s: the capped account (%q) or the group's API (%d) was asked", code, body, tried, a.n)
						}
						if fallback {
							if code != 200 || f.n != 1 {
								t.Fatalf("the fallback set: %d %s (fallback %d)", code, body, f.n)
							}
							return
						}
						if code != 429 || !strings.Contains(body, "usage cap reached") {
							t.Fatalf("no fallback: %d %s", code, body)
						}
					})
				}
			}
		}
	}
}
