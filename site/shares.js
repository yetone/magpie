// Shared artifacts (docs/subsystems/artifacts.md, "Sharing"): a page an
// agent published with Magpie Artifacts, which its user chose to share,
// served to anyone with its link.
//
// The link is usemagpie.ai/a/<id>: a page of the site's own, a slim bar
// with the artifact's title and a Report link over a frame. The artifact
// itself is in the frame, served from a host of its own (CONTENT,
// a.usemagpie.ai/<id>/<version>/<path>) under a CSP sandbox without
// allow-same-origin, as magpie serves it on the user's computer: someone
// else's code, with an opaque origin that reads nothing of the site's. A
// browser that opens the content host's page at the top (a link in a new
// tab, a typed address) is sent to the link instead, so it never shows
// without the bar.
//
//   - POST /api/shares shares one: multipart, a "meta" part
//     ({title, description, files: [path…]}, the page "index.html" among
//     the files) and part i holding files[i]. It answers {id, url, token,
//     version}; the token is given this once, and kept here only hashed.
//   - PUT /api/shares/<id>, with Authorization: Bearer <token>, puts a new
//     version up in its place, at the same link.
//   - DELETE /api/shares/<id>, with the token (or the site's admin token,
//     SHARE_ADMIN, for a page reported), takes it down for good.
//   - GET /api/shares/<id>/blobs, PUT and DELETE /api/shares/<id>/blobs/
//     <asset id>, with the token: the files the page uploaded through
//     claude.use("assets"), which are the artifact's and not a version's.
//     The page shows one at /_blob/<asset id>, an address naming no
//     share, so the content host serves it by its id alone (as magpie
//     does): blobs keeps which share holds each id, and an id another
//     share holds already is refused.
//
// SHARES (R2) holds the files, at <id>/<version>/<path>; SHARES_DB (D1)
// the shares and each address's asks, kept only hashed and for an hour,
// to hold each one to RATE shares or updates an hour. A version's files
// go up before its row points at them, and the version before is deleted
// after, so a viewer never meets half a version.

import * as Live from "./live.js";

export const SITE = "https://usemagpie.ai";
export const CONTENT = "a.usemagpie.ai";
export const MAX_BYTES = 32 << 20; // a version's files together
export const MAX_FILES = 256; // the page included
export const RATE = 30; // shares and updates an address makes in an hour
// a share's assets (internal/artifact assets.go): each as magpie takes
// it, and together at most MAX_BLOBS files, MAX_BLOB_BYTES
export const MAX_BLOBS = 256;
export const MAX_BLOB_BYTES = 100 << 20;
export const BLOB_RATE = 600; // asset uploads and deletes an address makes in an hour
const BLOB_TYPES = {
  "image/png": 20 << 20, "image/jpeg": 20 << 20, "image/gif": 20 << 20, "image/webp": 20 << 20, "image/svg+xml": 2 << 20,
  "video/mp4": 20 << 20, "video/webm": 20 << 20, "application/pdf": 20 << 20,
  "font/woff2": 20 << 20, "font/woff": 20 << 20, "font/ttf": 20 << 20, "font/otf": 20 << 20,
  "text/csv": 20 << 20, "text/markdown": 20 << 20, "application/json": 20 << 20, "text/plain": 20 << 20,
  "text/css": 16 << 20, "text/javascript": 16 << 20,
};
const BLOB_ID = /^[a-z2-7]{32}$/;
// An id a share has held, or asked for, stays the share's while the share
// is up: a row with no type (and no bytes) keeps it when the asset is
// taken down, or was refused. The content host serves /_blob/<id> by the
// id alone, so a page still naming one it no longer has (its data, an
// older copy, an asset refused for its size) would otherwise show
// whatever another share put up under that id next: an image swapped, or
// a script, a stylesheet or an SVG of a stranger's run as this page's.
// A share keeps this many ids in all; past it a taken-down id is let go.
const MAX_KEPT_IDS = 4096;
const MAX_TITLE = 200;
const MAX_DESCRIPTION = 1000;
const MAX_PATH = 512;
const ID = /^[a-z2-7]{16}$/;
const OWNER = /^u_[0-9a-f]{24}$/; // magpie's viewer id (internal/artifact viewerID)
const MAX_CAPS = 64 << 10;
const PAGE = "index.html";
// what the frame lets the page do, as magpie's own artifactPageCSP
const SANDBOX = "allow-scripts allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox allow-downloads";
const REPORT_TO = "yetoneful@gmail.com";

const SCHEMA = [
  "CREATE TABLE IF NOT EXISTS shares (id TEXT PRIMARY KEY, token TEXT NOT NULL, title TEXT NOT NULL, description TEXT NOT NULL, version INTEGER NOT NULL, files TEXT NOT NULL, bytes INTEGER NOT NULL, created INTEGER NOT NULL, updated INTEGER NOT NULL)",
  "CREATE TABLE IF NOT EXISTS asks (who TEXT NOT NULL, hour INTEGER NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (who, hour))",
  "CREATE TABLE IF NOT EXISTS blobs (id TEXT PRIMARY KEY, share TEXT NOT NULL, type TEXT NOT NULL, bytes INTEGER NOT NULL, created INTEGER NOT NULL)",
  "CREATE INDEX IF NOT EXISTS blobs_share ON blobs (share)",
];
// columns added since the table was made: a live share's capabilities,
// and its owner's id and name (live.js)
const COLUMNS = [
  ["caps", "TEXT NOT NULL DEFAULT '{}'"],
  ["owner_id", "TEXT"],
  ["owner_name", "TEXT"],
];
async function migrate(db) {
  await db.batch(SCHEMA.map((s) => db.prepare(s)));
  const have = new Set(((await db.prepare("PRAGMA table_info(shares)").all()).results || []).map((r) => r.name));
  for (const [c, t] of COLUMNS) {
    if (have.has(c)) continue;
    try {
      await db.prepare(`ALTER TABLE shares ADD COLUMN ${c} ${t}`).run();
    } catch (e) {
      // another request added it first
      if (!/duplicate column/i.test(String(e?.message))) throw e;
    }
  }
}
const made = new WeakMap();
export function schema(db) {
  if (!made.has(db))
    made.set(
      db,
      migrate(db).catch((e) => {
        made.delete(db);
        throw e;
      }),
    );
  return made.get(db);
}

