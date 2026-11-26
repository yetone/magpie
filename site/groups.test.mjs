// node --test site/: shared routing groups (groups.js), on a D1 stood in
// for by node:sqlite.
import { test } from "node:test";
import assert from "node:assert/strict";
import { DatabaseSync } from "node:sqlite";
import { api, page, prune, RATE, ASK_RATE, MAX_FORMAT, MAX_MEMBERS, PER_PAGE } from "./groups.js";
import worker from "./worker.js";

// d1 is the part of D1's API groups.js uses
function d1() {
  const db = new DatabaseSync(":memory:");
  const stmt = (sql, args = []) => ({
    bind: (...a) => {
      if (a.length > 100) throw new Error("D1 binds at most 100 values");
      return stmt(sql, a);
    },
    run: async () => {
      const r = db.prepare(sql).run(...args);
      return { success: true, meta: { changes: Number(r.changes) } };
    },
    first: async () => db.prepare(sql).get(...args) ?? null,
    all: async () => ({ results: db.prepare(sql).all(...args) }),
    async exec() {
      return /^\s*(SELECT|.*RETURNING)/is.test(sql) ? this.all() : this.run();
    },
  });
  return {
    raw: db,
    prepare: (sql) => stmt(sql),
    async batch(list) {
      db.exec("BEGIN");
      try {
        const out = [];
        for (const s of list) out.push(await s.exec());
        db.exec("COMMIT");
        return out;
      } catch (e) {
        db.exec("ROLLBACK");
        throw e;
      }
    },
  };
}

const store = new Map();
globalThis.caches ??= {
  default: {
    match: async (req) => (store.has(req.url) ? new Response(store.get(req.url), { headers: { "Content-Type": "application/json" } }) : undefined),
    put: async (req, res) => void store.set(req.url, await res.text()),
  },
};

function setup() {
  store.clear();
  const env = { SHARES_DB: d1(), SHARE_ADMIN: "admin-secret-token" };
  const waits = [];
  const ctx = { waitUntil: (p) => waits.push(p) };
  return { env, ctx, settle: () => Promise.all(waits.splice(0)) };
}

function req(method, path, { body, token, ip = "198.51.100.7", lang } = {}) {
  const headers = { "CF-Connecting-IP": ip };
  if (token) headers.Authorization = "Bearer " + token;
  if (lang) headers["Accept-Language"] = lang;
  return new Request(`https://usemagpie.ai${path}`, { method, body: body === undefined ? undefined : typeof body === "string" ? body : JSON.stringify(body), headers });
}

const GROUP = {
  magpie: "routing-group",
  version: 2,
  group: {
    id: "coding",
    name: "Coding",
    members: ["anthropic/claude-opus-4-6", "openai/gpt-5.5", "deepseek/deepseek-v4"],
    rules: [{ use: "deepseek/deepseek-v4", intent: "a quick question" }, { use: "openai/gpt-5.5", tokens: 200000 }],
    keys: ["k1"],
    accounts: { "anthropic/claude-opus-4-6": ["me@x"] },
    note: "my own",
  },
};
const meta = (o = {}) => ({ title: "Cheap first", description: "Cheap models first, Opus for hard ones", author: "earayu", public: true, group: GROUP, ...o });

async function share(s, o = {}, ip) {
  const r = await api(req("POST", "/api/groups", { body: meta(o), ip }), s.env, s.ctx);
  assert.equal(r.status, 201, await r.clone().text());
  return r.json();
}
const DEV = (n) => n.toString(16).padStart(32, "0");

