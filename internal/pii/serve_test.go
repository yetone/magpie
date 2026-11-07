package pii

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/redact"
)

// person is a name at [s, e), as sure as the fake pf-serve says (main_test.go).
func person(s, e int) redact.Found {
	return redact.Found{Start: s, End: e, Label: "private_person", Score: float64(float32(0.98))}
}

// Short texts go to pf-serve joined, in one request, and what it finds
// comes back to each text at that text's own offsets; a text seen before
// isn't asked about again.
func TestFindJoinsTextsAndMapsBack(t *testing.T) {
	log, _ := gpuInstall(t, "ok")
	texts := []string{"hi Alice", "nothing here", "Bob, Alice and Alice"}
	got, err := Find(texts)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]redact.Found{{person(3, 8)}, nil, {person(5, 10), person(15, 20)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Find = %v, want %v", got, want)
	}
	if reqs := fakeLines(t, log, "request"); len(reqs) != 1 || !strings.HasPrefix(reqs[0], "request 1 ") {
		t.Fatalf("requests = %q, want one of one joined text", reqs)
	}
	again, err := Find([]string{"Bob, Alice and Alice", "new Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, [][]redact.Found{want[2], {person(4, 9)}}) {
		t.Fatalf("Find again = %v", again)
	}
	reqs := fakeLines(t, log, "request")
	if len(reqs) != 2 || strings.Contains(reqs[1], "Bob") {
		t.Fatalf("requests = %q, want the second to ask about the new text alone", reqs)
	}
	if n := len(fakeLines(t, log, "start")); n != 1 {
		t.Fatalf("pf-serve started %d times, want once", n)
	}
	if st := Now(); !st.Running || !st.GPU || st.Device != "MTL0" || st.LoadMS != 7 {
		t.Fatalf("Now = %+v", st)
	}
}

func TestJoinSplitsAtChunkSize(t *testing.T) {
	long := strings.Repeat("x", chunkSize)
	texts := []string{"a", "b", long, "c"}
	chunks, parts := join(pf.missing(texts, []int{0, 1, 2, 3}, nil))
	// the last text first: TestNewestTextReadFirst
	if len(chunks) != 3 || chunks[0] != "c" || chunks[1] != long || chunks[2] != "b"+sep+"a" {
		t.Fatalf("chunks = %d: %.20q", len(chunks), chunks)
	}
	if !reflect.DeepEqual(parts, [][]part{{{"c", 0}}, {{long, 0}}, {{"b", 0}, {"a", 1 + len(sep)}}}) {
		t.Fatalf("parts = %v", parts)
	}
}

// A span the model ran across the separator into the next text is cut at
// each text's end.
func TestWithinCutsSpansAtTheTextsEnds(t *testing.T) {
	fs := []redact.Found{person(2, 9), {Start: 0, End: 2, Label: "secret"}}
	if got := within(fs, 0, 5); !reflect.DeepEqual(got, []redact.Found{person(2, 5), {Start: 0, End: 2, Label: "secret"}}) {
		t.Fatalf("first text: %v", got)
	}
	if got := within(fs, 7, 12); !reflect.DeepEqual(got, []redact.Found{person(0, 2)}) {
		t.Fatalf("second text: %v", got)
	}
	if got := within(fs, 10, 12); got != nil {
		t.Fatalf("third text: %v", got)
	}
}

// pf-serve that dies on the GPU is started again on the CPU, which the
// request then gets its answer from, and stays there; the status says why.
func TestGPUDeathMovesToCPU(t *testing.T) {
	log, gpu := gpuInstall(t, "gpudies")
	got, err := Find([]string{"hi Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, [][]redact.Found{{person(3, 8)}}) {
		t.Fatalf("Find = %v", got)
	}
	if starts := fakeLines(t, log, "start"); !reflect.DeepEqual(starts, []string{gpu, "start pf-serve CPU"}) {
		t.Fatalf("starts = %q", starts)
	}
	st := Now()
	if st.GPU || st.Device != "CPU" || !strings.Contains(st.GPUError, "device lost") {
		t.Fatalf("Now = %+v, want on the CPU saying the GPU was lost", st)
	}
	Stop()
	if _, err := Find([]string{"Alice again"}); err != nil {
		t.Fatal(err)
	}
	if starts := fakeLines(t, log, "start"); len(starts) != 3 || starts[2] != "start pf-serve CPU" {
		t.Fatalf("starts = %q, want the CPU after a stop too", starts)
	}
}

// The answer of a GPU pf-serve that died is read only after it has
// exited and been waited for, and the status still says the GPU was lost,
// not "read |0: file already closed" (#3741: cmd.Wait closed the pipe
// under the read).
func TestGPUDeathReadAfterExit(t *testing.T) {
	old := beforeRead
	beforeRead = func(h *helper) {
		if h.hello.GPU {
			<-h.exited
		}
	}
	t.Cleanup(func() { beforeRead = old })
	gpuInstall(t, "gpudies")
	if _, err := Find([]string{"hi Alice"}); err != nil {
		t.Fatal(err)
	}
	if st := Now(); st.Device != "CPU" || !strings.Contains(st.GPUError, "device lost") {
		t.Fatalf("Now = %+v, want on the CPU saying the GPU was lost", st)
	}
}

// A pf-serve-vulkan that can't start (no Vulkan loader) leaves it to
// pf-serve.
func TestVulkanThatWontStartFallsBack(t *testing.T) {
	log := fakeInstall(t, "novulkan", true)
	if _, err := Find([]string{"hi Alice"}); err != nil {
		t.Fatal(err)
	}
	if starts := fakeLines(t, log, "start"); !reflect.DeepEqual(starts, []string{"start pf-serve-vulkan MTL0", "start pf-serve CPU"}) {
		t.Fatalf("starts = %q", starts)
	}
}

// A GPU whose load hangs (lazyeo's M2: pf-serve kept loading for over a
// minute while every request was answered 504 after 2) is given
// gpuLoadTimeout, then the CPU is tried, and kept until the GPU is tried
// again (TestLostGPUTriedAgain). On macOS, whose pf-serve is Metal's, too: there it was the only
// one tried.
func TestHungGPULoadMovesToCPU(t *testing.T) {
	for _, vulkan := range []bool{false, true} {
		if !vulkan && runtime.GOOS != "darwin" {
			continue // pf-serve is the CPU's alone there
		}
		t.Run(fmt.Sprint("vulkan=", vulkan), func(t *testing.T) {
			log := fakeInstall(t, "gpuloadhang", vulkan)
			gpu := "start pf-serve MTL0"
			if vulkan {
				gpu = "start pf-serve-vulkan MTL0"
			}
			// The GPU's load is given up once the stand-in has started
			// on it, not after a duration a busy machine can outlast.
			was, wasGiveUp := gpuLoadTimeout, giveUp
			gpuLoadTimeout = 3 * time.Second
			done := make(chan struct{})
			giveUp = func(d time.Duration) <-chan time.Time {
				if d != gpuLoadTimeout {
					return wasGiveUp(d)
				}
				c := make(chan time.Time, 1)
				go func() {
					for {
						b, _ := os.ReadFile(log)
						if strings.Contains(string(b), gpu+"\n") {
							c <- time.Now()
							return
						}
						select {
						case <-done:
							return
						case <-time.After(10 * time.Millisecond):
						}
					}
				}()
				return c
			}
			t.Cleanup(func() { close(done); gpuLoadTimeout, giveUp = was, wasGiveUp })
			got := within10s(t, "a request in a hung GPU load", func() found {
				fs, err := Find([]string{"hi Alice"})
				return found{fs, err}
			})
			if got.err != nil || !reflect.DeepEqual(got.fs, [][]redact.Found{{person(3, 8)}}) {
				t.Fatalf("Find = %v, %v", got.fs, got.err)
			}
			if starts := fakeLines(t, log, "start"); !reflect.DeepEqual(starts, []string{gpu, "start pf-serve CPU"}) {
				t.Fatalf("starts = %q, want the GPU, then the CPU", starts)
			}
			if st := Now(); st.GPU || !strings.Contains(st.GPUError, "didn't load in 3s on the GPU") {
				t.Fatalf("Now = %+v, want on the CPU saying the GPU didn't load", st)
			}
			Stop()
			if _, err := Find([]string{"Alice again"}); err != nil {
				t.Fatal(err)
			}
			if starts := fakeLines(t, log, "start"); len(starts) != 3 || starts[2] != "start pf-serve CPU" {
				t.Fatalf("starts = %q, want the CPU straight away after", starts)
			}
		})
	}
}

// The GPU's load budget leaves room in a request's 2 minutes (the
// gateway's unroutedMost) for the CPU's load after it.
func TestGPULoadBudgetFitsARequest(t *testing.T) {
	if gpuLoadTimeout > time.Minute {
		t.Fatalf("gpuLoadTimeout = %s: a hung GPU load and the CPU's after it don't fit in a request's 2m", gpuLoadTimeout)
	}
}

// pf-serve that magpie ended for taking too long says so, not ggml's
// startup lines, which were all lazyeo's Settings showed ("pf-serve
// ended: ggml_metal_device_init: tensor API disabled for pre-M5 …").
func TestEndedForSlownessSaysSo(t *testing.T) {
	fakeInstall(t, "hang", false)
	was := classifyTimeout
	classifyTimeout = func(int) time.Duration { return 300 * time.Millisecond }
	t.Cleanup(func() { classifyTimeout = was })
	_, err := Find([]string{"hi Alice"})
	if err == nil || !strings.Contains(err.Error(), "didn't answer in 300ms (1 KB") || !strings.Contains(err.Error(), "magpie ended it") {
		t.Fatalf("err = %v, want it said magpie ended it for not answering", err)
	}
	if strings.Contains(err.Error(), "ggml_metal") || strings.Contains(Now().Failed, "ggml_metal") {
		t.Fatalf("err = %v, want no ggml startup lines", err)
	}
}

// One that dies says how it exited and its last words, past the startup.
func TestDeathSaysExitAndLastWords(t *testing.T) {
	fakeInstall(t, "asserts", false)
	_, err := Find([]string{"hi Alice"})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "GGML_ASSERT(ne00 % 4 == 0) failed") {
		t.Fatalf("err = %v, want the exit status and the assert", err)
	}
	if strings.Contains(err.Error(), "tensor API") || strings.Contains(err.Error(), "compiling pipeline") {
		t.Fatalf("err = %v, want no ggml startup lines", err)
	}
}

func TestLastSaid(t *testing.T) {
	for _, x := range []struct{ in, want string }{
		{"ggml_metal_device_init: tensor API disabled for pre-M5 and pre-A19 devices\nggml_metal_library_init: loaded in 0.323 sec\n", ""},
		{"ggml_metal_init: allocating\nggml_metal_device_init: failed to allocate buffer\n", "ggml_metal_device_init: failed to allocate buffer"},
		{"ggml_vulkan: Found 1 Vulkan devices:\nggml_metal: device lost\n", "ggml_metal: device lost"},
		{"a\nb\nc\nd\n", "b | c | d"},
	} {
		if got := lastSaid(x.in, 3); got != x.want {
			t.Errorf("lastSaid(%q) = %q, want %q", x.in, got, x.want)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	for n, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 5: 8 * time.Minute, 6: 10 * time.Minute, 40: 10 * time.Minute} {
		if got := retryAfter(n); got != want {
			t.Errorf("retryAfter(%d) = %s, want %s", n, got, want)
		}
	}
}

// A model that failed holds the requests that need it back at once until
// it is tried again, the ones queued behind the failing one too: each
// tried it again before, a load and a timeout apiece, and every request
// waited out its 2 minutes for a 504. They are still held back, never
// sent masked by the rules alone. Stop (the model turned off and on)
// tries it at once.
func TestFailedModelHoldsRequestsBackAtOnce(t *testing.T) {
	log := fakeInstall(t, "hang", false)
	was := classifyTimeout
	classifyTimeout = func(int) time.Duration { return 300 * time.Millisecond }
	t.Cleanup(func() { classifyTimeout = was })
	first := findAsync(context.Background(), "hi Alice")
	waitForStart(t, log)
	waitFor(t, "the first request reaching pf-serve", func() bool { return len(fakeLines(t, log, "request")) == 1 })
	queued := findAsync(context.Background(), "another Alice")
	if r := within10s(t, "the first request", func() found { return <-first }); r.err == nil {
		t.Fatal("the first request was answered by a hung pf-serve")
	}
	r := within10s(t, "the queued request", func() found { return <-queued })
	if r.err == nil || !strings.Contains(r.err.Error(), "tried again in") || !strings.Contains(r.err.Error(), "didn't answer") {
		t.Fatalf("the queued request: %v, want held back with the model's failure", r.err)
	}
	if _, err := Find([]string{"a third Alice"}); err == nil || !strings.Contains(err.Error(), "tried again in") {
		t.Fatalf("a later request: %v, want held back", err)
	}
	if n := len(fakeLines(t, log, "start")); n != 2 {
		t.Fatalf("started %d times, want 2: the first request's try and its one again", n)
	}
	srv.mu.Lock()
	srv.retryAt = time.Now().Add(-time.Second)
	srv.mu.Unlock()
	if _, err := Find([]string{"a fourth Alice"}); err == nil || strings.Contains(err.Error(), "tried again in") {
		t.Fatalf("once it's due: %v, want it tried and failing again", err)
	}
	if n := len(fakeLines(t, log, "start")); n != 4 {
		t.Fatalf("started %d times, want 4 once it was due", n)
	}
	srv.mu.Lock()
	again := srv.retryAt.Sub(time.Now())
	srv.mu.Unlock()
	if again < 40*time.Second {
		t.Fatalf("tried again in %s after the second failure, want a minute", again)
	}
	Stop()
	if err := heldBack(); err != nil {
		t.Fatalf("after Stop: %v, want tried at once", err)
	}
}

// Every pf-serve starts with ggml's Metal residency heartbeat off: it
// woke an idle one ~146 times a second (#3185). The CPU one after a GPU
// death too.
func TestServeStartsWithoutResidencyHeartbeat(t *testing.T) {
	log, _ := gpuInstall(t, "gpudies")
	if _, err := Find([]string{"hi Alice"}); err != nil {
		t.Fatal(err)
	}
	got := fakeLines(t, log, "residency")
	if len(got) != 2 {
		t.Fatalf("residency lines = %q, want a GPU and a CPU start", got)
	}
	for _, l := range got {
		if l != "residency 1" {
			t.Fatalf("residency lines = %q, want GGML_METAL_NO_RESIDENCY=1 on each", got)
		}
	}
}

// A pf-serve that doesn't answer is killed, and the request fails rather
// than wait for good.
func TestHungServeIsKilled(t *testing.T) {
	log := fakeInstall(t, "hang", false)
	was := classifyTimeout
	classifyTimeout = func(int) time.Duration { return 300 * time.Millisecond }
	t.Cleanup(func() { classifyTimeout = was })
	_, err := Find([]string{"hi Alice"})
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("err = %v, want the model failed", err)
	}
	if n := len(fakeLines(t, log, "start")); n != 2 {
		t.Fatalf("started %d times, want 2 (one try again)", n)
	}
	if Now().Running {
		t.Fatal("a hung pf-serve is still taken for running")
	}
}

func TestServeThatCantLoadSaysWhy(t *testing.T) {
	fakeInstall(t, "noload", false)
	_, err := Find([]string{"hi Alice"})
	if err == nil || !strings.Contains(err.Error(), "can't load noload") {
		t.Fatalf("err = %v", err)
	}
}

// A model in a folder whose name isn't ASCII loads: on Windows pf-serve's
// argv is in the ANSI code page and its fopen reads UTF-8, so a Chinese
// user name in C:\Users failed every load, on the GPU and the CPU alike,
// with the path in mojibake (AIMetaPhD on X). pf-serve gets the model's
// name, run in its folder; the stand-in refuses a byte outside ASCII as
// pf-serve does.
func TestModelInFolderNotASCIILoads(t *testing.T) {
	d := filepath.Join(t.TempDir(), "用户 Ünïcode", ".cache", "privacy-filter")
	log := fakeInstallAt(t, "ok", true, d)
	got, err := Find([]string{"hi Alice"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got[0]) != 1 {
		t.Fatalf("found %v", got)
	}
	if m := fakeLines(t, log, "model "); len(m) == 0 || m[0] != "model "+modelName() {
		t.Fatalf("pf-serve was given %q, want the model's name", m)
	}
}

// pf-serve's error names the model by the name it was given; the user
// reads the whole path, in UTF-8.
func TestModelErrorNamesWholePath(t *testing.T) {
	d := filepath.Join(t.TempDir(), "用户")
	fakeInstallAt(t, "badmodel", false, d)
	_, err := Find([]string{"hi Alice"})
	if err == nil || !strings.Contains(err.Error(), "failed to read GGUF: "+modelPath()) {
		t.Fatalf("err = %v, want %s in it", err, modelPath())
	}
}

// Warm starts pf-serve before a request asks, and one that can't start
// says why in Now; a request that is answered clears it.
func TestWarm(t *testing.T) {
	wait := func(what string, ok func(Status) bool) {
		for deadline := time.Now().Add(10 * time.Second); !ok(Now()); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %+v", what, Now())
			}
		}
	}
	fakeInstall(t, "noload", false)
	Warm()
	wait("no failure", func(s Status) bool { return strings.Contains(s.Failed, "can't load noload") })

	log, _ := gpuInstall(t, "ok")
	srv.failed = "before"
	Warm()
	wait("not running", func(s Status) bool { return s.Running && s.Device == "MTL0" && s.Failed == "" })
	if n := len(fakeLines(t, log, "start")); n != 1 {
		t.Fatalf("started %d times, want 1", n)
	}
}

