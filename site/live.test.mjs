// node --test site/: live shared artifacts (live.js): visitors, sessions,
// the share's data and its rooms, on D1 and a Durable Object's SQLite
// stood in for by node:sqlite, and its sockets by objects that keep what
// they were sent.
import { test } from "node:test";
import assert from "node:assert/strict";
import { DatabaseSync } from "node:sqlite";
import { api as shareAPI, page } from "./shares.js";
import { api, ShareLive, mint, claimsOf, TOKEN_TTL, placeOf } from "./live.js";
import { LEVEL } from "./livedb.js";
import { bucket } from "./r2stub.mjs";
import { d1 } from "./d1stub.mjs";

// a socket's two ends: what the object sends reaches the page's end
class Sock {
  constructor() {
    this.got = [];
    this.closed = null;
    this.att = null;
    this.peer = null;
  }
  send(text) {
    if (this.closed) throw new Error("closed");
    this.peer.got.push(JSON.parse(text));
  }
  close(code, why) {
    this.closed = { code, why };
    this.peer.closed = { code, why };
  }
  serializeAttachment(a) {
    this.att = structuredClone(a);
  }
  deserializeAttachment() {
    return structuredClone(this.att);
  }
}
globalThis.WebSocketPair = function () {
  const a = new Sock(), b = new Sock();
  a.peer = b;
  b.peer = a;
  return [a, b];
};

function doStorage() {
  const db = new DatabaseSync(":memory:");
  return {
    db,
    sql: {
      exec(q, ...b) {
        const st = db.prepare(q);
        const rows = /^\s*(SELECT|PRAGMA|WITH)\b/i.test(q) ? st.all(...b) : (st.run(...b), []);
        return {
          toArray: () => rows,
          one() {
            if (rows.length !== 1) throw new Error(`Expected exactly one result from SQL query, but got ${rows.length}.`);
            return rows[0];
          },
          [Symbol.iterator]: () => rows[Symbol.iterator](),
        };
      },
    },
    transactionSync(fn) {
      db.exec("BEGIN");
      try {
        const r = fn();
        db.exec("COMMIT");
        return r;
      } catch (e) {
        db.exec("ROLLBACK");
        throw e;
      }
    },
    async deleteAll() {
      for (const { name } of db.prepare("SELECT name FROM sqlite_master WHERE type = 'table'").all()) db.exec(`DROP TABLE "${name}"`);
    },
  };
}

// namespace is the LIVE binding: one object a name, made on first use
function namespace() {
  const objs = new Map();
  return {
    objs,
    idFromName: (n) => n,
    get(id) {
      if (!objs.has(id)) {
        const socks = [];
        const ctx = {
          storage: doStorage(),
          acceptWebSocket: (ws, tags) => socks.push({ ws, tags }),
          getWebSockets: (tag) => socks.filter((s) => !s.ws.closed && (tag == null || s.tags.includes(tag))).map((s) => s.ws),
        };
        objs.set(id, new ShareLive(ctx, {}));
      }
      return { fetch: (req) => objs.get(id).fetch(req) };
    },
  };
}

function setup() {
  const env = { SHARES_DB: d1(), SHARES: bucket(), LIVE: namespace(), LIVE_KEY: "test-live-key-0123456789", SHARE_ADMIN: "admin-secret-token" };
  const waits = [];
  const ctx = { waitUntil: (p) => waits.push(p) };
  return { env, ctx, settle: () => Promise.all(waits.splice(0)) };
}

const OWNER_ID = "u_0123456789abcdef01234567";
const CAPS = {
  db: { rules: [{ path: "votes", read: "view", write: "owner" }, { path: "votes/{self}", write: "interact" }] },
  room: { topics: { move: "interact" } },
  user: { scopes: ["profile"] },
};
const PAGE = "<!doctype html><title>Poll</title><p>vote";

