package gateway

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// zhangxiaoxiaoxx on X: with Claude signed in, a switch so that no other
// agent's request is forwarded or routed onto it. Only switch Claude
// Code's sign-in is that switch, and it holds on every route that reaches
// the Claude accounts, not only a request naming them: a routing group
// with a Claude member first, a provider's fallback to one and a group's
// classifier on one, each asked by another agent. None runs a Claude
// Code through the bridge; the group goes on to its other member.
func TestClaudeKeptToClaudeCodeOnEveryRoute(t *testing.T) {
	claudeMadeFirst(t, false)
	calls := filepath.Join(t.TempDir(), "calls")
	claudeRefuses(t, calls, "tok-none") // every account answers
	serveOn(t, "b", "kb", []string{"m"}, &keyed{})
	serveOn(t, "f", "kf", []string{"m"}, &keyed{fail: map[string]int{"kf": 500}})
	f, err := provider.Find("f")
	if err != nil {
		t.Fatal(err)
	}
	f.Fallback = []string{"claude/claude-sonnet-5"}
	if err := provider.Save(*f); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "first", Name: "First", Members: []string{"claude/claude-sonnet-5", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "judged", Name: "Judged", Members: []string{"b/m"}, Routing: provider.Ordered,
		Classifier: "claude/claude-sonnet-5", Rules: []provider.Rule{{Use: "b/m", Intent: "refactoring"}}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetSwitchOnly("claude", true); err != nil {
		t.Fatal(err)
	}
	s := New()
	t.Cleanup(s.subscription.abortAll)
	ask := func(model string) (int, string) {
		body := `{"model":"` + model + `","messages":[{"role":"user","content":"rename this package"}]}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("User-Agent", "opencode/1.2.3")
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	ran := func(route string) {
		t.Helper()
		if b, _ := os.ReadFile(calls); len(b) != 0 {
			t.Errorf("%s: a Claude Code ran through the bridge: %q", route, b)
		}
		os.Remove(calls)
	}
	if code, out := ask("group/first"); code != 200 || !strings.Contains(out, "from kb") {
		t.Errorf("group with a Claude member first: %d %s", code, out)
	}
	ran("group member")
	ask("f/m") // f fails, its fallback is a Claude account
	ran("fallback")
	if code, out := ask("group/judged"); code != 200 || !strings.Contains(out, "from kb") {
		t.Errorf("group judged by a Claude model: %d %s", code, out)
	}
	ran("classifier")

	// unticked, the same routes reach the Claude accounts again: the
	// test sees the bridge when it is used
	os.Remove(calls)
	if err := provider.SetSwitchOnly("claude", false); err != nil {
		t.Fatal(err)
	}
	if code, out := ask("group/first"); code != 200 || !strings.Contains(out, "healthy account") {
		t.Fatalf("unticked: %d %s", code, out)
	}
	if b, _ := os.ReadFile(calls); len(b) == 0 {
		t.Fatal("unticked: no Claude Code ran")
	}
}