const TYPES = {
  html: "text/html; charset=utf-8", htm: "text/html; charset=utf-8", css: "text/css; charset=utf-8",
  js: "text/javascript; charset=utf-8", mjs: "text/javascript; charset=utf-8", json: "application/json; charset=utf-8",
  map: "application/json; charset=utf-8", txt: "text/plain; charset=utf-8", md: "text/markdown; charset=utf-8",
  csv: "text/csv; charset=utf-8", xml: "application/xml; charset=utf-8", svg: "image/svg+xml",
  png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp", avif: "image/avif",
  ico: "image/x-icon", bmp: "image/bmp", woff: "font/woff", woff2: "font/woff2", ttf: "font/ttf", otf: "font/otf",
  mp3: "audio/mpeg", wav: "audio/wav", ogg: "audio/ogg", m4a: "audio/mp4", mp4: "video/mp4", webm: "video/webm",
  mov: "video/quicktime", pdf: "application/pdf", wasm: "application/wasm", glb: "model/gltf-binary", gltf: "model/gltf+json",
};
export function typeOf(path) {
  const ext = path.slice(path.lastIndexOf(".") + 1).toLowerCase();
  return TYPES[ext] || "application/octet-stream";
}

// cleanPath is a file's path as kept, or "" for one that isn't a plain
// relative path (as internal/artifact's CleanPath)
export function cleanPath(p) {
  if (typeof p !== "string" || !p || p.length > MAX_PATH || p.includes("\\") || p.startsWith("/") || /[\0-\x1f\x7f]/.test(p)) return "";
  const parts = p.split("/");
  if (parts.some((s) => s === "" || s === "." || s === "..")) return "";
  return p;
}

export function json(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" },
  });
}
export const refuse = (status, error) => json({ error }, status);

function hex(buf) {
  return [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, "0")).join("");
}
export async function sha256(s) {
  return hex(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s)));
}
function randomID() {
  const a = "abcdefghijklmnopqrstuvwxyz234567";
  return [...crypto.getRandomValues(new Uint8Array(16))].map((b) => a[b & 31]).join("");
}
export function randomToken() {
  return btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(32)))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// same is a and b compared in a time that doesn't depend on where they
// differ
export function same(a, b) {
  if (typeof a !== "string" || typeof b !== "string" || a.length !== b.length) return false;
  let d = 0;
  for (let i = 0; i < a.length; i++) d |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return d === 0;
}

// held counts the caller's ask of a kind (shares, visitors: sign-ins
// don't spend what sharing has), and reports whether it has made RATE
// this hour already
export async function held(db, req, now, kind = "shares", most = RATE) {
  const who = (await sha256("magpie-" + kind + ":" + (req.headers.get("CF-Connecting-IP") || ""))).slice(0, 24);
  const row = await db
    .prepare("INSERT INTO asks (who, hour, n) VALUES (?1, ?2, 1) ON CONFLICT (who, hour) DO UPDATE SET n = n + 1 RETURNING n")
    .bind(who, Math.floor(now / 36e5))
    .first();
  return (row?.n ?? 0) > most;
}