function form(meta, data) {
  const f = new FormData();
  f.set("meta", JSON.stringify({ title: "Poll", description: "", files: ["index.html"], ...meta }));
  f.set("0", new Blob([PAGE]));
  if (data !== undefined) f.set("data", new Blob([JSON.stringify(data)]));
  return f;
}
function req(method, path, { body, token, json, ip = "198.51.100.7", headers = {} } = {}) {
  const h = { "CF-Connecting-IP": ip, ...headers };
  if (token) h.Authorization = "Bearer " + token;
  if (json !== undefined) {
    h["Content-Type"] = "application/json";
    body = JSON.stringify(json);
  }
  return new Request(`https://usemagpie.ai${path}`, { method, body, headers: h });
}
async function share(s, caps = CAPS, data = [{ path: "votes/" + OWNER_ID, data: { pick: "a" } }, { path: "config/c", data: { q: "Lunch?" } }]) {
  const r = await shareAPI(req("POST", "/api/shares", { body: form({ capabilities: caps, owner: OWNER_ID }, data) }), s.env, s.ctx);
  assert.equal(r.status, 201, await r.clone().text());
  return r.json();
}
const call = (s, path, opts) => api(req(opts?.method || "POST", path, opts), s.env, s.ctx);
async function visitor(s, name) {
  const r = await call(s, "/api/visitors", { json: { name } });
  assert.equal(r.status, 201, await r.clone().text());
  return r.json();
}
async function session(s, id, body = {}, token) {
  const r = await call(s, `/api/shares/${id}/session`, { json: body, token });
  assert.equal(r.status, 200, await r.clone().text());
  return r.json();
}
const db = (s, id, t, c) => call(s, `/api/shares/${id}/rt/db`, { json: { t, ...c } });

test("live: a visitor signs in by name alone, and can rename", async () => {
  const s = setup();
  const v = await visitor(s, "  Ann‮  Lee\u0000 ");
  assert.equal(v.name, "Ann Lee");
  assert.match(v.id, /^[a-z2-7]{16}$/);
  assert.ok(v.key.length >= 40);
  // only the key's hash is kept
  assert.equal(JSON.stringify(s.env.SHARES_DB.raw.prepare("SELECT * FROM visitors").all()).includes(v.key), false);
  assert.equal((await call(s, "/api/visitors", { json: { name: " ​ " } })).status, 400);
  assert.equal((await call(s, "/api/visitors", { json: { name: "x".repeat(100) } })).status, 201);
  const me = await call(s, "/api/visitors/me", { method: "GET", token: v.key });
  assert.deepEqual(await me.json(), { id: v.id, name: "Ann Lee" });
  const ren = await call(s, "/api/visitors/me", { method: "PATCH", token: v.key, json: { name: "Ann" } });
  assert.deepEqual(await ren.json(), { id: v.id, name: "Ann" });
  assert.equal((await call(s, "/api/visitors/me", { method: "GET", token: "not-a-key" })).status, 401);
});

test("live: a session names the viewer: nobody, a visitor, or the owner by a one-time code", async () => {
  const s = setup();
  const sh = await share(s);
  const anon = await session(s, sh.id);
  assert.deepEqual(anon.viewer, { id: "", name: "", level: "view", guest: false, owner: false });
  assert.deepEqual(anon.caps, CAPS);
  assert.equal(anon.api, `https://usemagpie.ai/api/shares/${sh.id}/rt`);
  const c = await claimsOf(s.env, anon.token, sh.id);
  assert.equal(c.l, LEVEL.view);
  // another share's, a changed one, and an old one are no session
  assert.equal(await claimsOf(s.env, anon.token, "aaaaaaaaaaaaaaaa"), null);
  assert.equal(await claimsOf(s.env, anon.token.replace(/.$/, (x) => (x === "A" ? "B" : "A")), sh.id), null);
  assert.equal(await claimsOf(s.env, anon.token, sh.id, Date.now() + TOKEN_TTL + 1), null);
  assert.equal(await claimsOf({ LIVE_KEY: "another-key-0123456789" }, anon.token, sh.id), null);

  const v = await visitor(s, "Bo");
  const vs = await session(s, sh.id, { visitor: v.key });
  assert.deepEqual(vs.viewer, { id: v.id, name: "Bo", level: "interact", guest: true, owner: false });

  // a code only the share's token gets, good once
  assert.equal((await call(s, `/api/shares/${sh.id}/owner`, { token: "wrong" })).status, 403);
  const got = await (await call(s, `/api/shares/${sh.id}/owner`, { token: sh.token })).json();
  assert.equal(got.url, `https://usemagpie.ai/a/${sh.id}#owner=${got.code}`);
  const os = await session(s, sh.id, { code: got.code });
  assert.deepEqual(os.viewer, { id: OWNER_ID, name: "", level: "owner", guest: false, owner: true });
  assert.ok(os.ownerKey);
  assert.equal((await session(s, sh.id, { code: got.code })).viewer.level, "view");
  // the key it traded for keeps them the owner; signed in, by their name
  const again = await session(s, sh.id, { owner: os.ownerKey, visitor: v.key });
  assert.deepEqual(again.viewer, { id: OWNER_ID, name: "Bo", level: "owner", guest: false, owner: true });
  assert.equal(again.ownerKey, undefined);
  // not another share's owner
  const other = await share(s);
  assert.equal((await session(s, other.id, { owner: os.ownerKey })).viewer.level, "view");
  // an expired code is no code
  const late = await (await call(s, `/api/shares/${sh.id}/owner`, { token: sh.token })).json();
  s.env.SHARES_DB.raw.prepare("UPDATE codes SET exp = 1").run();
  assert.equal((await session(s, sh.id, { code: late.code })).viewer.level, "view");
});

