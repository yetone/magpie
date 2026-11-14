// node --test site/: the Stable update channel (ssharpcc on X) is chosen
// from the release ledger, a week at a time, an urgent release at once.
// GitHub, the edge cache and KV are stood in for; every clock is held.
import { test, beforeEach } from "node:test";
import assert from "node:assert/strict";
import worker, { stableOf, weekStart, urgentIn, keepLedger, URGENT_MARK } from "./worker.js";

const DAY = 86400e3;
const at = (iso) => Date.parse(iso);
const iso = (ms) => new Date(ms).toISOString();

test("weekStart: the Monday 00:00 UTC on or before", () => {
  assert.equal(iso(weekStart(at("2026-10-28T12:00:00Z"))), "2026-10-26T00:00:00.000Z"); // a Wednesday
  assert.equal(iso(weekStart(at("2026-10-26T00:00:00Z"))), "2026-10-26T00:00:00.000Z"); // the Monday itself
  assert.equal(iso(weekStart(at("2026-11-01T23:59:59Z"))), "2026-10-26T00:00:00.000Z"); // the Sunday
  assert.equal(iso(weekStart(at("2026-11-02T00:00:00Z"))), "2026-11-02T00:00:00.000Z");
  // across a month and a year
  assert.equal(iso(weekStart(at("2027-01-01T05:00:00Z"))), "2026-12-28T00:00:00.000Z");
});

test("urgentIn: a line starting with the marker, quoted or not, in any case", () => {
  assert.equal(URGENT_MARK, "**Urgent release:**");
  assert.ok(urgentIn("**Urgent release:** Claude sign-ins could be banned\n\n### Fixes\n- one"));
  assert.ok(urgentIn("### Fixes\n- one\n\n> **Urgent release:** lost settings"));
  assert.ok(urgentIn("  **URGENT RELEASE:** why"));
  assert.ok(!urgentIn("- not an **Urgent release:** in a bullet's middle"));
  assert.ok(!urgentIn("Urgent release: without the bold"));
  assert.ok(!urgentIn(""));
  assert.ok(!urgentIn(null));
});

// a Wednesday: this week began Monday 10-26, so its cut is Friday 10-23
const WED = at("2026-10-28T12:00:00Z");
const rel = (version, published, urgent = false) => ({ version, published, urgent });
const LIST = [
  rel("0.1.10", "2026-10-27T09:00:00Z"),
  rel("0.1.9", "2026-10-23T00:00:00Z"), // at the cut itself
  rel("0.1.8", "2026-10-20T08:00:00Z"),
];

test("stableOf: the newest release out three days before the week began", () => {
  assert.equal(stableOf(LIST, WED).version, "0.1.9");
  // a second after the cut is too new
  const late = [rel("0.1.9", "2026-10-23T00:00:01Z"), rel("0.1.8", "2026-10-20T08:00:00Z")];
  assert.equal(stableOf(late, WED).version, "0.1.8");
  // the list's order doesn't matter
  assert.equal(stableOf([...LIST].reverse(), WED).version, "0.1.9");
});

test("stableOf: held the whole week, and moves on as the next begins", () => {
  for (const now of ["2026-10-26T00:00:00Z", "2026-10-29T00:00:00Z", "2026-11-01T23:59:59Z"])
    assert.equal(stableOf(LIST, at(now)).version, "0.1.9", now);
  // Monday 11-02: its cut is Friday 10-30, and 0.1.10 was out by then
  assert.equal(stableOf(LIST, at("2026-11-02T00:00:00Z")).version, "0.1.10");
  // a release out the day before the cut that week (10-29) is picked too
  const more = [rel("0.1.12", "2026-10-31T00:00:00Z"), rel("0.1.11", "2026-10-29T00:00:00Z"), ...LIST];
  assert.equal(stableOf(more, at("2026-11-04T00:00:00Z")).version, "0.1.11");
});

