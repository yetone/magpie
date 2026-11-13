package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// freshRoom gives the test a claudeRoom of its own, at most runs at once
// (claudeRunsMost in settings), what the machine's memory holds left out.
func freshRoom(t *testing.T, most int) {
	t.Helper()
	old := claudeRoom
	claudeRoom = &claudeRoomT{freed: make(chan struct{})}
	memRuns.Lock()
	known, n, why := memRuns.known, memRuns.n, memRuns.why
	memRuns.known, memRuns.n, memRuns.why = true, 0, ""
	memRuns.Unlock()
	// the package's tests share one settings.json: the bound is put back
	// as it was, or later tests' runs wait for room
	was := settings.Load().ClaudeRunsMost
	t.Cleanup(func() {
		claudeRoom = old
		memRuns.Lock()
		memRuns.known, memRuns.n, memRuns.why = known, n, why
		memRuns.Unlock()
		setRunsMost(t, was)
	})
	setRunsMost(t, most)
}

// setRunsMost sets claudeRunsMost, the rest of settings.json as it is.
func setRunsMost(t *testing.T, n int) {
	t.Helper()
	s := settings.Load()
	s.ClaudeRunsMost = n
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// aliveCount is a fake Claude Code's start that logs how many of the runs
// started so far are alive, itself among them.
const aliveCount = `n=0
for f in $(dirname $log)/*; do kill -0 $(basename $f) 2>/dev/null && n=$((n+1)); done
echo "alive $n" >> $log
`

// mostAlive is the most runs alive at once that any run's start saw.
func (h *resumeHarness) mostAlive() int {
	most := 0
	for _, l := range h.runs() {
		for _, line := range strings.Split(l, "\n") {
			if n, ok := strings.CutPrefix(line, "alive "); ok {
				v, _ := strconv.Atoi(n)
				most = max(most, v)
			}
		}
	}
	return most
}

// askCode is ask, its status and message given back rather than fatal.
func (h *resumeHarness) askCode(system string, said ...string) (int, string) {
	var msgs []string
	for i, m := range said {
		if i > 0 {
			msgs = append(msgs, `{"role":"assistant","content":"ok"}`)
		}
		msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":%q}`, m))
	}
	body := `{"model":"claude-sonnet-5","max_tokens":100,"system":"` + system + `","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[` + strings.Join(msgs, ",") + `]}`
	var u Usage
	return h.s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u)
}

// No more Claude Code processes run than the machine is said to hold:
// six conversations asked at once on a room of two each get their answer,
// the runs before them let go as they wait, and no run's start finds more
// than two alive. Unbounded, all six ran at once: on a 512 MiB service
// the kernel killed them (#2956).
func TestClaudeRunsHeldToTheRoom(t *testing.T) {
	h := newScriptHarness(t, "ok", aliveCount)
	freshRoom(t, 2)
	var wg sync.WaitGroup
	codes := make([]int, 6)
	msgs := make([]string, 6)
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], msgs[i] = h.askCode("rules "+strconv.Itoa(i), "hi")
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("conversation %d: %d %s", i, c, msgs[i])
		}
	}
	if n := len(h.runs()); n != 6 {
		t.Fatalf("%d runs started, want 6", n)
	}
	if most := h.mostAlive(); most > 2 {
		t.Fatalf("%d Claude Code processes were alive at once, 2 may be", most)
	}
}

// A run waiting for its conversation's next turn is let go to make room,
// its session shelved: that next turn resumes it (--resume), told only
// the new message, not the whole conversation again.
func TestClaudeRoomLetsAnIdleRunGoAndItResumes(t *testing.T) {
	h := newScriptHarness(t, "ok", aliveCount)
	freshRoom(t, 1)
	h.ask("rules A", "a1")
	var pidA string
	for pid := range h.runs() {
		pidA = pid
	}
	h.ask("rules B", "b1")
	if most := h.mostAlive(); most > 1 {
		t.Fatalf("%d alive at once, 1 may be", most)
	}
	h.ask("rules A", "a1", "a2")
	runs := h.runs()
	if len(runs) != 3 {
		t.Fatalf("runs: %v", runs)
	}
	resumed := false
	for pid, l := range runs {
		if pid != pidA && strings.Contains(l, "--resume s"+pidA) {
			resumed = true
			// Only what the run was told: the log's random request ids
			// can spell "a1" too.
			for _, line := range strings.Split(l, "\n") {
				if strings.HasPrefix(line, "told ") && strings.Contains(line, "a1") {
					t.Fatalf("the resumed run was told the conversation again:\n%s", l)
				}
			}
		}
	}
	if !resumed {
		t.Fatalf("A's next turn didn't resume its session s%s: %v", pidA, runs)
	}
	if most := h.mostAlive(); most > 1 {
		t.Fatalf("%d alive at once, 1 may be", most)
	}
}