test("groups: a shared group is answered back without what the app never shares, its token kept only hashed", async () => {
  const s = setup();
  const got = await share(s);
  assert.match(got.id, /^[a-z2-7]{12}$/);
  assert.equal(got.url, `https://usemagpie.ai/g/${got.id}`);
  assert.equal(got.version, 1);
  assert.ok(got.token.length >= 40);
  assert.equal(JSON.stringify(s.env.SHARES_DB.raw.prepare("SELECT * FROM groups").all()).includes(got.token), false);

  const r = await api(req("GET", `/api/groups/${got.id}`), s.env, s.ctx);
  assert.equal(r.status, 200);
  assert.match(r.headers.get("Cache-Control"), /public, max-age=60/);
  const one = await r.json();
  assert.equal(one.title, "Cheap first");
  assert.equal(one.author, "earayu");
  assert.equal(one.public, true);
  assert.equal(one.version, 1);
  assert.equal(one.uses, 0);
  assert.equal(one.likes, 0);
  assert.deepEqual(one.group.group.members, GROUP.group.members);
  assert.equal(one.group.magpie, "routing-group");
  assert.equal(one.group.version, 2);
  for (const k of ["keys", "accounts", "note"]) assert.equal(k in one.group.group, false, k);
});

test("groups: an update needs the token, or the admin's to take it down; a group taken down is gone", async () => {
  const s = setup();
  const got = await share(s);
  const put = (token, o = {}) => api(req("PUT", `/api/groups/${got.id}`, { body: meta(o), token }), s.env, s.ctx);
  assert.equal((await put("not-the-token")).status, 403);
  assert.equal((await put()).status, 403);
  const up = await put(got.token, { title: "Cheap first, v2", public: false });
  assert.equal(up.status, 200);
  assert.deepEqual(await up.json(), { id: got.id, url: got.url, version: 2 });
  const one = await (await api(req("GET", `/api/groups/${got.id}`), s.env, s.ctx)).json();
  assert.equal(one.title, "Cheap first, v2");
  assert.equal(one.public, false);
  assert.equal(one.version, 2);

  const other = await share(s);
  // a like and a use are dropped with the group
  await api(req("POST", "/api/groups/like", { body: { id: DEV(1), group: other.id, on: true } }), s.env, s.ctx);
  assert.equal((await api(req("DELETE", `/api/groups/${other.id}`, { token: got.token }), s.env, s.ctx)).status, 403);
  const del = await api(req("DELETE", `/api/groups/${other.id}`, { token: "admin-secret-token" }), s.env, s.ctx);
  assert.deepEqual(await del.json(), { deleted: true });
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT COUNT(*) AS n FROM group_likes").get().n, 0);
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT COUNT(*) AS n FROM group_uses").get().n, 0);
  assert.equal((await api(req("DELETE", `/api/groups/${got.id}`, { token: got.token }), s.env, s.ctx)).status, 200);
  for (const id of [got.id, other.id]) {
    assert.equal((await api(req("GET", `/api/groups/${id}`), s.env, s.ctx)).status, 404);
    assert.equal((await api(req("PUT", `/api/groups/${id}`, { body: meta(), token: got.token }), s.env, s.ctx)).status, 404);
    const p = await page(req("GET", `/g/${id}`), s.env);
    assert.equal(p.status, 404);
    assert.match(await p.text(), /isn’t shared any more/);
  }
});

