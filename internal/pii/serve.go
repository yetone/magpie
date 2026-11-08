package pii

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/redact"
)

// hello is the line pf-serve writes once the model is loaded.
type hello struct {
	Device   string `json:"device"`
	GPU      bool   `json:"gpu"`
	MS       int    `json:"ms"`
	GPUError string `json:"gpuError"`
	Error    string `json:"error"`
}

// helper is one pf-serve running.
type helper struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
	// outf is out's end of the stdout pipe. It is magpie's, not
	// cmd's: cmd.Wait closes a StdoutPipe as the process exits, so a
	// read after a death got "read |0: file already closed" in place of
	// the EOF that says pf-serve died, and the status showed that as why
	// the GPU was lost (#3741). Closed by kill and end.
	outf   *os.File
	stderr *tail
	hello  hello
	exited chan struct{}
	// slow says magpie ended it, given too long to answer
	slow atomic.Pointer[string]
}

// srv is the pf-serve magpie runs, one at a time. Its requests take
// turns on its one pipe: turn is held for each, from start to answer, and
// a request's caller can stop waiting for it (FindContext). mu guards
// only the fields below, and is never held while pf-serve loads or
// answers: the status (Now), Stop and the idle timer took it, and on a
// cold start every request and the Settings page waited minutes behind
// one load (#3682).
var srv struct {
	mu   sync.Mutex
	h    *helper
	idle *time.Timer
	// cpu is set once pf-serve died on the GPU, didn't load there, or
	// hung there twice in a row: it runs on the CPU until gpuAgain, when
	// the GPU is tried again (gpuRetryAfter); gpuLost says why, gpuFails
	// how many times in a row the GPU was lost. A GPU that answers clears
	// them. Before, nothing did short of a restart of magpie, and one
	// request too slow for its time on a weak GPU left every request after
	// it on a CPU 3x slower (#3682).
	cpu      bool
	gpuLost  string
	gpuFails int
	gpuAgain time.Time
	// failed is why the last request got no answer, until one does;
	// lastErr is that error, fails how many requests in a row got none,
	// and retryAt when the model is tried again: until then a request
	// that needs it is held back at once (heldBack) rather than wait
	// minutes for a load that failed the time before
	failed  string
	lastErr error
	fails   int
	retryAt time.Time
	// loading is set while pf-serve loads the model
	loading bool
	// stop is closed by Stop: a load under way gives up, and the request
	// it was for fails rather than start pf-serve again
	stop chan struct{}
}

// turn is pf-serve's pipe, held by the one request on it.
var turn = make(chan struct{}, 1)

// waiting counts the requests waiting for their turn.
var waiting atomic.Int32

// stopCh is srv.stop, made the first time; mu is held.
func stopCh() chan struct{} {
	if srv.stop == nil {
		srv.stop = make(chan struct{})
	}
	return srv.stop
}

// loadTimeout is how long pf-serve has to load the model on the CPU:
// 1.6 GB read from a slow disk, and the warm-up's texts read on a slow
// CPU (20s on an M4 Pro's).
var loadTimeout = 3 * time.Minute

// gpuLoadTimeout is how long it has on a GPU, where it takes seconds
// (0.9-10s on an M4 Pro): one that takes longer is taken for hung, and
// the CPU is tried, until the GPU is tried again. With 3 minutes for
// either, every request in a hung load waited out the gateway's 2
// minutes and was answered 504; the gateway no longer counts masking's
// time (#3682), and 40s still keeps a hung GPU's wait short.
var gpuLoadTimeout = 40 * time.Second

// giveUp is when a load is given up on; a test ends one when its state
// says so rather than after a duration.
var giveUp = time.After