// With no run to let go and none ending, a turn waits claudeRoomWait and
// is answered 503, which routing passes to the next account, with what
// held it and how to change it; once room is freed, the next turn runs.
func TestClaudeRoomFullIs503(t *testing.T) {
	h := newScriptHarness(t, "ok", "")
	freshRoom(t, 1)
	old := claudeRoomWait
	claudeRoomWait = 50 * time.Millisecond
	t.Cleanup(func() { claudeRoomWait = old })
	release, err := claudeRoom.take(context.Background(), "a test holding the room")
	if err != nil {
		t.Fatal(err)
	}
	code, msg := h.askCode("rules", "hi")
	if code != 503 || !strings.Contains(msg, "1 Claude Code processes are running") || !strings.Contains(msg, "claudeRunsMost") {
		t.Fatalf("%d %s", code, msg)
	}
	if !claudeNoRoom(code, []byte(msg)) {
		t.Fatal("routing doesn't know a full room's 503")
	}
	if n := len(h.runs()); n != 0 {
		t.Fatalf("%d runs started with no room", n)
	}
	release(false)
	if code, msg := h.askCode("rules", "hi"); code != 200 {
		t.Fatalf("%d %s", code, msg)
	}
}

// A one-off Claude Code — a model test, a warm-up, a /usage read — takes
// a place too, and waits for one.
func TestClaudeOneOffsTakeRoom(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("no true")
	}
	testenv.SetHome(t, t.TempDir())
	freshRoom(t, 1)
	release, err := claudeRoom.take(context.Background(), "a test holding the room")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = runClaudeTree(ctx, exec.Command("touch", marker), "a one-off")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a one-off with no room: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the one-off ran with no room")
	}
	release(false)
	if err := runClaudeTree(context.Background(), exec.Command("touch", marker), "a one-off"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the one-off didn't run with room")
	}
	claudeRoom.mu.Lock()
	live := claudeRoom.live
	claudeRoom.mu.Unlock()
	if live != 0 {
		t.Fatalf("%d places still taken after the one-off", live)
	}
}

// memFixture is a machine's /proc and /sys/fs/cgroup under a folder.
func memFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The reporter's service (#2956): systemd's MemoryMax=512M on
// magpie.service, cgroup v2, on a machine of 987728 kB. It holds 2.
func TestClaudeMemRunsFromTheCgroup(t *testing.T) {
	root := memFixture(t, map[string]string{
		"proc/meminfo":     "MemTotal:         987728 kB\nMemFree:           81200 kB\n",
		"proc/self/cgroup": "0::/system.slice/magpie.service\n",
		"sys/fs/cgroup/system.slice/magpie.service/memory.max":  "536870912\n",
		"sys/fs/cgroup/system.slice/magpie.service/memory.high": "max\n",
		"sys/fs/cgroup/system.slice/memory.max":                 "max\n",
	})
	limit, why := memLimit("linux", root)
	if limit != 536870912 || !strings.Contains(why, "memory.max") {
		t.Fatalf("limit %d (%s)", limit, why)
	}
	if n := runsFor(limit); n != 2 {
		t.Fatalf("512 MiB holds %d", n)
	}

	// the machine alone (#2294's small server): MemTotal
	root = memFixture(t, map[string]string{
		"proc/meminfo":                        "MemTotal:         987728 kB\n",
		"proc/self/cgroup":                    "0::/user.slice\n",
		"sys/fs/cgroup/user.slice/memory.max": "max\n",
	})
	limit, why = memLimit("linux", root)
	if limit != 987728<<10 || why != "MemTotal" {
		t.Fatalf("limit %d (%s)", limit, why)
	}
	if n := runsFor(limit); n != 5 {
		t.Fatalf("964 MiB holds %d", n)
	}

	// cgroup v1, the container's own root mounted
	root = memFixture(t, map[string]string{
		"proc/meminfo":     "MemTotal:        8000000 kB\n",
		"proc/self/cgroup": "12:memory:/docker/abc\n11:cpu,cpuacct:/docker/abc\n",
		"sys/fs/cgroup/memory/memory.limit_in_bytes": "1073741824\n",
	})
	if limit, why = memLimit("linux", root); limit != 1<<30 {
		t.Fatalf("v1 limit %d (%s)", limit, why)
	}
	if n := runsFor(limit); n != 6 {
		t.Fatalf("1 GiB holds %d", n)
	}

	// so little the reserve takes it all: 1 still runs
	if n := runsFor(200 << 20); n != 1 {
		t.Fatalf("200 MiB holds %d", n)
	}
}

