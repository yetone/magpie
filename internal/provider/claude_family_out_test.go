package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// claudeFableSaid is Claude Code's line for an account out of its Fable
// limit alone, as its 2.1 binary builds it ("You've hit your " and the
// window's words, "Fable limit", then the reset), its reset two days on.
func claudeFableSaid(now time.Time) string {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	r := now.In(sh).Add(48 * time.Hour)
	return fmt.Sprintf("Claude Code: You've hit your Fable limit · resets %s at 5am (Asia/Shanghai)", r.Format("Jan 2"))
}

// claudeOnAs sets what Claude Code answered on lately (noteClaudeModel),
// none for no models.
func claudeOnAs(t *testing.T, models ...string) {
	t.Helper()
	set := func(m map[string]time.Time) {
		claudeOnModels.Lock()
		claudeOnModels.at = m
		claudeOnModels.Unlock()
	}
	m := map[string]time.Time{}
	for _, model := range models {
		m[claudeModelFamily(model)] = time.Now()
	}
	set(m)
	t.Cleanup(func() { set(nil) })
}

// A spare out of its Fable limit alone is still a spare for Claude Code
// on Opus, and is passed over for Claude Code on Fable; with the model
// not known it is passed over, as every limit was before one was kept
// per family.
func TestClaudeFableOutSpareForOpusOnly(t *testing.T) {
	for _, c := range []struct {
		name   string
		models []string
		taken  bool // spares[0], out for Fable, is moved to
	}{
		{"opus", []string{"claude-opus-5-5"}, true},
		{"fable", []string{"claude-fable-5-1"}, false},
		{"both sessions", []string{"claude-opus-5-5", "claude-fable-5-1[1m]"}, false},
		{"not known", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			used := map[string]string{"": "100"}
			spares, ran := claudeSpares(t, used)
			used[claudeAccountDir(spares[1])] = "10"
			keptReading(t, spares[0], time.Hour) // fresh enough, and reads unused
			claudeOnAs(t, c.models...)
			noteClaudeSaid(spares[0], claudeFableSaid(time.Now()), time.Now())
			want := spares[1]
			if c.taken {
				want = spares[0]
			}
			if _, to, _, ok := NextLogin(context.Background(), "claude"); !ok || to != want {
				t.Fatalf("to %q ok=%v, want %q", to, ok, want)
			}
			if n := ran(claudeAccountDir(spares[0])); n != 0 {
				t.Fatalf("the spare was read %d times", n)
			}
			// `magpie accounts switch claude next` never to the one out
			if to, _, err := NextInOrder(context.Background(), "claude"); err != nil || !c.taken && to != spares[1] {
				t.Fatalf("next: %q %v, want %q", to, err, spares[1])
			}
		})
	}
}

// Claude Code on an account out of its Fable limit alone stays on it on
// Opus, the account's other windows with room, and is moved off on Fable.
func TestClaudeFableOutMovesOnlyFable(t *testing.T) {
	for _, c := range []struct {
		name   string
		models []string
		moved  bool
	}{
		{"opus", []string{"claude-opus-5-5"}, false},
		{"fable", []string{"claude-fable-5-1"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			used := map[string]string{"": "10"}
			spares, _ := claudeSpares(t, used)
			used[claudeAccountDir(spares[0])] = "10"
			used[claudeAccountDir(spares[1])] = "20"
			claudeOnAs(t, c.models...)
			noteClaudeSaid(claudeActive(t), claudeFableSaid(time.Now()), time.Now())
			from, to, _, ok := NextLogin(context.Background(), "claude")
			if ok != c.moved {
				t.Fatalf("%q→%q ok=%v, want moved %v", from, to, ok, c.moved)
			}
			// a weekly limit, the whole account's, moves it on Opus too
			if !c.moved {
				noteClaudeSaid(claudeActive(t), "Claude Code: You've hit your weekly limit · resets "+time.Now().Add(48*time.Hour).UTC().Format("Jan 2")+" at 5am (UTC)", time.Now())
				if _, _, _, ok := NextLogin(context.Background(), "claude"); !ok {
					t.Fatal("out for the week, Claude Code on Opus stayed")
				}
			}
		})
	}
}