// version reads a share's version from the request: its meta and files,
// or an error to answer with
async function version(req) {
  const len = +(req.headers.get("Content-Length") || 0);
  if (len > MAX_BYTES + (1 << 20)) return { error: `a shared page and its files hold ${MAX_BYTES >> 20} MB at most`, status: 413 };
  let form;
  try {
    form = await req.formData();
  } catch {
    return { error: "the body isn't multipart/form-data", status: 400 };
  }
  let meta;
  try {
    meta = JSON.parse(form.get("meta"));
  } catch {
    return { error: "no meta part, or it isn't JSON", status: 400 };
  }
  const title = typeof meta?.title === "string" ? meta.title.trim().slice(0, MAX_TITLE) : "";
  const description = typeof meta?.description === "string" ? meta.description.trim().slice(0, MAX_DESCRIPTION) : "";
  if (!title) return { error: "a shared page needs a title", status: 400 };
  if (!Array.isArray(meta.files) || !meta.files.length || meta.files.length > MAX_FILES)
    return { error: `files lists the page and at most ${MAX_FILES - 1} files beside it`, status: 400 };
  const files = [];
  const seen = new Set();
  let bytes = 0;
  for (let i = 0; i < meta.files.length; i++) {
    const path = cleanPath(meta.files[i]);
    if (!path || seen.has(path)) return { error: `files[${i}] isn't a relative path, or is there twice`, status: 400 };
    seen.add(path);
    const part = form.get(String(i));
    if (!part || typeof part === "string") return { error: `no part ${i} for ${path}`, status: 400 };
    const body = new Uint8Array(await part.arrayBuffer());
    bytes += body.length;
    if (bytes > MAX_BYTES) return { error: `a shared page and its files hold ${MAX_BYTES >> 20} MB at most`, status: 413 };
    files.push({ path, body });
  }
  if (!seen.has(PAGE)) return { error: `files has no ${PAGE}, the page`, status: 400 };
  // what the version declares (live.js serves db, room and user), and
  // the magpie user's id, which their own data/users/<id>/ is kept under
  let caps = {};
  if (meta.capabilities != null) {
    const c = meta.capabilities;
    if (typeof c !== "object" || Array.isArray(c) || JSON.stringify(c).length > MAX_CAPS) return { error: `capabilities is an object of at most ${MAX_CAPS >> 10} KiB`, status: 400 };
    caps = c;
  }
  if (meta.owner != null && !(typeof meta.owner === "string" && OWNER.test(meta.owner))) return { error: "owner is magpie's viewer id, u_ and 24 hex digits", status: 400 };
  const owner = meta.owner ?? null;
  // the page's data, for a share that keeps it here: [{path, data,
  // version?, updated?}], as magpie's store dumps it
  let data = null;
  const part = form.get("data");
  if (part != null) {
    const text = typeof part === "string" ? part : await part.text();
    bytes += text.length;
    if (bytes > MAX_BYTES) return { error: `a shared page and its files hold ${MAX_BYTES >> 20} MB at most`, status: 413 };
    try {
      data = JSON.parse(text);
    } catch {
      return { error: "the data part isn't JSON", status: 400 };
    }
    if (!Array.isArray(data)) return { error: "the data part is a list of documents", status: 400 };
  }
  return { title, description, files, bytes, caps, owner, data };
}

async function putVersion(bucket, id, n, files) {
  for (let i = 0; i < files.length; i += 16)
    await Promise.all(
      files.slice(i, i + 16).map((f) => bucket.put(`${id}/${n}/${f.path}`, f.body, { httpMetadata: { contentType: typeOf(f.path) } })),
    );
}

async function dropPrefix(bucket, prefix) {
  for (;;) {
    const got = await bucket.list({ prefix, limit: 1000 });
    const keys = got.objects.map((o) => o.key);
    if (keys.length) await bucket.delete(keys);
    if (!got.truncated || !keys.length) return;
  }
}

const shareURL = (id) => `${SITE}/a/${id}`;

// capsOf is a share's row's capabilities, {} when there are none
export function capsOf(row) {
  try {
    const c = JSON.parse(row?.caps || "{}");
    return c && typeof c === "object" && !Array.isArray(c) ? c : {};
  } catch {
    return {};
  }
}

async function authorized(req, env, row) {
  const m = (req.headers.get("Authorization") || "").match(/^Bearer\s+(\S+)$/);
  if (!m) return false;
  if (env.SHARE_ADMIN && same(m[1], env.SHARE_ADMIN)) return true;
  return same(await sha256(m[1]), row.token);
}

// handle answers /api/shares and /api/shares/<id>, or null for another
// path
export async function api(req, env, ctx, now = Date.now()) {
  const path = new URL(req.url).pathname;
  const one = path.match(/^\/api\/shares\/([a-z2-7]{16})$/);
  const blob = path.match(/^\/api\/shares\/([a-z2-7]{16})\/blobs(?:\/([^/]*))?$/);
  if (path !== "/api/shares" && !one && !blob) return null;
  const db = env.SHARES_DB, bucket = env.SHARES;
  if (!db || !bucket) return refuse(503, "sharing isn't set up here");
  await schema(db);
  if (blob) return blobAPI(req, env, ctx, now, blob[1], blob[2]);
  if (!one) {
    if (req.method !== "POST") return refuse(405, "POST shares a page");
    if (await held(db, req, now)) return refuse(429, `at most ${RATE} shares or updates an hour; try again later`);
    const v = await version(req);
    if (v.error) return refuse(v.status, v.error);
    const id = randomID(), token = randomToken();
    await putVersion(bucket, id, 1, v.files);
    try {
      // the page's data goes to its object before the share is there to
      // be opened
      if (v.data && v.caps.db) await Live.seed(env, id, v.caps, v.data);
      await db
        .prepare("INSERT INTO shares (id, token, title, description, version, files, bytes, created, updated, caps, owner_id) VALUES (?1, ?2, ?3, ?4, 1, ?5, ?6, ?7, ?7, ?8, ?9)")
        .bind(id, await sha256(token), v.title, v.description, JSON.stringify(v.files.map((f) => f.path)), v.bytes, now, JSON.stringify(v.caps), v.owner)
        .run();
    } catch (e) {
      ctx.waitUntil(dropPrefix(bucket, `${id}/`));
      ctx.waitUntil(Live.drop(env, id).catch(() => {}));
      if (e instanceof Live.SeedError) return refuse(400, "the page's data wasn't taken: " + e.message);
      throw e;
    }
    return json({ id, url: shareURL(id), token, version: 1 }, 201);
  }
  const id = one[1];
  if (req.method !== "PUT" && req.method !== "DELETE") return refuse(405, "PUT updates a shared page, DELETE takes it down");
  const row = await db.prepare("SELECT token, version, caps FROM shares WHERE id = ?1").bind(id).first();
  if (!row) return refuse(404, "no such shared page: it was taken down");
  if (!(await authorized(req, env, row))) return refuse(403, "that isn't this page's token");
  if (req.method === "DELETE") {
    await db.batch([db.prepare("DELETE FROM shares WHERE id = ?1").bind(id), db.prepare("DELETE FROM blobs WHERE share = ?1").bind(id)]);
    ctx.waitUntil(dropPrefix(bucket, `${id}/`));
    ctx.waitUntil(Live.drop(env, id));
    return json({ deleted: true });
  }
  if (await held(db, req, now)) return refuse(429, `at most ${RATE} shares or updates an hour; try again later`);
  const v = await version(req);
  if (v.error) return refuse(v.status, v.error);
  // a share that kept no data here before takes the page's now; one that
  // did keeps its own, which its viewers have been writing (the share's
  // object tells them apart: it was seeded or it wasn't), even when a
  // version between declared no db
  if (v.data && v.caps.db) {
    try {
      await Live.seed(env, id, v.caps, v.data);
    } catch (e) {
      if (e instanceof Live.SeedError) return refuse(400, "the page's data wasn't taken: " + e.message);
      throw e;
    }
  }
  const n = row.version + 1;
  await putVersion(bucket, id, n, v.files);
  const done = await db
    .prepare("UPDATE shares SET version = ?1, title = ?2, description = ?3, files = ?4, bytes = ?5, updated = ?6, caps = ?9 WHERE id = ?7 AND version = ?8")
    .bind(n, v.title, v.description, JSON.stringify(v.files.map((f) => f.path)), v.bytes, now, id, row.version, JSON.stringify(v.caps))
    .run();
  if (!done.meta?.changes) {
    // taken down, or another update won, while this one went up
    ctx.waitUntil(dropPrefix(bucket, `${id}/${n}/`));
    return refuse(409, "the page changed while this update went up; try again");
  }
  ctx.waitUntil(dropPrefix(bucket, `${id}/${row.version}/`));
  // every open view reloads to it; a share that has never been live has
  // no object to tell
  if (Live.live(v.caps) || Live.live(capsOf(row))) ctx.waitUntil(Live.version(env, id, n, v.caps));
  return json({ id, url: shareURL(id), version: n });
}