test("stableOf: an urgent release is Stable at once, until a weekly pick passes it", () => {
  const urgent = [rel("0.1.11", "2026-10-28T10:00:00Z", true), ...LIST];
  assert.equal(stableOf(urgent, WED).version, "0.1.11");
  assert.equal(stableOf(urgent, WED).urgent, true);
  // not before it is out
  assert.equal(stableOf(urgent, at("2026-10-28T09:59:59Z")).version, "0.1.9");
  // an urgent one older than the weekly pick changes nothing
  const old = [rel("0.1.10", "2026-10-27T09:00:00Z"), rel("0.1.9", "2026-10-23T00:00:00Z"), rel("0.1.8", "2026-10-20T08:00:00Z", true)];
  assert.equal(stableOf(old, WED).version, "0.1.9");
  // releases after an urgent one wait for the week as any do
  const after = [rel("0.1.12", "2026-10-28T11:00:00Z"), ...urgent];
  assert.equal(stableOf(after, WED).version, "0.1.11");
  // a release with no date isn't picked
  assert.equal(stableOf([rel("0.1.13", null, true), ...LIST], WED).version, "0.1.9");
});

test("stableOf: unknown when the list doesn't reach back to the cut", () => {
  const short = [rel("0.1.10", "2026-10-27T09:00:00Z"), rel("0.1.9", "2026-10-24T00:00:00Z")];
  assert.equal(stableOf(short, WED), null);
  assert.equal(stableOf([], WED), null);
  // an urgent one is known all the same
  assert.equal(stableOf([rel("0.1.11", "2026-10-28T10:00:00Z", true), ...short], WED).version, "0.1.11");
});

// --- the ledger, the cron and the feeds ---

// kv stands in for the LEDGER namespace; writes counts its puts, broken
// makes every read throw.
function kv() {
  const m = new Map();
  const ns = {
    m,
    writes: 0,
    broken: false,
    async get(key, opts = {}) {
      if (ns.broken) throw new Error("kv down");
      const v = m.has(key) ? m.get(key) : null;
      return v !== null && (opts.type === "json" || opts === "json") ? JSON.parse(v) : v;
    },
    async put(key, v) {
      ns.writes++;
      m.set(key, String(v));
    },
  };
  return ns;
}

// GITHUB is the release list GitHub has, newest first; apiDown stands in
// for its API limiting the worker, asks counts the API's asks.
let GITHUB = [], apiDown = false, asks = 0;
const store = new Map();
globalThis.caches = {
  default: {
    match: async (req) => (store.has(req.url) ? new Response(store.get(req.url)) : undefined),
    put: async (req, res) => void store.set(req.url, await res.text()),
  },
};
const gh = (r) => ({ tag_name: "v" + r.version, body: r.notes, html_url: "https://github.com/r/v" + r.version, published_at: r.published, draft: false, prerelease: false, assets: [{ name: "magpie-linux-amd64", size: 7, browser_download_url: "https://dl/" + r.version }] });
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
globalThis.fetch = async (u) => {
  u = String(u);
  if (u.startsWith("https://api.github.com/")) {
    asks++;
    if (apiDown) return new Response('{"message":"API rate limit exceeded"}', { status: 403 });
    const path = u.split("/repos/yetone/magpie-releases/")[1];
    const tag = path.match(/^releases\/tags\/v(.+)$/);
    if (tag) {
      const r = GITHUB.find((r) => r.version === tag[1]);
      return r ? Response.json(gh(r)) : new Response("{}", { status: 404 });
    }
    if (path === "releases/latest") return Response.json(gh(GITHUB[0]));
    const page = path.match(/^releases\?per_page=100(?:&page=(\d+))?$/);
    if (page) {
      const n = +(page[1] || 1);
      return Response.json(GITHUB.slice((n - 1) * 100, n * 100).map(gh));
    }
  }
  if (u.endsWith("/releases.atom")) {
    // the feed: the ten newest, their notes as HTML, the date the last edit's
    const entries = GITHUB.slice(0, 10).map(
      (r) => `<entry><updated>2030-01-01T00:00:00Z</updated><link rel="alternate" type="text/html" href="https://github.com/yetone/magpie-releases/releases/tag/v${r.version}"/><content type="html">${esc(r.html || `<p>${r.notes} (as the feed has it)</p>`)}</content></entry>`,
    );
    return new Response(`<feed>${entries.join("")}</feed>`);
  }
  const sums = u.match(/\/releases\/download\/v([^/]+)\/SHA256SUMS$/);
  if (sums && GITHUB.some((r) => r.version === sums[1])) return new Response(`sum${sums[1]}  magpie-linux-amd64\n`);
  return new Response("not found", { status: 404 });
};

