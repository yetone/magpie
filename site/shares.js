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
//
// SHARES (R2) holds the files, at <id>/<version>/<path>; SHARES_DB (D1)
// the shares and each address's asks, kept only hashed and for an hour,
// to hold each one to RATE shares or updates an hour. A version's files
// go up before its row points at them, and the version before is deleted
// after, so a viewer never meets half a version.

const SITE = "https://usemagpie.ai";
export const CONTENT = "a.usemagpie.ai";
export const MAX_BYTES = 32 << 20; // a version's files together
export const MAX_FILES = 256; // the page included
export const RATE = 30; // shares and updates an address makes in an hour
const MAX_TITLE = 200;
const MAX_DESCRIPTION = 1000;
const MAX_PATH = 512;
const ID = /^[a-z2-7]{16}$/;
const PAGE = "index.html";
// what the frame lets the page do, as magpie's own artifactPageCSP
const SANDBOX = "allow-scripts allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox allow-downloads";
const REPORT_TO = "yetoneful@gmail.com";

const SCHEMA = [
  "CREATE TABLE IF NOT EXISTS shares (id TEXT PRIMARY KEY, token TEXT NOT NULL, title TEXT NOT NULL, description TEXT NOT NULL, version INTEGER NOT NULL, files TEXT NOT NULL, bytes INTEGER NOT NULL, created INTEGER NOT NULL, updated INTEGER NOT NULL)",
  "CREATE TABLE IF NOT EXISTS asks (who TEXT NOT NULL, hour INTEGER NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (who, hour))",
];
const made = new WeakMap();
function schema(db) {
  if (!made.has(db))
    made.set(
      db,
      db.batch(SCHEMA.map((s) => db.prepare(s))).catch((e) => {
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

function json(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" },
  });
}
const refuse = (status, error) => json({ error }, status);

function hex(buf) {
  return [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, "0")).join("");
}
async function sha256(s) {
  return hex(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s)));
}
function randomID() {
  const a = "abcdefghijklmnopqrstuvwxyz234567";
  return [...crypto.getRandomValues(new Uint8Array(16))].map((b) => a[b & 31]).join("");
}
function randomToken() {
  return btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(32)))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// same is a and b compared in a time that doesn't depend on where they
// differ
function same(a, b) {
  if (typeof a !== "string" || typeof b !== "string" || a.length !== b.length) return false;
  let d = 0;
  for (let i = 0; i < a.length; i++) d |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return d === 0;
}

// held counts the caller's ask, and reports whether it has made RATE
// this hour already
async function held(db, req, now) {
  const who = (await sha256("magpie-shares:" + (req.headers.get("CF-Connecting-IP") || ""))).slice(0, 24);
  const row = await db
    .prepare("INSERT INTO asks (who, hour, n) VALUES (?1, ?2, 1) ON CONFLICT (who, hour) DO UPDATE SET n = n + 1 RETURNING n")
    .bind(who, Math.floor(now / 36e5))
    .first();
  return (row?.n ?? 0) > RATE;
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
  return { title, description, files, bytes };
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
  if (path !== "/api/shares" && !one) return null;
  const db = env.SHARES_DB, bucket = env.SHARES;
  if (!db || !bucket) return refuse(503, "sharing isn't set up here");
  await schema(db);
  if (!one) {
    if (req.method !== "POST") return refuse(405, "POST shares a page");
    if (await held(db, req, now)) return refuse(429, `at most ${RATE} shares or updates an hour; try again later`);
    const v = await version(req);
    if (v.error) return refuse(v.status, v.error);
    const id = randomID(), token = randomToken();
    await putVersion(bucket, id, 1, v.files);
    try {
      await db
        .prepare("INSERT INTO shares (id, token, title, description, version, files, bytes, created, updated) VALUES (?1, ?2, ?3, ?4, 1, ?5, ?6, ?7, ?7)")
        .bind(id, await sha256(token), v.title, v.description, JSON.stringify(v.files.map((f) => f.path)), v.bytes, now)
        .run();
    } catch (e) {
      ctx.waitUntil(dropPrefix(bucket, `${id}/`));
      throw e;
    }
    return json({ id, url: shareURL(id), token, version: 1 }, 201);
  }
  const id = one[1];
  if (req.method !== "PUT" && req.method !== "DELETE") return refuse(405, "PUT updates a shared page, DELETE takes it down");
  const row = await db.prepare("SELECT token, version FROM shares WHERE id = ?1").bind(id).first();
  if (!row) return refuse(404, "no such shared page: it was taken down");
  if (!(await authorized(req, env, row))) return refuse(403, "that isn't this page's token");
  if (req.method === "DELETE") {
    await db.prepare("DELETE FROM shares WHERE id = ?1").bind(id).run();
    ctx.waitUntil(dropPrefix(bucket, `${id}/`));
    return json({ deleted: true });
  }
  if (await held(db, req, now)) return refuse(429, `at most ${RATE} shares or updates an hour; try again later`);
  const v = await version(req);
  if (v.error) return refuse(v.status, v.error);
  const n = row.version + 1;
  await putVersion(bucket, id, n, v.files);
  const done = await db
    .prepare("UPDATE shares SET version = ?1, title = ?2, description = ?3, files = ?4, bytes = ?5, updated = ?6 WHERE id = ?7 AND version = ?8")
    .bind(n, v.title, v.description, JSON.stringify(v.files.map((f) => f.path)), v.bytes, now, id, row.version)
    .run();
  if (!done.meta?.changes) {
    // taken down, or another update won, while this one went up
    ctx.waitUntil(dropPrefix(bucket, `${id}/${n}/`));
    return refuse(409, "the page changed while this update went up; try again");
  }
  ctx.waitUntil(dropPrefix(bucket, `${id}/${row.version}/`));
  return json({ id, url: shareURL(id), version: n });
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
  const row = await env.SHARES_DB.prepare("SELECT title, description, version FROM shares WHERE id = ?1").bind(m[1]).first();
  if (!row) return gone();
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
a{color:var(--mute);text-decoration:none;white-space:nowrap;transition:color .15s ease}
a:hover{color:var(--fg)}
iframe{flex:1;width:100%;border:0}
</style>
</head>
<body>
<header><h1 title="${title}">${title}</h1><a href="${SITE}/" target="_blank" rel="noopener">magpie</a><a href="${esc(report)}">Report</a></header>
<iframe src="${esc(src)}" title="${title}" sandbox="${SANDBOX}" allow="clipboard-write; fullscreen" referrerpolicy="no-referrer"></iframe>
</body>
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
      "Content-Security-Policy": `default-src 'none'; style-src 'unsafe-inline'; img-src ${SITE}; frame-src https://${CONTENT}; base-uri 'none'; form-action 'none'`,
      "X-Content-Type-Options": "nosniff",
      "Referrer-Policy": "no-referrer",
    },
  });
}

// content answers every request to the content host: a version's file,
// in the sandbox
export async function content(req, env) {
  const url = new URL(req.url);
  const m = url.pathname.match(/^\/([a-z2-7]{16})\/([1-9][0-9]{0,8})\/(.*)$/);
  if (req.method !== "GET" && req.method !== "HEAD") return new Response("not found", { status: 404 });
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

// prune drops the addresses' counts from before the hour before
export async function prune(env, now = Date.now()) {
  const db = env.SHARES_DB;
  if (!db) return;
  await schema(db);
  await db.prepare("DELETE FROM asks WHERE hour < ?1").bind(Math.floor(now / 36e5) - 1).run();
}