func TestNotInstalled(t *testing.T) {
	fakeInstall(t, "ok", false)
	modelSize = 5 // the file on disk isn't the model's size
	if _, err := Find([]string{"hi Alice"}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v, want ErrNotInstalled", err)
	}
}

// pf-serve with no request for idleAfter is ended, and started again by
// the next one.
func TestIdleServeEnds(t *testing.T) {
	log := fakeInstall(t, "ok", false)
	idleAfter = 50 * time.Millisecond
	if _, err := Find([]string{"hi Alice"}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); Now().Running; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("pf-serve still runs")
		}
	}
	if _, err := Find([]string{"Alice, later"}); err != nil {
		t.Fatal(err)
	}
	if n := len(fakeLines(t, log, "start")); n != 2 {
		t.Fatalf("started %d times, want 2", n)
	}
}

func TestCacheKeepsTheLatest(t *testing.T) {
	c := newLRU(2)
	k := func(b byte) [32]byte { return [32]byte{b} }
	c.put(k(1), []redact.Found{person(0, 1)})
	c.put(k(2), nil)
	c.get(k(1))
	c.put(k(3), nil)
	if _, ok := c.get(k(2)); ok {
		t.Fatal("the least recent entry was kept")
	}
	if fs, ok := c.get(k(1)); !ok || len(fs) != 1 {
		t.Fatal("an entry read lately was dropped")
	}
}