test("groups: what isn't a routing group, or carries a link, is turned away", async () => {
  const s = setup();
  const bad = async (body, want) => {
    const r = await api(req("POST", "/api/groups", { body }), s.env, s.ctx);
    assert.equal(r.status, 400, JSON.stringify(body).slice(0, 120));
    if (want) assert.match((await r.json()).error, want);
  };
  await bad("{not json");
  await bad(meta({ title: "Best at https://relay.example" }), /no links/);
  await bad(meta({ title: "Buy keys at cheapkeys.xyz" }), /no links/);
  await bad(meta({ description: "see www.example" }), /no links/);
  await bad(meta({ author: "relay.vip" }), /no links/);
  await bad(meta({ title: "   " }), /needs a title/);
  await bad(meta({ title: "x".repeat(81) }), /at most 80/);
  await bad(meta({ description: "x".repeat(301) }), /at most 300/);
  await bad(meta({ author: "x".repeat(41) }), /at most 40/);
  await bad(meta({ group: { ...GROUP, magpie: "plugin" } }), /routing group/);
  await bad(meta({ group: { ...GROUP, version: MAX_FORMAT + 1 } }), /version/);
  await bad(meta({ group: { ...GROUP, version: 0 } }), /version/);
  await bad(meta({ group: { ...GROUP, group: { ...GROUP.group, members: Array.from({ length: MAX_MEMBERS + 1 }, (_, i) => `p/m${i}`) } } }), /at most 64/);
  await bad(meta({ group: { ...GROUP, group: { ...GROUP.group, members: ["p/<script>"] } } }), /member/);
  await bad(meta({ group: { ...GROUP, group: { ...GROUP.group, members: [] } } }), /a member, or a pattern/);
  // a pattern alone is a group too
  const r = await api(req("POST", "/api/groups", { body: meta({ group: { ...GROUP, version: 1, group: { id: "c", name: "C", members: [], match: ["claude-*"] } } }) }), s.env, s.ctx);
  assert.equal(r.status, 201);
  // control characters and the marks that turn text around go; a title
  // the app's words have is kept (a model's name with a dot is no link)
  const ok = await share(s, { title: "Opus‮ 4.6 first\u0007", description: "line\none" });
  const one = await (await api(req("GET", `/api/groups/${ok.id}`), s.env, s.ctx)).json();
  assert.equal(one.title, "Opus 4.6 first");
  assert.equal(one.description, "line one");
  // a body over the size is refused before it is read
  const big = await api(req("POST", "/api/groups", { body: JSON.stringify(meta({ description: "x".repeat(70 << 10) })) }), s.env, s.ctx);
  assert.equal(big.status, 413);
});

test("groups: an address shares or updates at most RATE times an hour", async () => {
  const s = setup();
  const now = Date.UTC(2026, 10, 26, 10);
  let first;
  for (let i = 0; i < RATE; i++) {
    const r = await api(req("POST", "/api/groups", { body: meta() }), s.env, s.ctx, now);
    assert.equal(r.status, 201);
    first ??= await r.json();
  }
  assert.equal((await api(req("POST", "/api/groups", { body: meta() }), s.env, s.ctx, now)).status, 429);
  assert.equal((await api(req("PUT", `/api/groups/${first.id}`, { body: meta(), token: first.token }), s.env, s.ctx, now)).status, 429);
  // another address, and the next hour, are not held
  assert.equal((await api(req("POST", "/api/groups", { body: meta(), ip: "203.0.113.9" }), s.env, s.ctx, now)).status, 201);
  assert.equal((await api(req("POST", "/api/groups", { body: meta() }), s.env, s.ctx, now + 36e5)).status, 201);
  // reports and likes are counted apart from shares
  assert.equal((await api(req("POST", "/api/groups/report", { body: { id: DEV(1), groups: [first.id] } }), s.env, s.ctx, now)).status, 200);
  // the address itself is kept only hashed
  assert.equal(JSON.stringify(s.env.SHARES_DB.raw.prepare("SELECT * FROM asks").all()).includes("198.51.100.7"), false);
});