// retryAfter is how long after the model failed a request that needs it
// is held back at once, its n-th failure in a row: 30s, doubled each
// time, 10 minutes at most. A model that fails on each try (a GPU's
// driver that hangs, a CPU too slow for the requests) held every
// request for its 2 minutes before, and each one tried it again.
func retryAfter(n int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < n && d < 10*time.Minute; i++ {
		d *= 2
	}
	return min(d, 10*time.Minute)
}

// gpuRetryAfter is how long after the GPU was lost, the n-th time in a
// row, the GPU is tried again: 10 minutes, doubled each time, 2 hours at
// most. A try costs a load (seconds; gpuLoadTimeout for one that hangs)
// and the request it is for goes on to the CPU's if it fails.
func gpuRetryAfter(n int) time.Duration {
	d := 10 * time.Minute
	for i := 1; i < n && d < 2*time.Hour; i++ {
		d *= 2
	}
	return min(d, 2*time.Hour)
}

// lostGPULocked moves the model to the CPU, why being what went wrong on
// the GPU, until the GPU is tried again; mu is held.
func lostGPULocked(why string) {
	srv.gpuFails++
	srv.cpu, srv.gpuLost = true, why
	srv.gpuAgain = time.Now().Add(gpuRetryAfter(srv.gpuFails))
}

// classifyTimeout is how long one request may take before pf-serve is
// taken for hung and ended: a second for each 4 KB on the slowest CPU,
// and 30 more.
var classifyTimeout = func(bytes int) time.Duration {
	return 30*time.Second + time.Duration(bytes/4096)*time.Second
}

// candidate is a program to start, and whether on the CPU.
type candidate struct {
	path string
	cpu  bool
}

// candidates are the programs to start, in turn: on a GPU,
// pf-serve-vulkan where there is one (it needs the Vulkan loader a GPU's
// driver installs, and can't start without it), and macOS's pf-serve,
// which runs on Metal; then pf-serve on the CPU, which runs anywhere.
// macOS's was tried on Metal alone: one whose load hung there, or ended,
// was tried again on Metal by every request (lazyeo, an M2).
// A CPU without what pf-serve is built for runs pf-serve-base, on the
// CPU alone: pf-serve-vulkan is built like pf-serve, and would end the
// same way.
func candidates(cpu bool) []candidate {
	if !fullX86() {
		if _, err := os.Stat(exe("pf-serve-base")); err == nil {
			return []candidate{{exe("pf-serve-base"), true}}
		}
	}
	var out []candidate
	if !cpu {
		if _, err := os.Stat(exe("pf-serve-vulkan")); err == nil {
			out = append(out, candidate{exe("pf-serve-vulkan"), false})
		} else if runtime.GOOS == "darwin" {
			out = append(out, candidate{exe("pf-serve"), false})
		}
	}
	return append(out, candidate{exe("pf-serve"), true})
}

// start runs pf-serve, the first of candidates that loads the model.
// stop ends a load under way.
func start(cpu bool, stop <-chan struct{}) (*helper, error) {
	if !Installed() {
		return nil, ErrNotInstalled
	}
	if !strings.HasSuffix(binDir(), "-"+target()) {
		// only the Intel pf-serve under Rosetta, which may not start at
		// all: Apple silicon's is fetched first, and one that can't be
		// leaves the Intel one to try
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		ensureServe(ctx)
		cancel()
	}
	upgrades.Add(1)
	go func() {
		defer upgrades.Done()
		upgrade()
	}()
	var errs []error
	gpuLost := ""
	for _, c := range candidates(cpu) {
		h, err := startOne(c.path, c.cpu, stop)
		if err == nil {
			if gpuLost != "" {
				// the GPU's failed and the CPU's loaded: the CPU until
				// the GPU is tried again, as for a GPU that dies
				// answering
				srv.mu.Lock()
				lostGPULocked(gpuLost)
				srv.mu.Unlock()
			}
			return h, nil
		}
		if errors.Is(err, errStopped) {
			return nil, err
		}
		if !c.cpu {
			gpuLost = err.Error()
		}
		errs = append(errs, err)
	}
	return nil, fmt.Errorf("the local masking model didn't start: %w", errors.Join(errs...))
}