test("live: the page's data is seeded, read by its rules, and written by who may", async () => {
  const s = setup();
  const sh = await share(s);
  const anon = await session(s, sh.id);
  const v = await visitor(s, "Bo");
  const vs = await session(s, sh.id, { visitor: v.key });

  let r = await db(s, sh.id, anon.token, { op: "get", path: "config/c" });
  assert.equal(r.status, 200);
  assert.equal(r.headers.get("Access-Control-Allow-Origin"), "*");
  assert.deepEqual((await r.json()).doc.data, { q: "Lunch?" });
  // nobody signed in writes nothing; a visitor writes their own vote only
  r = await db(s, sh.id, anon.token, { op: "set", path: "votes/x", data: { pick: "b" } });
  assert.equal(r.status, 400);
  // refused as the store on the computer refuses it
  assert.equal((await r.json()).error.code, "invalid_argument");
  r = await db(s, sh.id, vs.token, { op: "set", path: "votes/" + v.id, data: { pick: "b" } });
  assert.equal(r.status, 200, await r.clone().text());
  r = await db(s, sh.id, vs.token, { op: "set", path: "votes/" + OWNER_ID, data: { pick: "z" } });
  assert.equal((await r.json()).error.code, "invalid_argument");
  r = await db(s, sh.id, anon.token, { op: "query", collection: "votes" });
  assert.deepEqual((await r.json()).docs.map((d) => d.data.pick).sort(), ["a", "b"]);

  // no token, a bad one, or another share's is refused
  assert.equal((await db(s, sh.id, undefined, { op: "get", path: "config/c" })).status, 401);
  assert.equal((await db(s, sh.id, "x.y", { op: "get", path: "config/c" })).status, 401);
  const other = await share(s);
  assert.equal((await db(s, other.id, vs.token, { op: "get", path: "config/c" })).status, 401);
  // the share's token is the agent, as the owner
  r = await call(s, `/api/shares/${sh.id}/rt/db`, { token: sh.token, json: { op: "set", path: "votes/" + OWNER_ID, data: { pick: "c" } } });
  assert.equal(r.status, 200, await r.clone().text());

  // magpie takes it back with the token
  assert.equal((await call(s, `/api/shares/${sh.id}/data`, { method: "GET" })).status, 403);
  const all = await (await call(s, `/api/shares/${sh.id}/data`, { method: "GET", token: sh.token })).json();
  assert.equal(all.held, true);
  assert.deepEqual(all.docs.map((d) => [d.path, d.data]).sort(), [["config/c", { q: "Lunch?" }], ["votes/" + OWNER_ID, { pick: "c" }], ["votes/" + v.id, { pick: "b" }]].sort());
  for (const d of all.docs) assert.ok(d.version >= 1 && Date.parse(d.updated) > 0);
});