test("groups: the list has the listed groups alone, the most used or the newest first, and finds by name or model", async () => {
  const s = setup();
  const now = Date.UTC(2026, 10, 26, 10);
  const a = await share(s, { title: "Alpha" });
  const b = await share(s, { title: "Beta 100% off_peak", group: { ...GROUP, group: { ...GROUP.group, members: ["zhipu/glm-5", "moonshot/kimi-k3"] } } });
  const hidden = await share(s, { title: "Hidden one", public: false });
  // b is used by two apps, a by one
  for (const [d, ids] of [[1, [b.id]], [2, [b.id, a.id, hidden.id]]])
    assert.equal((await api(req("POST", "/api/groups/report", { body: { id: DEV(d), groups: ids } }), s.env, s.ctx, now)).status, 200);
  const list = async (q) => {
    const r = await api(req("GET", "/api/groups" + q), s.env, s.ctx, now);
    assert.equal(r.status, 200);
    return r.json();
  };
  const top = await list("?sort=top");
  assert.deepEqual(top.groups.map((g) => g.title), ["Beta 100% off_peak", "Alpha"]);
  assert.equal(top.groups[0].uses, 2);
  assert.deepEqual(top.groups[0].members, ["zhipu/glm-5", "moonshot/kimi-k3"]);
  assert.equal(top.groups[0].rules, 2);
  assert.equal(top.more, false);
  assert.equal("token" in top.groups[0], false);
  s.env.SHARES_DB.raw.prepare("UPDATE groups SET created = created + 1000 WHERE id = ?").run(a.id);
  store.clear();
  assert.deepEqual((await list("?sort=new")).groups.map((g) => g.title), ["Alpha", "Beta 100% off_peak"]);
  assert.deepEqual((await list("?q=KIMI")).groups.map((g) => g.title), ["Beta 100% off_peak"]);
  assert.deepEqual((await list("?q=100%25")).groups.map((g) => g.title), ["Beta 100% off_peak"]);
  // % and _ are the letters they are, not LIKE's wildcards
  assert.deepEqual((await list("?q=a%25")).groups, []);
  assert.deepEqual((await list("?q=hidden")).groups, []);
  // the version check names any group, listed or not, and nothing else
  const ids = await list(`?ids=${a.id},${hidden.id},aaaaaaaaaaaa`);
  assert.deepEqual(ids.groups.map((g) => g.id).sort(), [a.id, hidden.id].sort());
  assert.ok(ids.groups.every((g) => Object.keys(g).sort().join() === "id,title,updated,version"));
  assert.equal((await api(req("GET", "/api/groups?ids=nope"), s.env, s.ctx, now)).status, 400);
  // pages of PER_PAGE
  for (let i = 0; i < PER_PAGE; i++) await share(s, { title: "More " + i }, "203.0.113." + (i % 3));
  store.clear();
  const p0 = await list("?sort=new");
  assert.equal(p0.groups.length, PER_PAGE);
  assert.equal(p0.more, true);
  const p1 = await list("?sort=new&page=1");
  assert.equal(p1.groups.length, 2);
  assert.equal(p1.more, false);
});

test("groups: the list is kept at the edge; the version check never is", async () => {
  const s = setup();
  await share(s, { title: "First" });
  const r = await api(req("GET", "/api/groups"), s.env, s.ctx);
  assert.match(r.headers.get("Cache-Control"), /max-age=300/);
  await s.settle();
  assert.equal(store.size, 1);
  await share(s, { title: "Second" });
  assert.equal((await (await api(req("GET", "/api/groups?sort=top"), s.env, s.ctx)).json()).groups.length, 1);
  const ids = await api(req("GET", "/api/groups?ids=aaaaaaaaaaaa"), s.env, s.ctx);
  assert.equal(ids.headers.get("Cache-Control"), "no-store");
  await s.settle();
  assert.equal(store.size, 1);
});

test("groups: a report replaces what an app had, and names no group that isn't shared", async () => {
  const s = setup();
  const now = Date.UTC(2026, 10, 26, 10);
  const a = await share(s), b = await share(s);
  const rep = (groups, id = DEV(7)) => api(req("POST", "/api/groups/report", { body: { id, groups } }), s.env, s.ctx, now);
  assert.equal((await rep([a.id, b.id, "aaaaaaaaaaaa"])).status, 200);
  const rows = () => s.env.SHARES_DB.raw.prepare("SELECT gid FROM group_uses WHERE device = ? ORDER BY gid").all(DEV(7)).map((r) => r.gid);
  assert.deepEqual(rows(), [a.id, b.id].sort());
  await rep([b.id]);
  assert.deepEqual(rows(), [b.id]);
  await rep([]);
  assert.deepEqual(rows(), []);
  assert.equal((await rep([a.id], "not-an-id")).status, 400);
  assert.equal((await rep(["UPPER"])).status, 400);
  assert.equal((await rep(Array.from({ length: 51 }, () => a.id))).status, 400);
  assert.equal((await api(req("GET", "/api/groups/report"), s.env, s.ctx)).status, 405);
});