// upgrades are the upgrade checks start set going, for a test to wait for.
var upgrades sync.WaitGroup

// errStopped is a request's when Stop ended pf-serve under it.
var errStopped = errors.New("the local masking model was stopped")

func startOne(path string, cpu bool, stop <-chan struct{}) (*helper, error) {
	// pf-serve is given the model's name, run in its folder, and never
	// the whole path: on Windows a C program's argv comes in the ANSI
	// code page, where ggml's fopen reads UTF-8 and the GPU's weights
	// are read again through a narrow ifstream, so a folder with a
	// character outside ASCII (a Chinese user name in C:\Users) failed
	// to load on every device, its error a path in mojibake. magpie's
	// own names are ASCII.
	cmd := proc.Command(path, modelName())
	cmd.Dir = filepath.Dir(modelPath())
	// Without GGML_METAL_NO_RESIDENCY, ggml's Metal residency set keeps a
	// heartbeat that woke an idle pf-serve ~146 times a second for the 10
	// minutes it stays up after a request, and macOS's Energy Impact
	// counted magpie among the top for it (#3185). With it set: 0.3 a
	// second, and the first request after an idle spell takes 0.1-0.45s
	// more (51-78ms hot either way).
	cmd.Env = append(os.Environ(), "GGML_METAL_NO_RESIDENCY=1")
	// PF_MOE_CHUNK is left at the engine's default (the window, 4096) on
	// every device: on an RX 470 through Vulkan, interleaved A/B in one
	// session read the same spans 6-9% faster at 4096 than at 2048 (#3682;
	// the 1.77x once measured for 2048 was the machine's own 2x swing
	// between runs hours apart), and on an M4 Pro's Metal 2048 was ~6%
	// slower. A PF_MOE_CHUNK the user set is inherited through Environ.
	if cpu {
		cmd.Env = append(cmd.Env, "PF_DEVICE=cpu")
	}
	wait := loadTimeout
	if !cpu {
		wait = gpuLoadTimeout
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, w, err := os.Pipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	cmd.Stdout = w
	h := &helper{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 64<<10), outf: out, stderr: &tail{}, exited: make(chan struct{})}
	cmd.Stderr = h.stderr
	err = cmd.Start()
	// pf-serve has its own copy: once it exits, a read here sees EOF
	w.Close()
	if err != nil {
		out.Close()
		return nil, err
	}
	go func() {
		cmd.Wait()
		close(h.exited)
	}()
	line := make(chan string, 1)
	go func() {
		s, _ := h.out.ReadString('\n')
		line <- s
	}()
	var s string
	select {
	case s = <-line:
	case <-giveUp(wait):
		h.kill()
		return nil, fmt.Errorf("%s: the model didn't load in %s%s", baseName(path), wait, onWhat(cpu))
	case <-stop:
		h.kill()
		return nil, errStopped
	}
	if err := json.Unmarshal([]byte(s), &h.hello); err != nil || h.hello.Error != "" || h.hello.Device == "" {
		h.kill()
		why := h.hello.Error
		if why == "" {
			why = h.died()
		}
		// the name it was given ("failed to read GGUF: <name>"), as the
		// path the user can find
		why = strings.ReplaceAll(why, " "+modelName(), " "+modelPath())
		return nil, fmt.Errorf("%s: %s", baseName(path), why)
	}
	return h, nil
}

func baseName(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	return path[strings.LastIndexByte(path, '/')+1:]
}

func onWhat(cpu bool) string {
	if cpu {
		return " on the CPU"
	}
	return " on the GPU"
}