test("live: data that isn't taken refuses the share whole", async () => {
  const s = setup();
  const r = await shareAPI(req("POST", "/api/shares", { body: form({ capabilities: CAPS }, [{ path: "votes", data: {} }]) }), s.env, s.ctx);
  assert.equal(r.status, 400);
  assert.match((await r.json()).error, /data wasn't taken/);
  await s.settle();
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT count(*) n FROM shares").get().n, 0);
  assert.equal(s.env.SHARES.objs.size, 0);
  for (const bad of ["{", "{}"]) {
    const f = form({ capabilities: CAPS });
    f.set("data", new Blob([bad]));
    assert.equal((await shareAPI(req("POST", "/api/shares", { body: f }), s.env, s.ctx)).status, 400);
  }
  const f = form({ capabilities: CAPS, owner: "someone" });
  assert.equal((await shareAPI(req("POST", "/api/shares", { body: f }), s.env, s.ctx)).status, 400);
});

// open is a page's socket on the share's object, as live.js's rt/live
// hands it the viewer
async function open(s, id, sess) {
  const c = await claimsOf(s.env, sess.token, id);
  s.env.LIVE.get(id);
  const obj = s.env.LIVE.objs.get(id);
  const ws = obj.accept({ id: c.v, name: c.n, level: c.l, guest: !!c.g, exp: c.e });
  const say = (m) => obj.webSocketMessage(ws.peer, JSON.stringify(m));
  const hi = ws.got.shift();
  assert.equal(hi.t, "hi");
  return { ws, say, sid: hi.sid, obj, take: () => ws.got.splice(0) };
}

test("live: the room: who may join, emit and set presence, and who hears it", async () => {
  const s = setup();
  const sh = await share(s);
  const a = await visitor(s, "Ann"), b = await visitor(s, "Bo");
  const A = await open(s, sh.id, await session(s, sh.id, { visitor: a.key }));
  const B = await open(s, sh.id, await session(s, sh.id, { visitor: b.key }));
  const N = await open(s, sh.id, await session(s, sh.id));

  await N.say({ t: "join", room: "", ref: 1 });
  assert.deepEqual(N.take(), [{ t: "error", ref: 1, code: "not_granted", message: "sign in to be in the room" }]);
  await A.say({ t: "join", room: "", ref: 1 });
  let [j] = A.take();
  assert.equal(j.t, "joined");
  assert.deepEqual(j.peers.map((p) => [p.peer, p.isMe, p.sameTab, p.guest, p.by]), [[A.sid, true, true, true, a.id]]);
  await B.say({ t: "join", room: "", ref: 2 });
  assert.equal(B.take()[0].peers.length, 2);
  assert.deepEqual(A.take().map((m) => [m.t, m.peer.peer, m.peer.isMe]), [["peer", B.sid, false]]);

  // a topic opened to interact; any other is the owner's
  await B.say({ t: "emit", room: "", topic: "move", data: { x: 1 } });
  assert.deepEqual(A.take(), [{ t: "msg", room: "", topic: "move", data: { x: 1 }, from: { peer: B.sid, by: b.id, isMe: false, sameTab: false, kind: "viewer", guest: true } }]);
  assert.equal(B.take()[0].from.sameTab, true);
  await B.say({ t: "emit", room: "", topic: "slide", data: 2, ref: 3 });
  assert.equal(B.take()[0].code, "not_permitted");
  assert.deepEqual(A.take(), []);
  assert.equal(N.take().length, 0);

  // presence merges, null drops a key, and is bounded
  await A.say({ t: "presence", room: "", patch: { cursor: [1, 2], color: "red" } });
  await A.say({ t: "presence", room: "", patch: { color: null } });
  const ps = B.take();
  assert.deepEqual(ps.at(-1).peer.presence, { cursor: [1, 2] });
  await A.say({ t: "presence", room: "", patch: { big: "x".repeat(5000) }, ref: 4 });
  assert.equal(A.take()[0].code, "invalid_argument");
  await A.say({ t: "presence", room: "", patch: JSON.parse('{"__proto__": 1}'), ref: 5 });
  assert.equal(A.take()[0].code, "invalid_argument");

  // a named room is heard only by who joined it
  await A.say({ t: "join", room: "game-1" });
  A.take();
  await A.say({ t: "emit", room: "game-1", topic: "move", data: 1 });
  assert.deepEqual(B.take(), []);
  // leaving, or the socket going, tells the rest
  await B.say({ t: "leave", room: "" });
  assert.deepEqual(A.take().map((m) => [m.t, m.peer]), [
    ["msg", undefined],
    ["gone", B.sid],
  ]);
  await A.obj.webSocketClose(A.ws.peer);
  A.ws.peer.closed = { code: 1000 };
  await B.say({ t: "join", room: "game-1" });
  assert.deepEqual(B.take()[0].peers.map((p) => p.peer), [B.sid]);
});

test("live: the bar's room is anyone signed in, by name, and carries nothing", async () => {
  const s = setup();
  // a page with db alone: no room of its own, the bar's still
  const sh = await share(s, { db: {} }, []);
  const v = await visitor(s, "Ann");
  const o = await (await call(s, `/api/shares/${sh.id}/owner`, { token: sh.token })).json();
  const A = await open(s, sh.id, await session(s, sh.id, { visitor: v.key }));
  const O = await open(s, sh.id, await session(s, sh.id, { code: o.code }));
  const N = await open(s, sh.id, await session(s, sh.id));
  await A.say({ t: "join", room: "~" });
  await O.say({ t: "join", room: "~" });
  assert.deepEqual(O.take()[0].peers.map((p) => p.presence), [{ name: "Ann", owner: false }, { name: "", owner: true }]);
  assert.deepEqual(A.take().filter((m) => m.t === "peer").map((m) => m.peer.presence), [{ name: "", owner: true }]);
  await N.say({ t: "join", room: "~", ref: 1 });
  assert.equal(N.take()[0].code, "not_granted");
  for (const t of ["emit", "presence"]) {
    await A.say({ t, room: "~", topic: "x", data: 1, patch: { name: "Owner", owner: true }, ref: 2 });
    assert.equal(A.take()[0].code, "not_permitted");
  }
  assert.deepEqual(O.take(), []);
  // the page's own room still needs the room capability
  await A.say({ t: "join", room: "", ref: 3 });
  assert.equal(A.take()[0].code, "not_granted");
});

test("live: the bar's room isn't among the rooms a page may be in", async () => {
  const s = setup();
  const sh = await share(s);
  const v = await visitor(s, "Ann");
  const A = await open(s, sh.id, await session(s, sh.id, { visitor: v.key }));
  await A.say({ t: "join", room: "~" });
  await A.say({ t: "join", room: "" });
  for (let i = 0; i < 16; i++) await A.say({ t: "join", room: "r" + i });
  assert.ok(A.take().every((m) => m.t === "joined"));
  await A.say({ t: "join", room: "r16", ref: 1 });
  assert.equal(A.take()[0].code, "limit_reached");
});

test("live: a write tells each open page the documents it can read", async () => {
  const s = setup();
  const caps = { db: { rules: [{ path: "votes", read: "view", write: "owner" }, { path: "votes/{self}", write: "interact" }] } };
  const sh = await share(s, caps, []);
  const a = await visitor(s, "Ann"), b = await visitor(s, "Bo");
  const as = await session(s, sh.id, { visitor: a.key }), bs = await session(s, sh.id, { visitor: b.key });
  const A = await open(s, sh.id, as), B = await open(s, sh.id, bs), N = await open(s, sh.id, await session(s, sh.id));
  assert.equal((await db(s, sh.id, as.token, { op: "set", path: "votes/" + a.id, data: { pick: 1 } })).status, 200);
  assert.equal((await db(s, sh.id, as.token, { op: "set", path: `data/users/${a.id}/draft`, data: { t: "x" } })).status, 200);
  assert.deepEqual(A.take(), [{ t: "changed", paths: ["votes/" + a.id] }, { t: "changed", paths: [`data/users/${a.id}/draft`] }]);
  assert.deepEqual(B.take(), [{ t: "changed", paths: ["votes/" + a.id] }]);
  assert.deepEqual(N.take(), [{ t: "changed", paths: ["votes/" + a.id] }]);
});

test("live: a new version reloads every page, and a page taken down ends its data", async () => {
  const s = setup();
  const sh = await share(s);
  const v = await visitor(s, "Ann");
  const vs = await session(s, sh.id, { visitor: v.key });
  assert.equal((await db(s, sh.id, vs.token, { op: "set", path: "votes/" + v.id, data: { pick: "b" } })).status, 200);
  const A = await open(s, sh.id, vs);

  // the viewers' data is kept: a version's own isn't seeded over it
  const put = await shareAPI(req("PUT", `/api/shares/${sh.id}`, { token: sh.token, body: form({ capabilities: CAPS }, [{ path: "config/c", data: { q: "Dinner?" } }]) }), s.env, s.ctx);
  assert.equal(put.status, 200, await put.clone().text());
  await s.settle();
  assert.deepEqual(A.ws.got, [{ t: "version", n: 2 }]);
  assert.equal(A.ws.closed.code, 4000);
  let r = await db(s, sh.id, vs.token, { op: "get", path: "config/c" });
  assert.deepEqual((await r.json()).doc.data, { q: "Lunch?" });
  r = await db(s, sh.id, vs.token, { op: "get", path: "votes/" + v.id });
  assert.deepEqual((await r.json()).doc.data, { pick: "b" });
  // the capabilities follow the version: room no more
  const B = await open(s, sh.id, vs);
  const put2 = await shareAPI(req("PUT", `/api/shares/${sh.id}`, { token: sh.token, body: form({ capabilities: { db: {} } }) }), s.env, s.ctx);
  assert.equal(put2.status, 200);
  await s.settle();
  assert.equal(B.ws.closed.code, 4000);
  const C = await open(s, sh.id, await session(s, sh.id, { visitor: v.key }));
  await C.say({ t: "join", room: "", ref: 1 });
  assert.equal(C.take()[0].code, "not_granted");

  const code = await (await call(s, `/api/shares/${sh.id}/owner`, { token: sh.token })).json();
  await session(s, sh.id, { code: code.code });
  await call(s, `/api/shares/${sh.id}/owner`, { token: sh.token });
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT count(*) n FROM owners WHERE share = ?").get(sh.id).n, 1);
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT count(*) n FROM codes WHERE share = ?").get(sh.id).n, 1);
  const del = await shareAPI(req("DELETE", `/api/shares/${sh.id}`, { token: sh.token }), s.env, s.ctx);
  assert.equal(del.status, 200);
  await s.settle();
  assert.deepEqual(C.ws.got.at(-1), { t: "error", code: "revoked", message: "the page isn't shared any more" });
  assert.equal(C.ws.closed.code, 4001);
  assert.equal(s.env.LIVE.objs.get(sh.id).ctx.storage.db.prepare("SELECT count(*) n FROM sqlite_master WHERE type = 'table'").get().n, 0);
  // a socket's close comes after its tables went
  await C.obj.webSocketClose(C.ws.peer);
  // the owners' keys and codes went with it
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT count(*) n FROM owners WHERE share = ?").get(sh.id).n, 0);
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT count(*) n FROM codes WHERE share = ?").get(sh.id).n, 0);
  r = await db(s, sh.id, vs.token, { op: "get", path: "config/c" });
  assert.equal(r.status, 404);
  assert.equal((await r.json()).error.code, "revoked");
});

// The page's data comes with the first version that declares db, even
// when the share was live before (a room alone), and stays the share's
// through a version that drops db, so magpie takes back what it holds
test("live: the data is seeded once, by the first version with db", async () => {
  const s = setup();
  const put = async (sh, caps, data) => {
    const r = await shareAPI(req("PUT", `/api/shares/${sh.id}`, { token: sh.token, body: form({ capabilities: caps }, data) }), s.env, s.ctx);
    assert.equal(r.status, 200, await r.clone().text());
    await s.settle();
  };
  const dump = async (sh) => (await call(s, `/api/shares/${sh.id}/data`, { method: "GET", token: sh.token })).json();
  const sh = await share(s, { room: {} }, undefined);
  assert.deepEqual(await dump(sh), { held: false, docs: [] });
  await put(sh, CAPS, [{ path: "config/c", data: { q: "Lunch?" } }]);
  let d = await dump(sh);
  assert.equal(d.held, true);
  assert.deepEqual(d.docs.map((x) => [x.path, x.data]), [["config/c", { q: "Lunch?" }]]);
  await put(sh, { room: {} });
  await put(sh, CAPS, [{ path: "config/c", data: { q: "Dinner?" } }]);
  d = await dump(sh);
  assert.deepEqual(d.docs.map((x) => [x.path, x.data]), [["config/c", { q: "Lunch?" }]]);
});

// A share that was never live has no object: a new version tells none
test("live: a static share's update makes no object", async () => {
  const s = setup();
  const sh = await share(s, {}, undefined);
  const r = await shareAPI(req("PUT", `/api/shares/${sh.id}`, { token: sh.token, body: form({ capabilities: {} }) }), s.env, s.ctx);
  assert.equal(r.status, 200);
  await s.settle();
  assert.equal(s.env.LIVE.objs.has(sh.id), false);
});

test("live: a page shared without data or a room has nothing here", async () => {
  const s = setup();
  const sh = await share(s, {}, undefined);
  const anon = await session(s, sh.id);
  assert.deepEqual(anon.caps, {});
  assert.equal((await db(s, sh.id, anon.token, { op: "get", path: "a/b" })).status, 403);
  const html = await (await page(req("GET", `/a/${sh.id}`), s.env)).text();
  assert.doesNotMatch(html, /<script/);
  // a socket is a WebSocket; one of nobody's, on such a page, is refused
  assert.equal((await call(s, `/api/shares/${sh.id}/rt/live?t=${anon.token}`, { method: "GET" })).status, 426);
  const ws = await call(s, `/api/shares/${sh.id}/rt/live?t=${anon.token}`, { method: "GET", headers: { Upgrade: "websocket" } });
  assert.equal(ws.status, 403);
  assert.equal(s.env.LIVE.objs.size, 0);
});

test("live: the link's page runs only its own script, and talks only to the site", async () => {
  const s = setup();
  const sh = await share(s);
  const res = await page(req("GET", `/a/${sh.id}`), s.env);
  const html = await res.text();
  const csp = res.headers.get("Content-Security-Policy");
  const nonce = csp.match(/script-src 'nonce-([^']+)'/)[1];
  const scripts = [...html.matchAll(/<script([^>]*)>([\s\S]*?)<\/script>/g)];
  assert.equal(scripts.length, 1);
  assert.equal(scripts[0][1], ` nonce="${nonce}"`);
  assert.match(csp, /connect-src 'self' wss:\/\/usemagpie\.ai;/);
  assert.match(csp, /default-src 'none'/);
  // it parses, and names this share
  new Function(scripts[0][2]);
  assert.match(scripts[0][2], new RegExp(`\\)\\(\\{"id":"${sh.id}","key":"[A-Za-z0-9_-]{40,}"\\}\\)$`));
  // a new nonce each time
  const again = (await page(req("GET", `/a/${sh.id}`), s.env)).headers.get("Content-Security-Policy");
  assert.notEqual(again, csp);
});