// Every pf-serve keeps the engine's own MoE chunk, the window (4096):
// on an RX 470 through Vulkan 4096 was 6-9% faster than 2048 (#3682), on
// Metal 2048 was ~6% slower. A PF_MOE_CHUNK the user set is theirs, on
// every one.
func TestServeKeepsMoEChunkUnlessUserSets(t *testing.T) {
	cases := []struct {
		name   string
		vulkan bool
		user   string // "" unset
		want   []string
	}{
		{"vulkan", true, "", []string{"moechunk pf-serve-vulkan ", "moechunk pf-serve "}},
		{"vulkan, user's own", true, "3072", []string{"moechunk pf-serve-vulkan 3072", "moechunk pf-serve 3072"}},
	}
	if runtime.GOOS == "darwin" {
		cases = append(cases, struct {
			name   string
			vulkan bool
			user   string
			want   []string
		}{"metal", false, "", []string{"moechunk pf-serve ", "moechunk pf-serve "}})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("PF_MOE_CHUNK", c.user)
			if c.user == "" {
				os.Unsetenv("PF_MOE_CHUNK")
			}
			log := fakeInstall(t, "gpudies", c.vulkan)
			if _, err := Find([]string{"hi Alice"}); err != nil {
				t.Fatal(err)
			}
			if got := fakeLines(t, log, "moechunk"); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("moechunk lines = %q, want %q", got, c.want)
			}
		})
	}
}

