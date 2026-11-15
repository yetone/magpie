// node --test site/: the Daily update channel (fishmagic80 on X) is picked
// once a day from the release ledger: the newest release out by 20:00 UTC
// with all its files and a green CI run of its own. GitHub, the edge cache
// and KV are stood in for; every clock is held.
import { test, beforeEach } from "node:test";
import assert from "node:assert/strict";
import worker, { dayCut, dailyOf, keepDaily, keepLedger, RELEASE_FILES, URGENT_MARK } from "./worker.js";

const DAY = 86400e3;
const HOUR = 3600e3;
const at = (iso) => Date.parse(iso);
const iso = (ms) => new Date(ms).toISOString();

test("dayCut: the 20:00 UTC on or before", () => {
  assert.equal(iso(dayCut(at("2026-11-15T21:00:00Z"))), "2026-11-15T20:00:00.000Z");
  assert.equal(iso(dayCut(at("2026-11-15T20:00:00Z"))), "2026-11-15T20:00:00.000Z");
  assert.equal(iso(dayCut(at("2026-11-15T19:59:59Z"))), "2026-11-14T20:00:00.000Z");
  assert.equal(iso(dayCut(at("2027-01-01T03:00:00Z"))), "2026-12-31T20:00:00.000Z");
});

test("dailyOf: the newest out by the cut with every file and a green run", () => {
  const cut = at("2026-11-15T20:00:00Z");
  const r = (version, published, files, green) => ({ version, published, files, green });
  const list = [
    r("0.1.9", "2026-11-15T20:00:01Z", 17, true), // after the cut
    r("0.1.8", "2026-11-15T19:00:00Z", 17, false), // red or no run
    r("0.1.7", "2026-11-15T18:00:00Z", 16, true), // a file short
    r("0.1.6", "2026-11-15T17:00:00Z", 17, true),
    r("0.1.5", "2026-11-15T16:00:00Z", 17, true),
  ];
  assert.equal(RELEASE_FILES, 17);
  assert.equal(dailyOf(list, cut).version, "0.1.6");
  assert.equal(dailyOf([...list].reverse(), cut).version, "0.1.6");
  assert.equal(dailyOf(list.slice(0, 3), cut), null);
  assert.equal(dailyOf([r("0.1.10", null, 17, true)], cut), null);
});

// --- the cron and the feed ---

function kv() {
  const m = new Map();
  const ns = {
    m,
    broken: false,
    async get(key, opts = {}) {
      if (ns.broken) throw new Error("kv down");
      const v = m.has(key) ? m.get(key) : null;
      return v !== null && (opts.type === "json" || opts === "json") ? JSON.parse(v) : v;
    },
    async put(key, v) {
      m.set(key, String(v));
    },
  };
  return ns;
}