// died says why pf-serve ended: that magpie ended it, given too long to
// answer, or how it exited; and what it said last that isn't ggml's
// startup lines. Those were all a user saw of a pf-serve magpie had
// ended for being slow (lazyeo: "pf-serve ended: ggml_metal_device_init:
// tensor API disabled for pre-M5 …", which every Mac before the M5
// prints and means nothing).
func (h *helper) died() string {
	exited := true
	select {
	case <-h.exited:
	case <-time.After(2 * time.Second):
		exited = false
	}
	var why []string
	if s := h.slow.Load(); s != nil {
		why = append(why, *s)
	} else if exited && h.cmd.ProcessState != nil {
		why = append(why, "ended ("+h.cmd.ProcessState.String()+")")
	} else {
		why = append(why, "ended")
	}
	if t := lastSaid(h.stderr.String(), 3); t != "" {
		why = append(why, t)
	}
	return strings.Join(why, ": ")
}

// startup are the lines ggml writes as it starts, every time, on a GPU
// and on the CPU alike: what the device is, the kernels it built.
var startup = regexp.MustCompile(`^(ggml_metal_(device_init|library_init|init|free|library_compile_pipeline)|ggml_vulkan|load_backend|register_backend|register_device):`)

// trouble is a line that says something went wrong, startup's or not.
var trouble = regexp.MustCompile(`(?i)error|fail|assert|abort|unable|cannot|can't|could not|out of memory|exception|lost`)

// deviceLost is a GPU the driver reset under pf-serve, as ggml's Vulkan
// says it (vk::DeviceLostError, VK_ERROR_DEVICE_LOST) and Metal's.
var deviceLost = regexp.MustCompile(`(?i)DeviceLost|device[ _]lost`)

// lastSaid is the last n lines of stderr that say more than ggml's
// startup, joined.
func lastSaid(stderr string, n int) string {
	var out []string
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || startup.MatchString(l) && !trouble.MatchString(l) {
			continue
		}
		out = append(out, l)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return strings.Join(out, " | ")
}

func (h *helper) kill() {
	h.in.Close()
	if h.cmd.Process != nil {
		h.cmd.Process.Kill()
	}
	<-h.exited
	h.outf.Close()
}

// Warm starts pf-serve in the background, where the model is on disk, so
// that the first request doesn't wait for it to load, and Settings shows
// where it runs.
func Warm() {
	if Installed() {
		go func() {
			turn <- struct{}{}
			defer func() { <-turn }()
			classifySafely(classify, []string{"warm"})
		}()
	}
}

// Stop ends pf-serve, if it runs, and a load under way, at once: it
// waits for no request's turn. A request on it fails, and pf-serve is
// killed rather than given the 5s an idle one has to end.
func Stop() {
	free := false
	select {
	case turn <- struct{}{}:
		free = true
		defer func() { <-turn }()
	default:
	}
	srv.mu.Lock()
	h := detachLocked()
	// turned on again, it is tried at once
	srv.lastErr, srv.fails, srv.retryAt = nil, 0, time.Time{}
	if srv.stop != nil {
		close(srv.stop)
		srv.stop = nil
	}
	srv.mu.Unlock()
	if h != nil && !free {
		h.kill()
		return
	}
	end(h)
}

// stopWhenFree ends pf-serve once no request is on it, so that the next
// one starts the pf-serve on disk now (upgrade).
func stopWhenFree() {
	turn <- struct{}{}
	defer func() { <-turn }()
	srv.mu.Lock()
	h := detachLocked()
	srv.mu.Unlock()
	end(h)
}

// detachLocked takes pf-serve out of srv, for end; mu is held.
func detachLocked() *helper {
	if srv.idle != nil {
		srv.idle.Stop()
		srv.idle = nil
	}
	h := srv.h
	srv.h = nil
	return h
}

// end ends h, with no lock held: stdin ending ends it, and one that
// doesn't is killed.
func end(h *helper) {
	if h == nil {
		return
	}
	h.in.Close()
	select {
	case <-h.exited:
		h.outf.Close()
	case <-time.After(5 * time.Second):
		h.kill()
	}
}