test("live: an expired socket is closed for a new session", async () => {
  const s = setup();
  const sh = await share(s);
  const v = await visitor(s, "Ann");
  const t = await mint(s.env, { s: sh.id, v: v.id, n: "Ann", l: LEVEL.interact, g: true, e: Date.now() + 1 });
  s.env.LIVE.get(sh.id);
  const obj = s.env.LIVE.objs.get(sh.id);
  const ws = obj.accept({ id: v.id, name: "Ann", level: LEVEL.interact, guest: true, exp: Date.now() - 1 });
  await obj.webSocketMessage(ws.peer, JSON.stringify({ t: "join", room: "" }));
  assert.equal(ws.closed.code, 4002);
  assert.equal(await claimsOf(s.env, t, sh.id, Date.now() + 5), null);
});

test("live: a name in any script reaches the share's object", async () => {
  const s = setup();
  const sh = await share(s);
  const v = await visitor(s, "李雷 Émile");
  const vs = await session(s, sh.id, { visitor: v.key });
  const r = await db(s, sh.id, vs.token, { op: "set", path: "votes/" + v.id, data: { pick: "b" } });
  assert.equal(r.status, 200, await r.clone().text());
});

test("live: one place's sockets are bounded, a viewer with no name too, and a quiet socket's session ends", async () => {
  const s = setup();
  const sh = await share(s);
  s.env.LIVE.get(sh.id);
  const obj = s.env.LIVE.objs.get(sh.id);
  const now = Date.now();
  const anon = (at, exp = now + 6e4) => ({ id: "", name: "", level: LEVEL.view, guest: false, exp, at });
  // the socket is taken before the 101 is made, which node's Response refuses
  const tryOpen = (v) => {
    try {
      return obj.open(v).status;
    } catch {
      return 101;
    }
  };
  for (let i = 0; i < 50; i++) obj.accept(anon("p1"));
  const r = obj.open(anon("p1"));
  assert.equal(r.status, 429, "a 51st socket from one place, no name to count by");
  assert.equal((await r.json()).error.code, "limit_reached");
  assert.equal(tryOpen(anon("p2")), 101, "another place still gets in");
  assert.equal(obj.ctx.getWebSockets("at:p2").length, 1);
  // sessions that ended while their sockets stayed quiet are closed on the next open
  const old = [];
  for (let i = 0; i < 50; i++) old.push(obj.accept(anon("p3", now - 1)));
  assert.equal(tryOpen(anon("p3")), 101);
  assert.ok(old.every((ws) => ws.closed?.code === 4002), "every expired socket closed");
  assert.equal(obj.ctx.getWebSockets("at:p3").length, 1);
  // the place: an address, an IPv6 one by its /64, in any spelling
  const at = (ip) => placeOf(req("GET", "/", { ip }));
  assert.equal(await at("2001:db8:1:2::1"), await at("2001:0db8:0001:0002:ffff:0:0:2"));
  assert.notEqual(await at("2001:db8:1:2::1"), await at("2001:db8:1:3::1"));
  assert.notEqual(await at("198.51.100.7"), await at("198.51.100.8"));
  assert.equal(await at(""), "");
  // the route counts by it
  const v = await session(s, sh.id);
  const r2 = await call(s, `/api/shares/${sh.id}/rt/live?t=${encodeURIComponent(v.token)}`, { method: "GET", ip: "203.0.113.9", headers: { Upgrade: "websocket" } }).catch(() => null);
  assert.ok(obj.ctx.getWebSockets("at:" + (await at("203.0.113.9"))).length === 1, "the socket the route took is counted by its address");
  void r2;
});