// GITHUB is magpie-releases' list, newest first: {version, published,
// notes, files}. SHAS is each tag's commit in yetone/magpie, GREEN the
// commits the Test workflow passed. srcDown makes yetone/magpie's API fail.
let GITHUB = [], SHAS = {}, GREEN = [], srcDown = false, srcAsks = [];
const store = new Map();
globalThis.caches = {
  default: {
    match: async (req) => (store.has(req.url) ? new Response(store.get(req.url)) : undefined),
    put: async (req, res) => void store.set(req.url, await res.text()),
  },
};
const names = Array.from({ length: RELEASE_FILES }, (_, i) => (i ? `magpie-file-${i}` : "magpie-linux-amd64"));
const gh = (r) => ({
  tag_name: "v" + r.version,
  body: r.notes || "",
  html_url: "https://github.com/r/v" + r.version,
  published_at: r.published,
  draft: false,
  prerelease: false,
  assets: names.slice(0, r.files ?? RELEASE_FILES).map((name) => ({ name, size: 7, browser_download_url: `https://dl/${r.version}/${name}` })),
});
globalThis.fetch = async (u) => {
  u = String(u);
  if (u.startsWith("https://api.github.com/repos/yetone/magpie-releases/")) {
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
  if (u.startsWith("https://api.github.com/repos/yetone/magpie/")) {
    const path = u.split("/repos/yetone/magpie/")[1];
    srcAsks.push(path);
    if (srcDown) return new Response('{"message":"API rate limit exceeded"}', { status: 403 });
    if (path.startsWith("actions/workflows/test.yml/runs?")) {
      assert.match(path, /branch=main/);
      assert.match(path, /status=success/);
      return Response.json({ workflow_runs: GREEN.map((head_sha) => ({ head_sha, conclusion: "success" })) });
    }
    const refs = path.match(/^git\/matching-refs\/tags\/v(.+)$/);
    if (refs)
      return Response.json(
        Object.entries(SHAS)
          .filter(([v]) => v.startsWith(refs[1]))
          .map(([v, sha]) => ({ ref: "refs/tags/v" + v, object: { sha, type: "commit" } })),
      );
  }
  const sums = u.match(/\/releases\/download\/v([^/]+)\/SHA256SUMS$/);
  if (sums && GITHUB.some((r) => r.version === sums[1])) return new Response(`sum${sums[1]}  magpie-linux-amd64\n`);
  return new Response("not found", { status: 404 });
};

beforeEach(() => {
  store.clear();
  srcDown = false;
  srcAsks = [];
});

// a day of releases, one an hour up to now: 0.1.(n) newest
function day(n, newest) {
  GITHUB = Array.from({ length: n }, (_, i) => ({ version: `0.1.${n - i}`, published: iso(newest - i * HOUR), notes: `- change 0.1.${n - i}` }));
  SHAS = Object.fromEntries(GITHUB.map((r) => [r.version, "sha" + r.version]));
}

test("keepDaily: once a cut, the newest out by it with all files and its own green run", async () => {
  const NOW = at("2026-11-15T20:05:00Z"); // just past the cut
  day(30, NOW); // 0.1.30 out at 20:05, 0.1.29 at 19:05, ...
  GITHUB.find((r) => r.version === "0.1.28").files = 16; // a file short
  GREEN = ["sha0.1.28", "sha0.1.26", "sha0.1.25", "sha0.1.30"]; // 0.1.30 is after the cut
  const env = { LEDGER: kv() };
  await keepLedger(env, NOW);
  await keepDaily(env, NOW);
  let d = JSON.parse(env.LEDGER.m.get("daily"));
  assert.equal(d.v, "0.1.26");
  assert.equal(d.cut, "2026-11-15T20:00:00.000Z");

  // later the same day: nothing asked, nothing moves, however green
  srcAsks = [];
  GREEN = [...GREEN, "sha0.1.29"];
  await keepDaily(env, NOW + 3 * HOUR);
  assert.deepEqual(srcAsks, []);
  assert.equal(JSON.parse(env.LEDGER.m.get("daily")).v, "0.1.26");

  // the next cut: the newest by then
  day(54, NOW + 24 * HOUR - 5 * 60e3); // 0.1.54 out at 19:00 the next day
  GREEN = ["sha0.1.29", "sha0.1.52"];
  await keepLedger(env, NOW + DAY);
  await keepDaily(env, NOW + DAY);
  d = JSON.parse(env.LEDGER.m.get("daily"));
  assert.equal(d.v, "0.1.52");
  assert.equal(d.cut, "2026-11-16T20:00:00.000Z");
});

test("keepDaily: nothing green in reach keeps the last pick; GitHub failing writes nothing", async () => {
  const NOW = at("2026-11-15T20:05:00Z");
  day(30, NOW);
  GREEN = ["sha0.1.20"];
  const env = { LEDGER: kv() };
  await keepLedger(env, NOW);
  await keepDaily(env, NOW);
  assert.equal(JSON.parse(env.LEDGER.m.get("daily")).v, "0.1.20");

  // a day with no green run: Daily stays, never goes back
  day(54, NOW + DAY);
  GREEN = ["sha0.1.10"];
  await keepLedger(env, NOW + DAY);
  await keepDaily(env, NOW + DAY);
  let d = JSON.parse(env.LEDGER.m.get("daily"));
  assert.equal(d.v, "0.1.20");
  assert.equal(d.cut, "2026-11-16T20:00:00.000Z");

  // GitHub says no: nothing written, the next run tries again
  srcDown = true;
  await keepDaily(env, NOW + 2 * DAY);
  assert.equal(JSON.parse(env.LEDGER.m.get("daily")).cut, "2026-11-16T20:00:00.000Z");
  srcDown = false;
  GREEN = ["sha0.1.40"];
  await keepDaily(env, NOW + 2 * DAY);
  d = JSON.parse(env.LEDGER.m.get("daily"));
  assert.equal(d.v, "0.1.40");
  assert.equal(d.cut, "2026-11-17T20:00:00.000Z");

  // a ledger that can't be read: nothing written
  env.LEDGER.broken = true;
  await keepDaily(env, NOW + 3 * DAY);
  env.LEDGER.broken = false;
  assert.equal(JSON.parse(env.LEDGER.m.get("daily")).v, "0.1.40");
});

test("keepDaily: tags are asked ten at a time, a bounded number of asks", async () => {
  const NOW = at("2026-11-15T20:05:00Z");
  day(200, NOW);
  GREEN = []; // nothing green: it looks as far as it may
  const env = { LEDGER: kv() };
  await keepLedger(env, NOW);
  await keepDaily(env, NOW);
  const tagAsks = srcAsks.filter((p) => p.startsWith("git/"));
  assert.ok(tagAsks.length <= 6, tagAsks.join(" "));
  assert.equal(env.LEDGER.m.get("daily"), undefined); // never known: no pick
});

const ctx = { waitUntil: (p) => p };
const ask = async (path, env) => {
  const res = await worker.fetch(new Request("https://usemagpie.ai" + path), env, ctx);
  return { status: res.status, body: await res.json(), cache: res.headers.get("Cache-Control") };
};

test("latest?channel=daily: the day's pick, an urgent release at once; 503 until a pick", async () => {
  const now = Date.now();
  GITHUB = [
    { version: "0.1.4", published: iso(now), notes: "- newest" },
    { version: "0.1.3", published: iso(now - 2 * DAY), notes: "- daily one\n\n<!-- lang:zh -->\n\n- 每日的" },
    { version: "0.1.2", published: iso(now - 3 * DAY), notes: "- old" },
  ];
  SHAS = { "0.1.4": "a4", "0.1.3": "a3", "0.1.2": "a2" };
  GREEN = ["a3", "a2"]; // 0.1.4's run hasn't finished
  const env = { LEDGER: kv() };
  await keepLedger(env, now);
  // before the cron has picked: unknown, never kept
  let got = await ask("/api/latest?channel=daily", env);
  assert.equal(got.status, 503);
  assert.equal(got.cache, "no-store");
  assert.equal((await ask("/api/latest?channel=daily", {})).status, 503);

  await keepDaily(env, now);
  got = await ask("/api/latest?channel=daily", env);
  assert.equal(got.status, 200);
  assert.equal(got.body.version, "0.1.3");
  assert.equal(got.body.channel, "daily");
  assert.equal(got.body.urgent, false);
  assert.equal(got.body.notes, "- daily one");
  assert.equal((await ask("/api/latest?channel=daily&lang=zh", env)).body.notes, "- 每日的");
  // every version is as it was
  assert.equal((await ask("/api/latest", env)).body.version, "0.1.4");

  // an urgent release is Daily at once
  GITHUB = [{ version: "0.1.5", published: iso(now), notes: `${URGENT_MARK} a ban risk\n\n- fix` }, ...GITHUB];
  await keepLedger(env, now);
  store.clear();
  got = await ask("/api/latest?channel=daily", env);
  assert.equal(got.body.version, "0.1.5");
  assert.equal(got.body.urgent, true);

  // /download/daily follows it; /download stays the newest
  store.clear();
  const res = await worker.fetch(new Request("https://usemagpie.ai/download/daily/linux"), env, ctx);
  assert.equal(res.status, 302);
  assert.match(res.headers.get("Location"), /0\.1\.5/);
});
