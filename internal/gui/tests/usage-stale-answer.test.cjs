// Run with `node --test internal/gui/tests/usage-stale-answer.test.cjs`; no
// browser is needed. The Overview draws only the newest read of its figures:
// the reader may pick another period or tab while one is on its way, and an
// older answer — the figures of a period that is no longer the one on the
// page — must not land under the new one's name. The period alone does not say
// which read is newest, so picking today, 7 days and today again drops the
// first as well, and a read of the timer's (refreshUsage) and a pick's
// (loadUsage) count against each other, whichever asked first. The
// browser-level refresh behaviour (the timer, Refresh now, the panel) is in
// usage-refresh-flight.test.cjs; this one lifts the real functions out of
// app.js and drives them with a faked api() and page, so it checks the code
// that ships without Playwright.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");

// Another revision of app.js can be selected with MAGPIE_APP_JS to check
// the same behavior cases against the implementation before the fix.
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
const loadUsageSource = lift("async function loadUsage(asked) {");

// values cross the vm's realm boundary (its Array and Object are not the
// test's), so what is handed to assert is copied into this one first
const plain = (v) => JSON.parse(JSON.stringify(v));

// page() is a page with the globals loadUsage reads and writes, a fake api()
// whose answers the test hands out one at a time (oldest or newest first), and
// a record of what renderUsage() was given. pick()/tab() are the reader
// clicking a period or a tab, which is what calls loadUsage.
function page() {
  const script = [
    `
let view = "usage", usageTab = "usage", period = "today", usage = null;
let usageRead = 0;
const reads = [];        // api() paths, in the order they were asked for
const drawn = [];        // renderUsage(): { period, usage }
const quotaCalls = [];   // loadQuotas() arguments
const resolvers = [];    // the answers, held until the test gives them
function api(path) {
  return new Promise((res, rej) => { reads.push(path); resolvers.push({ path, res, rej }); });
}
function renderUsage() { drawn.push({ period, usage: usage && usage.tag }); }
function renderUsageTab() {}
function renderUsageEvery() {}
function renderUsageLoading() {}
function loadQuotas(asked) { quotaCalls.push(asked === true); }
function loadSessions() { return Promise.resolve("sessions"); }
function loadLedger() { return Promise.resolve("requests"); }
`,
    loadUsageSource,
    `
globalThis.page = {
  reads, drawn, quotaCalls,
  load: (asked) => loadUsage(asked),
  pick: (p) => { period = p; return loadUsage(); },
  tab: (t) => { usageTab = t; return loadUsage(); },
  leave: (v) => { view = v; },
  now: () => ({ view, usageTab, period, usage: usage && usage.tag }),
  // answer the i-th read with its own period's figures
  answer: (i) => { const r = resolvers[i]; r.res({ tag: r.path.slice("usage?period=".length) + "-data" }); },
  fail: (i) => resolvers[i].rej(new Error("no answer")),
};
`,
  ].join("\n");
  const context = vm.createContext({});
  vm.runInContext(script, context, { filename: "loadUsage.js" });
  return vm.runInContext("page", context);
}

test("an older period's answer, landing last, does not draw", async () => {
  const p = page();
  const today = p.pick("today"); // the reader picks today, its read held
  const week = p.pick("7d");     // and then 7 days, before today answers
  assert.deepEqual(plain(p.reads), ["usage?period=today", "usage?period=7d"]);
  p.answer(1);                   // 7 days answers first and is drawn
  await week;
  assert.deepEqual(plain(p.drawn).at(-1), { period: "7d", usage: "7d-data" });
  p.answer(0);                   // today answers late
  await today;
  assert.equal(p.now().usage, "7d-data", "the late answer of the older period was drawn over the newer one");
  assert.deepEqual(plain(p.drawn).at(-1), { period: "7d", usage: "7d-data" }, "the page was drawn again from the older period's answer");
});

test("a period picked and picked again: the older read is dropped, not the one that shares its period", async () => {
  const p = page();
  const first = p.pick("today");
  const week = p.pick("7d");
  const again = p.pick("today"); // back to today, a third read
  p.answer(2);                   // today's newest answers
  await again;
  assert.deepEqual(plain(p.now()), { view: "usage", usageTab: "usage", period: "today", usage: "today-data" });
  p.answer(1);                   // 7 days, asked before it, answers late
  await week;
  p.answer(0);                   // and the first today, whose period is the one on the page again
  await first;
  assert.equal(p.now().usage, "today-data", "a read older than the newest one was drawn: the period alone was taken for the newest");
  assert.equal(p.drawn.length, 1, "the page was drawn more than once: " + JSON.stringify(plain(p.drawn)));
});