// A request pf-serve takes too long over on the GPU (#3682: 480 KB on an
// RX 470/580, killed at classifyTimeout) is tried again on the GPU, not
// moved to a CPU 3x slower for the rest of the run.
func TestGPUTimeoutStaysOnGPU(t *testing.T) {
	log, gpu := gpuInstall(t, "gpuhangonce")
	was := classifyTimeout
	classifyTimeout = func(int) time.Duration { return 300 * time.Millisecond }
	t.Cleanup(func() { classifyTimeout = was })
	got, err := Find([]string{"hi Alice"})
	if err != nil || !reflect.DeepEqual(got, [][]redact.Found{{person(3, 8)}}) {
		t.Fatalf("Find = %v, %v", got, err)
	}
	if starts := fakeLines(t, log, "start"); !reflect.DeepEqual(starts, []string{gpu, gpu}) {
		t.Fatalf("starts = %q, want the GPU tried again after its timeout", starts)
	}
	if st := Now(); !st.GPU || st.GPUError != "" {
		t.Fatalf("Now = %+v, want on the GPU with nothing lost", st)
	}
	if _, err := Find([]string{"Alice again"}); err != nil {
		t.Fatal(err)
	}
	if starts := fakeLines(t, log, "start"); len(starts) != 2 {
		t.Fatalf("starts = %q, want the GPU's pf-serve kept", starts)
	}
}