// classify asks pf-serve, started if it isn't, what is in texts. One that
// fails is ended and started again. One that failed on the GPU is started
// again on the GPU: one that died there is most often a GPU the driver
// reset under it (vk::DeviceLostError, kongbai01203's after an idle
// spell), which a new pf-serve gets a new device of, and one magpie ended
// for taking too long had a request too slow for its time. Twice in a row
// the GPU is lost, and the CPU is tried, until the GPU is tried again: at
// once for one that died, and from the next request for one that hung,
// whose request has waited twice already. The caller holds turn.
func classify(texts []string) ([][]redact.Found, error) {
	out, err := classifyTurn(texts)
	srv.mu.Lock()
	srv.failed, srv.lastErr = "", nil
	switch {
	case err == nil:
		srv.fails, srv.retryAt = 0, time.Time{}
	case errors.Is(err, errStopped):
		// turned off, not failed
		srv.failed = err.Error()
	default:
		srv.failed, srv.lastErr = err.Error(), err
		srv.fails++
		srv.retryAt = time.Now().Add(retryAfter(srv.fails))
	}
	srv.mu.Unlock()
	return out, err
}

// heldBack is the error of a request that needs the model while it isn't
// tried again yet, after it failed; nil when it may be tried.
func heldBack() error {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.lastErr == nil || !time.Now().Before(srv.retryAt) {
		return nil
	}
	return fmt.Errorf("%w; it is tried again in %s, and until then a request it hasn't read is held back at once. To send requests masked by the rules alone, turn the model off in Settings → Privacy",
		srv.lastErr, time.Until(srv.retryAt).Round(time.Second))
}

func classifyTurn(texts []string) ([][]redact.Found, error) {
	var last error
	for try := 0; try < 3; try++ {
		srv.mu.Lock()
		h, cpu, stop := srv.h, srv.cpu, stopCh()
		var gone *helper
		if cpu && !time.Now().Before(srv.gpuAgain) {
			// the GPU is tried again: a pf-serve on the CPU makes way.
			// One that fails there moves back to the CPU (start, or
			// below), with longer until the next try.
			cpu = false
			if h != nil && !h.hello.GPU {
				gone, h = detachLocked(), nil
			}
		}
		if h == nil {
			srv.loading = true
		}
		srv.mu.Unlock()
		end(gone)
		if h == nil {
			var err error
			h, err = start(cpu, stop)
			srv.mu.Lock()
			srv.loading = false
			if err == nil && stopped(stop) {
				err = errStopped
			}
			if err != nil {
				srv.mu.Unlock()
				if h != nil {
					h.kill()
				}
				return nil, err
			}
			srv.h = h
			srv.mu.Unlock()
		}
		out, err := h.classify(texts)
		srv.mu.Lock()
		if err == nil {
			if srv.h == h {
				idleLocked(h)
			}
			if h.hello.GPU {
				// the GPU answers: it is the model's again
				srv.cpu, srv.gpuLost, srv.gpuFails = false, "", 0
			}
			srv.mu.Unlock()
			return out, nil
		}
		last = err
		if srv.h == h {
			srv.h = nil
		}
		if stopped(stop) {
			srv.mu.Unlock()
			h.kill()
			return nil, errStopped
		}
		hung := h.slow.Load() != nil
		if h.hello.GPU && try > 0 {
			lostGPULocked(err.Error())
		}
		// else the try again is on the GPU: a GPU the driver reset
		// (device lost), a crash, or a hang that a new pf-serve gets
		// past, is no lost GPU yet
		srv.mu.Unlock()
		h.kill()
		if try == 1 && (!h.hello.GPU || hung) {
			// a third try only after the GPU died twice, on the CPU
			break
		}
	}
	return nil, fmt.Errorf("the local masking model failed: %w", last)
}

func stopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// idleLocked ends h after idleAfter with no request; mu is held. The timer
// takes the turn only when it is free: a request on it sets the timer
// again when it is answered.
func idleLocked(h *helper) {
	if srv.idle != nil {
		srv.idle.Stop()
	}
	srv.idle = time.AfterFunc(idleAfter, func() {
		select {
		case turn <- struct{}{}:
		default:
			return
		}
		defer func() { <-turn }()
		srv.mu.Lock()
		var gone *helper
		if srv.h == h {
			gone = detachLocked()
		}
		srv.mu.Unlock()
		end(gone)
	})
}

// classify is one request: n, then each text's length and bytes; the
// reply is a status, then each text's spans (pf-serve.cpp says how).
func (h *helper) classify(texts []string) ([][]redact.Found, error) {
	var b bytes.Buffer
	size := 0
	put := func(v uint32) { binary.Write(&b, binary.LittleEndian, v) }
	put(uint32(len(texts)))
	for _, t := range texts {
		put(uint32(len(t)))
		b.WriteString(t)
		size += len(t)
	}
	done, timeout := make(chan struct{}), classifyTimeout(size)
	defer close(done)
	go func() {
		select {
		case <-done:
		case <-time.After(timeout):
			// a hung pf-serve: killed, its pipe ends the read below
			s := fmt.Sprintf("didn't answer in %s (%d KB%s), so magpie ended it", timeout, (size+1023)/1024, onWhat(!h.hello.GPU))
			h.slow.Store(&s)
			h.cmd.Process.Kill()
		}
	}()
	if _, err := h.in.Write(b.Bytes()); err != nil {
		return nil, fmt.Errorf("pf-serve %s (%v)", h.died(), err)
	}
	beforeRead(h)
	r := &reader{r: h.out}
	status := r.u32()
	if status == 1 {
		msg := r.bytes(int(r.u32()))
		if r.err != nil {
			return nil, r.fail(h)
		}
		return nil, errors.New(string(msg))
	}
	out := make([][]redact.Found, len(texts))
	for i, t := range texts {
		n := int(r.u32())
		if r.err == nil && n > len(t)+1 {
			r.err = fmt.Errorf("%d spans in %d bytes", n, len(t))
		}
		for j := 0; j < n && r.err == nil; j++ {
			start, end := int32(r.u32()), int32(r.u32())
			// pf-serve sends only those over its default threshold, 0.5: Floor
			score := math.Float32frombits(r.u32())
			label := string(r.bytes(int(r.u8())))
			out[i] = append(out[i], redact.Found{Start: int(start), End: int(end), Label: label, Score: float64(score)})
		}
	}
	if status != 0 && r.err == nil {
		r.err = fmt.Errorf("status %d", status)
	}
	if r.err != nil {
		return nil, r.fail(h)
	}
	return out, nil
}

// beforeRead runs between sending a request and reading its answer, for
// a test to read after pf-serve has exited (TestGPUDeathReadAfterExit).
var beforeRead = func(*helper) {}

type reader struct {
	r   *bufio.Reader
	err error
	b   [4]byte
}

func (r *reader) u32() uint32 {
	if r.err == nil {
		_, r.err = io.ReadFull(r.r, r.b[:4])
	}
	if r.err != nil {
		return 0
	}
	return binary.LittleEndian.Uint32(r.b[:4])
}

func (r *reader) u8() byte {
	if r.err == nil {
		var c byte
		c, r.err = r.r.ReadByte()
		return c
	}
	return 0
}

func (r *reader) bytes(n int) []byte {
	if r.err != nil {
		return nil
	}
	b := make([]byte, n)
	_, r.err = io.ReadFull(r.r, b)
	return b
}

func (r *reader) fail(h *helper) error {
	if errors.Is(r.err, io.EOF) || errors.Is(r.err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("pf-serve %s", h.died())
	}
	return r.err
}

// tail keeps the last 4 KB written to it.
type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