test("a read whose tab was left behind is dropped, and one asked for on the way back draws", async () => {
  const p = page();
  const held = p.pick("7d");     // Overview's read, held
  assert.equal(await p.tab("requests"), "requests", "the Requests tab reads the ledger, not the overview");
  assert.equal(await p.tab("sessions"), "sessions");
  assert.deepEqual(plain(p.reads), ["usage?period=7d"], "a tab that is not the Overview asks for no overview");
  const back = p.tab("usage");   // back to the Overview, which reads again
  assert.deepEqual(plain(p.reads), ["usage?period=7d", "usage?period=7d"]);
  p.answer(1);
  await back;
  assert.equal(p.now().usage, "7d-data");
  p.answer(0);                   // the read held from before the tab was left
  await held;
  assert.equal(p.now().usage, "7d-data", "a read from before the tab was left drew after it came back");
  assert.equal(p.drawn.length, 1);
});

test("a read whose page was left is dropped", async () => {
  const p = page();
  const held = p.pick("7d");
  p.leave("providers");
  p.answer(0);
  await held;
  assert.equal(p.now().usage, null, "a read drew after the reader left the Usage page");
  assert.deepEqual(plain(p.drawn), []);
});

test("the asked read still reads the allowances, and the timer's read only asks", async () => {
  const p = page();
  const asked = p.load(true); // the reader opening the Usage page
  assert.deepEqual(plain(p.quotaCalls), [true], "an asked read reads the allowances at once");
  p.answer(0);
  await asked;
  assert.equal(p.now().usage, "today-data");
  const quiet = p.load(false); // the timer's read
  assert.deepEqual(plain(p.quotaCalls), [true, false], "the timer's read asks for the allowances, without the asked flag");
  p.answer(1);
  assert.equal(await quiet, undefined);
});

// ---- both readers: a pick (loadUsage) and the timer's read (refreshUsage) ----

const refreshSource = lift("function refreshUsage(now = false) {");

// pageBoth() is the Overview with both readers live: a page that already has
// figures (refreshUsage only reads when it has some), the flight/queue state
// refreshUsage keeps, and the same held answers as page(). pick() is a period
// or tab clicked, refresh() the timer or Refresh now, seed() the figures the
// page already drew.
function pageBoth() {
  const script = [
    `
let view = "usage", usageTab = "usage", period = "today", usage = null;
let usageRead = 0;
let usageLast = 0, usageReadAt = 0, quotasAsked = 0, sessionsAt = 0;
let usageFlight = null, usageQueued = false, usageQueuedNow = false;
let sessions = null, ledger = null, ledOffset = 0;
let loading = false;   // the "loading" class renderUsageLoading() puts on #view-usage
const performance = { now: () => 0 };
const reads = [];        // api() paths, in the order they were asked for
const drawn = [];        // renderUsage(): { period, usage }
const quotaCalls = [];   // loadQuotas() arguments
const resolvers = [];    // the answers, held until the test gives them
function api(path) {
  return new Promise((res, rej) => { reads.push(path); resolvers.push({ path, res, rej }); });
}
// the page's own element: what the loading flag is read and written through
function $(sel) {
  return {
    classList: {
      contains: (c) => c === "loading" && loading,
      add: (c) => { if (c === "loading") loading = true; },
      remove: (c) => { if (c === "loading") loading = false; },
    },
    setAttribute: () => {}, removeAttribute: () => {},
  };
}
function renderUsage() { loading = false; drawn.push({ period, usage: usage && usage.tag }); }
function renderUsageTab() {}
function renderUsageEvery() {}
function renderUsageLoading() { loading = true; }
function loadQuotas(asked) { quotaCalls.push(asked === true); return Promise.resolve(); }
function loadSessions() { return Promise.resolve("sessions"); }
function loadLedger() { return Promise.resolve("requests"); }
`,
    loadUsageSource,
    refreshSource,
    `
globalThis.page = {
  reads, drawn, quotaCalls,
  seed: (tag, p) => { usage = { tag, period: p }; period = p; },
  pick: (p) => { period = p; return loadUsage(); },
  refresh: (now) => refreshUsage(now),
  // refreshUsage asks on a microtask of its own, so the read is in place a few
  // turns after refresh() was called
  tick: async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); },
  now: () => ({ view, usageTab, period, usage: usage && usage.tag, reads: reads.length, loading }),
  // answer the i-th read with its own period and the figures the test names
  answer: (i, tag) => { const r = resolvers[i]; r.res({ period: r.path.slice("usage?period=".length), tag }); },
  // answer with the very figures the page already has: the same JSON
  answerSame: (i) => resolvers[i].res(usage),
  fail: (i) => resolvers[i].rej(new Error("no answer")),
};
`,
  ].join("\n");
  const context = vm.createContext({});
  vm.runInContext(script, context, { filename: "loadUsage+refreshUsage.js" });
  return vm.runInContext("page", context);
}

