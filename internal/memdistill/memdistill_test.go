package memdistill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/memory"
	"github.com/yetone/magpie/internal/sessions"
)

// sandbox points distill.json and memory.json at the test's own files,
// holds the clock, and lists the sessions given, each read as the
// transcript named for it.
type sandbox struct {
	at    time.Time
	list  []sessions.Session
	trs   map[string]sessions.Transcript
	asked []string // the sessions the model was asked about, by the first user line
	reply func(user string) (string, error)
	dir   string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	dir := t.TempDir()
	sb := &sandbox{at: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC), trs: map[string]sessions.Transcript{}, dir: dir}
	oldPath, oldMem, oldNow, oldList, oldRead := Path, memory.Path, now, list, read
	Path = func() string { return filepath.Join(dir, "distill.json") }
	memory.Path = func() string { return filepath.Join(dir, "memory.json") }
	now = func() time.Time { return sb.at }
	list = func() []sessions.Session { return sb.list }
	read = func(s sessions.Session) (sessions.Transcript, error) {
		tr, ok := sb.trs[s.ID]
		if !ok {
			return tr, sessions.ErrNoTranscript
		}
		return tr, nil
	}
	t.Cleanup(func() { Path, memory.Path, now, list, read = oldPath, oldMem, oldNow, oldList, oldRead })
	return sb
}

// session adds a readable session of codex's, last active ago, whose
// user said each of said.
func (sb *sandbox) session(id string, ago time.Duration, said ...string) *sessions.Session {
	sb.list = append(sb.list, sessions.Session{Agent: "codex", ID: id, Cwd: filepath.Join(sb.dir, "proj"), Last: sb.at.Add(-ago), Transcript: true})
	var parts []sessions.Part
	for _, s := range said {
		parts = append(parts, sessions.Part{Role: "user", Kind: "text", Text: s},
			sessions.Part{Role: "assistant", Kind: "thinking", Text: "secret thoughts"},
			sessions.Part{Role: "assistant", Kind: "tool_use", Name: "shell", Text: "rm -rf build"},
			sessions.Part{Role: "assistant", Kind: "text", Text: "Done."})
	}
	sb.trs[id] = sessions.Transcript{Parts: parts}
	return &sb.list[len(sb.list)-1]
}

func (sb *sandbox) complete(_ context.Context, model, effort, system, user string) (string, error) {
	_, conv, _ := strings.Cut(user, "Conversation:\n\n")
	first, _, _ := strings.Cut(conv, "\n")
	if effort != "" {
		model += ":" + effort
	}
	sb.asked = append(sb.asked, model+" "+first)
	if sb.reply == nil {
		return "[]", nil
	}
	return sb.reply(user)
}

func on(t *testing.T, model string) {
	t.Helper()
	if _, err := Set(true, model); err != nil {
		t.Fatal(err)
	}
}