// What Claude Code's words or Anthropic's window name a limit for: one
// family, or the whole account.
func TestClaudeLimitFamily(t *testing.T) {
	for _, c := range []struct{ kind, said, want string }{
		{"", "Claude Code: You've hit your weekly limit · resets Nov 24 at 5am (Asia/Shanghai)", ""},
		{"", "You've hit your session limit · resets 11pm (UTC)", ""},
		{"", "You've hit your limit · resets 5pm (UTC)", ""},
		{"", "You've hit your 5-hour limit", ""},
		{"", "Claude Code: You've hit your Fable limit · resets Nov 24 at 5am (Asia/Shanghai)", "fable"},
		{"", "You've reached your Fable limit for this week. Switch models?", "fable"},
		{"", "You've hit your Opus limit · resets Nov 24", "opus"},
		{"seven_day_overage_included", "You've hit your limit", "fable"},
		{"seven_day_opus", "", "opus"},
		{"seven_day", "You've hit your Fable limit", ""}, // Anthropic's window decides
		{"five_hour", "", ""},
	} {
		if got := claudeLimitFamily(c.kind, c.said); got != c.want {
			t.Errorf("%q %q: %q, want %q", c.kind, c.said, got, c.want)
		}
	}
	for model, want := range map[string]string{
		"claude-fable-5-1[1m]": "fable", "Fable": "fable", "opus": "opus", "claude-sonnet-5-5": "sonnet",
		"claude-haiku-5-5": "haiku", "default": "", "<synthetic>": "",
	} {
		if got := claudeModelFamily(model); got != want {
			t.Errorf("%q: %q, want %q", model, got, want)
		}
	}
	// Claude Code's own word that it went on, on usage credits, is no limit
	if claudeLimitSaid.MatchString("Fable limit reached · continuing on Opus uses usage credits") {
		t.Error("going on past the Fable limit read as out")
	}
	if !claudeLimitSaid.MatchString("You've reached your Fable limit for this week. Switch models?") {
		t.Error("the Fable limit reached isn't a limit")
	}
}