test("groups: a like is once an app, taken back, and counts the app as a user", async () => {
  const s = setup();
  const now = Date.UTC(2026, 10, 26, 10);
  const a = await share(s);
  const like = async (device, on) => {
    const r = await api(req("POST", "/api/groups/like", { body: { id: DEV(device), group: a.id, on } }), s.env, s.ctx, now);
    assert.equal(r.status, 200);
    return (await r.json()).likes;
  };
  assert.equal(await like(1, true), 1);
  assert.equal(await like(1, true), 1);
  assert.equal(await like(2, true), 2);
  assert.equal(await like(1, false), 1);
  const one = await (await api(req("GET", `/api/groups/${a.id}`), s.env, s.ctx, now)).json();
  assert.equal(one.likes, 1);
  assert.equal(one.uses, 2);
  assert.equal((await api(req("POST", "/api/groups/like", { body: { id: DEV(1), group: "aaaaaaaaaaaa", on: true } }), s.env, s.ctx, now)).status, 404);
  // reports and likes are held to ASK_RATE an hour
  for (let i = 0; i < ASK_RATE; i++) await api(req("POST", "/api/groups/like", { body: { id: DEV(3), group: a.id, on: true }, ip: "192.0.2.1" }), s.env, s.ctx, now);
  assert.equal((await api(req("POST", "/api/groups/like", { body: { id: DEV(3), group: a.id, on: true }, ip: "192.0.2.1" }), s.env, s.ctx, now)).status, 429);
});

test("groups: a use counts for a week, and is pruned after a month", async () => {
  const s = setup();
  const now = Date.UTC(2026, 10, 26, 10);
  const a = await share(s);
  await api(req("POST", "/api/groups/report", { body: { id: DEV(1), groups: [a.id] } }), s.env, s.ctx, now);
  const uses = async (t) => (await (await api(req("GET", `/api/groups/${a.id}`), s.env, s.ctx, t)).json()).uses;
  assert.equal(await uses(now + 6 * 864e5), 1);
  assert.equal(await uses(now + 8 * 864e5), 0);
  await prune(s.env, now + 29 * 864e5);
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT COUNT(*) AS n FROM group_uses").get().n, 1);
  await prune(s.env, now + 31 * 864e5);
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT COUNT(*) AS n FROM group_uses").get().n, 0);
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT COUNT(*) AS n FROM asks").get().n, 0);
});

test("groups: a group's page shows its models, routing and rules, escaped, with Open in magpie", async () => {
  const s = setup();
  const got = await share(s, { title: `<script>alert(1)</script> & "quotes"`, description: "<img src=x onerror=alert(1)>", author: "<b>me</b>" });
  const r = await page(req("GET", `/g/${got.id}`), s.env);
  assert.equal(r.status, 200);
  assert.match(r.headers.get("Cache-Control"), /no-transform/);
  const csp = r.headers.get("Content-Security-Policy");
  const nonce = csp.match(/'nonce-([^']+)'/)[1];
  const html = await r.text();
  assert.equal(html.includes("<script>alert(1)"), false);
  assert.equal(html.includes("<img src=x"), false);
  assert.equal(html.includes("<b>me</b>"), false);
  assert.ok(html.includes("&lt;script&gt;alert(1)&lt;/script&gt; &amp; &quot;quotes&quot;"));
  // every script the page runs carries the response's nonce
  const scripts = [...html.matchAll(/<script([^>]*)>/g)].map((m) => m[1]);
  assert.ok(scripts.length >= 2);
  for (const a of scripts) assert.equal(a, ` nonce="${nonce}"`);
  assert.ok(html.includes(`href="magpie://group?id=${got.id}"`));
  assert.ok(html.includes("anthropic/claude-opus-4-6</span><span class=\"arr\">→</span>"));
  assert.match(html, /“a quick question”/);
  assert.match(html, /≥ 200,000 tokens/);
  assert.match(html, /<meta property="og:description" content="A routing group for magpie: anthropic\/claude-opus-4-6 → openai\/gpt-5.5 → deepseek\/deepseek-v4">/);
  assert.match(html, /og:image" content="https:\/\/usemagpie.ai\/img\/og.png"/);
  assert.match(html, /mailto:yetoneful@gmail.com\?subject=Report%20a%20shared%20routing%20group%3A%20/);
  assert.equal(html.includes('name="robots"'), false);
  // the text to copy is the shared group itself, without what the app
  // never shares
  const text = html.match(/<pre id="text">([\s\S]*?)<\/pre>/)[1].replace(/&quot;/g, '"').replace(/&amp;/g, "&").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&#39;/g, "'");
  const shared = JSON.parse(text);
  assert.equal(shared.magpie, "routing-group");
  assert.equal("keys" in shared.group, false);
  // HEAD has no body
  assert.equal(await (await page(req("HEAD", `/g/${got.id}`), s.env)).text(), "");
});