func memories(t *testing.T) []*memory.Memory {
	t.Helper()
	ms, _, err := memory.All()
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// Off by default: nothing is read, and nothing is written.
func TestOffReadsNothing(t *testing.T) {
	sb := newSandbox(t)
	sb.session("s1", time.Hour, "use pnpm", "always pnpm")
	if res, err := Run(context.Background(), "auto/m", sb.complete); err != nil || res.Read != 0 || len(sb.asked) != 0 {
		t.Fatalf("%+v %v %v", res, err, sb.asked)
	}
	if _, err := os.Stat(Path()); err == nil {
		t.Fatal("off, but distill.json was written")
	}
}

// Only a main session that has been quiet for Quiet, is readable, isn't
// magpie's own, said enough and is newer than Since is read; what it
// proposes waits as a candidate with where it came from, when it quotes
// the user (case, spaces and punctuation aside), and is dropped when the
// quote is missing or words the user never wrote; it isn't read again
// until it goes on.
func TestReadsQuietSessionsOnce(t *testing.T) {
	sb := newSandbox(t)
	on(t, "")
	sb.session("quiet", time.Hour, "quiet: we deploy with make ship", "never use npm here, pnpm only")
	sb.session("going", 5*time.Minute, "going: one", "two")
	sb.session("short", time.Hour, "short: hi")
	sb.session("sub", time.Hour, "sub: one", "two").ParentSession = "quiet"
	sb.session("gw", time.Hour, "gw: one", "two").Gateway = true
	sb.session("old", 8*24*time.Hour, "old: one", "two")
	sb.session("own", time.Hour, "own: one", "two").Agent = Agent
	sb.session("unread", time.Hour).Transcript = false
	sb.reply = func(user string) (string, error) {
		return "Here they are:\n```json\n" + `[
			{"text":"Use pnpm, never npm","kind":"rule","scope":"project","why":"said so","quote":"never use npm here, pnpm only"},
			{"text":"Deploys go out with make ship","kind":"nonsense","scope":"project","quote":"We deploy with  Make ship."},
			{"text":"Prefers short answers","kind":"preference","scope":"global","quote":"pnpm only"},
			{"text":"Runs tests on mote","kind":"rule","scope":"global","quote":"heavy commands run on mote"},
			{"text":"Uses tabs","kind":"preference","scope":"global"},
			{"text":"","kind":"fact","quote":"pnpm"}
		]` + "\n```", nil
	}
	res, err := Run(context.Background(), "auto/m", sb.complete)
	if err != nil || res.Read != 1 || res.Added != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(sb.asked) != 1 || sb.asked[0] != "auto/m User: quiet: we deploy with make ship" {
		t.Fatalf("asked about %q", sb.asked)
	}
	ms := memories(t)
	proj := memory.ProjectOf(filepath.Join(sb.dir, "proj"))
	want := map[string]string{"Use pnpm, never npm": "rule project:" + proj.Key, "Deploys go out with make ship": "fact project:" + proj.Key, "Prefers short answers": "preference global"}
	for _, m := range ms {
		if want[m.Text] != m.Kind+" "+m.Scope || m.Origin != memory.FromInferred || m.Status != memory.Candidate || len(m.Sources) != 1 || m.Sources[0].Session != "quiet" || m.Sources[0].Agent != "codex" {
			t.Errorf("%+v", m)
		}
		delete(want, m.Text)
	}
	if len(want) != 0 {
		t.Fatalf("not kept: %v", want)
	}
	for _, m := range ms {
		if m.Text == "Use pnpm, never npm" && (m.Sources[0].Quote != "never use npm here, pnpm only" || m.Why != "said so") {
			t.Fatalf("%+v", m)
		}
	}
	st, _ := Load()
	if st.Read != 1 || st.Added != 3 || st.LastModel != "auto/m" || st.Error != "" {
		t.Fatalf("%+v", st)
	}

	// read once
	if res, err := Run(context.Background(), "auto/m", sb.complete); err != nil || res.Read != 0 || len(sb.asked) != 1 {
		t.Fatalf("read again: %+v %v %q", res, err, sb.asked)
	}
	// it went on: read again, once quiet, with the model picked; the same
	// memories aren't added twice
	sb.at = sb.at.Add(time.Hour)
	sb.list[0].Last = sb.at.Add(-40 * time.Minute)
	on(t, "picked/m")
	if res, err := Run(context.Background(), "auto/m", sb.complete); err != nil || res.Read != 2 || res.Added != 0 {
		t.Fatalf("%+v %v %q", res, err, sb.asked)
	}
	if got := sb.asked[1:]; got[0] != "picked/m User: quiet: we deploy with make ship" || got[1] != "picked/m User: going: one" {
		t.Fatalf("%q", got)
	}
	if n := len(memories(t)); n != 3 {
		t.Fatalf("%d memories", n)
	}
}

// What the model is sent: the user's and the assistant's words, not
// thinking or tool calls; the memories already known; and instructions
// that name neither the app nor a vendor.
func TestWhatIsSent(t *testing.T) {
	sb := newSandbox(t)
	on(t, "")
	text, kind, scope := "Answers in Chinese", memory.Preference, memory.Global
	if _, _, err := memory.Save(memory.Input{Text: &text, Kind: &kind, Scope: &scope}, memory.FromUser, nil, nil); err != nil {
		t.Fatal(err)
	}
	sb.session("s", time.Hour, "first", "second")
	var sys, user string
	complete := func(_ context.Context, _, _, s, u string) (string, error) { sys, user = s, u; return "[]", nil }
	if _, err := Run(context.Background(), "a/m", complete); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"User: first", "Assistant: Done.", "User: second", "- Answers in Chinese", "Project: proj"} {
		if !strings.Contains(user, w) {
			t.Errorf("not sent: %q\n%s", w, user)
		}
	}
	for _, w := range []string{"secret thoughts", "rm -rf"} {
		if strings.Contains(user, w) {
			t.Errorf("sent: %q", w)
		}
	}
	for _, w := range []string{"magpie", "claude", "codex", "openai", "anthropic", "gpt", "gemini"} {
		if strings.Contains(strings.ToLower(sys+strings.Split(user, "Conversation:")[0]), w) {
			t.Errorf("names %q", w)
		}
	}
}

