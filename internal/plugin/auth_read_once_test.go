package plugin

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestAuthReadOncePerVersion: host.js reads and parses plugin-auth.json
// once per version of the file, not on every request (its account, its
// renewal and the plugin's getAuth each read it). A file rewritten with
// the same size inside one tick of the file system's clock, which leaves
// the same time, is still read again, and a plugin's copy is its own.
func TestAuthReadOncePerVersion(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", authReadOnceJS, filepath.Join(t.TempDir(), "plugin-auth.json"))
	cmd.Stdin = bytes.NewReader(hostJS)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

const authReadOnceJS = `
const assert = require('node:assert/strict')
const realFs = require('node:fs')
const vm = require('node:vm')
const source = realFs.readFileSync(0, 'utf8')
const section = (from, to) => {
  const start = source.indexOf(from)
  const end = source.indexOf(to, start)
  assert.ok(start >= 0 && end > start, 'production section exists')
  return source.slice(start, end)
}
const authPath = process.argv[1]
let reads = 0
const fs = { ...realFs, readFileSync: (p, ...rest) => {
  if (p === authPath) reads++
  return realFs.readFileSync(p, ...rest)
} }
const context = vm.createContext({ fs, authPath, Buffer, structuredClone, Date, toErr: () => {}, send: () => {}, pause: () => {}, forgetAccount: () => {}, providerOf: (k) => k.split("#")[0] })
const run = code => vm.runInContext(code, context)
run(section('function parseAuth(', '// writeDurable'))
const past = new Date(Date.now() - 60e3)
const write = (v, at) => {
  realFs.writeFileSync(authPath, JSON.stringify(v))
  if (at) realFs.utimesSync(authPath, at, at)
}

write({ a: { type: 'api', key: 'k1' } }, past)
for (let i = 0; i < 100; i++) assert.equal(run('readAuth()').a.key, 'k1')
assert.equal(reads, 1, 'a settled file is read once for 100 reads, read ' + reads + ' times')

// written again: read again
write({ a: { type: 'api', key: 'k2' }, b: { type: 'api', key: 'k3' } }, new Date(past.getTime() + 1000))
assert.equal(run('readAuth()').a.key, 'k2')

// a file written just now, rewritten with the same size and time (two
// writes inside one tick of the clock): its bytes tell them apart
const now = new Date()
write({ a: { type: 'api', key: 'x1' } }, now)
assert.equal(run('readAuth()').a.key, 'x1')
write({ a: { type: 'api', key: 'x2' } }, now)
assert.equal(run('readAuth()').a.key, 'x2', 'a same-size rewrite in one tick is read')

// what a plugin is handed is its own to change
const copy = run('authCopy("a")')
copy.key = 'changed'
assert.equal(run('readAuth()').a.key, 'x2')
const all = run('authCopy()')
all.a.key = 'changed'
assert.equal(run('readAuth()').a.key, 'x2')
console.log('ok')
`
