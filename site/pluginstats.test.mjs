// node --test site/: Discover's counts (pluginstats.js), on a D1 stood in
// for by node:sqlite.
import { test } from "node:test";
import assert from "node:assert/strict";
import { DatabaseSync } from "node:sqlite";
import { handle, prune, RATE, MOST } from "./pluginstats.js";

// d1 is the part of D1's API pluginstats.js uses
function d1() {
  const db = new DatabaseSync(":memory:");
  const stmt = (sql, args = []) => ({
    bind: (...a) => stmt(sql, a),
    run: async () => (db.prepare(sql).run(...args), { success: true }),
    first: async () => db.prepare(sql).get(...args) ?? null,
    all: async () => ({ results: db.prepare(sql).all(...args) }),
    async exec() {
      return /^\s*(SELECT|.*RETURNING)/is.test(sql) ? this.all() : this.run().then(() => ({ results: [] }));
    },
  });
  return {
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
globalThis.caches = {
  default: {
    match: async (req) => (store.has(req.url) ? new Response(store.get(req.url)) : undefined),
    put: async (req, res) => void store.set(req.url, await res.text()),
  },
};
const ctx = { waitUntil: (p) => p };
const id = (n) => n.toString(16).padStart(32, "0");
const DAY = 864e5;
const T0 = Date.parse("2026-11-10T12:00:00Z");

function ask(env, path, body, now = T0, ip = "203.0.113.1") {
  const req = new Request("https://usemagpie.ai" + path, {
    method: body === undefined ? "GET" : "POST",
    headers: { "CF-Connecting-IP": ip, "Content-Type": "application/json" },
    body: body === undefined ? undefined : typeof body === "string" ? body : JSON.stringify(body),
  });
  return handle(req, env, ctx, now);
}
async function counts(env, now = T0) {
  store.clear();
  return (await (await ask(env, "/api/plugins/stats", undefined, now)).json()).plugins;
}

test("users are the ids that reported a plugin in the last week", async () => {
  const env = { PLUGINS: d1() };
  assert.equal((await ask(env, "/api/plugins/report", { id: id(1), plugins: ["opencode-a", "github:alice/b"] })).status, 200);
  await ask(env, "/api/plugins/report", { id: id(2), plugins: ["opencode-a"] });
  // the same id again is one user
  await ask(env, "/api/plugins/report", { id: id(2), plugins: ["opencode-a"] }, T0 + DAY);
  assert.deepEqual(await counts(env, T0 + DAY), { "opencode-a": { users: 2 }, "github:alice/b": { users: 1 } });
  // an id that removed a plugin stops counting for it at its next report
  await ask(env, "/api/plugins/report", { id: id(1), plugins: [] }, T0 + DAY);
  assert.deepEqual(await counts(env, T0 + DAY), { "opencode-a": { users: 1 } });
  // eight days on without a report, nobody
  assert.deepEqual(await counts(env, T0 + 9 * DAY), {});
  // the cron drops rows a month old
  await prune(env, T0 + 40 * DAY);
  assert.equal((await env.PLUGINS.prepare("SELECT COUNT(*) AS n FROM installs").first()).n, 0);
});

test("a like is one per id, can be taken back, and counts the id as a user", async () => {
  const env = { PLUGINS: d1() };
  const like = async (n, on) => (await (await ask(env, "/api/plugins/like", { id: id(n), plugin: "opencode-a", on })).json()).likes;
  assert.equal(await like(1, true), 1);
  assert.equal(await like(1, true), 1);
  assert.equal(await like(2, true), 2);
  assert.equal(await like(1, false), 1);
  assert.deepEqual(await counts(env), { "opencode-a": { users: 2, likes: 1 } });
});

test("the counts are held at the edge for an hour", async () => {
  const env = { PLUGINS: d1() };
  await ask(env, "/api/plugins/report", { id: id(1), plugins: ["opencode-a"] });
  store.clear();
  const first = await ask(env, "/api/plugins/stats");
  assert.match(first.headers.get("Cache-Control"), /max-age=3600/);
  await ask(env, "/api/plugins/report", { id: id(2), plugins: ["opencode-a"] });
  assert.deepEqual((await (await ask(env, "/api/plugins/stats")).json()).plugins, { "opencode-a": { users: 1 } });
});

test("only an app's id and the names Discover lists are taken", async () => {
  const env = { PLUGINS: d1() };
  const bad = [
    "not json",
    { plugins: ["opencode-a"] },
    { id: "me@example.com", plugins: ["opencode-a"] },
    { id: id(1), plugins: "opencode-a" },
    { id: id(1), plugins: ["/Users/me/secret-plugin"] },
    { id: id(1), plugins: ["git+ssh://git@corp.example/x.git"] },
    { id: id(1), plugins: ["https://github.com/a/b"] },
    { id: id(1), plugins: ["x".repeat(215)] },
    { id: id(1), plugins: Array.from({ length: MOST + 1 }, (_, i) => `p${i}`) },
  ];
  for (const b of bad) assert.equal((await ask(env, "/api/plugins/report", b)).status, 400, JSON.stringify(b).slice(0, 80));
  for (const b of [{ id: id(1), plugin: "../x", on: true }, { id: id(1), plugin: "opencode-a", on: "yes" }])
    assert.equal((await ask(env, "/api/plugins/like", b)).status, 400);
  assert.deepEqual(await counts(env), {});
  // the caller's address is kept only hashed
  const rows = await env.PLUGINS.prepare("SELECT who FROM asks").all();
  assert.ok(rows.results.length > 0 && rows.results.every((r) => !r.who.includes("203.0.113")));
});

test("an address makes RATE asks an hour", async () => {
  const env = { PLUGINS: d1() };
  for (let i = 0; i < RATE; i++)
    assert.equal((await ask(env, "/api/plugins/like", { id: id(i), plugin: "opencode-a", on: true })).status, 200);
  assert.equal((await ask(env, "/api/plugins/like", { id: id(99), plugin: "opencode-a", on: true })).status, 429);
  // another address, or the next hour, is let through
  assert.equal((await ask(env, "/api/plugins/like", { id: id(99), plugin: "opencode-a", on: true }, T0, "198.51.100.2")).status, 200);
  assert.equal((await ask(env, "/api/plugins/like", { id: id(98), plugin: "opencode-a", on: true }, T0 + 36e5)).status, 200);
});

test("without the database, the counts are refused, not made up", async () => {
  assert.equal((await ask({}, "/api/plugins/stats")).status, 503);
  assert.equal(await handle(new Request("https://usemagpie.ai/api/plugins/other"), {}, ctx), null);
});