// claude-limits.json from before families were kept reads as it did,
// whole accounts out; one with a family's entry alone has no "until" for
// an older magpie to read as out; one that can't be read is unknown.
func TestClaudeSaidFileFormats(t *testing.T) {
	claudeThree(t)
	now := time.Now()
	until := now.Add(30 * time.Hour).UTC().Truncate(time.Second)
	old := fmt.Sprintf(`{"worktrial@x.com":{"until":%q,"said":"Claude Code: You've hit your weekly limit · resets Nov 24 at 5am (Asia/Shanghai)"}}`, until.Format(time.RFC3339))
	if err := os.MkdirAll(filepath.Dir(claudeSaidOutPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeSaidOutPath(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, fams := range [][]string{nil, {"opus"}, {"fable"}} {
		if !claudeSaidOutFor("worktrial@x.com", fams, now) {
			t.Fatalf("the old file's account isn't out on %v", fams)
		}
	}
	keepClaudeOutFor("worktrial@x.com", "fable", until.Add(time.Hour), "You've hit your Fable limit", now)
	keepClaudeOutFor("pro@x.com", "fable", until, "You've hit your Fable limit", now)
	b, _ := os.ReadFile(claudeSaidOutPath())
	var raw map[string]map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["pro@x.com"]["until"]; ok {
		t.Fatalf("a family's entry alone has an until an older magpie reads: %s", b)
	}
	if s := claudeSaidNow()["worktrial@x.com"]; !s.Until.Equal(until) || !s.Models["fable"].Until.Equal(until.Add(time.Hour)) {
		t.Fatalf("the account's whole entry or its Fable one lost: %+v", s)
	}
	if !claudeSaidOutFor("pro@x.com", []string{"fable"}, now) || claudeSaidOutFor("pro@x.com", []string{"opus"}, now) {
		t.Fatal("the Fable limit isn't Fable's alone")
	}
	if got := ClaudeModelOutUntil("PRO@x.com", "claude-fable-5-1", now); !got.Equal(until) {
		t.Fatalf("gateway: Fable out till %v, want %v", got, until)
	}
	if got := ClaudeModelOutUntil("pro@x.com", "claude-opus-5-5", now); !got.IsZero() {
		t.Fatalf("gateway: Opus out till %v", got)
	}
	// a half-written file is unknown: nothing out on its word
	if err := os.WriteFile(claudeSaidOutPath(), b[:len(b)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readClaudeSaid(); ok {
		t.Fatal("a half-written file read as known")
	}
	if claudeSaidOut("pro@x.com", now) {
		t.Fatal("a half-written file kept an account out")
	}
	// a good read of the account keeps its Fable limit while the reading
	// counts Fable's window full, and ends it once it doesn't
	keepClaudeOutFor("pro@x.com", "fable", until, "You've hit your Fable limit", now)
	r := until
	clearClaudeSaid("pro@x.com", []QuotaWindow{{Name: "7 days", Used: 40}, {Name: "7 days · Fable", Used: 100, ResetsAt: &r, Model: "fable"}})
	if !claudeSaidOutFor("pro@x.com", []string{"fable"}, now) {
		t.Fatal("a reading of Fable's window full ended its limit")
	}
	clearClaudeSaid("pro@x.com", []QuotaWindow{{Name: "7 days", Used: 40}, {Name: "7 days · Fable", Used: 12, ResetsAt: &r, Model: "fable"}})
	if claudeSaidOutFor("pro@x.com", []string{"fable"}, now) {
		t.Fatal("a reading of Fable's window with room kept its limit")
	}
}

// A refusal Claude Code wrote for Anthropic's Fable window
// (seven_day_overage_included) keeps the account out for Fable alone.
func TestClaudeRefusalKeptForItsFamily(t *testing.T) {
	claudeOutSoon(t, "50")
	user := claudeActive(t)
	session := filepath.Join(filepath.Dir(claudeCredentialsPath()), "projects", "-work", "s1.jsonl")
	writeSession(t, session, `{"type":"user","message":{"role":"user","content":"go"},"timestamp":"`+time.Now().UTC().Format(time.RFC3339Nano)+`"}`)
	claudeLookDue()
	claudeOn.Lock()
	claudeOn.since = claudeOn.since.Add(-2 * time.Minute)
	claudeOn.Unlock()
	resets := time.Now().Add(49 * time.Hour).Truncate(time.Second)
	line := strings.NewReplacer(`"rateLimitType":"seven_day"`, `"rateLimitType":"seven_day_overage_included"`,
		"You've hit your weekly limit", "You've hit your Fable limit").Replace(claudeWeeklyRefusal(time.Now(), resets))
	// the reply Claude Code was on before it, as its session has it
	writeSession(t, session, `{"parentUuid":"p","isSidechain":false,"message":{"model":"claude-fable-5-1","id":"m0","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}]},"type":"assistant","uuid":"u0","timestamp":"`+time.Now().UTC().Format(time.RFC3339Nano)+`"}`)
	writeSession(t, session, line)
	claudeLookDue()
	got := claudeSaidNow()[strings.ToLower(user)]
	if !got.Until.IsZero() || !got.Models["fable"].Until.Equal(resets) {
		t.Fatalf("kept %+v, want Fable alone till %v", got, resets)
	}
	if fams := claudeOnFamilies(time.Now()); !slices.Equal(fams, []string{"fable"}) {
		t.Fatalf("Claude Code on %v, want fable", fams)
	}
	if claudeSaidOutFor(user, []string{"opus"}, time.Now()) || !claudeSaidOutFor(user, []string{"fable"}, time.Now()) {
		t.Fatal("the Fable refusal isn't Fable's alone")
	}
	// a /usage read that failed meanwhile: out for Claude Code on Fable only
	q := SubscriptionQuota{Error: "Claude Code: network is unreachable"}
	if claudeOutUnread(user, []string{"opus"}, true, q, time.Now()) || !claudeOutUnread(user, []string{"fable"}, true, q, time.Now()) {
		t.Fatal("the Fable refusal, read unread, isn't Fable's alone")
	}
}

// With no reply read lately, Claude Code's model is the one its own
// settings name, read only.
func TestClaudeOnFamiliesFromSettings(t *testing.T) {
	claudeThree(t)
	claudeOnAs(t)
	if fams := claudeOnFamilies(time.Now()); fams != nil {
		t.Fatalf("no settings: %v, want not known", fams)
	}
	p := filepath.Join(filepath.Dir(claudeCredentialsPath()), "settings.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"model": "opus", "theme": "dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if fams := claudeOnFamilies(time.Now()); !slices.Equal(fams, []string{"opus"}) {
		t.Fatalf("settings on opus: %v", fams)
	}
	// a reply read more lately than claudeOnRecent doesn't count
	claudeOnModels.Lock()
	claudeOnModels.at = map[string]time.Time{"fable": time.Now().Add(-2 * claudeOnRecent)}
	claudeOnModels.Unlock()
	if fams := claudeOnFamilies(time.Now()); !slices.Equal(fams, []string{"opus"}) {
		t.Fatalf("an old reply counted: %v", fams)
	}
	noteClaudeModel([]byte(`{"parentUuid":"p","isSidechain":false,"message":{"model":"claude-fable-5-1","id":"m","type":"message","role":"assistant","content":[]},"type":"assistant","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`))
	noteClaudeModel([]byte(`{"message":{"model":"<synthetic>","content":[]},"type":"assistant","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`))
	if fams := claudeOnFamilies(time.Now()); !slices.Equal(fams, []string{"fable"}) {
		t.Fatalf("a reply on Fable read: %v", fams)
	}
}
