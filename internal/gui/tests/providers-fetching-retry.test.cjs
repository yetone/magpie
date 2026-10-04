// Run with `node --test internal/gui/tests/providers-fetching-retry.test.cjs`;
// no browser is needed. While the backend says the accounts' lists are on
// their way (fetching), the page asks again every few seconds — the timer is
// the only thing that brings them in, so a read that failed, or a reload that
// did, must put the next one back rather than ending the asking. The guards
// that pause it (another page, an open editor, a hidden window, lists that are
// in) still hold, and only one timer is ever pending. providers-fetching.test.cjs
// checks the same feature in a browser; this one lifts the real
// providersWhileFetching out of app.js and drives it with faked timers and a
// faked api(), so it runs without Playwright.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");

const APP = process.env.MAGPIE_APP_JS || path.resolve(__dirname, "../assets/app.js");
const source = fs.readFileSync(APP, "utf8");

// lift a top-level function out of app.js whole: from its `function` line to
// the `}` at the left margin that closes it
function lift(header) {
  const start = source.indexOf(header);
  assert.notEqual(start, -1, `app.js no longer has ${header}`);
  const end = source.indexOf("\n}\n", start);
  assert.notEqual(end, -1, `${header} is not closed at the left margin`);
  return source.slice(start, end + 2);
}
const providersSource = lift("function providersWhileFetching() {");

// values cross the vm's realm boundary (its Object is not the test's), so what
// is handed to assert is copied into this one first
const plain = (v) => JSON.parse(JSON.stringify(v));

// page() is the Providers page with the globals the function reads, timers
// that only fire when the test says so, and a fake api()/loadProviders().
// fire() runs the pending timer and waits for the read it starts.
function page() {
  const script = [
    `
let view = "providers", editing = null, adding = false;
const document = { hidden: false };
let providers = { fetching: true, providers: [], gateway: { calls: 0 } };
const timers = [];
let seq = 0, apiCalls = 0, reloads = 0, reloadFails = false;
let answer = () => Promise.resolve(null);
function setTimeout(fn, ms) { const t = { id: ++seq, ms, cancelled: false, fired: false, fn }; timers.push(t); return t; }
function clearTimeout(t) { if (t) t.cancelled = true; }
function api(path) { apiCalls++; return answer(path); }
// as the real loadProviders ends by asking again, so does this one — unless it
// throws first, which is the reload that failed
async function loadProviders() { reloads++; if (reloadFails) throw new Error("reload failed"); providersWhileFetching(); }
`,
    providersSource,
    `
globalThis.page = {
  arm: () => { providersWhileFetching(); return timers.filter((t) => !t.cancelled && !t.fired).length; },
  pending: () => timers.filter((t) => !t.cancelled && !t.fired).length,
  fire: async () => { const t = timers.filter((x) => !x.cancelled && !x.fired).pop(); t.fired = true; await t.fn(); },
  counts: () => ({ api: apiCalls, reloads }),
  fetching: (v) => { providers.fetching = v; },
  set: (k, v) => { if (k === "view") view = v; else if (k === "editing") editing = v; else if (k === "adding") adding = v; else if (k === "hidden") document.hidden = v; else if (k === "reloadFails") reloadFails = v; },
  answers: (payload) => { answer = () => Promise.resolve(payload); },
  refuses: () => { answer = () => Promise.reject(new Error("no answer")); },
};
`,
  ].join("\n");
  const context = vm.createContext({});
  vm.runInContext(script, context, { filename: "providersWhileFetching.js" });
  return vm.runInContext("page", context);
}

const changed = () => ({ fetching: true, providers: [{ id: "a" }], gateway: { calls: 0 } });
const unchanged = () => ({ fetching: false, providers: [], gateway: { calls: 0 } });

test("a read that failed is asked again, one timer at a time", async () => {
  const p = page();
  p.refuses();
  assert.equal(p.arm(), 1, "the first poll was not set");
  await p.fire(); // the read fails
  assert.equal(p.pending(), 1, "a failed read ended the asking: nothing is set to try again");
  await p.fire(); // and it fails again
  assert.equal(p.pending(), 1, "the asking did not go on");
  assert.deepEqual(plain(p.counts()), { api: 2, reloads: 0 });
});

test("an answer that changed the list reloads it, and the reload's timer is the only one", async () => {
  const p = page();
  p.answers(changed());
  p.arm();
  await p.fire();
  assert.deepEqual(plain(p.counts()), { api: 1, reloads: 1 }, "the changed list was not reloaded");
  assert.equal(p.pending(), 1, "the reload left no timer, or left two");
});

test("a reload that failed is asked again too", async () => {
  const p = page();
  p.answers(changed());
  p.set("reloadFails", true);
  p.arm();
  await p.fire();
  assert.deepEqual(plain(p.counts()), { api: 1, reloads: 1 });
  assert.equal(p.pending(), 1, "a failed reload ended the asking");
});

test("an answer that says the lists are in stops the asking", async () => {
  const p = page();
  p.answers(unchanged());
  p.arm();
  await p.fire();
  assert.equal(p.pending(), 0, "the asking went on after the lists were in");
});

test("nothing is set while the lists are not on their way", () => {
  const p = page();
  p.fetching(false);
  assert.equal(p.arm(), 0);
});

test("arming twice leaves one timer", () => {
  const p = page();
  assert.equal(p.arm(), 1);
  assert.equal(p.arm(), 1, "a second arming added a second timer");
});

for (const [guard, set] of [
  ["another page", (p) => p.set("view", "agents")],
  ["an open editor", (p) => p.set("editing", "p1")],
  ["a provider being added", (p) => p.set("adding", true)],
  ["a hidden window", (p) => p.set("hidden", true)],
]) {
  test(`a poll under ${guard} reads nothing and sets nothing`, async () => {
    const p = page();
    p.refuses();
    p.arm();
    set(p);
    await p.fire();
    assert.deepEqual(plain(p.counts()), { api: 0, reloads: 0 }, "it read under " + guard);
    assert.equal(p.pending(), 0, "it kept asking under " + guard);
  });
}