// releases makes n releases, one every gap ms back from newest, the newest
// first: 0.1.(n) down to 0.1.1.
function releases(n, newest, gap, urgent = []) {
  return Array.from({ length: n }, (_, i) => {
    const version = `0.1.${n - i}`;
    const marked = urgent.includes(version);
    return { version, published: iso(newest - i * gap), notes: (marked ? `${URGENT_MARK} lost settings\n\n` : "") + `- change ${version}` };
  });
}

beforeEach(() => {
  store.clear();
  apiDown = false;
  asks = 0;
});

test("keepLedger: a new ledger is filled from GitHub's pages, three weeks back at most four asks a run", async () => {
  const NOW = at("2026-10-28T12:00:00Z");
  // 600 releases, one every 2h: page 3 ends 25 days back
  GITHUB = releases(600, NOW - 3600e3, 2 * 3600e3, ["0.1.590"]);
  const env = { LEDGER: kv() };
  await keepLedger(env, NOW);
  assert.ok(asks <= 4, `${asks} asks`);
  let ix = JSON.parse(env.LEDGER.m.get("index"));
  assert.equal(ix.list[0].v, "0.1.600");
  assert.equal(ix.list[0].p, GITHUB[0].published);
  assert.equal(ix.list.find((r) => r.v === "0.1.590").u, true);
  assert.equal(ix.list.filter((r) => r.u).length, 1);
  // pages 1 to 3 reach 25 days back, past the 21 it wants: no fourth
  assert.equal(ix.list.length, 300);
  assert.equal(asks, 3);
  // each release's notes by its day
  const day = GITHUB[0].published.slice(0, 10);
  assert.equal(JSON.parse(env.LEDGER.m.get("notes:" + day))["0.1.600"], "- change 0.1.600");

  // the next run: nothing new, nothing written
  const w = env.LEDGER.writes;
  asks = 0;
  await keepLedger(env, NOW + 15 * 60e3);
  assert.equal(env.LEDGER.writes, w);
  assert.equal(asks, 1);

  // a new release: the index and its day's notes, and nothing else
  GITHUB = [{ version: "0.1.601", published: iso(NOW + 20 * 60e3), notes: "- new" }, ...GITHUB];
  await keepLedger(env, NOW + 30 * 60e3);
  assert.equal(env.LEDGER.writes, w + 2);
  ix = JSON.parse(env.LEDGER.m.get("index"));
  assert.equal(ix.list[0].v, "0.1.601");
  assert.equal(ix.list.length, 301);
});

test("keepLedger: four asks a run at most, the next run reaching on", async () => {
  const NOW = at("2026-10-28T12:00:00Z");
  // one an hour: three weeks back is 504 releases, six pages
  GITHUB = releases(1000, NOW, 3600e3);
  const env = { LEDGER: kv() };
  await keepLedger(env, NOW);
  assert.equal(asks, 4);
  assert.equal(JSON.parse(env.LEDGER.m.get("index")).list.length, 400);
  // the first page again, then on from where the ledger ends (pages 5, 6)
  asks = 0;
  await keepLedger(env, NOW);
  assert.equal(asks, 3);
  const ix = JSON.parse(env.LEDGER.m.get("index"));
  assert.ok(Date.parse(ix.list.at(-1).p) <= NOW - 21 * DAY, ix.list.at(-1).p);
  assert.equal(new Set(ix.list.map((r) => r.v)).size, ix.list.length);
  // there: one ask a run from then on
  asks = 0;
  await keepLedger(env, NOW);
  assert.equal(asks, 1);
});

test("keepLedger: a ledger that can't be read is never written over", async () => {
  const NOW = at("2026-10-28T12:00:00Z");
  GITHUB = releases(50, NOW, 3600e3);
  const env = { LEDGER: kv() };
  await keepLedger(env, NOW);
  const before = env.LEDGER.m.get("index");
  env.LEDGER.broken = true;
  GITHUB = releases(51, NOW + 3600e3, 3600e3);
  await keepLedger(env, NOW + 3600e3);
  assert.equal(env.LEDGER.m.get("index"), before);
  // no ledger at all: nothing to keep
  await keepLedger({}, NOW);
});