// A GPU that hangs twice in a row is a GPU lost: the next request goes
// to the CPU, as for one that dies. Until the GPU is tried again.
func TestGPUHungTwiceMovesToCPU(t *testing.T) {
	log, gpu := gpuInstall(t, "gpuhang")
	was := classifyTimeout
	classifyTimeout = func(int) time.Duration { return 300 * time.Millisecond }
	t.Cleanup(func() { classifyTimeout = was })
	if _, err := Find([]string{"hi Alice"}); err == nil {
		t.Fatal("a GPU that hangs twice answered")
	}
	if starts := fakeLines(t, log, "start"); !reflect.DeepEqual(starts, []string{gpu, gpu}) {
		t.Fatalf("starts = %q, want the GPU twice", starts)
	}
	srv.mu.Lock()
	srv.retryAt = time.Time{} // not held back: tried at once
	srv.mu.Unlock()
	got, err := Find([]string{"hi Alice"})
	if err != nil || !reflect.DeepEqual(got, [][]redact.Found{{person(3, 8)}}) {
		t.Fatalf("Find = %v, %v", got, err)
	}
	if starts := fakeLines(t, log, "start"); len(starts) != 3 || starts[2] != "start pf-serve CPU" {
		t.Fatalf("starts = %q, want the CPU next", starts)
	}
	if st := Now(); st.GPU || !strings.Contains(st.GPUError, "didn't answer in") {
		t.Fatalf("Now = %+v, want on the CPU saying the GPU didn't answer", st)
	}
}

