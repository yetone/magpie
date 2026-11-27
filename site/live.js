// Live shared artifacts (docs/subsystems/artifacts.md, "Live sharing"): a
// shared page that declares db, room or user keeps its data and its room here,
// so everyone with the link sees the same rows and each other.
//
// Who is viewing:
//   - nobody signed in: reads the page's data where its rules let a
//     viewer (level view), and isn't in the room;
//   - a visitor, signed in by name alone (POST /api/visitors, which
//     answers a key the link's page keeps in its own storage): level
//     interact, guest, in the room;
//   - the owner, the magpie user who shared it: magpie asks for a
//     one-time code with the share's token (POST /api/shares/<id>/owner)
//     and opens the link with #owner=<code>; the link's page trades it
//     for an owner key it keeps. The owner's id is the one magpie names
//     its viewer by, so the data/users/<id>/ magpie's own page wrote are
//     still theirs here.
//
// The link's page (shares.js, page) asks POST /api/shares/<id>/session
// with what it keeps, and hands the frame what it answers: the API, a
// token signed with LIVE_KEY good for TOKEN_TTL, the viewer and the
// capabilities. The frame's runtime calls the API with the token:
//   - POST <api>/db, the store's calls, as magpie's own page API answers
//     them (livedb.js); the share's token as Bearer is the agent
//   - POST <api>/profiles, the names of ids, where the page declares
//     user with the profile scope
//   - GET <api>/live?t=<token>, a WebSocket: the room, and which
//     documents changed
// GET /api/shares/<id>/data, with the share's token, is {held, docs}: every
// document, for magpie to take back when the page stops being shared, and
// whether the page's data was ever seeded here (held: false is nothing to
// take back).
//
// Each share has a Durable Object (ShareLive, by the share's id) that
// keeps its documents in its own SQLite and holds its sockets, hibernating
// between messages; who is in which room, and their presence, is kept in
// its tables, since a socket's attachment holds 2 KB.
//
// The room is the room capability's (contract 0.2.99): a peer is one
// open page; emit and presence go to everyone in the room; topics are
// the owner's (level admin) unless the declaration opens one to
// interact; presence is anyone's who is in. Messages from the client:
//   {t: "join", room, ref}       room "" is the lobby; "~" is the link
//                                page's bar, who else has it open: its
//                                presence is the viewer's name, set here,
//                                and nothing is sent in it
//   {t: "leave", room}
//   {t: "emit", room, topic, data}
//   {t: "presence", room, patch}
// and from here:
//   {t: "hi", sid}               the socket's peer label
//   {t: "joined", ref, room, peers}
//   {t: "peer", room, peer}      joined, or their presence changed
//   {t: "gone", room, peer}
//   {t: "msg", room, topic, data, from}
//   {t: "changed", paths}        documents this viewer can read
//   {t: "version", n}            a new version: every view reloads
//   {t: "error", ref?, code, message}

import { LEVEL, parseRules, canRead, Store } from "./livedb.js";
import { json, refuse, sha256, randomToken, same, held, schema, capsOf, SITE } from "./shares.js";

export const TOKEN_TTL = 12 * 36e5;
const CODE_TTL = 10 * 6e4;
const MAX_NAME = 40;
const MAX_MESSAGE = 64 << 10;
const MAX_DATA = 4096; // an emit's data, and a merged presence, in bytes
const MAX_DEPTH = 8;
const MAX_ROOMS = 16; // named rooms a page is in, the lobby besides
const MAX_PEERS = 200; // in one room
const RATE = 40, BURST = 80; // emits and presence a second, a page's
const TOPIC = /^[a-z][a-z0-9_.-]{0,47}$/;
const ROOM = /^[a-z0-9][a-z0-9_.-]{0,47}$/;
const BAR = "~";
const MAX_SOCKETS = 1000; // a share's open pages
const MAX_VIEWER_SOCKETS = 20; // one signed-in viewer's
// one address's (a /64 for IPv6), signed in or not: a viewer with no
// name has no id to count by, so without it one machine could fill a
// share's MAX_SOCKETS and keep everyone else out
const MAX_PLACE_SOCKETS = 50;
const CORS = { "Access-Control-Allow-Origin": "*", "Cache-Control": "no-store" };