test("keepLedger: with the API limited, the feed's ten, their dates and the API's notes kept", async () => {
  const NOW = at("2026-10-28T12:00:00Z");
  GITHUB = releases(30, NOW - 3600e3, 3600e3);
  const env = { LEDGER: kv() };
  await keepLedger(env, NOW);
  // two new ones, one marked urgent; the API says no
  GITHUB = [
    { version: "0.1.32", published: iso(NOW + 3600e3), notes: "", html: "<blockquote><p><strong>Urgent release:</strong> a ban risk</p></blockquote><ul><li>fix</li></ul>" },
    { version: "0.1.31", published: iso(NOW), notes: "- plain" },
    ...GITHUB,
  ];
  apiDown = true;
  await keepLedger(env, NOW + 2 * 3600e3);
  const ix = JSON.parse(env.LEDGER.m.get("index"));
  assert.deepEqual(ix.list.slice(0, 2).map((r) => [r.v, r.u]), [["0.1.32", true], ["0.1.31", false]]);
  // one the ledger had keeps the date it came out, not the feed's edit
  assert.equal(ix.list.find((r) => r.v === "0.1.30").p, GITHUB[2].published);
  // and its notes the API's markdown
  const day = GITHUB[2].published.slice(0, 10);
  assert.equal(JSON.parse(env.LEDGER.m.get("notes:" + day))["0.1.30"], "- change 0.1.30");
  // and in no other day's
  const feedDay = JSON.parse(env.LEDGER.m.get("notes:2030-01-01") || "{}");
  assert.equal(feedDay["0.1.30"], undefined);
});

test("keepLedger: a ledger left a day behind is mended past its hole", async () => {
  const NOW = at("2026-10-28T12:00:00Z");
  GITHUB = releases(400, NOW - 40 * DAY, 3600e3);
  const env = { LEDGER: kv() };
  await keepLedger(env, NOW - 40 * DAY);
  // 150 more while the cron was stopped: the first page shares none
  GITHUB = [...releases(550, NOW, 3600e3).slice(0, 150), ...GITHUB];
  asks = 0;
  await keepLedger(env, NOW);
  const ix = JSON.parse(env.LEDGER.m.get("index"));
  const have = new Set(ix.list.map((r) => r.v));
  for (let v = 401; v <= 550; v++) assert.ok(have.has(`0.1.${v}`), `0.1.${v}`);
  assert.ok(asks <= 4);
  // nothing older than 45 days stays
  assert.ok(ix.list.every((r) => Date.parse(r.p) >= NOW - 45 * DAY));
});

const ctx = { waitUntil: (p) => p };
const ask = async (path, env) => {
  const res = await worker.fetch(new Request("https://usemagpie.ai" + path), env, ctx);
  return { status: res.status, body: await res.json() };
};

test("latest?channel=stable: the week's pick from the ledger; without channel, the newest as before", async () => {
  // dates from the real clock (the route reads Date.now()): 0.1.2 is out
  // twelve days, older than any week's cut; the rest are an hour old
  const now = Date.now();
  GITHUB = [
    { version: "0.1.4", published: iso(now - 3600e3), notes: "- newest" },
    { version: "0.1.3", published: iso(now - 2 * 3600e3), notes: "- newer" },
    { version: "0.1.2", published: iso(now - 12 * DAY), notes: "### Fixes\n\n- stable one\n\n<!-- lang:zh -->\n\n- 稳定的" },
    { version: "0.1.1", published: iso(now - 13 * DAY), notes: "- old" },
  ];
  const env = { LEDGER: kv() };
  await keepLedger(env, now);
  let got = await ask("/api/latest?channel=stable", env);
  assert.equal(got.status, 200);
  assert.equal(got.body.version, "0.1.2");
  assert.equal(got.body.channel, "stable");
  assert.equal(got.body.urgent, false);
  assert.equal(got.body.notes, "### Fixes\n\n- stable one");
  assert.equal(got.body.assets["magpie-linux-amd64"].sha256, "sum0.1.2");
  assert.equal((await ask("/api/latest?channel=stable&lang=zh", env)).body.notes, "- 稳定的");
  // every other ask is every version, as it was
  for (const q of ["", "?channel=every", "?channel=beta"]) {
    got = await ask("/api/latest" + q, env);
    assert.equal(got.body.version, "0.1.4", q);
    assert.equal(got.body.channel, undefined, q);
  }

  // an urgent release goes to Stable at once
  GITHUB = [{ version: "0.1.5", published: iso(now - 60e3), notes: `${URGENT_MARK} a security hole\n\n- fix` }, ...GITHUB];
  await keepLedger(env, now);
  store.clear();
  got = await ask("/api/latest?channel=stable", env);
  assert.equal(got.body.version, "0.1.5");
  assert.equal(got.body.urgent, true);

  // the API limited: the files from SHA256SUMS, the notes from the ledger
  store.clear();
  apiDown = true;
  got = await ask("/api/latest?channel=stable", env);
  assert.equal(got.status, 200);
  assert.equal(got.body.version, "0.1.5");
  assert.match(got.body.notes, /a security hole/);
  assert.equal(got.body.assets["magpie-linux-amd64"].sha256, "sum0.1.5");
});

