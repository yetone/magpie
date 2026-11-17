package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// mdwsk88: Claude Desktop lists Copilot's Claude Opus 5.5 as
// magpie-5238445297.anthropic.claude-opus-5-5 (claudeLooking), and a
// session picked on it names that id on every turn. Copilot's /models once
// answered without its Claude models, the list was saved so, and the
// catalog had no entry under that number for the next hour: aliased kept
// the id, unserved took it for a model magpie doesn't serve, and every
// turn of the session went to the model another Desktop session had run
// last (desktopPicked, movedGone) — another provider's model for 25
// minutes, Desktop still saying Opus. The id is magpie's for the user's
// opus pick, whose provider still serves it: the turn goes there, as
// asked.

// aliasFor's form for a Claude model with reasoning levels (claudeLooking)
func desktopClaudeAlias(id, model string) string {
	return "magpie-" + aliasNumber(id) + desktopClaudeInfix + model
}

// outOfCatalog saves a provider whose list leaves its opus out for now,
// and another Desktop session's provider, and says the catalog has no
// entry for the opus.
func outOfCatalog(t *testing.T, anthropic string) {
	t.Helper()
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "k", Anthropic: anthropic, Models: []string{"claude-sonnet-5-5"}},
		{ID: "other", Name: "Other", Key: "k", Anthropic: anthropic, Models: []string{"some-model"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range provider.CatalogOf("claude-desktop") {
		if e.ID == "relay/claude-opus-5-5" {
			t.Fatal("the catalog lists relay/claude-opus-5-5: nothing is left out")
		}
	}
}

func TestDesktopAliasOfTierPickLeftOutOfCatalog(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	outOfCatalog(t, "http://127.0.0.1:1")
	const opus = "relay/claude-opus-5-5"
	if err := SetDesktopTier("opus", opus); err != nil {
		t.Fatal(err)
	}
	alias := desktopClaudeAlias(opus, "claude-opus-5-5")
	turn := []byte(`{"max_tokens":32000,"tools":[{"name":"Bash"}],"messages":[{"role":"user","content":"hi"}]}`)

	// another session's turn, on another provider's model: the model
	// Desktop's turns last ran on
	if to, why := desktopMove("other/some-model", turn, true, false); to != "other/some-model" || why != "" {
		t.Fatalf("the other session's turn: %q by %q", to, why)
	}
	for _, id := range []string{alias, alias + "[1m]"} {
		if got, ok := aliased(id); !ok || got != opus {
			t.Errorf("aliased(%q) = %q, %v; want the opus pick %q", id, got, ok, opus)
		}
	}
	asked := unprefixed(alias)
	if asked != opus {
		t.Fatalf("unprefixed(%q) = %q, want %q", alias, asked, opus)
	}
	if to, why := desktopMove(asked, turn, true, false); to != opus || why != "" {
		t.Fatalf("the opus session's turn went to %q by %q, want %q as asked", to, why, opus)
	}

	// a model picked for two tiers and listed once more for the second
	// (desktopTierAlias): read back by that number too
	const sonnet = "relay/claude-sonnet-5-6"
	for _, tier := range []string{"sonnet", "haiku"} {
		if err := SetDesktopTier(tier, sonnet); err != nil {
			t.Fatal(err)
		}
	}
	second := "magpie-" + aliasNumber(desktopTierKey(sonnet, "haiku")) + desktopClaudeInfix + "claude-sonnet-5-6"
	if got, ok := aliased(second); !ok || got != sonnet {
		t.Errorf("aliased(%q) = %q, %v; want %q", second, got, ok, sonnet)
	}
	// the haiku pick changed since: a session saved on that number is still
	// on the sonnet pick, by whichever tier, as the catalog's are
	if err := SetDesktopTier("haiku", "relay/claude-sonnet-5-5"); err != nil {
		t.Fatal(err)
	}
	if got, ok := aliased(second); !ok || got != sonnet {
		t.Errorf("after the haiku pick changed, aliased(%q) = %q, %v; want %q", second, got, ok, sonnet)
	}

	// an id of magpie's for a model the user picked for no tier, and the
	// catalog left out, is no pick's
	if got, ok := aliased(desktopClaudeAlias("relay/claude-opus-4-8", "claude-opus-4-8")); ok {
		t.Errorf("an alias of no pick read as %q", got)
	}
}

// A routing group picked for a tier and deleted since is no pick to read
// an alias back to: its id goes to no provider (#6719).
func TestDesktopAliasSkipsGroupPick(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	outOfCatalog(t, "http://127.0.0.1:1")
	const group = provider.GroupPrefix + "gone"
	if err := SetDesktopTier("opus", group); err != nil {
		t.Fatal(err)
	}
	if got, ok := aliased(desktopEffortAlias + aliasNumber(group)); ok {
		t.Errorf("a deleted group's alias read as %q", got)
	}
}

// magpie's own id for a Claude model it can't find at all (its provider
// removed, or its list without it, and picked for no tier) still names
// the Claude model and so its tier: the model picked for that tier takes
// it, as it takes Claude Code's own id of one (desktopTierTurn), not the
// model another session last ran on. With no pick for the tier, or for an
// id that names no Claude model, it goes as before.
func TestDesktopClaudeAliasGoneTakesItsTier(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	outOfCatalog(t, "http://127.0.0.1:1")
	if err := SetDesktopTier("opus", "relay/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	turn := []byte(`{"max_tokens":32000,"tools":[{"name":"Bash"}],"messages":[{"role":"user","content":"hi"}]}`)
	if to, why := desktopMove("other/some-model", turn, true, false); to != "other/some-model" || why != "" {
		t.Fatalf("the other session's turn: %q by %q", to, why)
	}

	gone := desktopClaudeAlias("removed/claude-opus-5-5", "claude-opus-5-5")
	if got := unprefixed(gone); got != gone {
		t.Fatalf("unprefixed(%q) = %q: the test wants it unknown", gone, got)
	}
	for _, id := range []string{gone, gone + "[1m]"} {
		if to, why := desktopMove(id, turn, true, false); to != "relay/claude-opus-5-5" || why != movedTier {
			t.Errorf("%s: went to %q by %q, want the opus pick by %s", id, to, why, movedTier)
		}
	}
	if !strings.HasPrefix(aliasClaude(gone), "claude-opus-") || aliasClaude("claude-opus-5-5") != "" || aliasClaude("magpie-12.anthropic.claude-opus-5-5") != "" {
		t.Errorf("aliasClaude reads %q, %q, %q", aliasClaude(gone), aliasClaude("claude-opus-5-5"), aliasClaude("magpie-12.anthropic.claude-opus-5-5"))
	}

	// no haiku pick: as before, the model the last turn ran on
	haiku := desktopClaudeAlias("removed/claude-haiku-4-5", "claude-haiku-4-5")
	if to, why := desktopMove(haiku, turn, true, false); to != "other/some-model" || why != movedGone {
		t.Errorf("an alias of a tier with no pick went to %q by %q", to, why)
	}
	// an id that names no Claude model: as before
	other := desktopEffortAlias + aliasNumber("removed/some-model")
	if to, why := desktopMove(other, turn, true, false); to != "other/some-model" || why != movedGone {
		t.Errorf("an alias of a model that is no Claude model went to %q by %q", to, why)
	}
}

// The whole way through: a turn of the opus session, after another
// session's turn on another provider's model, reaches the opus pick's
// provider as asked, and
// the Routing row says nothing moved it.
func TestDesktopAliasLeftOutRoute(t *testing.T) {
	f := &fake{reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	up := setup(t, provider.Anthropic, f)
	outOfCatalog(t, up.URL)
	const opus = "relay/claude-opus-5-5"
	if err := SetDesktopTier("opus", opus); err != nil {
		t.Fatal(err)
	}
	s := New()
	h := s.Handler()
	send := func(model string) *Route {
		t.Helper()
		f.got = nil
		body := `{"model":"` + model + `","max_tokens":32000,"stream":true,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", Token+"-claude-desktop")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", model, rec.Code, rec.Body)
		}
		s.trace.mu.Lock()
		defer s.trace.mu.Unlock()
		return s.trace.routes[len(s.trace.routes)-1]
	}

	send("other/some-model")
	r := send(desktopClaudeAlias(opus, "claude-opus-5-5"))
	if got := modelOf(f.got); got != "claude-opus-5-5" {
		t.Fatalf("the opus session's turn was sent as %q, want claude-opus-5-5", got)
	}
	if r.Moved != nil || r.Model != opus || r.Provider != "relay" {
		t.Fatalf("route: model %q provider %q moved %+v; want %s on relay, not moved", r.Model, r.Provider, r.Moved, opus)
	}
}