// The cgroup path walked is a "/" path whatever the host reads it on.
// filepath's root is "\" on Windows, so the walk there never reached "/"
// and read \memory.max forever, holding the whole suite (#4113).
func TestClaudeMemWalkEndsAtRoot(t *testing.T) {
	root := memFixture(t, map[string]string{
		"proc/self/cgroup": "0::/system.slice/magpie.service\n",
		"sys/fs/cgroup/system.slice/magpie.service/memory.max": "536870912\n",
	})
	done := make(chan struct{})
	var limit int64
	var why string
	go func() {
		limit, why = memLimit("linux", root)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("the cgroup walk never ended at \"/\"")
	}
	if limit != 536870912 || !strings.Contains(why, "system.slice/magpie.service") {
		t.Fatalf("limit %d (%s)", limit, why)
	}
}

// Memory not read is not known, and bounds nothing: never 0 or 1 places
// from files that weren't there, nor on another system.
func TestClaudeMemRunsUnknownBoundsNothing(t *testing.T) {
	if limit, _ := memLimit("linux", t.TempDir()); limit != 0 {
		t.Fatalf("nothing readable gave %d", limit)
	}
	root := memFixture(t, map[string]string{"proc/meminfo": "MemTotal:  987728 kB\n"})
	if limit, _ := memLimit("darwin", root); limit != 0 {
		t.Fatalf("darwin gave %d", limit)
	}
	testenv.SetHome(t, t.TempDir())
	freshRoom(t, 0)
	memRuns.Lock()
	memRuns.known = false
	memRuns.Unlock()
	oldGOOS, oldRoot := memGOOS, memRoot
	memGOOS, memRoot = "linux", t.TempDir()
	t.Cleanup(func() { memGOOS, memRoot = oldGOOS, oldRoot })
	if n, why := claudeRunsMost(); n != 0 {
		t.Fatalf("unknown memory bounded runs to %d (%s)", n, why)
	}
}

// A test's root is not this computer, even one naming this computer's
// system: holding nothing, it leaves memory unknown to every reader that
// falls back on the computer's memory. The computer's memory stood in for
// such a root, so each read the test machine's (#5118: a Codex turn's
// limit on an "unknown" machine was 998 MiB on ubuntu CI, 1994 MiB on WSL).
func TestAFixtureRootIsNotThisComputer(t *testing.T) {
	oldGOOS, oldRoot := memGOOS, memRoot
	memGOOS, memRoot = runtime.GOOS, t.TempDir()
	t.Cleanup(func() { memGOOS, memRoot = oldGOOS, oldRoot })
	if n, why := bodyRoomSize(); n != 0 {
		t.Errorf("body room %d MiB (%s), want none", n>>20, why)
	}
	runBudget.Once = sync.Once{}
	t.Cleanup(func() { runBudget.Once = sync.Once{} })
	if n, why := claudeRunBudget(); n != claudeRunUnknown {
		t.Errorf("run budget %d MiB (%s), want the unknown %d MiB", n>>20, why, claudeRunUnknown>>20)
	}
	if n := codexBodyLimit(); n != defaultBodyLimit {
		t.Errorf("Codex turn limit %d MiB, want %d MiB", n>>20, defaultBodyLimit>>20)
	}
}

// The user's claudeRunsMost wins over what the memory holds.
func TestClaudeRunsMostTheUsersOwn(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	freshRoom(t, 0)
	memRuns.Lock()
	memRuns.known = false
	memRuns.Unlock()
	oldGOOS, oldRoot := memGOOS, memRoot
	memGOOS, memRoot = "linux", memFixture(t, map[string]string{
		"proc/meminfo":     "MemTotal:         987728 kB\n",
		"proc/self/cgroup": "0::/system.slice/magpie.service\n",
		"sys/fs/cgroup/system.slice/magpie.service/memory.max": "536870912\n",
	})
	t.Cleanup(func() { memGOOS, memRoot = oldGOOS, oldRoot })
	if n, why := claudeRunsMost(); n != 2 || !strings.Contains(why, "512 MiB") {
		t.Fatalf("auto: %d (%s)", n, why)
	}
	setRunsMost(t, 5)
	if n, why := claudeRunsMost(); n != 5 || !strings.Contains(why, "settings") {
		t.Fatalf("the user's 5 gave %d (%s)", n, why)
	}
}