// blobAPI answers a share's assets: GET lists them, PUT puts one up (its
// bytes the body, its type the Content-Type), DELETE takes one down
async function blobAPI(req, env, ctx, now, id, aid) {
  const db = env.SHARES_DB, bucket = env.SHARES;
  if (aid === undefined ? req.method !== "GET" : req.method !== "PUT" && req.method !== "DELETE")
    return refuse(405, aid === undefined ? "GET lists a shared page's assets" : "PUT puts an asset up, DELETE takes it down");
  if (aid !== undefined && !BLOB_ID.test(aid)) return refuse(400, "an asset's id is 32 of a-z and 2-7");
  const row = await db.prepare("SELECT token FROM shares WHERE id = ?1").bind(id).first();
  if (!row) return refuse(404, "no such shared page: it was taken down");
  if (!(await authorized(req, env, row))) return refuse(403, "that isn't this page's token");
  if (aid === undefined) {
    const got = (await db.prepare("SELECT id, type, bytes FROM blobs WHERE share = ?1 AND type != '' ORDER BY created, id").bind(id).all()).results || [];
    return json({ blobs: got, maxFiles: MAX_BLOBS, maxBytes: MAX_BLOB_BYTES });
  }
  if (await held(db, req, now, "blobs", BLOB_RATE)) return refuse(429, `at most ${BLOB_RATE} asset uploads or deletes an hour; try again later`);
  const had = await db.prepare("SELECT share, type FROM blobs WHERE id = ?1").bind(aid).first();
  if (had && had.share !== id) return refuse(409, "another shared page holds that asset");
  if (req.method === "DELETE") {
    if (had) {
      // kept as the share's, with no type, unless it keeps too many
      const kept = await db.prepare("SELECT COUNT(*) AS n FROM blobs WHERE share = ?1").bind(id).first();
      if ((kept?.n ?? 0) < MAX_KEPT_IDS) await db.prepare("UPDATE blobs SET type = '', bytes = 0 WHERE id = ?1 AND share = ?2").bind(aid, id).run();
      else await db.prepare("DELETE FROM blobs WHERE id = ?1 AND share = ?2").bind(aid, id).run();
      await bucket.delete(`${id}/_blob/${aid}`);
    }
    return json({ deleted: !!had?.type });
  }
  // an id refused is still kept for this share: its page names it
  const refused = async (status, message) => {
    await keepID(db, aid, id, now);
    return refuse(status, message);
  };
  const type = req.headers.get("Content-Type") || "";
  const most = BLOB_TYPES[type];
  if (!most) return refused(415, "not a type an asset may be");
  if (+(req.headers.get("Content-Length") || 0) > most) return refused(413, `a ${type} asset is ${most >> 20} MB at most`);
  const body = new Uint8Array(await req.arrayBuffer());
  if (!body.length) return refused(400, "the asset is empty");
  if (body.length > most) return refused(413, `a ${type} asset is ${most >> 20} MB at most`);
  const sum = await db.prepare("SELECT COUNT(*) AS n, COALESCE(SUM(bytes), 0) AS b FROM blobs WHERE share = ?1 AND id != ?2 AND type != ''").bind(id, aid).first();
  if (sum.n + 1 > MAX_BLOBS || sum.b + body.length > MAX_BLOB_BYTES)
    return refused(413, `a shared page's assets are at most ${MAX_BLOBS} files, ${MAX_BLOB_BYTES >> 20} MB together`);
  await bucket.put(`${id}/_blob/${aid}`, body, { httpMetadata: { contentType: type } });
  // the row last, and only while no other share took the id and this
  // share is still up: a takedown that ran meanwhile leaves no row
  const done = await db
    .prepare("INSERT INTO blobs (id, share, type, bytes, created) SELECT ?1, ?2, ?3, ?4, ?5 WHERE EXISTS (SELECT 1 FROM shares WHERE id = ?2) ON CONFLICT (id) DO UPDATE SET type = ?3, bytes = ?4 WHERE share = ?2")
    .bind(aid, id, type, body.length, now)
    .run();
  if (!done.meta?.changes) {
    ctx.waitUntil(bucket.delete(`${id}/_blob/${aid}`));
    return refuse(409, "the page was taken down, or another one holds that asset");
  }
  return json({ id: aid, type, bytes: body.length }, had?.type ? 200 : 201);
}