test("a pick in flight, then the timer's read: the older pick's answer is dropped", async () => {
  const p = pageBoth();
  p.seed("today-old", "today");
  const pick = p.pick("7d");        // the reader picks 7 days, its read held
  const timer = p.refresh(false);   // the timer's read of the same 7 days
  await p.tick();
  assert.deepEqual(plain(p.reads), ["usage?period=7d", "usage?period=7d"], "both readers did not ask");
  p.answer(1, "7d-timer");          // the timer's, asked second, answers first
  await timer;
  assert.equal(p.now().usage, "7d-timer");
  p.answer(0, "7d-pick");           // the pick's answers late
  await pick;
  assert.equal(p.now().usage, "7d-timer", "a read older than the newest one was drawn, from the other reader");
  assert.equal(p.drawn.length, 1, "the page was drawn again: " + JSON.stringify(plain(p.drawn)));
});

test("the timer's read in flight, then a pick: the older timer's answer is dropped", async () => {
  const p = pageBoth();
  p.seed("7d-old", "7d");
  const timer = p.refresh(false);   // the timer's read of 7 days, held
  await p.tick();
  const pick = p.pick("7d");        // the reader picks 7 days again, which reads
  assert.deepEqual(plain(p.reads), ["usage?period=7d", "usage?period=7d"]);
  p.answer(1, "7d-pick");
  await pick;
  assert.equal(p.now().usage, "7d-pick");
  p.answer(0, "7d-timer");          // the timer's answers late
  await timer;
  assert.equal(p.now().usage, "7d-pick", "the older timer's read was drawn over the newer pick's");
  assert.equal(p.drawn.length, 1);
});

test("today, 7 days and today again, the middle read the timer's", async () => {
  const p = pageBoth();
  p.seed("today-old", "today");
  const week = p.pick("7d");        // a pick of 7 days
  const timer = p.refresh(false);   // the timer's read, also 7 days
  await p.tick();
  const back = p.pick("today");     // and today picked again
  assert.deepEqual(plain(p.reads), ["usage?period=7d", "usage?period=7d", "usage?period=today"]);
  p.answer(2, "today-new");         // the newest answers and is drawn
  await back;
  assert.deepEqual(plain(p.now()), { view: "usage", usageTab: "usage", period: "today", usage: "today-new", reads: 3, loading: false });
  p.answer(1, "7d-timer");          // both older reads answer late
  await timer;
  p.answer(0, "7d-pick");
  await week;
  assert.equal(p.now().usage, "today-new", "an older read was drawn: " + JSON.stringify(plain(p.drawn)));
  assert.equal(p.drawn.length, 1);
});

test("the timer's own read still draws, and a second one waits its turn", async () => {
  const p = pageBoth();
  p.seed("7d-old", "7d");
  const first = p.refresh(true);    // Refresh now
  await p.tick();
  const second = p.refresh(true);   // asked while the first is on its way
  await p.tick();
  assert.equal(p.reads.length, 1, "the second read went out beside the first");
  p.answer(0, "7d-1");
  await p.tick();                   // the first read is done and the queued one has gone out
  assert.equal(p.now().usage, "7d-1", "the timer's read did not draw");
  assert.equal(p.reads.length, 2, "the queued read was never served");
  p.answer(1, "7d-2");
  await second;                     // both promises cover the queued read
  assert.equal(await first, undefined);
  assert.equal(p.now().usage, "7d-2");
  assert.equal(p.drawn.length, 2, "both timer reads drew: " + JSON.stringify(plain(p.drawn)));
});

test("a pick showing the skeleton, the timer answering with the same figures, the pick's answer late", async () => {
  const p = pageBoth();
  p.seed("today-1", "today");
  const manual = p.pick("today");   // the reader picks the period already shown
  assert.equal(p.now().loading, true, "a pick does not put the skeleton up");
  const timer = p.refresh(false);   // the timer reads it too, and claims the newest read
  await p.tick();
  p.answerSame(1);                  // the timer's answer is the very figures on hand
  await timer;
  p.answer(0, "today-late");        // the pick's answers late, and is dropped
  await manual;
  assert.equal(p.now().loading, false, "the page was left on the skeleton the superseded read put up");
  assert.deepEqual(plain(p.drawn), [{ period: "today", usage: "today-1" }], "the figures on hand were not drawn to end it: " + JSON.stringify(plain(p.drawn)));
  assert.equal(p.now().usage, "today-1", "the late answer was drawn");
});

test("the timer's read with nothing changed still does not draw", async () => {
  const p = pageBoth();
  p.seed("today-1", "today");
  const timer = p.refresh(true);    // the ordinary tick, nothing on the page loading
  await p.tick();
  p.answerSame(0);
  await timer;
  assert.deepEqual(plain(p.drawn), [], "a timer read with nothing changed drew again");
  assert.equal(p.now().loading, false);
});