test("groups: an unlisted group's page isn't indexed; the page speaks the browser's language", async () => {
  const s = setup();
  const got = await share(s, { public: false, group: { ...GROUP, group: { ...GROUP.group, routing: "rotate" } } });
  const html = await (await page(req("GET", `/g/${got.id}`), s.env)).text();
  assert.match(html, /<meta name="robots" content="noindex">/);
  assert.match(html, /Each in turn/);
  assert.ok(html.includes('<span class="arr">·</span>'));
  const zh = await (await page(req("GET", `/g/${got.id}`, { lang: "zh-CN,zh;q=0.9,en;q=0.8" }), s.env)).text();
  assert.match(zh, /在 magpie 中打开/);
  assert.match(zh, /<html lang="zh-CN">/);
  const ja = await (await page(req("GET", `/g/${got.id}?lang=ja`, { lang: "zh-CN" }), s.env)).text();
  assert.match(ja, /magpie で開く/);
  assert.ok(ja.includes('href="/groups?lang=ja"'));
  const en = await (await page(req("GET", `/g/${got.id}`, { lang: "fr-FR,de" }), s.env)).text();
  assert.match(en, /Open in magpie/);
});

test("groups: the gallery lists the listed groups, finds and pages", async () => {
  const s = setup();
  await share(s, { title: "Alpha <x>", author: "ann" });
  await share(s, { title: "Hidden", public: false });
  const r = await page(req("GET", "/groups"), s.env);
  assert.equal(r.status, 200);
  const html = await r.text();
  assert.ok(html.includes("Alpha &lt;x&gt;"));
  assert.equal(html.includes("Hidden"), false);
  assert.match(html, /by ann/);
  assert.match(html, /<form class="find" method="get" action="\/groups"/);
  assert.match(html, /aria-current="page">Popular/);
  const none = await (await page(req("GET", "/groups?q=nothing-like-it&sort=new"), s.env)).text();
  assert.match(none, /Nothing here yet/);
  assert.match(none, /aria-current="page">New/);
  assert.match(none, /noindex/);
  assert.equal(await page(req("POST", "/groups"), s.env), null);
  assert.equal(await page(req("GET", "/g/short"), s.env), null);
});

test("groups: the worker routes the pages and the API", async () => {
  const s = setup();
  const r = await worker.fetch(req("POST", "/api/groups", { body: meta({ title: "Via worker" }) }), s.env, s.ctx);
  assert.equal(r.status, 201);
  const got = await r.json();
  assert.match(await (await worker.fetch(req("GET", `/g/${got.id}`), s.env, s.ctx)).text(), /Via worker/);
  assert.match(await (await worker.fetch(req("GET", "/groups"), s.env, s.ctx)).text(), /Via worker/);
  assert.equal((await worker.fetch(req("GET", `/api/groups/${got.id}`), s.env, s.ctx)).status, 200);
  assert.equal((await worker.fetch(req("GET", "/api/groups"), s.env, s.ctx)).status, 200);
  assert.equal((await worker.fetch(req("POST", "/api/groups/like", { body: { id: DEV(1), group: got.id, on: true } }), s.env, s.ctx)).status, 200);
  assert.equal((await worker.fetch(req("POST", "/api/groups/report", { body: { id: DEV(1), groups: [] } }), s.env, s.ctx)).status, 200);
  assert.equal((await worker.fetch(req("GET", "/api/groups/nope"), s.env, s.ctx)).status, 404);
  // no database: sharing says so, and a group's page is gone
  assert.equal((await worker.fetch(req("GET", "/api/groups"), {}, s.ctx)).status, 503);
  assert.equal((await worker.fetch(req("GET", `/g/${got.id}`), {}, s.ctx)).status, 404);
});