const LIVE_SCHEMA = [
  "CREATE TABLE IF NOT EXISTS visitors (id TEXT PRIMARY KEY, key TEXT NOT NULL UNIQUE, name TEXT NOT NULL, created INTEGER NOT NULL)",
  "CREATE TABLE IF NOT EXISTS owners (share TEXT NOT NULL, key TEXT NOT NULL, created INTEGER NOT NULL, PRIMARY KEY (share, key))",
  "CREATE TABLE IF NOT EXISTS codes (code TEXT PRIMARY KEY, share TEXT NOT NULL, exp INTEGER NOT NULL)",
];
const made = new WeakMap();
function liveSchema(db) {
  if (!made.has(db))
    made.set(
      db,
      schema(db)
        .then(() => db.batch(LIVE_SCHEMA.map((s) => db.prepare(s))))
        .catch((e) => {
          made.delete(db);
          throw e;
        }),
    );
  return made.get(db);
}

const enc = (s) => new TextEncoder().encode(s);
function b64u(bytes) {
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
function unb64u(s) {
  const bin = atob(s.replace(/-/g, "+").replace(/_/g, "/"));
  return Uint8Array.from(bin, (c) => c.charCodeAt(0));
}
function viewerID() {
  const a = "abcdefghijklmnopqrstuvwxyz234567";
  return [...crypto.getRandomValues(new Uint8Array(16))].map((b) => a[b & 31]).join("");
}

const keys = new Map();
function hmac(env) {
  if (!env.LIVE_KEY) throw new Error("LIVE_KEY isn't set");
  if (!keys.has(env.LIVE_KEY))
    keys.set(env.LIVE_KEY, crypto.subtle.importKey("raw", enc(env.LIVE_KEY), { name: "HMAC", hash: "SHA-256" }, false, ["sign", "verify"]));
  return keys.get(env.LIVE_KEY);
}

// mint signs a viewer's claims: s the share, v their id ("" for nobody
// signed in), n their name, l their level, g guest, a the agent, e when
// it ends
export async function mint(env, claims) {
  const body = b64u(enc(JSON.stringify(claims)));
  const sig = await crypto.subtle.sign("HMAC", await hmac(env), enc(body));
  return body + "." + b64u(new Uint8Array(sig));
}

// claimsOf is a token's claims when it is ours, for share, and not over
export async function claimsOf(env, token, share, now = Date.now()) {
  if (typeof token !== "string") return null;
  const dot = token.indexOf(".");
  if (dot < 1 || token.length > 2048) return null;
  try {
    const body = token.slice(0, dot);
    const ok = await crypto.subtle.verify("HMAC", await hmac(env), unb64u(token.slice(dot + 1)), enc(body));
    if (!ok) return null;
    const c = JSON.parse(new TextDecoder().decode(unb64u(body)));
    return c && c.s === share && c.e > now ? c : null;
  } catch {
    return null;
  }
}

function cleanName(s) {
  if (typeof s !== "string") return "";
  // no control, format or private-use characters, and a name's length
  return s.replace(/[\p{Cc}\p{Cf}\p{Co}\p{Zl}\p{Zp}]/gu, "").replace(/\s+/g, " ").trim().slice(0, MAX_NAME);
}

const bearer = (req) => (req.headers.get("Authorization") || "").match(/^Bearer\s+(\S+)$/)?.[1] || "";

async function bodyOf(req, max = 1 << 20) {
  const text = await req.text();
  if (text.length > max) return null;
  try {
    return JSON.parse(text || "{}");
  } catch {
    return null;
  }
}

function cors(res) {
  const h = new Headers(res.headers);
  for (const [k, v] of Object.entries(CORS)) h.set(k, v);
  return new Response(res.body, { status: res.status, headers: h, webSocket: res.webSocket });
}
const rtError = (status, code, message) => cors(json({ error: { code, message } }, status));


// live answers whether a share with these capabilities has anything here
// (user too: a page that asks who is viewing needs the session the link
// page signs in)
export const live = (caps) => !!(caps && (caps.db || caps.room || caps.user));

function stub(env, id) {
  return env.LIVE.get(env.LIVE.idFromName(id));
}

// toLive is a request to the share's object at path, as viewer, with caps
// placeOf is who a socket comes from, for counting: a hash of the
// address Cloudflare saw, an IPv6 one by its /64 (one machine holds a
// whole /64), "" when there is none
export async function placeOf(req) {
  let ip = (req.headers.get("CF-Connecting-IP") || "").trim().toLowerCase();
  if (!ip) return "";
  if (ip.includes(":")) {
    const [head, tail = ""] = ip.split("::");
    const h = head ? head.split(":") : [], t = tail ? tail.split(":") : [];
    const all = ip.includes("::") ? [...h, ...Array(Math.max(0, 8 - h.length - t.length)).fill("0"), ...t] : h;
    ip = all.slice(0, 4).map((g) => g.replace(/^0+(?=.)/, "")).join(":") + "::/64";
  }
  return (await sha256("magpie-live:" + ip)).slice(0, 24);
}

function toLive(env, id, path, { viewer, caps, body, headers = {}, method = "POST" } = {}) {
  const h = new Headers(headers);
  // a header carries Latin-1 only, and a name or a rule may hold any text
  if (viewer) h.set("X-Live-Viewer", encodeURIComponent(JSON.stringify(viewer)));
  if (caps) h.set("X-Live-Caps", encodeURIComponent(JSON.stringify(caps)));
  return stub(env, id).fetch(new Request("https://live/" + path, { method, headers: h, body }));
}

// SeedError is data the share's object refused
export class SeedError extends Error {}

// seed, version and drop are what shares.js tells a share's object
export async function seed(env, id, caps, docs) {
  if (!env.LIVE) return;
  const r = await toLive(env, id, "seed", { caps, body: JSON.stringify(docs) });
  if (!r.ok) throw new SeedError((await r.json().catch(() => ({})))?.error?.message || "the data wasn't taken");
}
export async function version(env, id, n, caps) {
  if (!env.LIVE) return;
  await toLive(env, id, "version", { caps, body: JSON.stringify({ n }) });
}
export async function drop(env, id) {
  if (!env.LIVE) return;
  await toLive(env, id, "drop", {});
  // the owners' keys and codes go with the share (the tables are there
  // once a link page has been opened)
  const db = env.SHARES_DB;
  if (db)
    await db
      .batch([db.prepare("DELETE FROM owners WHERE share = ?1").bind(id), db.prepare("DELETE FROM codes WHERE share = ?1").bind(id)])
      .catch(() => {});
}

async function shareRow(db, id) {
  return db.prepare("SELECT token, caps, owner_id, owner_name FROM shares WHERE id = ?1").bind(id).first();
}
async function isShareToken(env, row, token) {
  if (!token) return false;
  if (env.SHARE_ADMIN && same(token, env.SHARE_ADMIN)) return true;
  return same(await sha256(token), row.token);
}

// api answers /api/visitors… and /api/shares/<id>/<more>, or null for
// another path
export async function api(req, env, ctx, now = Date.now()) {
  const url = new URL(req.url);
  const path = url.pathname;
  const vis = path === "/api/visitors" || path === "/api/visitors/me";
  const sm = path.match(/^\/api\/shares\/([a-z2-7]{16})\/(session|owner|data|rt\/db|rt\/profiles|rt\/live)$/);
  if (!vis && !sm) return null;
  if (req.method === "OPTIONS")
    return new Response(null, { status: 204, headers: { ...CORS, "Access-Control-Allow-Methods": "GET, POST, PATCH", "Access-Control-Allow-Headers": "Authorization, Content-Type", "Access-Control-Max-Age": "86400" } });
  const db = env.SHARES_DB;
  if (!db || !env.LIVE || !env.LIVE_KEY) return refuse(503, "live sharing isn't set up here");
  await liveSchema(db);

  if (path === "/api/visitors") {
    if (req.method !== "POST") return refuse(405, "POST signs a visitor in");
    const b = await bodyOf(req, 4096);
    const name = cleanName(b?.name);
    if (!name) return refuse(400, "a visitor needs a name");
    if (await held(db, req, now, "visitors")) return refuse(429, "too many sign-ins from here this hour; try again later");
    const id = viewerID(), key = randomToken();
    await db.prepare("INSERT INTO visitors (id, key, name, created) VALUES (?1, ?2, ?3, ?4)").bind(id, await sha256(key), name, now).run();
    return json({ id, key, name }, 201);
  }
  if (path === "/api/visitors/me") {
    if (req.method !== "PATCH" && req.method !== "GET") return refuse(405, "GET reads, PATCH renames the visitor");
    const row = await db.prepare("SELECT id, name FROM visitors WHERE key = ?1").bind(await sha256(bearer(req))).first();
    if (!row) return refuse(401, "no such visitor");
    if (req.method === "GET") return json(row);
    const name = cleanName((await bodyOf(req, 4096))?.name);
    if (!name) return refuse(400, "a visitor needs a name");
    await db.prepare("UPDATE visitors SET name = ?1 WHERE id = ?2").bind(name, row.id).run();
    return json({ id: row.id, name });
  }

  const [, id, what] = sm;
  const row = await shareRow(db, id);
  if (!row) return what.startsWith("rt/") ? rtError(404, "revoked", "the page isn't shared any more") : refuse(404, "no such shared page: it was taken down");
  const caps = capsOf(row);

  if (what === "owner") {
    if (req.method !== "POST") return refuse(405, "POST asks for an owner's code");
    if (!(await isShareToken(env, row, bearer(req)))) return refuse(403, "that isn't this page's token");
    const code = randomToken();
    await db.batch([
      db.prepare("DELETE FROM codes WHERE exp < ?1").bind(now),
      db.prepare("INSERT INTO codes (code, share, exp) VALUES (?1, ?2, ?3)").bind(await sha256(code), id, now + CODE_TTL),
    ]);
    return json({ code, url: `${SITE}/a/${id}#owner=${code}` });
  }

  if (what === "data") {
    if (req.method !== "GET") return refuse(405, "GET reads the page's data");
    if (!(await isShareToken(env, row, bearer(req)))) return refuse(403, "that isn't this page's token");
    return toLive(env, id, "dump", { method: "GET" });
  }

  if (what === "session") {
    if (req.method !== "POST") return refuse(405, "POST opens a session");
    const b = (await bodyOf(req, 4096)) || {};
    let owner = false, ownerKey;
    if (await isShareToken(env, row, bearer(req))) owner = true;
    if (!owner && typeof b.code === "string" && b.code) {
      const c = await db.prepare("DELETE FROM codes WHERE code = ?1 AND share = ?2 RETURNING exp").bind(await sha256(b.code), id).first();
      if (c && c.exp > now) {
        ownerKey = randomToken();
        await db.prepare("INSERT INTO owners (share, key, created) VALUES (?1, ?2, ?3)").bind(id, await sha256(ownerKey), now).run();
        owner = true;
      }
    }
    if (!owner && typeof b.owner === "string" && b.owner)
      owner = !!(await db.prepare("SELECT 1 AS y FROM owners WHERE share = ?1 AND key = ?2").bind(id, await sha256(b.owner)).first());
    let visitor = null;
    if (typeof b.visitor === "string" && b.visitor)
      visitor = await db.prepare("SELECT id, name FROM visitors WHERE key = ?1").bind(await sha256(b.visitor)).first();
    let viewer;
    if (owner) {
      let oid = row.owner_id;
      if (!oid) {
        oid = viewerID();
        await db.prepare("UPDATE shares SET owner_id = ?1 WHERE id = ?2 AND owner_id IS NULL").bind(oid, id).run();
        oid = (await shareRow(db, id))?.owner_id || oid;
      }
      const name = visitor?.name || row.owner_name || "";
      if (visitor && visitor.name !== row.owner_name) await db.prepare("UPDATE shares SET owner_name = ?1 WHERE id = ?2").bind(visitor.name, id).run();
      viewer = { id: oid, name, level: "owner", guest: false, owner: true };
    } else if (visitor) viewer = { id: visitor.id, name: visitor.name, level: "interact", guest: true, owner: false };
    else viewer = { id: "", name: "", level: "view", guest: false, owner: false };
    const exp = now + TOKEN_TTL;
    const token = await mint(env, { s: id, v: viewer.id, n: viewer.name, l: LEVEL[viewer.level], g: viewer.guest, e: exp });
    return json({ api: `${SITE}/api/shares/${id}/rt`, token, exp, viewer, caps, ...(ownerKey ? { ownerKey } : {}) });
  }

  // the frame's runtime: the token in the body, or for the socket in the
  // query; the share's own token as Bearer is the agent
  if (what === "rt/live") {
    if (req.headers.get("Upgrade") !== "websocket") return rtError(426, "invalid_argument", "this is a WebSocket");
    const c = await claimsOf(env, url.searchParams.get("t"), id, now);
    if (!c) return rtError(401, "expired", "the session ended; open the page again");
    // a signed-in viewer is in the bar's room on any page
    if (!live(caps) && !c.v) return rtError(403, "not_granted", "the page declares none of db, room and user");
    return toLive(env, id, "live", { method: "GET", viewer: { id: c.v, name: c.n, level: c.l, guest: !!c.g, exp: c.e, at: await placeOf(req) }, caps, headers: { Upgrade: "websocket" } });
  }
  if (req.method !== "POST") return rtError(405, "invalid_argument", "POST");
  const b = await bodyOf(req);
  if (!b || typeof b !== "object") return rtError(400, "invalid_argument", "the body isn't JSON");
  let viewer;
  if (await isShareToken(env, row, bearer(req))) viewer = { id: row.owner_id || "", name: "", level: LEVEL.owner, guest: false, agent: true };
  else {
    const c = await claimsOf(env, b.t, id, now);
    if (!c) return rtError(401, "expired", "the session ended; open the page again");
    viewer = { id: c.v, name: c.n, level: c.l, guest: !!c.g };
  }
  if (what === "rt/profiles") {
    const ids = Array.isArray(b.ids) ? b.ids.filter((x) => typeof x === "string" && x.length <= 64).slice(0, 200) : [];
    const names = !!(caps.user && Array.isArray(caps.user.scopes) && caps.user.scopes.includes("profile"));
    const profiles = {};
    for (const x of ids) profiles[x] = { id: x, name: "", guest: x !== row.owner_id };
    if (names && ids.length) {
      const got = await db.prepare("SELECT id, name FROM visitors WHERE id IN (SELECT value FROM json_each(?1))").bind(JSON.stringify(ids)).all();
      for (const r of got.results || []) profiles[r.id].name = r.name;
      if (row.owner_id && profiles[row.owner_id]) profiles[row.owner_id].name = row.owner_name || "";
    }
    return cors(json({ profiles }));
  }
  if (!caps.db) return rtError(403, "not_granted", "the page doesn't declare db");
  delete b.t;
  return cors(await toLive(env, id, "db", { viewer, caps, body: JSON.stringify(b) }));
}

// -- the share's object ------------------------------------------------------

function depthOf(v, d = 0) {
  if (d > MAX_DEPTH) return d;
  if (v && typeof v === "object") {
    let m = d + 1;
    for (const k in v) m = Math.max(m, depthOf(v[k], d + 1));
    return m;
  }
  return d;
}
const bytes = (v) => enc(JSON.stringify(v)).length;
const plain = (v) => v && typeof v === "object" && !Array.isArray(v);

export class ShareLive {
  constructor(ctx, env) {
    this.ctx = ctx;
    this.env = env;
    this.sql = ctx.storage.sql;
    this.store = new Store(this.sql, (fn) => ctx.storage.transactionSync(fn));
    this.sql.exec("CREATE TABLE IF NOT EXISTS live_meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)");
    this.sql.exec("CREATE TABLE IF NOT EXISTS live_members (sid TEXT NOT NULL, room TEXT NOT NULL, presence TEXT NOT NULL, updated INTEGER NOT NULL, PRIMARY KEY (sid, room))");
    // pages whose socket went while this object was away are gone
    const open = ctx.getWebSockets().map((ws) => ws.deserializeAttachment()?.sid).filter(Boolean);
    this.sql.exec("DELETE FROM live_members WHERE sid NOT IN (SELECT value FROM json_each(?))", JSON.stringify(open));
    this.buckets = new Map();
    this.capsText = this.sql.exec("SELECT v FROM live_meta WHERE k = 'caps'").toArray()[0]?.v || "{}";
    this.rulesFor = null;
  }

  caps() {
    try {
      return JSON.parse(this.capsText);
    } catch {
      return {};
    }
  }
  setCaps(text) {
    if (!text || text === this.capsText) return;
    this.capsText = text;
    this.rulesFor = null;
    this.sql.exec("INSERT INTO live_meta (k, v) VALUES ('caps', ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v", text);
  }
  // held: the page's data was seeded here, so the share keeps it
  held() {
    return this.sql.exec("SELECT v FROM live_meta WHERE k = 'held'").toArray().length > 0;
  }
  rules() {
    if (!this.rulesFor) {
      let r = null;
      try {
        r = parseRules(this.caps().db || {});
      } catch {
        r = parseRules({});
      }
      this.rulesFor = r;
    }
    return this.rulesFor;
  }

  async fetch(req) {
    const path = new URL(req.url).pathname.slice(1);
    const header = (k) => {
      try {
        return decodeURIComponent(req.headers.get(k) || "");
      } catch {
        return "";
      }
    };
    this.setCaps(header("X-Live-Caps"));
    let viewer = null;
    try {
      viewer = JSON.parse(header("X-Live-Viewer") || "null");
    } catch {}
    try {
      switch (path) {
        case "db": {
          const c = await req.json();
          const res = this.store.call({ id: viewer.id, level: viewer.level, agent: !!viewer.agent }, this.rules(), c, Date.now());
          if (res.changed && res.changed.length) this.changed(res.changed);
          return new Response(JSON.stringify(res.body), { status: res.status, headers: { "Content-Type": "application/json; charset=utf-8" } });
        }
        case "live":
          return this.open(viewer);
        case "seed": {
          // the page's data comes here once, with the first version that
          // declares db; after that the share's own, which its viewers
          // write, is kept
          if (this.held()) return json({ ok: true, kept: true });
          this.store.seed(await req.json());
          this.sql.exec("INSERT INTO live_meta (k, v) VALUES ('held', '1') ON CONFLICT (k) DO NOTHING");
          return json({ ok: true });
        }
        case "dump":
          return json({ held: this.held(), docs: this.store.dump() });
        case "version": {
          const { n } = await req.json();
          this.closeAll({ t: "version", n }, 4000, "a new version");
          return json({ ok: true });
        }
        case "drop":
          this.closeAll({ t: "error", code: "revoked", message: "the page isn't shared any more" }, 4001, "unshared");
          await this.ctx.storage.deleteAll();
          return json({ ok: true });
      }
    } catch (e) {
      return json({ error: { code: "invalid_argument", message: String(e?.message || e) } }, 400);
    }
    return json({ error: { code: "invalid_argument", message: "no such call" } }, 404);
  }

  open(viewer) {
    this.sweep(Date.now());
    if (this.ctx.getWebSockets().length >= MAX_SOCKETS) return json({ error: { code: "limit_reached", message: `a shared page is open in ${MAX_SOCKETS} places at most` } }, 429);
    if (viewer.id && this.ctx.getWebSockets("v:" + viewer.id).length >= MAX_VIEWER_SOCKETS)
      return json({ error: { code: "limit_reached", message: `one viewer has a page open in ${MAX_VIEWER_SOCKETS} places at most` } }, 429);
    if (viewer.at && this.ctx.getWebSockets("at:" + viewer.at).length >= MAX_PLACE_SOCKETS)
      return json({ error: { code: "limit_reached", message: `the page is open ${MAX_PLACE_SOCKETS} times from here already` } }, 429);
    return new Response(null, { status: 101, webSocket: this.accept(viewer) });
  }
  // accept takes a page's socket, and answers the page's end of it
  accept(viewer) {
    const pair = new WebSocketPair();
    const sid = viewerID();
    const tags = [sid];
    if (viewer.id) tags.push("v:" + viewer.id);
    if (viewer.at) tags.push("at:" + viewer.at);
    this.ctx.acceptWebSocket(pair[1], tags);
    pair[1].serializeAttachment({ sid, id: viewer.id || "", n: cleanName(viewer.name), o: viewer.level >= LEVEL.owner, level: viewer.level | 0, guest: !!viewer.guest, exp: viewer.exp || 0 });
    pair[1].send(JSON.stringify({ t: "hi", sid }));
    return pair[0];
  }

  closeAll(msg, code, why) {
    const text = JSON.stringify(msg);
    for (const ws of this.ctx.getWebSockets()) {
      try {
        ws.send(text);
        ws.close(code, why);
      } catch {}
    }
    this.sql.exec("DELETE FROM live_members");
  }

  // changed tells each page the documents it can read that were written
  changed(paths) {
    const rules = this.rules();
    for (const ws of this.ctx.getWebSockets()) {
      const a = ws.deserializeAttachment();
      if (!a) continue;
      const mine = paths.filter((p) => canRead(rules, { id: a.id, level: a.level }, p));
      if (mine.length)
        try {
          ws.send(JSON.stringify({ t: "changed", paths: mine }));
        } catch {}
    }
  }

  socket(sid) {
    return this.ctx.getWebSockets(sid)[0] || null;
  }
  members(room) {
    return this.sql.exec("SELECT sid, presence, updated FROM live_members WHERE room = ? ORDER BY rowid", room).toArray();
  }
  // peer is how a member shows to the page whose viewer is to
  peer(m, a, to) {
    const by = this.caps().user ? a.id || null : null;
    return { peer: m.sid, by, isMe: !!a.id && a.id === to.id, sameTab: m.sid === to.sid, kind: "viewer", guest: a.guest, presence: JSON.parse(m.presence), updatedAt: m.updated };
  }
  // tell sends each member of room what make(their attachment) gives
  tell(room, make, skip) {
    for (const m of this.members(room)) {
      if (m.sid === skip) continue;
      const ws = this.socket(m.sid);
      const to = ws?.deserializeAttachment();
      if (!to) continue;
      const msg = make(to);
      if (msg)
        try {
          ws.send(JSON.stringify(msg));
        } catch {}
    }
  }

  allowed(sid) {
    const now = Date.now();
    let b = this.buckets.get(sid);
    if (!b) this.buckets.set(sid, (b = { n: BURST, at: now }));
    b.n = Math.min(BURST, b.n + ((now - b.at) / 1000) * RATE);
    b.at = now;
    if (b.n < 1) return false;
    b.n -= 1;
    return true;
  }

  async webSocketMessage(ws, raw) {
    const a = ws.deserializeAttachment();
    if (!a) return;
    const fail = (code, message, ref) => ws.send(JSON.stringify({ t: "error", code, message, ...(ref != null ? { ref } : {}) }));
    if (typeof raw !== "string" || raw.length > MAX_MESSAGE || enc(raw).length > MAX_MESSAGE) return fail("invalid_argument", "a message is text, at most 64 KiB");
    if (a.exp && a.exp < Date.now()) {
      ws.close(4002, "expired");
      return;
    }
    let m;
    try {
      m = JSON.parse(raw);
    } catch {
      return fail("invalid_argument", "a message is JSON");
    }
    if (!plain(m)) return fail("invalid_argument", "a message is an object");
    const ref = typeof m.ref === "number" || typeof m.ref === "string" ? m.ref : undefined;
    if (m.room === BAR) return this.bar(ws, a, m, ref, fail);
    const caps = this.caps();
    if (!caps.room) return fail("not_granted", "the page doesn't declare room", ref);
    if (!a.id) return fail("not_granted", "sign in to be in the room", ref);
    const room = typeof m.room === "string" ? m.room : "";
    if (room && !ROOM.test(room)) return fail("invalid_argument", "a room's name is ^[a-z0-9][a-z0-9_.-]{0,47}$", ref);
    const inRoom = () => this.sql.exec("SELECT presence FROM live_members WHERE sid = ? AND room = ?", a.sid, room).toArray()[0];

    switch (m.t) {
      case "join": {
        if (!this.allowed(a.sid)) return fail("rate_limited", "joins are held to about 40 a second", ref);
        if (!inRoom()) {
          const rooms = this.sql.exec("SELECT COUNT(*) AS n FROM live_members WHERE sid = ? AND room NOT IN ('', ?)", a.sid, BAR).one().n;
          if (room && rooms >= MAX_ROOMS) return fail("limit_reached", `a page is in ${MAX_ROOMS} rooms at most`, ref);
          const n = this.sql.exec("SELECT COUNT(*) AS n FROM live_members WHERE room = ?", room).one().n;
          if (n >= MAX_PEERS) return fail("limit_reached", `a room holds ${MAX_PEERS} pages at most`, ref);
          const now = Date.now();
          this.sql.exec("INSERT INTO live_members (sid, room, presence, updated) VALUES (?, ?, '{}', ?)", a.sid, room, now);
          const me = { sid: a.sid, presence: "{}", updated: now };
          this.tell(room, (to) => ({ t: "peer", room, peer: this.peer(me, a, to) }), a.sid);
        }
        const peers = [];
        for (const mem of this.members(room)) {
          const att = this.socket(mem.sid)?.deserializeAttachment();
          if (att) peers.push(this.peer(mem, att, a));
        }
        ws.send(JSON.stringify({ t: "joined", ref, room, peers }));
        return;
      }
      case "leave": {
        if (!this.allowed(a.sid)) return fail("rate_limited", "leaves are held to about 40 a second", ref);
        if (!inRoom()) return;
        this.sql.exec("DELETE FROM live_members WHERE sid = ? AND room = ?", a.sid, room);
        this.tell(room, () => ({ t: "gone", room, peer: a.sid }));
        return;
      }
      case "presence": {
        const cur = inRoom();
        if (!cur) return fail("invalid_argument", "join the room first", ref);
        if (!plain(m.patch)) return fail("invalid_argument", "a presence patch is an object", ref);
        if (!this.allowed(a.sid)) return fail("rate_limited", "presence is held to about 40 a second", ref);
        const merged = JSON.parse(cur.presence);
        for (const [k, v] of Object.entries(m.patch)) {
          if (k === "__proto__" || k === "constructor" || k === "prototype") return fail("invalid_argument", `${k} can't be a key`, ref);
          if (v === null) delete merged[k];
          else merged[k] = v;
        }
        if (depthOf(merged) > MAX_DEPTH) return fail("invalid_argument", `presence nests ${MAX_DEPTH} deep at most`, ref);
        if (bytes(merged) > MAX_DATA) return fail("invalid_argument", `presence is ${MAX_DATA} bytes of JSON at most`, ref);
        const now = Date.now();
        const text = JSON.stringify(merged);
        this.sql.exec("UPDATE live_members SET presence = ?, updated = ? WHERE sid = ? AND room = ?", text, now, a.sid, room);
        const me = { sid: a.sid, presence: text, updated: now };
        this.tell(room, (to) => ({ t: "peer", room, peer: this.peer(me, a, to) }), a.sid);
        return;
      }
      case "emit": {
        if (!inRoom()) return fail("invalid_argument", "join the room first", ref);
        const topic = m.topic;
        if (typeof topic !== "string" || !TOPIC.test(topic)) return fail("invalid_argument", "a topic is ^[a-z][a-z0-9_.-]{0,47}$", ref);
        const need = (plain(caps.room.topics) && caps.room.topics[topic]) || "admin";
        if (!(a.level >= (LEVEL[need] ?? LEVEL.admin))) return fail("not_permitted", `only ${need} and up send on ${topic}`, ref);
        if (m.data !== undefined && (depthOf(m.data) > MAX_DEPTH || bytes(m.data) > MAX_DATA)) return fail("invalid_argument", `data is ${MAX_DATA} bytes of JSON at most, ${MAX_DEPTH} deep`, ref);
        if (!this.allowed(a.sid)) return fail("rate_limited", "emits are held to about 40 a second", ref);
        const by = caps.user ? a.id : null;
        this.tell(room, (to) => ({ t: "msg", room, topic, data: m.data, from: { peer: a.sid, by, isMe: a.id === to.id, sameTab: a.sid === to.sid, kind: "viewer", guest: a.guest } }));
        return;
      }
    }
    return fail("invalid_argument", "no such message", ref);
  }

  // bar is the link page's own room: who else has the page open, by name
  bar(ws, a, m, ref, fail) {
    if (!a.id) return fail("not_granted", "sign in to be in the room", ref);
    if (!this.allowed(a.sid)) return fail("rate_limited", "the bar's joins are held to about 40 a second", ref);
    const room = BAR;
    const inRoom = this.sql.exec("SELECT 1 AS y FROM live_members WHERE sid = ? AND room = ?", a.sid, room).toArray().length > 0;
    if (m.t === "leave") {
      if (!inRoom) return;
      this.sql.exec("DELETE FROM live_members WHERE sid = ? AND room = ?", a.sid, room);
      this.tell(room, () => ({ t: "gone", room, peer: a.sid }));
      return;
    }
    if (m.t !== "join") return fail("not_permitted", "nothing is sent in the page's bar", ref);
    if (!inRoom) {
      const n = this.sql.exec("SELECT COUNT(*) AS n FROM live_members WHERE room = ?", room).one().n;
      if (n >= MAX_PEERS) return fail("limit_reached", `a room holds ${MAX_PEERS} pages at most`, ref);
      const now = Date.now();
      const presence = JSON.stringify({ name: a.n || "", owner: !!a.o });
      this.sql.exec("INSERT INTO live_members (sid, room, presence, updated) VALUES (?, ?, ?, ?)", a.sid, room, presence, now);
      const me = { sid: a.sid, presence, updated: now };
      this.tell(room, (to) => ({ t: "peer", room, peer: this.peer(me, a, to) }), a.sid);
    }
    const peers = [];
    for (const mem of this.members(room)) {
      const att = this.socket(mem.sid)?.deserializeAttachment();
      if (att) peers.push(this.peer(mem, att, a));
    }
    ws.send(JSON.stringify({ t: "joined", ref, room, peers }));
  }

  // sweep closes the sockets whose session ended: a quiet one sends
  // nothing, so webSocketMessage never finds it expired, and it would
  // count against the limits for good
  sweep(now) {
    for (const ws of this.ctx.getWebSockets()) {
      const a = ws.deserializeAttachment();
      if (!a || !a.exp || a.exp >= now) continue;
      try {
        ws.close(4002, "expired");
      } catch {}
      this.gone(ws);
    }
  }
  async webSocketClose(ws) {
    this.gone(ws);
  }
  async webSocketError(ws) {
    this.gone(ws);
  }
  gone(ws) {
    const a = ws.deserializeAttachment();
    if (!a) return;
    this.buckets.delete(a.sid);
    // after drop the tables are gone with everything else
    if (!this.sql.exec("SELECT 1 FROM sqlite_master WHERE name = 'live_members'").toArray().length) return;
    const rooms = this.sql.exec("SELECT room FROM live_members WHERE sid = ?", a.sid).toArray().map((r) => r.room);
    this.sql.exec("DELETE FROM live_members WHERE sid = ?", a.sid);
    for (const room of rooms) this.tell(room, () => ({ t: "gone", room, peer: a.sid }));
  }
}