// keepID keeps aid as share's, with no asset, while the share is up, no
// other share holds it and share keeps fewer than MAX_KEPT_IDS
async function keepID(db, aid, share, now) {
  await db
    .prepare("INSERT INTO blobs (id, share, type, bytes, created) SELECT ?1, ?2, '', 0, ?3 WHERE EXISTS (SELECT 1 FROM shares WHERE id = ?2) AND (SELECT COUNT(*) FROM blobs WHERE share = ?2) < ?4 ON CONFLICT (id) DO NOTHING")
    .bind(aid, share, now, MAX_KEPT_IDS)
    .run();
}

const esc = (s) => s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

function gone() {
  return new Response(
    `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>Not shared</title>` +
      `<style>:root{color-scheme:light dark}body{margin:0;min-height:100vh;display:grid;place-items:center;font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",system-ui,sans-serif;background:Canvas;color:CanvasText}p{opacity:.7}</style>` +
      `<p>This page isn't shared any more.</p>`,
    { status: 404, headers: { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" } },
  );
}

// page answers usemagpie.ai/a/<id>[/<path>]: the bar and the frame, or
// null for another path
export async function page(req, env) {
  const url = new URL(req.url);
  const m = url.pathname.match(/^\/a\/([a-z2-7]{16})(\/.*)?$/);
  if (!m || (req.method !== "GET" && req.method !== "HEAD")) return null;
  if (!env.SHARES_DB) return gone();
  await schema(env.SHARES_DB);
  const row = await env.SHARES_DB.prepare("SELECT title, description, version, caps FROM shares WHERE id = ?1").bind(m[1]).first();
  if (!row) return gone();
  // a page with data or a room here is opened with a session (live.js)
  const live = !!env.LIVE && Live.live(capsOf(row));
  const nonce = randomToken();
  // the frame's key: its runtime reads it from the address it was opened
  // at and says it with hello, so a page a link in the frame navigates to
  // (same window, same opaque origin) is never handed the session
  const key = randomToken();
  const sub = (m[2] || "/").slice(1);
  if (sub && !cleanPath(sub)) return gone();
  const src = `https://${CONTENT}/${m[1]}/${row.version}/${sub.split("/").map(encodeURIComponent).join("/")}${url.search}`;
  const report = `mailto:${REPORT_TO}?subject=${encodeURIComponent("Report a shared page: " + m[1])}&body=${encodeURIComponent(shareURL(m[1]) + "\n\nWhat's wrong with it:\n")}`;
  const title = esc(row.title), desc = esc(row.description || "");
  const html = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>${title}</title>
${desc ? `<meta name="description" content="${desc}">\n<meta property="og:description" content="${desc}">\n` : ""}<meta property="og:title" content="${title}">
<meta name="robots" content="noindex">
<meta name="referrer" content="no-referrer">
<link rel="icon" href="/favicon.png">
<style>
:root{color-scheme:light dark;--bg:#fff;--fg:#1d1d1f;--mute:#6e6e73;--line:rgba(0,0,0,.1)}
@media (prefers-color-scheme:dark){:root{--bg:#1c1c1e;--fg:#f5f5f7;--mute:#98989d;--line:rgba(255,255,255,.12)}}
html,body{margin:0;height:100%;background:var(--bg);color:var(--fg)}
body{display:flex;flex-direction:column;font:13px/1.4 -apple-system,BlinkMacSystemFont,"Segoe UI",system-ui,sans-serif}
header{flex:none;display:flex;align-items:center;gap:16px;height:40px;padding:0 16px;border-bottom:1px solid var(--line)}
h1{flex:1;min-width:0;margin:0;font-size:13px;font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
a,.me{color:var(--mute);text-decoration:none;white-space:nowrap;transition:color .15s ease}
a:hover,.me:hover{color:var(--fg)}
iframe{flex:1;width:100%;border:0}
${live ? LIVE_CSS : ""}</style>
</head>
<body>
<header><h1 title="${title}">${title}</h1>${live ? `<span class="peers" hidden></span><button class="me" type="button" hidden></button>` : ""}<a href="${SITE}/" target="_blank" rel="noopener">magpie</a><a href="${esc(report)}">Report</a></header>
<iframe src="${esc(live ? src + "#magpie-share=" + key : src)}" title="${title}" sandbox="${SANDBOX}" allow="clipboard-write; fullscreen" referrerpolicy="no-referrer"></iframe>
${live ? `<form class="sheet" hidden><input maxlength="40" autocomplete="nickname" placeholder="Name others see" required><button type="submit">Join</button><button class="out" type="button" hidden>Sign out</button></form>
<script nonce="${nonce}">var __name = (f) => f;(${shell.toString()})(${JSON.stringify({ id: m[1], key })})</script>
` : ""}</body>
</html>
`;
  return new Response(req.method === "HEAD" ? null : html, {
    headers: {
      "Content-Type": "text/html; charset=utf-8",
      // no-transform: Cloudflare injects its Web Analytics beacon into
      // HTML a worker answers, and skips a response that says this; a shared
      // page is served as its author wrote it, with nothing of the site run
      // in it
      "Cache-Control": "no-cache, no-transform",
      "Content-Security-Policy": `default-src 'none'; script-src 'nonce-${nonce}'; style-src 'unsafe-inline'; img-src ${SITE}; frame-src https://${CONTENT}; connect-src 'self' ${SITE.replace(/^https:/, "wss:")}; base-uri 'none'; form-action 'none'`,
      "X-Content-Type-Options": "nosniff",
      "Referrer-Policy": "no-referrer",
    },
  });
}

const LIVE_CSS = `.me{flex:none;font:inherit;background:none;border:0;padding:0;cursor:pointer}
.peers{display:flex;align-items:center}
.peers span{display:grid;place-items:center;width:22px;height:22px;margin-left:-6px;border-radius:50%;border:2px solid var(--bg);background:var(--line);color:var(--fg);font-size:10px;font-weight:600}
.sheet{position:fixed;top:44px;right:12px;z-index:1;display:flex;flex-wrap:wrap;gap:8px;width:240px;padding:12px;border-radius:10px;background:var(--bg);border:1px solid var(--line);box-shadow:0 8px 28px rgba(0,0,0,.16);transform-origin:top right;transition:opacity .18s cubic-bezier(.23,1,.32,1),transform .18s cubic-bezier(.23,1,.32,1)}
.sheet[hidden]{display:none}
.sheet.in{opacity:0;transform:scale(.95)}
.sheet input{flex:1 1 100%;font:inherit;padding:6px 8px;border-radius:6px;border:1px solid var(--line);background:transparent;color:var(--fg)}
.sheet button{font:inherit;padding:5px 12px;border-radius:6px;border:1px solid var(--line);background:transparent;color:var(--fg);cursor:pointer}
.sheet button[type=submit]{background:var(--fg);color:var(--bg);border-color:var(--fg)}
.sheet button:active{transform:scale(.97)}
@media (prefers-reduced-motion:reduce){.sheet{transition:opacity .18s ease}.sheet.in{transform:none}}
`;

// shell runs in the link's page of a live share, as its own script (its
// source is sent, so it reads nothing from here). It signs the viewer in
// by name, opens a session (live.js), shows who else has the page open,
// and holds the one socket, which it relays to the frame:
//   frame → here  {magpie: "artifact-share", type: "hello", key} + a port
//   on the port:
//   frame → here  {magpie: "artifact-share", type: "live", msg} a room message
//                 {magpie: "artifact-share", type: "bye"} as it goes
//   here → frame  {magpie: "artifact-share", type: "session", api, token, viewer, caps}
//                 {magpie: "artifact-share", type: "live", msg} from the socket,
//                 and {t: "open"} or {t: "closed"} as it comes and goes
// The frame's origin is opaque, and a link in the frame opens another page
// in that same window, at the same opaque origin, so neither the origin
// nor the window tells the page's document from another. The frame is
// opened at #magpie-share=<key>; its runtime says the key with hello and
// hands over a port it made, and nothing is sent but on that port, which
// only that document holds. One that says hello without the key (the page
// reloaded itself, or another page) gets the frame opened again, at a new
// key.
// The link's page runs shell's source alone, without the bundle's
// helpers: wrangler keeps names, so esbuild may name each inner function
// with __name, which the page's script defines as nothing.
export function shell(cfg) {
  const ID = cfg.id, TAG = "artifact-share", BAR = "~";
  const frame = document.querySelector("iframe");
  const base = frame.getAttribute("src").split("#")[0];
  let key = cfg.key, reopened = [];
  const me = document.querySelector(".me"), peersEl = document.querySelector(".peers"), sheet = document.querySelector(".sheet");
  const field = sheet.querySelector("input");
  const store = {
    get(k) {
      try {
        return JSON.parse(localStorage.getItem(k) || "null");
      } catch {
        return null;
      }
    },
    set(k, v) {
      try {
        v == null ? localStorage.removeItem(k) : localStorage.setItem(k, JSON.stringify(v));
      } catch {}
    },
  };
  const OWNER = "magpie.owner." + ID, VISITOR = "magpie.visitor";
  let code = (location.hash.match(/(?:^#|&)owner=([A-Za-z0-9_-]+)/) || [])[1] || "";
  if (code) history.replaceState(null, "", location.pathname + location.search);
  let session = null, port = null, ws = null, wait = 1000, timer = 0, renewal = 0;
  const toFrame = (m) => port && port.postMessage(Object.assign({ magpie: TAG }, m));
  const give = () => session && toFrame({ type: "session", api: session.api, token: session.token, viewer: session.viewer, caps: session.caps });
  const drop = () => {
    if (port) port.close();
    port = null;
  };

  async function open() {
    const v = store.get(VISITOR);
    let r;
    try {
      r = await fetch("/api/shares/" + ID + "/session", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ visitor: v && v.key, owner: store.get(OWNER) || undefined, code: code || undefined }),
      });
    } catch {
      return later(open);
    }
    code = "";
    if (r.status === 404) return location.reload();
    if (!r.ok) return later(open);
    session = await r.json();
    if (session.ownerKey) store.set(OWNER, session.ownerKey);
    if (v && !session.viewer.guest && !session.viewer.owner) store.set(VISITOR, null); // a visitor no more
    bar();
    give();
    // the token ends at exp: a new one ten minutes before, and a socket
    // that carries it (the old one's claims end with it)
    clearTimeout(renewal);
    renewal = setTimeout(open, Math.max(60000, (session.exp || 0) - Date.now() - 600000));
    if (ws) {
      ws.onclose = null;
      ws.close();
      ws = null;
      seen.clear();
      showPeers();
      toFrame({ type: "live", msg: { t: "closed" } });
    }
    connect();
  }
  // lapsed: the token is past or about to be, so a socket can't open on it
  const lapsed = () => !session || (session.exp || 0) - Date.now() < 60000;
  function later(fn) {
    clearTimeout(timer);
    timer = setTimeout(fn, wait);
    wait = Math.min(wait * 2, 30000);
  }

  function connect() {
    const url = (location.protocol === "https:" ? "wss://" : "ws://") + location.host + "/api/shares/" + ID + "/rt/live?t=" + encodeURIComponent(session.token);
    try {
      ws = new WebSocket(url);
    } catch {
      return later(connect);
    }
    ws.onopen = () => {
      wait = 1000;
      if (session.viewer.id) ws.send(JSON.stringify({ t: "join", room: BAR }));
      toFrame({ type: "live", msg: { t: "open" } });
    };
    ws.onmessage = (e) => {
      let m;
      try {
        m = JSON.parse(e.data);
      } catch {
        return;
      }
      if (m.t === "version") return location.reload();
      if (m.room === BAR) return heard(m);
      toFrame({ type: "live", msg: m });
    };
    ws.onclose = (e) => {
      ws = null;
      seen.clear();
      showPeers();
      toFrame({ type: "live", msg: { t: "closed" } });
      if (e.code === 4000) return location.reload();
      if (e.code === 4001) return location.reload();
      if (e.code === 4002 || lapsed()) return later(open);
      later(connect);
    };
  }

  function reopen() {
    // at most three times a minute: a page that loses its key at once
    // isn't loaded over and over
    const now = Date.now();
    reopened = reopened.filter((t) => now - t < 60000);
    if (reopened.length >= 3) return;
    reopened.push(now);
    const b = new Uint8Array(18);
    crypto.getRandomValues(b);
    key = btoa(String.fromCharCode(...b)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
    frame.src = base + "#magpie-share=" + key;
  }
  addEventListener("message", (e) => {
    if (e.source !== frame.contentWindow || !e.data || e.data.magpie !== TAG || e.data.type !== "hello") return;
    drop();
    if (e.data.key !== key || !e.ports[0]) return reopen();
    port = e.ports[0];
    port.onmessage = (p) => {
      const d = p.data;
      if (!d || d.magpie !== TAG || p.target !== port) return;
      if (d.type === "bye") return drop();
      if (d.type !== "live") return;
      const m = d.msg;
      if (!m || typeof m !== "object" || m.room === BAR || !ws || ws.readyState !== 1) return;
      ws.send(JSON.stringify(m));
    };
    give();
    if (ws && ws.readyState === 1) toFrame({ type: "live", msg: { t: "open" } });
  });

  // who else has the page open, by the names they signed in with
  const seen = new Map();
  function heard(m) {
    if (m.t === "joined") {
      seen.clear();
      for (const p of m.peers) if (!p.sameTab) seen.set(p.peer, p.presence || {});
    } else if (m.t === "peer" && !m.peer.sameTab) seen.set(m.peer.peer, m.peer.presence || {});
    else if (m.t === "gone") seen.delete(m.peer);
    showPeers();
  }
  function showPeers() {
    const names = new Map();
    for (const p of seen.values()) names.set((p.owner ? "o:" : "v:") + (p.name || ""), p);
    peersEl.replaceChildren();
    const all = [...names.values()];
    for (const p of all.slice(0, 4)) {
      const s = document.createElement("span");
      s.textContent = ([...(p.name || "?")][0] || "?").toUpperCase();
      s.title = p.name || "";
      peersEl.append(s);
    }
    if (all.length > 4) {
      const s = document.createElement("span");
      s.textContent = "+" + (all.length - 4);
      peersEl.append(s);
    }
    peersEl.hidden = !all.length;
  }

  function bar() {
    const v = session.viewer;
    me.textContent = v.name || "Sign in";
    me.hidden = false;
  }
  function show(on) {
    if (on === !sheet.hidden) return;
    if (!on) return (sheet.hidden = true);
    const v = store.get(VISITOR);
    field.value = (v && v.name) || "";
    sheet.querySelector(".out").hidden = !v && !store.get(OWNER);
    sheet.querySelector("[type=submit]").textContent = v ? "Save" : "Join";
    sheet.classList.add("in");
    sheet.hidden = false;
    requestAnimationFrame(() => requestAnimationFrame(() => sheet.classList.remove("in")));
    field.focus();
  }
  me.addEventListener("click", () => show(sheet.hidden));
  addEventListener("keydown", (e) => e.key === "Escape" && show(false));
  addEventListener("pointerdown", (e) => !sheet.hidden && !sheet.contains(e.target) && e.target !== me && show(false));
  sheet.addEventListener("submit", async (e) => {
    e.preventDefault();
    const name = field.value.trim();
    if (!name) return;
    const v = store.get(VISITOR);
    const r = await fetch(v ? "/api/visitors/me" : "/api/visitors", {
      method: v ? "PATCH" : "POST",
      headers: Object.assign({ "Content-Type": "application/json" }, v ? { Authorization: "Bearer " + v.key } : {}),
      body: JSON.stringify({ name }),
    }).catch(() => null);
    if (!r || !r.ok) {
      field.setCustomValidity((r && (await r.json().catch(() => null))?.error?.message) || "Couldn't sign in; try again");
      field.reportValidity();
      field.addEventListener("input", () => field.setCustomValidity(""), { once: true });
      return;
    }
    const got = await r.json();
    store.set(VISITOR, { key: v ? v.key : got.key, name: got.name });
    location.reload();
  });
  sheet.querySelector(".out").addEventListener("click", () => {
    store.set(VISITOR, null);
    store.set(OWNER, null);
    location.reload();
  });

  open();
}

// content answers every request to the content host: a version's file,
// in the sandbox
export async function content(req, env) {
  const url = new URL(req.url);
  const m = url.pathname.match(/^\/([a-z2-7]{16})\/([1-9][0-9]{0,8})\/(.*)$/);
  if (req.method !== "GET" && req.method !== "HEAD") return new Response("not found", { status: 404 });
  const b = url.pathname.match(/^\/_blob\/([a-z2-7]{32})$/);
  if (b) return blobContent(req, env, b[1]);
  const bare = url.pathname.match(/^\/([a-z2-7]{16})\/?$/);
  if (bare) return Response.redirect(shareURL(bare[1]), 302);
  if (!m) return new Response("not found", { status: 404 });
  const [, id, n] = m;
  let path;
  try {
    path = m[3] === "" ? PAGE : decodeURIComponent(m[3]);
  } catch {
    return new Response("not found", { status: 404 });
  }
  if (path.endsWith("/")) path += PAGE;
  if (!cleanPath(path)) return new Response("not found", { status: 404 });
  // opened at the top, out of the frame: the link's page shows it, with
  // the bar
  if (req.headers.get("Sec-Fetch-Dest") === "document") {
    const sub = path === PAGE ? "" : "/" + path.split("/").map(encodeURIComponent).join("/");
    return Response.redirect(shareURL(id) + sub + url.search, 302);
  }
  if (!env.SHARES_DB || !env.SHARES) return new Response("not found", { status: 404 });
  await schema(env.SHARES_DB);
  // a page taken down, or a version since replaced, is served no more,
  // whatever of its files is still being deleted
  const row = await env.SHARES_DB.prepare("SELECT version FROM shares WHERE id = ?1").bind(id).first();
  if (!row || row.version !== +n) return new Response("not found", { status: 404, headers: { "Cache-Control": "no-store" } });
  const obj = await env.SHARES.get(`${id}/${n}/${path}`);
  if (!obj) return new Response("not found", { status: 404 });
  return new Response(req.method === "HEAD" ? null : obj.body, {
    headers: {
      "Content-Type": typeOf(path),
      // a version's files never change: a new version is a new address
      "Cache-Control": "public, max-age=3600, no-transform",
      "Content-Security-Policy": `sandbox ${SANDBOX}`,
      "X-Content-Type-Options": "nosniff",
      "Referrer-Policy": "no-referrer",
      "Cross-Origin-Resource-Policy": "cross-origin",
      "Access-Control-Allow-Origin": "*",
    },
  });
}

// blobContent serves an asset at /_blob/<id>, from the share holding it,
// as magpie's artifactBlob does: its own type, and opened as a document
// sandboxed (no scripts, not even an SVG's), a PDF left to the browser
async function blobContent(req, env, aid) {
  if (!env.SHARES_DB || !env.SHARES) return new Response("not found", { status: 404 });
  await schema(env.SHARES_DB);
  const row = await env.SHARES_DB.prepare("SELECT b.share, b.type FROM blobs b JOIN shares s ON s.id = b.share WHERE b.id = ?1 AND b.type != ''").bind(aid).first();
  if (!row) return new Response("not found", { status: 404, headers: { "Cache-Control": "no-store" } });
  const obj = await env.SHARES.get(`${row.share}/_blob/${aid}`);
  if (!obj) return new Response("not found", { status: 404, headers: { "Cache-Control": "no-store" } });
  const h = {
    "Content-Type": row.type,
    "Cache-Control": "public, max-age=3600, no-transform",
    "X-Content-Type-Options": "nosniff",
    "Referrer-Policy": "no-referrer",
    "Cross-Origin-Resource-Policy": "cross-origin",
    "Access-Control-Allow-Origin": "*",
  };
  if (row.type !== "application/pdf")
    h["Content-Security-Policy"] = "sandbox; default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; font-src 'self'; media-src 'self'";
  return new Response(req.method === "HEAD" ? null : obj.body, { headers: h });
}

// prune drops the addresses' counts from before the hour before
export async function prune(env, now = Date.now()) {
  const db = env.SHARES_DB;
  if (!db) return;
  await schema(db);
  await db.prepare("DELETE FROM asks WHERE hour < ?1").bind(Math.floor(now / 36e5) - 1).run();
}
