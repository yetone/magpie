package plugin

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestAuthLockWaitDoesNotFreezeTheHost (#5112): a plugin-auth.json.lock held
// by another host makes changeAuth wait, and the wait must leave the event
// loop free: every other plugin's timers, requests and model lists run on
// the same loop. Atomics.wait in pause() froze it for as long as the lock
// was held (up to 20s). The change is still made once the lock is let go,
// concurrent changes in one host don't lose each other's, and a lock
// left by a dead host (older than AUTH_LOCK_STALE) is taken over.
func TestAuthLockWaitDoesNotFreezeTheHost(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", authLockAsyncJS, filepath.Join(t.TempDir(), "plugin-auth.json"))
	cmd.Stdin = bytes.NewReader(hostJS)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

const authLockAsyncJS = `
const assert = require('node:assert/strict')
const realFs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')
const source = realFs.readFileSync(0, 'utf8')
const start = source.indexOf('function parseAuth(')
const end = source.indexOf('// ---- accounts', start)
assert.ok(start >= 0 && end > start, 'production section exists')
const authPath = process.argv[1]
const context = vm.createContext({
  fs: realFs, path, authPath, Buffer, structuredClone, Date, Promise, Math, JSON, Object, Error, Array, String, Number,
  setTimeout, process, toErr: () => {}, send: () => {},
})
vm.runInContext(source.slice(start, end), context)
const run = code => vm.runInContext(code, context)
const lock = authPath + '.lock'
const sleep = ms => new Promise(r => setTimeout(r, ms))

;(async () => {
  realFs.writeFileSync(authPath, JSON.stringify({ a: { type: 'api', key: 'k1' } }))

  // another host holds the lock for 600ms
  realFs.writeFileSync(lock, '')
  let ticks = 0
  const timer = setInterval(() => ticks++, 20)
  let released = false
  setTimeout(() => { released = true; try { realFs.unlinkSync(lock) } catch {} }, 600)
  const t0 = Date.now()
  const waiting = run('changeAuth((all) => { all.b = { type: "api", key: "k2" } })')
  await sleep(300)
  assert.ok(!released, 'the lock is still held at 300ms')
  assert.ok(ticks >= 8, 'a timer on the same loop kept running while changeAuth waited for the lock: ' + ticks + ' ticks')
  await waiting
  assert.ok(released, 'the change waited for the lock to be let go')
  assert.ok(Date.now() - t0 < 5000, 'and did not wait out the stale limit')
  clearInterval(timer)
  const now = JSON.parse(realFs.readFileSync(authPath, 'utf8'))
  assert.equal(now.b.key, 'k2')
  assert.equal(now.a.key, 'k1', 'the others are kept')
  assert.ok(!realFs.existsSync(lock), 'the lock is let go')

  // 20 changes at once lose none
  await Promise.all(Array.from({ length: 20 }, (_, i) => run('changeAuth((all) => { all.n = { n: (all.n?.n ?? 0) + 1 } })')))
  assert.equal(JSON.parse(realFs.readFileSync(authPath, 'utf8')).n.n, 20, 'no two writers at once')

  // a lock left by a host that died
  realFs.writeFileSync(lock, '')
  const old = new Date(Date.now() - 11e3)
  realFs.utimesSync(lock, old, old)
  await run('changeAuth((all) => { all.c = { type: "api", key: "k3" } })')
  assert.equal(JSON.parse(realFs.readFileSync(authPath, 'utf8')).c.key, 'k3', 'a stale lock is taken over')
  assert.ok(!realFs.existsSync(lock))
  console.log('ok')
})().catch(e => { console.error(e); process.exit(1) })
`
