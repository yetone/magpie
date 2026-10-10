package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHostJSCancellation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	for _, name := range []string{"stopped-gate", "split-chunk", "terminal", "blocked-head", "blocked-head-cancel-error", "ready-wait", "ready-success", "ready-error", "local-error", "local-error-cancel", "normal-reply", "held-window"} {
		t.Run(name, func(t *testing.T) {
			out, err := runHostJSCase(t, node, name)
			if err != nil {
				t.Fatalf("JavaScript cancellation regression: %v\n%s", err, out)
			}
			if len(out) > 0 {
				t.Logf("%s", out)
			}
		})
	}
}

// runHostJSCase runs one case of hostJSCancellationTest in node against the
// embedded host.js. A case that hangs is stopped after two minutes, or before
// this run's own deadline if that comes first, rather than at a fixed 10s a
// loaded machine can spend starting node. The cap keeps a real hang from
// using up most of a 10m -timeout, and from hanging a -timeout 0 run.
func runHostJSCase(t *testing.T, node, name string) ([]byte, error) {
	limit := 2 * time.Minute
	if d, ok := t.Deadline(); ok {
		limit = min(limit, time.Until(d)*9/10)
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	// ready-wait forces a garbage collection (--expose-gc). A function V8 is
	// optimizing on a worker thread is held until a worker is done with it,
	// and with it the request it was made for, so V8 optimizes on the main
	// thread (--no-concurrent-recompilation).
	cmd := exec.CommandContext(ctx, node, "--expose-gc", "--no-concurrent-recompilation", "-e", hostJSCancellationTest, name)
	cmd.Stdin = bytes.NewReader(hostJS)
	return cmd.CombinedOutput()
}

const hostJSCancellationTest = `
const assert = require('node:assert/strict')
const fs = require('node:fs')
const vm = require('node:vm')
const { EventEmitter } = require('node:events')
const source = fs.readFileSync(0, 'utf8')
const section = (from, to) => {
  const start = source.indexOf(from)
  const end = to ? source.indexOf(to, start) : source.length
  assert.ok(start >= 0 && end > start, 'production section exists')
  return source.slice(start, end)
}
const stdout = new EventEmitter()
const lines = []
let blockOn = ''
stdout.write = line => {
  const msg = JSON.parse(line)
  lines.push(msg)
  return blockOn !== 'all' && !(blockOn === 'chunk' && msg.event === 'chunk')
}
const rl = new EventEmitter()
const context = vm.createContext({
  Buffer, Headers, AbortController, DOMException, ReadableStream,
  process: { stdout, stdin: {}, exit: () => { throw Error('unexpected exit') } },
  readline: { createInterface: () => rl },
  options: async () => ({ fetch: async () => context.reply }),
  info: async () => ({ models: {} }),
  sdkHeaders: () => ({}), bodyOf: () => undefined,
  accountKey: () => '',
  via: { run: (_, fn) => fn() }, inScope: (_, key, fn) => fn(),
})
const run = code => vm.runInContext(code, context)
run(section('const rpcWrite =', '// A plugin writing to stdout'))
run('const hooks = []; const inflight = new Map(); const handlers = {}')
run(section('async function doFetch(', '// ---- the loop'))
run(section('// Everything waits for init;'))
const message = value => rl.emit('line', JSON.stringify(value))
const tick = () => new Promise(resolve => setImmediate(resolve))
const until = async (check, label) => {
  for (let iteration = 0; iteration < 30; iteration++) {
    if (check()) return
    await tick()
  }
  assert.ok(check(), label)
}
const idle = () => run('pending === 0 && inflight.size === 0')
const queued = () => run('outq.length')
const fetchOne = () => message({ id: 1, method: 'fetch', params: { window: 512 << 10, provider: 'test', model: 'test', url: 'http://localhost/' } })
const abortOne = () => message({ method: 'abort', params: { id: 1 } })
const initialize = promise => {
  context.initialization = promise
  run('typeof setReady === "function" ? setReady(initialization) : ready = initialization')
}
async function main() {
  const name = process.argv[1]
  initialize(Promise.resolve())
  await tick()
  context.reply = { status: 200, headers: new Headers(), body: null }
  if (name === 'stopped-gate') {
    run('globalThis.gate = makeGate(512 << 10)')
    assert.equal(await run('gate.take(64)'), 64)
    run('gate.stop(); gate.add(1)')
    assert.equal(await run('gate.take(64)'), -1, 'stopped gate refuses remaining credit')
  } else if (name === 'split-chunk') {
    blockOn = 'chunk'
    context.reply.body = (async function* () { yield Buffer.alloc(3 * (64 << 10)) })()
    fetchOne()
    await until(() => queued() === 1 && run('outq[0].line.includes("chunk")'), 'second slice queued while credit remains')
    abortOne()
    await until(idle, 'cancelled split-chunk fetch cleans registries and pending count')
    assert.equal(queued(), 0, 'no late chunk or terminal queued')
    assert.equal(lines.filter(msg => msg.event === 'chunk').length, 1)
    assert.equal(lines.filter(msg => msg.id === 1 && ('result' in msg || 'error' in msg)).length, 0)
  } else if (name === 'terminal') {
    blockOn = 'all'
    fetchOne()
    await until(() => queued() === 1, 'terminal queued behind head awaiting drain')
    abortOne()
    await until(idle, 'cancelled terminal wait cleans registries')
    assert.equal(queued(), 0, 'cancelled terminal removed')
  } else if (name === 'blocked-head' || name === 'blocked-head-cancel-error') {
    blockOn = 'all'
    run('send({ id: 99, result: null })')
    let bodyCancelCalls = 0
    context.reply.body = new ReadableStream({ cancel() {
      bodyCancelCalls++
      if (name === 'blocked-head-cancel-error') throw Error('body cancellation failed')
    } })
    fetchOne()
    await until(() => queued() === 1 && run('outq[0].line.includes("head")'), 'head queued behind blocked stdout')
    abortOne()
    await until(idle, 'cancelled blocked head cleans registries')
    console.log(JSON.stringify({ idle: idle(), queued: queued(), bodyCancelCalls, bodyLocked: context.reply.body.locked }))
    assert.equal(bodyCancelCalls, 1, 'acquired body cancelled before iteration')
    assert.equal(queued(), 0, 'cancelled head removed')
    assert.equal(context.reply.body.locked, false)
    assert.equal(lines.length, 1, 'body cancellation emits no duplicate error')
  } else if (name === 'ready-wait') {
    blockOn = 'all'
    let readyReactions = 0
    const unresolved = new Promise(() => {})
    const originalThen = unresolved.then.bind(unresolved)
    unresolved.then = (...args) => { readyReactions++; return originalThen(...args) }
    initialize(unresolved)
    run('send({id: 99, result: null})')
    const counts = emitter => Object.fromEntries(emitter.eventNames().map(event => [event, emitter.listenerCount(event)]))
    const listeners = () => ({ stdin: counts(rl), stdout: counts(stdout) })
    const sharedListeners = listeners()
    // A cancelled wait lets its request, controller, signal and credit gate
    // go. watch() reads them itself, so none stays in a variable of main,
    // which main keeps while it waits.
    const requests = []
    const watch = () => {
      const request = run('inflight.get(1)')
      assert.ok(request, 'a fetch waiting for init is in flight')
      requests.push([request, request.controller, request.controller.signal, request.gate].map(part => new WeakRef(part)))
    }
    for (let iteration = 0; iteration < 1000; iteration++) {
      fetchOne()
      watch()
      abortOne()
      await until(idle, 'cancelled bootstrap wait cleans registries')
      assert.equal(queued(), 0, 'cancelled bootstrap emits no late error')
    }
    assert.equal(lines.length, 1, 'stdout stayed blocked at original line')
    assert.equal(stdout.listenerCount('drain'), 1, 'only shared drain listener remains')
    assert.deepEqual(listeners(), sharedListeners, 'cancelled bootstrap waits leave no listener on stdin or stdout')
    const retainedWaiters = run('typeof readyWaiters === "undefined" ? -1 : readyWaiters.size')
    const retainedReadyReactions = readyReactions - (run('typeof setReady') === 'function' ? 1 : 0)
    // Waiters are counted before the collection: a FinalizationRegistry could
    // tidy them up after it. A WeakRef keeps its target until the turn that
    // made it ends, and what runs once a request is collected (a registry's
    // callback) runs on a later turn.
    await tick()
    gc()
    await tick()
    const retainedRequests = requests.filter(parts => parts.some(part => part.deref() !== undefined)).length
    console.log(JSON.stringify({ cancellations: 1000, idle: idle(), queued: queued(), readyReactions, retainedReadyReactions, retainedWaiters, retainedRequests }))
    assert.equal(queued(), 0, 'collected requests emit no late error')
    assert.equal(retainedWaiters, 0, 'cancelled bootstrap leaves no retained waiters')
    assert.equal(retainedReadyReactions, 0, 'cancelled requests leave no reactions on shared ready')
    assert.equal(readyReactions, 1, 'only shared initialization reaction remains')
    assert.equal(retainedRequests, 0, 'cancelled requests are garbage collected')
  } else if (name === 'ready-success' || name === 'ready-error') {
    let settle
    initialize(new Promise((resolve, reject) => { settle = name === 'ready-success' ? resolve : reject }))
    fetchOne()
    assert.equal(run('readyWaiters.size'), 1, 'bootstrap waiter registered')
    settle(name === 'ready-error' ? Error('init failed') : undefined)
    await until(idle, 'settled initialization cleans fetch')
    assert.equal(run('readyWaiters.size'), 0, 'settled initialization releases waiters')
    if (name === 'ready-error') {
      assert.equal(lines.length, 1)
      assert.equal(lines[0].error.message, 'init failed')
    } else {
      assert.deepEqual(lines.map(msg => msg.event || 'result'), ['head', 'result'])
    }
  } else if (name === 'mid-setup') {
    for (const stage of ['options', 'info']) {
      let release
      let entered = false
      let vendorCalls = 0
      const barrier = new Promise(resolve => { release = resolve })
      context.options = async () => {
        if (stage === 'options') { entered = true; await barrier }
        return { fetch: async () => { vendorCalls++; return context.reply } }
      }
      context.info = async () => {
        if (stage === 'info') { entered = true; await barrier }
        return { models: {} }
      }
      fetchOne()
      await until(() => entered, 'plugin setup entered its deferred await')
      assert.equal(run('inflight.get(1).state'), 'setup')
      abortOne()
      assert.equal(run('inflight.get(1).state'), 'cancelled', 'JS processed abort before setup resumes')
      assert.equal(run('inflight.get(1).controller.signal.aborted'), true)
      release()
      await until(idle, 'cancelled setup finishes')
      assert.equal(vendorCalls, 0, 'cancelled setup never starts vendor')
      context.options = async () => ({ fetch: async () => { vendorCalls++; return context.reply } })
      context.info = async () => ({ models: {} })
      fetchOne()
      await until(idle, 'healthy fetch after cancelled setup')
      assert.equal(vendorCalls, 1)
    }
    assert.deepEqual(lines.map(msg => msg.event || 'result'), ['head', 'result', 'head', 'result'])
  } else if (name === 'local-error') {
    context.options = async () => {
      run('inflight.get(1).controller.abort()')
      throw Error('local protocol failure')
    }
    fetchOne()
    await until(idle, 'local error settles')
    assert.equal(lines.length, 1)
    assert.equal(lines[0].error.message, 'local protocol failure', 'local abort does not suppress error owed to Go')
  } else if (name === 'local-error-cancel') {
    blockOn = 'all'
    run('send({ id: 99, result: null })')
    context.options = async () => {
      run('inflight.get(1).controller.abort()')
      throw Error('local protocol failure')
    }
    fetchOne()
    await until(() => queued() === 1, 'local error queued after local abort')
    abortOne()
    await until(idle, 'host cancel ends error wait even after local signal aborted')
    assert.equal(queued(), 0, 'late error removed after host cancel')
  } else if (name === 'held-window') {
    // the reader credits nothing back, so the fetch spends its whole window
    // and waits in gate.take; a cancel must wake that wait, or the fetch
    // never finishes
    context.reply.body = (async function* () { yield Buffer.alloc(1 << 20) })()
    fetchOne()
    const chunks = () => lines.filter(msg => msg.event === 'chunk')
    const spent = () => chunks().reduce((n, msg) => n + Buffer.from(msg.data, 'base64').length + run('FRAME_OVERHEAD'), 0)
    await until(() => spent() === 512 << 10, 'fetch spends its whole window')
    const held = chunks().length
    for (let iteration = 0; iteration < 5; iteration++) await tick()
    assert.equal(chunks().length, held, 'no frame past the window')
    assert.equal(idle(), false, 'fetch waits for credit')
    abortOne()
    await until(idle, 'cancel wakes a fetch waiting for credit')
    assert.equal(lines.filter(msg => msg.id === 1 && ('result' in msg || 'error' in msg)).length, 0)
  } else if (name === 'normal-reply') {
    context.reply.body = (async function* () { yield Buffer.from('complete reply') })()
    fetchOne()
    await until(idle, 'normal reply settles')
    assert.deepEqual(lines.map(msg => msg.event || 'result'), ['head', 'chunk', 'result'])
    assert.equal(Buffer.from(lines[1].data, 'base64').toString(), 'complete reply')
    assert.equal(lines[2].result, null)
  } else {
    throw Error('unknown test case')
  }
}
// node exits 0 once nothing is left to run, even while main() still awaits a
// promise that never settles, so a case passes only by reaching its end
let finished = false
process.on('exit', code => {
  if (finished || code !== 0) return
  process.exitCode = 1
  console.error(process.argv[1] + ' never finished: main() was still awaiting a promise that never settled when node ran out of work')
})
main().then(() => { finished = true }, err => { console.error(err); process.exitCode = 1 })
`

// fakeSignedIn starts a real Bun host with the fake plugin signed in to, its
// requests going to base, and gives the plugin's base URL.
func fakeSignedIn(t *testing.T, base string) string {
	t.Helper()
	sandbox(t)
	t.Setenv("FAKE_BASE", base)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKey(ctx, "fakeco", 0, nil, "k1", NewAccount); err != nil {
		t.Fatal(err)
	}
	o, err := LoaderOptions(ctx, "fakeco", "")
	if err != nil {
		t.Fatal(err)
	}
	return o.BaseURL
}

// TestFlowSlowReaderGetsTheWholeReply is the flow control's point: a reader
// slower than the vendor still gets every byte — credit pauses the upstream
// rather than the host cutting the reply at a fixed size.
func TestFlowSlowReaderGetsTheWholeReply(t *testing.T) {
	const size = 4 << 20
	payload := bytes.Repeat([]byte("0123456789abcdef"), size/16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		for off := 0; off < len(payload); off += 64 << 10 {
			end := min(off+(64<<10), len(payload))
			_, _ = w.Write(payload[off:end])
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()
	base := fakeSignedIn(t, srv.URL+"/v1")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Fetch(ctx, FetchRequest{
		Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
		URL: base + "/big", Method: "POST", Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	// read slower than the vendor writes, so the credit window must pause it
	// and let it go again rather than the reply being cut
	buf := make([]byte, 64<<10)
	var got []byte
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
			time.Sleep(time.Millisecond)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read at %d bytes: %v", len(got), err)
		}
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read %d bytes, want %d", len(got), len(payload))
	}
}

// TestFlowCancelAfterHeadStopsTheVendor: a request cancelled after its head,
// with nothing reading or closing the body, still stops the upstream fetch —
// the vendor's own request is cancelled.
func TestFlowCancelAfterHeadStopsTheVendor(t *testing.T) {
	hit := make(chan struct{})
	gone := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		once.Do(func() { close(hit) })
		<-r.Context().Done()
		close(gone)
	}))
	defer srv.Close()
	base := fakeSignedIn(t, srv.URL+"/v1")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rctx, rcancel := context.WithCancel(ctx)
	res, err := Fetch(rctx, FetchRequest{
		Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
		URL: base + "/hang", Method: "POST", Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	<-hit     // the vendor is in the request, its head has reached magpie
	rcancel() // nothing reads or closes the body
	select {
	case <-gone:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelling the request did not stop the vendor")
	}
	_ = res
}

// TestFlowTinyFramesStillArrive: a vendor that yields tiny pieces, read slowly,
// still gets every byte. The frame overhead pauses such a producer — it can
// keep at most window/(payload+overhead) frames queued — rather than the host
// cutting it, and the reader gets the whole reply.
func TestFlowTinyFramesStillArrive(t *testing.T) {
	const size = 4096
	payload := bytes.Repeat([]byte("abcdefgh"), size/8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		for i := range payload {
			_, _ = w.Write(payload[i : i+1])
			if i%16 == 0 {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()
	base := fakeSignedIn(t, srv.URL+"/v1")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Fetch(ctx, FetchRequest{
		Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
		URL: base + "/tiny", Method: "POST", Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf := make([]byte, 512)
	var got []byte
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
			time.Sleep(time.Millisecond)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read at %d bytes: %v", len(got), err)
		}
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read %d bytes, want %d", len(got), len(payload))
	}
}

// bigPiece is one piece of the production-scale stream: the 64 KiB block, or
// the same block as an SSE data event.
func bigPiece(block []byte, sse bool) []byte {
	if !sse {
		return block
	}
	return []byte("data: " + base64.StdEncoding.EncodeToString(block) + "\n\n")
}

// writeBig writes about total bytes of the pattern to w (nil to only measure),
// giving the exact length and its SHA-256. The vendor and the check build the
// same bytes, so a truncation anywhere shows up as a length or hash mismatch.
func writeBig(w io.Writer, total int, sse bool, block []byte) (int64, [32]byte) {
	h := sha256.New()
	var n int64
	for n < int64(total) {
		piece := bigPiece(block, sse)
		if w != nil {
			if _, err := w.Write(piece); err != nil {
				break
			}
		}
		h.Write(piece)
		n += int64(len(piece))
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return n, sum
}

// TestFlowBigReplyProd is the production-scale check: 100 MiB of a non-SSE body
// and of an SSE stream, each read through the real host and hashed; an SSE
// stream a reader pauses 12 s in the middle of and then finishes. It is behind
// MAGPIE_FLOW_BIG because each case moves 100 MiB, and it should be run against
// the Bun magpie itself picks (MAGPIE_BUN set to it).
func TestFlowBigReplyProd(t *testing.T) {
	if os.Getenv("MAGPIE_FLOW_BIG") == "" {
		t.Skip("set MAGPIE_FLOW_BIG=1 to run the 100 MiB flow cases")
	}
	const total = 100 << 20
	block := make([]byte, 64<<10)
	for i := range block {
		block[i] = byte(i * 7)
	}
	for _, tc := range []struct {
		name  string
		sse   bool
		pause time.Duration
	}{
		{"non-sse", false, 0},
		{"sse", true, 0},
		{"sse-paused", true, 12 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctype := "application/json"
			if tc.sse {
				ctype = "text/event-stream"
			}
			// how far the vendor has got, and whether it finished, so a pause
			// the reader takes shows whether the upstream was held back too
			var written atomic.Int64
			finished := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(finished)
				w.Header().Set("Content-Type", ctype)
				w.WriteHeader(200)
				fl := w.(http.Flusher)
				var n int64
				for n < int64(total) {
					piece := bigPiece(block, tc.sse)
					m, err := w.Write(piece)
					written.Add(int64(m))
					if err != nil {
						return
					}
					n += int64(len(piece))
					if n%(8<<20) < int64(len(piece)) {
						fl.Flush()
					}
				}
			}))
			defer srv.Close()
			base := fakeSignedIn(t, srv.URL+"/v1")

			wantLen, wantSum := writeBig(nil, total, tc.sse, block)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			res, err := Fetch(ctx, FetchRequest{
				Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
				URL: base + "/big", Method: "POST", Body: []byte(`{}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			h := sha256.New()
			var n int64
			paused := false
			var pausedAt, resumedAt, pauseOwed, pauseWindow int64
			var finishedBeforeResume, windowExhausted bool
			buf := make([]byte, 256<<10)
			for {
				m, err := res.Body.Read(buf)
				if m > 0 {
					h.Write(buf[:m])
					n += int64(m)
					if tc.pause > 0 && !paused && n >= 1<<20 {
						paused = true
						pausedAt = written.Load()
						t.Logf("pause_start: delivered=%d producer_written=%d expected=%d", n, pausedAt, wantLen)
						// Reflection keeps this public-API fixture compilable with
						// the old host.go overlay, which has no credit counters.
						b := res.Body.(*body)
						pauseEnd := time.Now().Add(tc.pause)
						for time.Now().Before(pauseEnd) {
							b.c.mu.Lock()
							fields := reflect.ValueOf(b.c).Elem()
							owed, window := fields.FieldByName("owed"), fields.FieldByName("window")
							if owed.IsValid() && window.IsValid() {
								pauseOwed, pauseWindow = owed.Int(), window.Int()
								windowExhausted = pauseWindow > 0 && pauseWindow-pauseOwed <= 2048
							}
							b.c.mu.Unlock()
							if windowExhausted {
								time.Sleep(time.Until(pauseEnd))
								break
							}
							time.Sleep(10 * time.Millisecond)
						}
						resumedAt = written.Load()
						select {
						case <-finished:
							finishedBeforeResume = true
						default:
						}
						t.Logf("pause_end_before_read: producer_written=%d expected=%d finished=%t window_exhausted=%t owed=%d window=%d", resumedAt, wantLen, finishedBeforeResume, windowExhausted, pauseOwed, pauseWindow)
					}
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("read at %d bytes: %v", n, err)
				}
			}
			got := h.Sum(nil)
			if n != wantLen {
				t.Fatalf("read %d bytes, want %d", n, wantLen)
			}
			if !bytes.Equal(got, wantSum[:]) {
				t.Fatalf("sha256 = %x, want %x", got, wantSum)
			}
			if tc.pause > 0 {
				// Preserve the original length/hash failure as the negative
				// control; only these new producer assertions are optional.
				if os.Getenv("MAGPIE_FLOW_ORIGINAL_BEHAVIOR_ONLY") != "1" {
					if !windowExhausted {
						t.Error("the paused reader did not exhaust the Go credit window")
					}
					if finishedBeforeResume || resumedAt >= wantLen {
						t.Errorf("producer finished before resume: written=%d expected=%d finished=%t", resumedAt, wantLen, finishedBeforeResume)
					}
				}
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Fatal("producer did not finish after resume")
				}
				t.Logf("producer_after_resume: written=%d expected=%d finished=true", written.Load(), wantLen)
			}
			t.Logf("%s: %d bytes, sha256 %x", tc.name, n, got)
		})
	}
}

// TestFlowAbortDuringSetupStaysHealthy: many fetches cancelled as soon as they
// are made — while the plugin is still setting them up, or waiting for a slow
// vendor's head — leave the host able to answer a later request. It is the Go
// side of the lifecycle: the host's slots come back, and the JS side's own
// finally (covered by construction) lets each request's controller and gate go.
func TestFlowAbortDuringSetupStaysHealthy(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	if out, err := runHostJSCase(t, node, "mid-setup"); err != nil {
		t.Fatalf("mid-setup cancellation: %v\n%s", err, out)
	}
}

func TestFlowEarlyCancellationStressStaysHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	base := fakeSignedIn(t, srv.URL+"/v1")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r := FetchRequest{
		Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
		URL: base + "/slow", Method: "POST", Body: []byte(`{}`),
	}
	for i := range 20 {
		fctx, fcancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			res, err := Fetch(fctx, r)
			if err == nil {
				_, _ = io.Copy(io.Discard, res.Body)
				res.Body.Close()
			}
		}()
		fcancel() // as early as possible: setup, or the head wait
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("fetch %d did not return after cancel", i)
		}
	}
	// the host still answers a request that is not cancelled
	res, err := Fetch(ctx, r)
	if err != nil {
		t.Fatalf("a fetch after the cancelled ones: %v", err)
	}
	defer res.Body.Close()
	if body, err := io.ReadAll(res.Body); err != nil || !bytes.Contains(body, []byte(`"ok":true`)) {
		t.Fatalf("body = %q, %v", body, err)
	}
}

// TestFlowHeadEnvelopeRejectsHugeHeaders: a vendor that answers with more
// headers than magpie's head envelope allows fails the fetch cleanly rather
// than the host taking an unbounded zero-charge head line.
func TestFlowHeadEnvelopeRejectsHugeHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Big", strings.Repeat("v", 96<<10)) // past the 64 KiB envelope
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	base := fakeSignedIn(t, srv.URL+"/v1")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := Fetch(ctx, FetchRequest{
		Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
		URL: base + "/bighead", Method: "POST", Body: []byte(`{}`),
	})
	if err == nil {
		t.Fatal("a reply with an oversized head was accepted")
	}
}