// A full room's 503 is no account's fault, and every Claude account would
// wait for the same room: in a group the next provider answers at once,
// no other Claude account is asked, none rests, and the Claude account
// isn't asked again after a moment; alone, the agent is told why.
func TestClaudeNoRoomPassesOnAndRestsNobody(t *testing.T) {
	fresh(t)
	claudeMadeFirst(t, false) // two Claude accounts
	freshRoom(t, 1)
	old := claudeRoomWait
	claudeRoomWait = 50 * time.Millisecond
	t.Cleanup(func() { claudeRoomWait = old })
	release, err := claudeRoom.take(context.Background(), "a test holding the room")
	if err != nil {
		t.Fatal(err)
	}
	defer release(false)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-5","content":[{"type":"text","text":"from key"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k", Anthropic: up.URL, Models: []string{"claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Two", Members: []string{"claude/claude-sonnet-5", "other/claude-sonnet-5"}, Routing: provider.Ordered, Patience: -1}); err != nil {
		t.Fatal(err)
	}
	s := New()
	t.Cleanup(s.subscription.abortAll)
	post := func(model string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"`+model+`","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`))
		r.Header.Set("x-api-key", Token)
		r.RemoteAddr = "127.0.0.1:12345"
		w := httptest.NewRecorder()
		lanGuard(s.Handler()).ServeHTTP(w, r)
		return w
	}
	w := post("group/two")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "from key") {
		t.Fatalf("group: %d %s", w.Code, w.Body)
	}
	tries := s.trace.routes[len(s.trace.routes)-1].Tries
	if len(tries) != 2 || tries[0].Status != 503 || tries[0].Rest != nil {
		t.Fatalf("tries %+v", tries)
	}
	noneRested(t, "claude")

	w = post("claude/claude-sonnet-5")
	if w.Code != 503 || !strings.Contains(w.Body.String(), claudeNoRoomSaid) {
		t.Fatalf("alone: %d %s", w.Code, w.Body)
	}
	if tries := s.trace.routes[len(s.trace.routes)-1].Tries; len(tries) != 1 {
		t.Fatalf("alone, asked %d times: %+v", len(tries), tries)
	}
	noneRested(t, "claude")
}

// A lead conversation waiting on its subagents' Task calls holds a Claude
// Code doing nothing, and each subagent needs one of its own: once the
// lead has waited roomParkLongest, a subagent waiting for room has its
// place, well before parkLongest and claudeRoomWait. Kept until
// parkLongest, the lead held the room while its subagents waited 2
// minutes and were answered 503 (Wing8Aqua on X). A lead parked less
// than that keeps its run.
func TestClaudeRoomLetsALeadWaitingOnSubagentsGo(t *testing.T) {
	fakeClaudeCalling(t)
	freshRoom(t, 1)
	oldWait, oldLook, oldPark := claudeRoomWait, roomLook, roomParkLongest
	roomLook = 20 * time.Millisecond
	t.Cleanup(func() { claudeRoomWait, roomLook, roomParkLongest = oldWait, oldLook, oldPark })

	t.Run("parked past roomParkLongest", func(t *testing.T) {
		claudeRoomWait = 30 * time.Second // parkLongest's 5 minutes would run past it
		roomParkLongest = 100 * time.Millisecond
		s := New()
		t.Cleanup(s.subscription.abortAll)
		lead := askForCall(t, s, context.Background(), "lead: start three subagents")
		if lead == 0 {
			return
		}
		began := time.Now()
		sub := askForCall(t, s, context.Background(), "subagent 1: read the code")
		if sub == 0 {
			return
		}
		if waited := time.Since(began); waited > 15*time.Second {
			t.Fatalf("the subagent waited %s for room", waited)
		}
		if leftover([]int{lead}, 3*time.Second) != 0 {
			t.Fatal("the lead's Claude Code still runs beside the subagent's, 1 may")
		}
	})
	t.Run("parked a moment", func(t *testing.T) {
		claudeRoomWait = 300 * time.Millisecond
		roomParkLongest = time.Hour
		s := New()
		t.Cleanup(s.subscription.abortAll)
		lead := askForCall(t, s, context.Background(), "lead: read a file")
		if lead == 0 {
			return
		}
		p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
		body := `{"model":"claude-sonnet-5","max_tokens":100,"messages":[{"role":"user","content":"another conversation"}]}`
		var u Usage
		code, msg := s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u)
		if code != 503 || !strings.Contains(msg, claudeNoRoomSaid) {
			t.Fatalf("%d %s", code, msg)
		}
		if !alive(lead) {
			t.Fatal("a run waiting on a tool call made a moment ago was let go")
		}
	})
}