test("live: a page asking only who is viewing is live", async () => {
  const s = setup();
  const sh = await share(s, { user: {} }, undefined);
  const html = await (await page(req("GET", `/a/${sh.id}`), s.env)).text();
  assert.match(html, /<script nonce=/);
  assert.match(html, /#magpie-share=/);
});

test("live: one viewer's sockets, and a page's joins, are bounded", async () => {
  const s = setup();
  const sh = await share(s);
  const v = await visitor(s, "Ann");
  s.env.LIVE.get(sh.id);
  const obj = s.env.LIVE.objs.get(sh.id);
  const who = { id: v.id, name: "Ann", level: LEVEL.interact, guest: true, exp: Date.now() + 6e4 };
  for (let i = 0; i < 20; i++) obj.accept(who);
  const r = obj.open(who);
  assert.equal(r.status, 429);
  assert.equal((await r.json()).error.code, "limit_reached");
  // joins spend the page's rate like emits
  const A = await open(s, sh.id, await session(s, sh.id, { visitor: (await visitor(s, "Bo")).key }));
  for (let i = 0; i < 80; i++) await A.say({ t: i % 2 ? "leave" : "join", room: "r" });
  A.take();
  await A.say({ t: "join", room: "r", ref: 9 });
  assert.equal(A.take()[0].code, "rate_limited");
});

test("live: sign-ins and shares are counted apart", async () => {
  const s = setup();
  for (let i = 0; i < 30; i++) await visitor(s, "V" + i);
  await share(s);
});