// A GPU lost is tried again when its time comes (gpuRetryAfter), with
// no restart of magpie: the CPU's pf-serve makes way for the GPU's, and a
// GPU that answers is the model's again.
func TestLostGPUTriedAgain(t *testing.T) {
	log, gpu := gpuInstall(t, "gpudiesonce")
	if _, err := Find([]string{"hi Alice"}); err != nil {
		t.Fatal(err)
	}
	if starts := fakeLines(t, log, "start"); !reflect.DeepEqual(starts, []string{gpu, "start pf-serve CPU"}) {
		t.Fatalf("starts = %q, want the GPU, then the CPU", starts)
	}
	if _, err := Find([]string{"Alice two"}); err != nil {
		t.Fatal(err)
	}
	if starts := fakeLines(t, log, "start"); len(starts) != 2 {
		t.Fatalf("starts = %q, want the CPU kept until the GPU's time", starts)
	}
	srv.mu.Lock()
	if !srv.cpu || srv.gpuFails != 1 || time.Until(srv.gpuAgain) < 9*time.Minute {
		t.Fatalf("cpu=%v fails=%d again in %s, want the CPU for 10 minutes", srv.cpu, srv.gpuFails, time.Until(srv.gpuAgain))
	}
	srv.gpuAgain = time.Now() // its time has come
	srv.mu.Unlock()
	got, err := Find([]string{"Alice three"})
	if err != nil || !reflect.DeepEqual(got, [][]redact.Found{{person(0, 5)}}) {
		t.Fatalf("Find = %v, %v", got, err)
	}
	if starts := fakeLines(t, log, "start"); len(starts) != 3 || starts[2] != gpu {
		t.Fatalf("starts = %q, want the GPU tried again", starts)
	}
	if st := Now(); !st.GPU || st.GPUError != "" {
		t.Fatalf("Now = %+v, want the GPU's again", st)
	}
	srv.mu.Lock()
	cpu, fails := srv.cpu, srv.gpuFails
	srv.mu.Unlock()
	if cpu || fails != 0 {
		t.Fatalf("cpu=%v fails=%d after the GPU answered", cpu, fails)
	}
}

func TestGPURetryAfter(t *testing.T) {
	for n, want := range map[int]time.Duration{1: 10 * time.Minute, 2: 20 * time.Minute, 3: 40 * time.Minute, 4: 80 * time.Minute, 5: 2 * time.Hour, 50: 2 * time.Hour} {
		if got := gpuRetryAfter(n); got != want {
			t.Errorf("gpuRetryAfter(%d) = %s, want %s", n, got, want)
		}
	}
}
