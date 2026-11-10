package plugin

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestRenewRetryBacksOff: a sign-in whose renewal keeps failing is tried
// again later and later, from RENEW_RETRY to RENEW_RETRY_MOST, counted
// per sign-in: a new one (signed in again) starts afresh. Two dead ima
// accounts were renewed ~80 times a minute for hours (JZGiVvkSOA75376 on
// X).
func TestRenewRetryBacksOff(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", renewBackoffJS)
	cmd.Stdin = bytes.NewReader(hostJS)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

const renewBackoffJS = `
const assert = require('node:assert/strict')
const vm = require('node:vm')
const source = require('node:fs').readFileSync(0, 'utf8')
const start = source.indexOf('const RENEW_LEAD =')
const end = source.indexOf('function leadOf(', start)
assert.ok(start >= 0 && end > start, 'the renewal section exists')
const unrenewed = new Map()
const context = vm.createContext({ unrenewed, secretOf: (a) => a.refresh, Date, Math })
vm.runInContext(source.slice(start, end), context)
const run = (code) => vm.runInContext(code, context)

const s = 1000, m = 60 * s
assert.deepEqual([1, 2, 3, 4, 5, 6, 7, 8, 50].map((n) => run('retryAfter(' + n + ')')),
  [30 * s, 1 * m, 2 * m, 4 * m, 8 * m, 16 * m, 30 * m, 30 * m, 30 * m])

const was = { refresh: 'r1' }
context.was = was
for (let i = 1; i <= 3; i++) {
  run('failedRenewal("k", was, false, "no")')
  assert.equal(unrenewed.get('k').n, i, 'failures of one sign-in are counted in a row')
}
context.again = { refresh: 'r2' }
run('failedRenewal("k", again, false, "")')
assert.equal(unrenewed.get('k').n, 1, 'a sign-in made again starts afresh')
assert.equal(unrenewed.get('k').secret, 'r2')
run('failedRenewal("k", again, true, "gone")')
assert.equal(unrenewed.get('k').gone, true)
`