// A long conversation is cut to its start and end within budget, never
// mid-rune.
func TestLongConversationCut(t *testing.T) {
	var parts []sessions.Part
	for i := range 200 {
		parts = append(parts, sessions.Part{Role: "user", Kind: "text", Text: fmt.Sprintf("turn %d 用中文回答", i)}, sessions.Part{Role: "assistant", Kind: "text", Text: strings.Repeat("长", 3000)})
	}
	s, turns := transcript(sessions.Transcript{Parts: parts})
	if turns != 200 || len(s) > budget+20 || !strings.Contains(s, "turn 0 ") || !strings.Contains(s, "turn 199 ") || !strings.Contains(s, "[…]") || !strings.ContainsRune(s, '长') {
		t.Fatalf("%d %d", turns, len(s))
	}
	if !strings.Contains(s, "…") || strings.ContainsRune(s, '�') {
		t.Fatal("cut badly")
	}
}

// A model that fails stops the run and says why; the session is read
// next time. At most PerRun are read in one run, the latest first.
func TestFailureAndCap(t *testing.T) {
	sb := newSandbox(t)
	on(t, "")
	for i := range PerRun + 2 {
		sb.session(fmt.Sprint("s", i), time.Hour+time.Duration(i)*time.Minute, fmt.Sprint("n", i), "more")
	}
	sb.reply = func(string) (string, error) { return "", errors.New("429 out of allowance") }
	if _, err := Run(context.Background(), "a/m", sb.complete); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatal(err)
	}
	if st, _ := Load(); !strings.Contains(st.Error, "a/m: 429 out of allowance") || len(st.Done) != 0 {
		t.Fatalf("%+v", st)
	}
	sb.reply, sb.asked = nil, nil
	if res, err := Run(context.Background(), "a/m", sb.complete); err != nil || res.Read != PerRun || sb.asked[0] != "a/m User: n0" {
		t.Fatalf("%+v %v %q", res, err, sb.asked)
	}
	if st, _ := Load(); st.Error != "" {
		t.Fatalf("%+v", st)
	}
	if res, _ := Run(context.Background(), "a/m", sb.complete); res.Read != 2 {
		t.Fatalf("%+v", res)
	}
	// no model at all: said, nothing read
	sb.session("late", time.Hour, "x", "y")
	if _, err := Run(context.Background(), "", sb.complete); err == nil {
		t.Fatal("no model, no error")
	}
	if st, _ := Load(); !strings.Contains(st.Error, "pick one") {
		t.Fatalf("%+v", st)
	}
}