test("latest?channel=stable: unknown (503, never kept) without a ledger that reaches the cut", async () => {
  const now = Date.now();
  GITHUB = [{ version: "0.1.2", published: iso(now - 3600e3), notes: "- new" }];
  for (const env of [{}, { LEDGER: kv() }]) {
    const res = await worker.fetch(new Request("https://usemagpie.ai/api/latest?channel=stable"), env, ctx);
    assert.equal(res.status, 503);
    assert.equal(res.headers.get("Cache-Control"), "no-store");
  }
  const env = { LEDGER: kv() };
  await keepLedger(env, now);
  assert.equal((await ask("/api/latest?channel=stable", env)).status, 503);
  const broken = { LEDGER: kv() };
  broken.LEDGER.broken = true;
  assert.equal((await ask("/api/latest?channel=stable", broken)).status, 503);
});

test("download/stable: a platform's link goes to that file of Stable's release; /download stays the newest (fishmagic80 on X)", async () => {
  const now = Date.now();
  GITHUB = [
    { version: "0.1.4", published: iso(now - 3600e3), notes: "- newest" },
    { version: "0.1.2", published: iso(now - 12 * DAY), notes: "- stable one" },
    { version: "0.1.1", published: iso(now - 13 * DAY), notes: "- old" },
  ];
  const env = { LEDGER: kv() };
  await keepLedger(env, now);
  store.clear();
  const go = (path, e = env) => worker.fetch(new Request("https://usemagpie.ai" + path), e, ctx);
  for (const path of ["/download/stable/linux", "/download/stable/linux-amd64", "/download/stable/magpie-linux-amd64"]) {
    const res = await go(path);
    assert.equal(res.status, 302, path);
    assert.match(res.headers.get("Location"), /0\.1\.2/, path);
  }
  const newest = await go("/download/linux");
  assert.equal(newest.status, 302);
  assert.match(newest.headers.get("Location"), /0\.1\.4/);
  // a file the release hasn't
  assert.equal((await go("/download/stable/nothing-like-it")).status, 404);
  // Stable unknown: said so, never kept
  for (const e of [{}, { LEDGER: Object.assign(kv(), { broken: true }) }]) {
    store.clear();
    const res = await go("/download/stable/linux", e);
    assert.equal(res.status, 503);
    assert.equal(res.headers.get("Cache-Control"), "no-store");
  }
});

test("notes: a range older than the hundred newest is read on from the ledger, one list", async () => {
  const now = Date.now();
  // 250 releases an hour apart: the API's first page has the newest 100
  GITHUB = releases(250, now, 3600e3);
  const env = { LEDGER: kv() };
  await keepLedger(env, now);
  store.clear();
  const got = await ask("/api/notes?after=0.1.20&upto=0.1.250&lang=en", env);
  assert.equal(got.status, 200);
  const vs = got.body.releases.map((r) => r.version);
  assert.equal(vs.length, 230);
  assert.equal(vs[0], "0.1.250");
  assert.equal(vs.at(-1), "0.1.21");
  assert.equal(new Set(vs).size, vs.length);
  assert.equal(got.body.releases.find((r) => r.version === "0.1.30").notes, "- change 0.1.30");
  // limit still caps, reaching past the hundred
  const back = await ask("/api/notes?upto=0.1.250&limit=120", env);
  assert.equal(back.body.releases.length, 120);
  assert.equal(back.body.releases.at(-1).version, "0.1.131");
  // a range the hundred cover never reads the ledger
  const reads = env.LEDGER.get;
  let read = 0;
  env.LEDGER.get = (...a) => (read++, reads(...a));
  assert.equal((await ask("/api/notes?after=0.1.240&upto=0.1.250", env)).body.releases.length, 10);
  assert.equal(read, 0);
});