// One the user deleted isn't proposed again from another conversation.
func TestDeletedNotProposedAgain(t *testing.T) {
	sb := newSandbox(t)
	on(t, "")
	sb.reply = func(string) (string, error) {
		return `[{"text":"Likes tabs","kind":"preference","scope":"global","quote":"x"}]`, nil
	}
	sb.session("a", time.Hour, "x", "y")
	if res, err := Run(context.Background(), "a/m", sb.complete); err != nil || res.Added != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if err := memory.Remove(memories(t)[0].ID); err != nil {
		t.Fatal(err)
	}
	sb.session("b", time.Hour, "x", "y")
	if res, err := Run(context.Background(), "a/m", sb.complete); err != nil || res.Read != 1 || res.Added != 0 || len(memories(t)) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// distill.json that can't be read is never written over, and nothing runs.
func TestUnreadableStateRunsNothing(t *testing.T) {
	sb := newSandbox(t)
	sb.session("s", time.Hour, "x", "y")
	if err := os.WriteFile(Path(), []byte(`{"on":tru`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), "a/m", sb.complete); err == nil || len(sb.asked) != 0 {
		t.Fatalf("%v %q", err, sb.asked)
	}
	if _, err := Set(false, ""); err == nil {
		t.Fatal("written over")
	}
	if b, _ := os.ReadFile(Path()); string(b) != `{"on":tru` {
		t.Fatalf("%s", b)
	}
}

// How far back reading looks is picked (RealZeroP27 on X: only the last
// 7 days were read): 7 days by default, as before; a longer range reads
// the older conversations in it, All every one listed, PerRun a run all
// the same. What was read already isn't read again. A state written
// before ranges could be picked keeps its own Since. A range or level not
// offered is refused.
func TestRangePicked(t *testing.T) {
	sb := newSandbox(t)
	// a state from before: switched on two days ago, so looking back to
	// nine days ago, and nothing else
	was := sb.at.Add(-9 * 24 * time.Hour).UTC()
	if err := os.WriteFile(Path(), []byte(`{"on":true,"since":"`+was.Format(time.RFC3339)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sb.session("d1", 24*time.Hour, "d1", "x")
	sb.session("d8", 8*24*time.Hour, "d8", "x")
	sb.session("d20", 20*24*time.Hour, "d20", "x")
	sb.session("d200", 200*24*time.Hour, "d200", "x")
	if _, err := Run(context.Background(), "a/m", sb.complete); err != nil {
		t.Fatal(err)
	}
	if st, _ := Load(); !st.Since.Equal(was) || st.Days != 0 || !slices.Equal(sb.asked, []string{"a/m User: d1", "a/m User: d8"}) {
		t.Fatalf("an old state read %q, since %v", sb.asked, st.Since)
	}

	sb.asked = nil
	if _, err := SetRange(30); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), "a/m", sb.complete); err != nil || !slices.Equal(sb.asked, []string{"a/m User: d20"}) {
		t.Fatalf("30 days read %q %v", sb.asked, err)
	}
	sb.asked = nil
	if st, err := SetRange(All); err != nil || !st.Since.IsZero() || st.Days != All {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := Run(context.Background(), "a/m", sb.complete); err != nil || !slices.Equal(sb.asked, []string{"a/m User: d200"}) {
		t.Fatalf("all read %q %v", sb.asked, err)
	}
	// back to 7: nothing older is read, and switched off and on it looks
	// back 7 days again
	if st, _ := SetRange(7); st.Days != 0 || !st.Since.Equal(sb.at.Add(-Back)) {
		t.Fatalf("%+v", st)
	}
	if _, err := SetRange(10); err == nil {
		t.Fatal("10 days taken")
	}
	if _, err := SetEffort("hard"); err == nil {
		t.Fatal("a level not known taken")
	}
	if st, _ := Load(); st.Days != 0 || st.Effort != "" {
		t.Fatalf("a refusal changed %+v", st)
	}

	// a fresh switch-on with All picked reads everything
	sb2 := newSandbox(t)
	sb2.session("old", 300*24*time.Hour, "old", "x")
	if _, err := SetRange(All); err != nil {
		t.Fatal(err)
	}
	on(t, "")
	if _, err := Run(context.Background(), "a/m", sb2.complete); err != nil || !slices.Equal(sb2.asked, []string{"a/m User: old"}) {
		t.Fatalf("%q %v", sb2.asked, err)
	}
}

// The reasoning level picked is what the model is asked at, reading and
// tidying alike; none picked, it is asked at its own default.
func TestEffortPicked(t *testing.T) {
	sb := newSandbox(t)
	on(t, "")
	sb.session("a", time.Hour, "a", "x")
	if _, err := Run(context.Background(), "a/m", sb.complete); err != nil || sb.asked[0] != "a/m User: a" {
		t.Fatalf("%q %v", sb.asked, err)
	}
	if st, err := SetEffort(" High "); err != nil || st.Effort != "high" {
		t.Fatalf("%+v %v", st, err)
	}
	sb.session("b", time.Hour, "b", "x")
	if _, err := Run(context.Background(), "a/m", sb.complete); err != nil || sb.asked[1] != "a/m:high User: b" {
		t.Fatalf("%q %v", sb.asked, err)
	}
	note(t, "One", memory.Global, memory.FromInferred)
	note(t, "Two", memory.Global, memory.FromInferred)
	var asked string
	if _, err := Dream(context.Background(), "a/m", func(_ context.Context, _, effort, _, _ string) (string, error) {
		asked = effort
		return "{}", nil
	}); err != nil || asked != "high" {
		t.Fatalf("tidied at %q: %v", asked, err)
	}
}

// A conversation read once isn't read again when it is more than a month
// old but inside the range: its mark was kept 30 days, so a session
// older than that, read when reading was switched on two months back,
// was read again every hour.
func TestOldConversationReadOnce(t *testing.T) {
	sb := newSandbox(t)
	if _, err := SetRange(90); err != nil {
		t.Fatal(err)
	}
	on(t, "")
	sb.session("old", 40*24*time.Hour, "old", "x")
	sb.session("new", time.Hour, "new", "x")
	for range 3 {
		if _, err := Run(context.Background(), "a/m", sb.complete); err != nil {
			t.Fatal(err)
		}
		sb.at = sb.at.Add(time.Hour)
	}
	if !slices.Equal(sb.asked, []string{"a/m User: new", "a/m User: old"}) {
		t.Fatalf("read %q", sb.asked)
	}
	// a mark goes once its session is over a month old and has left the
	// listing
	if st, _ := Load(); len(st.Done) != 2 {
		t.Fatalf("%+v", st.Done)
	}
	sb.at = sb.at.Add(60 * 24 * time.Hour)
	sb.list = sb.list[1:]
	sb.list[0].Last = sb.at.Add(-time.Hour)
	if _, err := Run(context.Background(), "a/m", sb.complete); err != nil {
		t.Fatal(err)
	}
	if st, _ := Load(); len(st.Done) != 1 || st.Done["codex/new"].IsZero() {
		t.Fatalf("%+v", st.Done)
	}
}

// A backlog is read at once when asked for (RealZeroP27 on X: at 10 an
// hour, 400 conversations took 40 hours). The hourly run reads PerRun
// and says how many it left; ReadAll reads every one left, with its
// progress seen while it goes; what is read isn't read again.
func TestBacklogReadAtOnce(t *testing.T) {
	sb := newSandbox(t)
	on(t, "")
	if _, err := SetRange(All); err != nil {
		t.Fatal(err)
	}
	for i := range Listed {
		sb.session(fmt.Sprint("s", i), time.Hour+time.Duration(i)*time.Hour, fmt.Sprint("n", i), "more")
	}
	res, err := Run(context.Background(), "a/m", sb.complete)
	if err != nil || res.Read != PerRun {
		t.Fatalf("hourly %+v %v", res, err)
	}
	if st, _ := Load(); st.Left != Listed-PerRun {
		t.Fatalf("left %d", st.Left)
	}
	var seen [][2]int
	sb.reply = func(string) (string, error) {
		n, m := Progress()
		seen = append(seen, [2]int{n, m})
		return "[]", nil
	}
	sb.asked = nil
	res, err = ReadAll(context.Background(), "a/m", sb.complete)
	if err != nil || res.Read != Listed-PerRun || len(sb.asked) != Listed-PerRun {
		t.Fatalf("read all %+v %v %d", res, err, len(sb.asked))
	}
	if seen[0] != [2]int{1, Listed - PerRun} || seen[len(seen)-1] != [2]int{Listed - PerRun, Listed - PerRun} {
		t.Fatalf("progress %v … %v", seen[0], seen[len(seen)-1])
	}
	if n, m := Progress(); n != 0 || m != 0 || Running() {
		t.Fatalf("after the run %d of %d", n, m)
	}
	if st, _ := Load(); st.Left != 0 || st.Error != "" {
		t.Fatalf("%+v", st)
	}
	sb.asked = nil
	if res, _ := ReadAll(context.Background(), "a/m", sb.complete); res.Read != 0 || len(sb.asked) != 0 {
		t.Fatalf("read again %+v", res)
	}
}

// Stop ends a run going, and so does switching off: the request in
// flight is dropped and read next time, and it isn't an error.
func TestStopEndsTheRun(t *testing.T) {
	sb := newSandbox(t)
	on(t, "")
	for i := range 20 {
		sb.session(fmt.Sprint("s", i), time.Hour+time.Duration(i)*time.Minute, fmt.Sprint("n", i), "more")
	}
	stopAt := func(n int, stop func()) func(context.Context, string, string, string, string) (string, error) {
		return func(ctx context.Context, model, effort, system, user string) (string, error) {
			if len(sb.asked) == n-1 {
				stop()
				<-ctx.Done()
				sb.asked = append(sb.asked, "stopped")
				return "", ctx.Err()
			}
			return sb.complete(ctx, model, effort, system, user)
		}
	}
	res, err := ReadAll(context.Background(), "a/m", stopAt(5, Stop))
	if err != nil || res.Read != 4 {
		t.Fatalf("%+v %v", res, err)
	}
	if st, _ := Load(); st.Error != "" || st.Left != 16 || len(st.Done) != 4 || !st.On {
		t.Fatalf("%+v", st)
	}
	sb.asked = nil
	res, err = ReadAll(context.Background(), "a/m", stopAt(3, func() { Set(false, "") }))
	if err != nil || res.Read != 2 {
		t.Fatalf("switched off: %+v %v", res, err)
	}
	if st, _ := Load(); st.On || st.Left != 14 {
		t.Fatalf("%+v", st)
	}
	// with none going, Stop does nothing, and the next run reads the rest
	Stop()
	on(t, "")
	sb.asked = nil
	if res, err := ReadAll(context.Background(), "a/m", sb.complete); err != nil || res.Read != 14 {
		t.Fatalf("%+v %v", res, err)
	}
}

// Switched off, the background reads ask no model: picking one loads
// every provider's models, the whole catalog. Switched on, they do.
func TestKeepOffAsksNoModel(t *testing.T) {
	newSandbox(t)
	ctx, cancel := context.WithCancel(context.Background())
	asked := make(chan struct{}, 1)
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		Keep(ctx, time.Millisecond, time.Millisecond, func() string {
			select {
			case asked <- struct{}{}:
			default:
			}
			return "m/x"
		}, nil)
	}()
	// a hundred rounds, at least, of 1ms each while off
	select {
	case <-asked:
		t.Fatal("switched off, it asked for a model")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := change(func(s *State) { s.On = true }); err != nil {
		t.Fatal(err)
	}
	<-asked // the round after, waited on and not timed
}
