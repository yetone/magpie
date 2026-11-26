// Shared routing groups (docs/subsystems/providers-accounts.md, "Routing
// groups"): a routing group a magpie user shared, served to anyone with its
// link, and listed in the gallery when its author chose to list it.
//
// What is shared is the text the Routing page's Copy as text gives (the
// app's provider.SharedGroup: {magpie: "routing-group", version, group}),
// with a title, a description and an author's name beside it. The group
// holds models by name, its rules and how it routes; never a key, an
// account or a provider's address, which the app leaves out before it
// sends, and which this side drops again if they come.
//
//   - POST /api/groups shares one: {title, description, author, public,
//     group}. It answers {id, url, token, version}; the token is given
//     this once, and kept here only hashed.
//   - PUT /api/groups/<id>, with Authorization: Bearer <token>, puts a new
//     version up in its place, at the same link.
//   - DELETE /api/groups/<id>, with the token (or the site's admin token,
//     SHARE_ADMIN, for a group reported), takes it down for good.
//   - GET /api/groups/<id> is one, with its users and likes; GET
//     /api/groups?sort=top|new&q=&page= the listed ones, a page at a time;
//     GET /api/groups?ids=a,b the version of each of these, listed or not,
//     for the app to tell an imported group's author changed it.
//   - POST /api/groups/report {id, groups} is the app's daily report of
//     the shared groups it has imported and still has, under its random
//     install id, as Discover's (pluginstats.js): a group's users are the
//     ids that reported it in the last seven days.
//   - POST /api/groups/like {id, group, on} likes one, once an id, or
//     takes the like back.
//   - usemagpie.ai/g/<id> is a group's page, with Open in magpie
//     (magpie://group?id=<id>), and /groups the gallery.
//
// SHARES_DB (D1) holds the groups, their users and likes, and each
// address's asks (the asks table shares.js keeps too, under its own
// hashes), kept only hashed and for an hour, to hold each one to RATE
// shares or updates an hour, and ASK_RATE reports and likes.

const SITE = "https://usemagpie.ai";
export const MARK = "routing-group";
export const MAX_FORMAT = 2; // the newest SharedGroup version this side knows
export const MAX_BODY = 64 << 10;
export const MAX_MEMBERS = 64;
export const RATE = 20; // shares and updates an address makes in an hour
export const ASK_RATE = 60; // reports and likes an address makes in an hour
export const PER_PAGE = 24;
export const MOST = 50; // groups in one report or one ids= ask
const MAX_TITLE = 80;
const MAX_DESCRIPTION = 300;
const MAX_AUTHOR = 40;
const MAX_MEMBER = 200;
const MAX_QUERY = 60;
const ID = /^[a-z2-7]{12}$/;
const DEVICE = /^[0-9a-f]{32}$/;
const MEMBER = /^[^\s<>"]+$/;
const WEEK = 7 * 864e5;
const KEEP = 30 * 864e5;
const REPORT_TO = "yetoneful@gmail.com";
// a link, or a name that reads as one, in the words beside a group: they
// are shown on the site's own pages, so they carry no ads
const LINK = /https?:\/\/|www\.|[a-z0-9-]+\.(com|net|org|io|ai|cn|xyz|top|cc|me|app|dev|co|vip|pro|site|shop|link)\b/i;
// what the app never shares (provider.ShareGroup and what it leaves out
// before it publishes), dropped if it comes: keys and accounts, the user's
// own note, and the state a group keeps of its own use
const PRIVATE = ["keys", "accounts", "redact", "note", "matched", "auto", "hidden", "disabled", "seen", "template", "former", "placed", "routedAt"];

const SCHEMA = [
  "CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, token TEXT NOT NULL, title TEXT NOT NULL, description TEXT NOT NULL, author TEXT NOT NULL, public INTEGER NOT NULL, body TEXT NOT NULL, models TEXT NOT NULL, version INTEGER NOT NULL, created INTEGER NOT NULL, updated INTEGER NOT NULL)",
  "CREATE INDEX IF NOT EXISTS groups_listed ON groups (public, created)",
  "CREATE TABLE IF NOT EXISTS group_uses (device TEXT NOT NULL, gid TEXT NOT NULL, day TEXT NOT NULL, PRIMARY KEY (device, gid))",
  "CREATE INDEX IF NOT EXISTS group_uses_day ON group_uses (day)",
  "CREATE INDEX IF NOT EXISTS group_uses_gid ON group_uses (gid, day)",
  "CREATE TABLE IF NOT EXISTS group_likes (device TEXT NOT NULL, gid TEXT NOT NULL, PRIMARY KEY (device, gid))",
  "CREATE INDEX IF NOT EXISTS group_likes_gid ON group_likes (gid)",
  "CREATE TABLE IF NOT EXISTS asks (who TEXT NOT NULL, hour INTEGER NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (who, hour))",
];
// the tables are made once an isolate, for each database it is given
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

function json(body, status = 200, cache = "no-store") {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8", "Cache-Control": cache },
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
  return [...crypto.getRandomValues(new Uint8Array(12))].map((b) => a[b & 31]).join("");
}
function randomToken() {
  return btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(32)))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
function randomNonce() {
  return btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(16))));
}

// same is a and b compared in a time that doesn't depend on where they
// differ
function same(a, b) {
  if (typeof a !== "string" || typeof b !== "string" || a.length !== b.length) return false;
  let d = 0;
  for (let i = 0; i < a.length; i++) d |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return d === 0;
}

// held counts the caller's ask, under kind, and reports whether it has
// made rate of them this hour already
async function held(db, req, now, kind, rate) {
  const who = (await sha256(kind + (req.headers.get("CF-Connecting-IP") || ""))).slice(0, 24);
  const row = await db
    .prepare("INSERT INTO asks (who, hour, n) VALUES (?1, ?2, 1) ON CONFLICT (who, hour) DO UPDATE SET n = n + 1 RETURNING n")
    .bind(who, Math.floor(now / 36e5))
    .first();
  return (row?.n ?? 0) > rate;
}
const heldShare = (db, req, now) => held(db, req, now, "magpie-groups:", RATE);
const heldAsk = (db, req, now) => held(db, req, now, "magpie-group-asks:", ASK_RATE);

const day = (t) => new Date(t).toISOString().slice(0, 10);
export const groupURL = (id) => `${SITE}/g/${id}`;

async function authorized(req, env, row) {
  const m = (req.headers.get("Authorization") || "").match(/^Bearer\s+(\S+)$/);
  if (!m) return false;
  if (env.SHARE_ADMIN && same(m[1], env.SHARE_ADMIN)) return true;
  return same(await sha256(m[1]), row.token);
}

// words is a title, description or author as kept: control characters
// and the marks that turn text around gone, at most max long, or an error
function words(v, max, name, need) {
  if (v == null) v = "";
  if (typeof v !== "string") return { error: `${name} is text` };
  const s = v.replace(/[\r\n\t]+/g, " ").replace(/[\0-\x1f\x7f‎‏‪-‮⁦-⁩]/g, "").trim();
  if (need && !s) return { error: `a shared group needs a ${name}` };
  if ([...s].length > max) return { error: `a ${name} is at most ${max} characters` };
  if (LINK.test(s)) return { error: "no links in a shared group's words" };
  return { s };
}

// read reads a shared group from the request: its words, whether it is
// listed, and the group as kept, or an error to answer with
async function read(req) {
  if (+(req.headers.get("Content-Length") || 0) > MAX_BODY) return { error: `a shared group is at most ${MAX_BODY >> 10} KB`, status: 413 };
  const text = await req.text();
  if (new TextEncoder().encode(text).length > MAX_BODY) return { error: `a shared group is at most ${MAX_BODY >> 10} KB`, status: 413 };
  let b;
  try {
    b = JSON.parse(text);
  } catch {
    return { error: "the body isn't JSON", status: 400 };
  }
  if (!b || typeof b !== "object" || Array.isArray(b)) return { error: "the body isn't a JSON object", status: 400 };
  const bad = (error) => ({ error, status: 400 });
  const title = words(b.title, MAX_TITLE, "title", true);
  if (title.error) return bad(title.error);
  const description = words(b.description, MAX_DESCRIPTION, "description");
  if (description.error) return bad(description.error);
  const author = words(b.author, MAX_AUTHOR, "author");
  if (author.error) return bad(author.error);
  const s = b.group;
  if (!s || typeof s !== "object" || s.magpie !== MARK) return bad(`group isn't a routing group magpie shared (magpie: "${MARK}")`);
  if (!Number.isInteger(s.version) || s.version < 1 || s.version > MAX_FORMAT) return bad(`group's version is 1 to ${MAX_FORMAT}`);
  const g = s.group;
  if (!g || typeof g !== "object" || Array.isArray(g)) return bad("group.group isn't a group");
  const members = g.members ?? [];
  if (!Array.isArray(members) || members.length > MAX_MEMBERS) return bad(`a group has at most ${MAX_MEMBERS} members`);
  for (const m of members)
    if (typeof m !== "string" || !m || m.length > MAX_MEMBER || !MEMBER.test(m)) return bad("a member is a model's name, \"provider/model\"");
  const match = Array.isArray(g.match) ? g.match.filter((p) => typeof p === "string" && p) : [];
  if (!members.length && !match.length) return bad("a shared group has a member, or a pattern it matches");
  const group = { ...g };
  for (const k of PRIVATE) delete group[k];
  const body = JSON.stringify({ magpie: MARK, version: s.version, group });
  return {
    title: title.s,
    description: description.s,
    author: author.s,
    public: b.public === true ? 1 : 0,
    body,
    models: members.join(" ").toLowerCase(),
  };
}

// counted is each group's users this week and its likes
const COUNTED = `(SELECT COUNT(*) FROM group_uses u WHERE u.gid = g.id AND u.day >= ?1) AS uses, (SELECT COUNT(*) FROM group_likes l WHERE l.gid = g.id) AS likes`;

function parsed(body) {
  try {
    return JSON.parse(body);
  } catch {
    return { group: {} };
  }
}

// listed is a page of the listed groups, the most used or the newest
// first, matching q if there is one
export async function listed(db, { sort = "top", q = "", page = 0 } = {}, now = Date.now()) {
  const args = [day(now - WEEK)];
  let where = "g.public = 1";
  if (q) {
    args.push("%" + q.toLowerCase().replace(/[\\%_]/g, (c) => "\\" + c) + "%");
    where += " AND (lower(g.title) LIKE ?2 ESCAPE '\\' OR lower(g.description) LIKE ?2 ESCAPE '\\' OR g.models LIKE ?2 ESCAPE '\\')";
  }
  const order = sort === "new" ? "g.created DESC" : "uses DESC, likes DESC, g.updated DESC";
  const { results } = await db
    .prepare(`SELECT g.id, g.title, g.description, g.author, g.version, g.updated, g.created, g.body, ${COUNTED} FROM groups g WHERE ${where} ORDER BY ${order} LIMIT ${PER_PAGE + 1} OFFSET ${page * PER_PAGE}`)
    .bind(...args)
    .all();
  const more = results.length > PER_PAGE;
  return {
    groups: results.slice(0, PER_PAGE).map((r) => {
      const g = parsed(r.body).group || {};
      return {
        id: r.id,
        url: groupURL(r.id),
        title: r.title,
        description: r.description,
        author: r.author,
        version: r.version,
        updated: r.updated,
        members: Array.isArray(g.members) ? g.members : [],
        routing: typeof g.routing === "string" ? g.routing : "",
        rules: Array.isArray(g.rules) ? g.rules.length : 0,
        uses: r.uses,
        likes: r.likes,
      };
    }),
    more,
  };
}

// one is a group with its users and likes, or null
async function one(db, id, now) {
  const r = await db.prepare(`SELECT g.*, ${COUNTED} FROM groups g WHERE g.id = ?2`).bind(day(now - WEEK), id).first();
  return r || null;
}

// the list's query, as read: sort, q and page, cleaned
function query(url) {
  const p = url.searchParams;
  const sort = p.get("sort") === "new" ? "new" : "top";
  const q = (p.get("q") || "").replace(/[\0-\x1f\x7f]/g, "").trim().slice(0, MAX_QUERY);
  const page = Math.min(Math.max(parseInt(p.get("page") || "0", 10) || 0, 0), 1000);
  return { sort, q, page };
}

const edge = () => globalThis.caches?.default;

async function bodyOf(req, most = 16 << 10) {
  if (+(req.headers.get("Content-Length") || 0) > most) return null;
  const text = await req.text();
  if (text.length > most) return null;
  try {
    const b = JSON.parse(text);
    return b && typeof b === "object" && !Array.isArray(b) ? b : null;
  } catch {
    return null;
  }
}

// api answers /api/groups and what is under it, or null for another path
export async function api(req, env, ctx, now = Date.now()) {
  const url = new URL(req.url);
  const path = url.pathname;
  if (path !== "/api/groups" && !path.startsWith("/api/groups/")) return null;
  const db = env.SHARES_DB;
  if (!db) return refuse(503, "sharing isn't set up here");
  await schema(db);
  if (path === "/api/groups") {
    if (req.method === "POST") return create(req, env, db, now);
    if (req.method !== "GET" && req.method !== "HEAD") return refuse(405, "POST shares a group, GET lists them");
    if (url.searchParams.has("ids")) {
      const ids = [...new Set((url.searchParams.get("ids") || "").split(",").map((s) => s.trim()).filter(Boolean))];
      if (ids.length > MOST || ids.some((id) => !ID.test(id))) return refuse(400, `ids is at most ${MOST} groups' ids, comma-separated`);
      if (!ids.length) return json({ groups: [] });
      const { results } = await db
        .prepare(`SELECT id, version, title, updated FROM groups WHERE id IN (${ids.map((_, i) => "?" + (i + 1)).join(", ")})`)
        .bind(...ids)
        .all();
      return json({ groups: results.map((r) => ({ id: r.id, version: r.version, title: r.title, updated: r.updated })) });
    }
    const qy = query(url);
    const key = new Request(`${SITE}/api/groups?sort=${qy.sort}&q=${encodeURIComponent(qy.q)}&page=${qy.page}`);
    const hit = await edge()?.match(key);
    if (hit) return hit;
    const res = json(await listed(db, qy, now), 200, "public, max-age=300");
    if (edge()) ctx.waitUntil(edge().put(key, res.clone()));
    return res;
  }
  if (path === "/api/groups/report") {
    if (req.method !== "POST") return refuse(405, "POST reports the groups an app has");
    return report(req, db, now);
  }
  if (path === "/api/groups/like") {
    if (req.method !== "POST") return refuse(405, "POST likes a group");
    return like(req, db, now);
  }
  const m = path.match(/^\/api\/groups\/([a-z2-7]{12})$/);
  if (!m) return refuse(404, "no such path");
  const id = m[1];
  if (req.method === "GET" || req.method === "HEAD") {
    const r = await one(db, id, now);
    if (!r) return refuse(404, "no such shared group: it was taken down");
    return json(
      {
        id,
        url: groupURL(id),
        title: r.title,
        description: r.description,
        author: r.author,
        public: !!r.public,
        version: r.version,
        created: r.created,
        updated: r.updated,
        group: parsed(r.body),
        uses: r.uses,
        likes: r.likes,
      },
      200,
      "public, max-age=60",
    );
  }
  if (req.method !== "PUT" && req.method !== "DELETE") return refuse(405, "PUT updates a shared group, DELETE takes it down");
  const row = await db.prepare("SELECT token, version FROM groups WHERE id = ?1").bind(id).first();
  if (!row) return refuse(404, "no such shared group: it was taken down");
  if (!(await authorized(req, env, row))) return refuse(403, "that isn't this group's token");
  if (req.method === "DELETE") {
    await db.batch([
      db.prepare("DELETE FROM groups WHERE id = ?1").bind(id),
      db.prepare("DELETE FROM group_uses WHERE gid = ?1").bind(id),
      db.prepare("DELETE FROM group_likes WHERE gid = ?1").bind(id),
    ]);
    return json({ deleted: true });
  }
  if (await heldShare(db, req, now)) return refuse(429, `at most ${RATE} shares or updates an hour; try again later`);
  const v = await read(req);
  if (v.error) return refuse(v.status, v.error);
  const n = row.version + 1;
  const done = await db
    .prepare("UPDATE groups SET version = ?1, title = ?2, description = ?3, author = ?4, public = ?5, body = ?6, models = ?7, updated = ?8 WHERE id = ?9 AND version = ?10")
    .bind(n, v.title, v.description, v.author, v.public, v.body, v.models, now, id, row.version)
    .run();
  if (!done.meta?.changes) return refuse(409, "the group changed while this update went up; try again");
  return json({ id, url: groupURL(id), version: n });
}

async function create(req, env, db, now) {
  if (await heldShare(db, req, now)) return refuse(429, `at most ${RATE} shares or updates an hour; try again later`);
  const v = await read(req);
  if (v.error) return refuse(v.status, v.error);
  const id = randomID(), token = randomToken();
  await db
    .prepare("INSERT INTO groups (id, token, title, description, author, public, body, models, version, created, updated) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, 1, ?9, ?9)")
    .bind(id, await sha256(token), v.title, v.description, v.author, v.public, v.body, v.models, now)
    .run();
  return json({ id, url: groupURL(id), token, version: 1 }, 201);
}

// report keeps the groups an app reported as used by it today, and drops
// the ones it no longer has
async function report(req, db, now) {
  const b = await bodyOf(req);
  if (!b || typeof b.id !== "string" || !DEVICE.test(b.id)) return refuse(400, "id is the app's install id, 32 hex digits");
  const list = b.groups ?? [];
  if (!Array.isArray(list) || list.length > MOST || list.some((g) => typeof g !== "string" || !ID.test(g)))
    return refuse(400, `groups is at most ${MOST} shared groups' ids`);
  if (await heldAsk(db, req, now)) return refuse(429, `at most ${ASK_RATE} asks an hour; try again later`);
  const ids = [...new Set(list)];
  let have = [];
  if (ids.length) {
    const { results } = await db
      .prepare(`SELECT id FROM groups WHERE id IN (${ids.map((_, i) => "?" + (i + 1)).join(", ")})`)
      .bind(...ids)
      .all();
    have = results.map((r) => r.id);
  }
  const today = day(now);
  const steps = [
    have.length
      ? db.prepare(`DELETE FROM group_uses WHERE device = ?1 AND gid NOT IN (${have.map((_, i) => "?" + (i + 2)).join(", ")})`).bind(b.id, ...have)
      : db.prepare("DELETE FROM group_uses WHERE device = ?1").bind(b.id),
    ...have.map((g) =>
      db.prepare("INSERT INTO group_uses (device, gid, day) VALUES (?1, ?2, ?3) ON CONFLICT (device, gid) DO UPDATE SET day = ?3").bind(b.id, g, today),
    ),
  ];
  await db.batch(steps);
  return json({ ok: true });
}

// like likes a group for an app, or takes its like back; a like counts
// the app as using the group too
async function like(req, db, now) {
  const b = await bodyOf(req);
  if (!b || typeof b.id !== "string" || !DEVICE.test(b.id)) return refuse(400, "id is the app's install id, 32 hex digits");
  if (typeof b.group !== "string" || !ID.test(b.group)) return refuse(400, "group is a shared group's id");
  if (await heldAsk(db, req, now)) return refuse(429, `at most ${ASK_RATE} asks an hour; try again later`);
  const there = await db.prepare("SELECT id FROM groups WHERE id = ?1").bind(b.group).first();
  if (!there) return refuse(404, "no such shared group: it was taken down");
  await db.batch(
    b.on === false
      ? [db.prepare("DELETE FROM group_likes WHERE device = ?1 AND gid = ?2").bind(b.id, b.group)]
      : [
          db.prepare("INSERT OR IGNORE INTO group_likes (device, gid) VALUES (?1, ?2)").bind(b.id, b.group),
          db.prepare("INSERT INTO group_uses (device, gid, day) VALUES (?1, ?2, ?3) ON CONFLICT (device, gid) DO UPDATE SET day = ?3").bind(b.id, b.group, day(now)),
        ],
  );
  const row = await db.prepare("SELECT COUNT(*) AS n FROM group_likes WHERE gid = ?1").bind(b.group).first();
  return json({ likes: row?.n ?? 0 });
}

// prune drops users not reported in a month and the addresses' counts
// from before the hour before
export async function prune(env, now = Date.now()) {
  const db = env.SHARES_DB;
  if (!db) return;
  await schema(db);
  await db.batch([
    db.prepare("DELETE FROM group_uses WHERE day < ?1").bind(day(now - KEEP)),
    db.prepare("DELETE FROM asks WHERE hour < ?1").bind(Math.floor(now / 36e5) - 1),
  ]);
}

// The pages: a group's (/g/<id>) and the gallery (/groups), in English,
// Chinese or Japanese, as ?lang= or the browser asks.

const ROUTING = {
  en: { "": "Smart", order: "In order", rotate: "Each in turn", usage: "Least used first", pace: "Paced", weight: "By weight", drain: "Use up, then next", "pace-drain": "Paced, then use up", manual: "Manual" },
  zh: { "": "智能", order: "按顺序", rotate: "轮流", usage: "最少使用优先", pace: "按节奏", weight: "按权重", drain: "用完再换", "pace-drain": "按节奏后用完", manual: "手动" },
  ja: { "": "スマート", order: "順番", rotate: "順番に交代", usage: "使用量の少ない順", pace: "ペース配分", weight: "重み付け", drain: "使い切ってから次へ", "pace-drain": "ペース配分後に使い切り", manual: "手動" },
};
const T = {
  en: {
    open: "Open in magpie", download: "Don’t have magpie? Download it", by: "by {}", models: "Models", matches: "Matches", routing: "Routing", rules: "Rules",
    when: "When", otherwise: "Always", tokens: "≥ {} tokens", images: "with images", effort: "reasoning ≥ {}", anyEffort: "any reasoning", compact: "compacting",
    uses: "{} using it", likes: "{} likes", text: "Copy as text", copy: "Copy", copied: "Copied", report: "Report",
    fine: "magpie shows what it adds, and which of these models you have, before it adds anything.",
    more: "More routing groups", gone: "This routing group isn’t shared any more.",
    gallery: "Routing groups", gallerySub: "Routing groups magpie users shared. Open one in magpie to add it.", search: "Search by name or model", go: "Search",
    top: "Popular", fresh: "New", none: "Nothing here yet.", next: "More", desc: "A routing group for magpie: {}",
  },
  zh: {
    open: "在 magpie 中打开", download: "还没有 magpie？下载", by: "作者 {}", models: "模型", matches: "匹配", routing: "路由", rules: "规则",
    when: "当", otherwise: "总是", tokens: "≥ {} tokens", images: "带图片", effort: "推理 ≥ {}", anyEffort: "任意推理", compact: "压缩对话时",
    uses: "{} 人在用", likes: "{} 赞", text: "复制为文字", copy: "复制", copied: "已复制", report: "举报",
    fine: "magpie 会先列出要添加的内容，以及其中哪些模型你已经有了，再添加。",
    more: "更多路由组", gone: "这个路由组已不再分享。",
    gallery: "路由组", gallerySub: "magpie 用户分享的路由组。在 magpie 中打开即可添加。", search: "按名称或模型搜索", go: "搜索",
    top: "最热", fresh: "最新", none: "这里还没有内容。", next: "更多", desc: "magpie 路由组：{}",
  },
  ja: {
    open: "magpie で開く", download: "magpie をお持ちでない方はダウンロード", by: "作成者 {}", models: "モデル", matches: "マッチ", routing: "ルーティング", rules: "ルール",
    when: "条件", otherwise: "常に", tokens: "≥ {} トークン", images: "画像あり", effort: "推論 ≥ {}", anyEffort: "推論あり", compact: "会話の圧縮時",
    uses: "{} 人が使用中", likes: "{} いいね", text: "テキストとしてコピー", copy: "コピー", copied: "コピーしました", report: "報告",
    fine: "magpie は追加する前に、内容とお持ちのモデルを表示します。",
    more: "他のルーティンググループ", gone: "このルーティンググループは共有されていません。",
    gallery: "ルーティンググループ", gallerySub: "magpie ユーザーが共有したルーティンググループ。magpie で開くと追加できます。", search: "名前またはモデルで検索", go: "検索",
    top: "人気", fresh: "新着", none: "まだありません。", next: "次へ", desc: "magpie のルーティンググループ：{}",
  },
};
const LANG_NAMES = { en: "en", zh: "zh-CN", ja: "ja" };

// langOf is the page's language: ?lang=, then the browser's first that
// the pages have, then English
export function langOf(req) {
  const url = new URL(req.url);
  const asked = (url.searchParams.get("lang") || "").toLowerCase();
  if (T[asked]) return { lang: asked, pinned: true };
  for (const part of (req.headers.get("Accept-Language") || "").split(",")) {
    const tag = part.split(";")[0].trim().toLowerCase().slice(0, 2);
    if (T[tag]) return { lang: tag, pinned: false };
  }
  return { lang: "en", pinned: false };
}

export const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const fill = (s, v) => s.replace("{}", v);

// chain is the members in order, with arrows when the group goes down them
// in order, or dots when it spreads over them
function chain(members, routing, most = Infinity) {
  const arrow = !routing || routing === "order" || routing === "drain";
  const shown = members.slice(0, most);
  const out = shown.map((m) => `<span class="m">${esc(m)}</span>`).join(arrow ? `<span class="arr">→</span>` : `<span class="arr">·</span>`);
  return out + (members.length > shown.length ? `<span class="arr">+${members.length - shown.length}</span>` : "");
}

// ruleLine is a rule as one line: its conditions, then where it sends
function ruleLine(r, t) {
  const when = [];
  if (typeof r.intent === "string" && r.intent) when.push(`“${esc(r.intent)}”`);
  if (Number.isFinite(r.tokens) && r.tokens > 0) when.push(esc(fill(t.tokens, r.tokens.toLocaleString("en"))));
  if (r.images === true) when.push(esc(t.images));
  if (typeof r.effort === "string" && r.effort) when.push(esc(r.effort === "on" ? t.anyEffort : fill(t.effort, r.effort)));
  if (Array.isArray(r.agents) && r.agents.length) when.push(esc(r.agents.filter((a) => typeof a === "string").join(", ")));
  if (r.compact === true) when.push(esc(t.compact));
  if (r.time && typeof r.time === "object" && typeof r.time.from === "string" && typeof r.time.to === "string")
    when.push(esc(`${r.time.from}–${r.time.to}` + (Array.isArray(r.time.days) && r.time.days.length ? ` ${r.time.days.join(" ")}` : "")));
  const to = [r.use, ...(Array.isArray(r.more) ? r.more : [])].filter((m) => typeof m === "string" && m);
  const use = to.map((m) => `<span class="m">${esc(m)}</span>`).join(`<span class="arr">→</span>`) + (typeof r.useEffort === "string" && r.useEffort ? ` <span class="eff">${esc(r.useEffort)}</span>` : "");
  return `<div class="rule"><span class="when">${when.length ? esc(t.when) + " " + when.join(" · ") : esc(t.otherwise)}</span><span class="arr">→</span>${use}</div>`;
}

// THEME is the site's theme pick, as its pages run it, before the page
// draws
const THEME = `(()=>{let m="system";try{m=localStorage.getItem("theme")||"system"}catch{}const q=matchMedia("(prefers-color-scheme: dark)");const a=()=>{const t=m==="system"?(q.matches?"dark":"light"):m;document.documentElement.dataset.theme=t;document.querySelector('meta[name="theme-color"]').content=t==="dark"?"#0b0b0c":"#fafafa"};q.addEventListener("change",()=>m==="system"&&a());a()})();`;

const STYLE = `
body { min-height: 100vh; min-height: 100dvh; display: flex; flex-direction: column; }
.stage { flex: 1; display: grid; place-items: center; padding: 48px 16px 64px; }
.card { width: 100%; max-width: 520px; background: var(--card); border: 1px solid var(--line); border-radius: 20px; box-shadow: var(--shadow); padding: 34px 30px 26px; }
.mark { display: flex; justify-content: center; margin-bottom: 20px; }
.mark .tile { width: 52px; height: 52px; border-radius: 14px; border: 1px solid var(--line); display: grid; place-items: center; background: var(--bg); }
.mark .tile img { width: 30px; height: 30px; }
h1 { font-size: 23px; letter-spacing: -.02em; line-height: 1.25; margin: 0 0 6px; font-weight: 660; overflow-wrap: anywhere; text-align: center; }
.sub { color: var(--ink3); font-size: 14px; margin: 0 0 8px; text-align: center; overflow-wrap: anywhere; }
.desc { color: var(--ink2); font-size: 15px; margin: 8px 0 0; text-align: center; overflow-wrap: anywhere; }
dl { margin: 22px 0 24px; border-top: 1px solid var(--line); }
dl > div { display: grid; grid-template-columns: 84px minmax(0, 1fr); gap: 12px; padding: 10px 0; border-bottom: 1px solid var(--line); font-size: 14px; }
dt { color: var(--ink3); }
dd { margin: 0; color: var(--ink); overflow-wrap: anywhere; }
.m { display: inline-block; font-size: 12.5px; border: 1px solid var(--line); border-radius: 999px; padding: 0 8px; margin: 2px 0; color: var(--ink2); overflow-wrap: anywhere; }
.arr { color: var(--ink3); margin: 0 5px; font-size: 12.5px; }
.rule { padding: 2px 0; }
.rule .when { color: var(--ink2); }
.eff { color: var(--ink3); font-size: 12.5px; }
.actions { display: grid; gap: 10px; }
.actions .btn { width: 100%; height: 44px; }
.counts { color: var(--ink3); font-size: 13px; text-align: center; margin: 14px 0 0; }
details { margin: 18px 0 0; border-top: 1px solid var(--line); padding-top: 12px; }
summary { cursor: pointer; color: var(--ink2); font-size: 14px; }
pre { max-height: 260px; overflow: auto; font-size: 12px; line-height: 1.5; background: var(--bg); border: 1px solid var(--line); border-radius: 10px; padding: 10px 12px; margin: 10px 0; white-space: pre-wrap; overflow-wrap: anywhere; }
details .btn { height: 34px; font-size: 14px; }
.fine { color: var(--ink3); font-size: 12.5px; margin: 18px 0 0; line-height: 1.55; text-align: center; }
.fine a, .foot a { color: var(--ink2); text-decoration: none; }
.fine a:hover, .foot a:hover { color: var(--ink); }
.foot { text-align: center; font-size: 13px; color: var(--ink3); padding: 0 16px 28px; }
.wide { max-width: 760px; }
.find { display: flex; gap: 8px; margin: 20px 0 14px; }
.find input { flex: 1; min-width: 0; height: 38px; border: 1px solid var(--line); border-radius: 10px; background: var(--bg); color: var(--ink); font: inherit; font-size: 14px; padding: 0 12px; }
.find .btn { height: 38px; font-size: 14px; }
.tabs { display: flex; gap: 16px; font-size: 14px; margin: 0 0 6px; }
.tabs a { color: var(--ink3); text-decoration: none; padding: 4px 0; }
.tabs a[aria-current] { color: var(--ink); font-weight: 600; }
.list { border-top: 1px solid var(--line); }
.g { display: block; padding: 14px 4px; border-bottom: 1px solid var(--line); text-decoration: none; color: var(--ink); transition: background-color .15s ease; }
@media (hover: hover) and (pointer: fine) { .g:hover { background: var(--bg); } }
.g b { font-weight: 600; font-size: 15px; overflow-wrap: anywhere; }
.g .by { color: var(--ink3); font-size: 13px; margin-left: 6px; }
.g .c { margin-top: 6px; }
.g .n { color: var(--ink3); font-size: 12.5px; margin-top: 6px; }
.empty { color: var(--ink3); text-align: center; padding: 28px 0; }
.next { display: block; text-align: center; margin: 16px 0 0; color: var(--ink2); text-decoration: none; font-size: 14px; }
@media (max-width: 480px) { .card { padding: 28px 20px 22px; border-radius: 16px; } dl > div { grid-template-columns: 1fr; gap: 4px; } }
`;

function headers(nonce, cache) {
  return {
    "Content-Type": "text/html; charset=utf-8",
    // no-transform: Cloudflare's injected beacon would be a script the
    // CSP below shuts out
    "Cache-Control": cache,
    Vary: "Accept-Language",
    "Content-Security-Policy": `default-src 'none'; style-src 'self' 'unsafe-inline'; script-src 'nonce-${nonce}'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`,
    "X-Content-Type-Options": "nosniff",
    "Referrer-Policy": "strict-origin-when-cross-origin",
  };
}

function shell({ lang, title, description, nonce, robots, og, body, script = "" }) {
  return `<!doctype html>
<html lang="${LANG_NAMES[lang]}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${title}</title>
<meta name="description" content="${description}">
<meta name="color-scheme" content="light dark">
<meta name="theme-color" content="#fafafa">
${robots ? '<meta name="robots" content="noindex">\n' : ""}<script nonce="${nonce}">${THEME}</script>
<link rel="icon" href="/favicon.png" type="image/png">
<link rel="stylesheet" href="/docs.css">
<meta property="og:title" content="${og.title}">
<meta property="og:description" content="${description}">
${og.url ? `<meta property="og:url" content="${og.url}">\n` : ""}<meta property="og:image" content="https://usemagpie.ai/img/og.png">
<meta name="twitter:card" content="summary_large_image">
<style>${STYLE}</style>
</head>
<body>
${body}
${script ? `<script nonce="${nonce}">${script}</script>\n` : ""}</body>
</html>
`;
}

function gone(lang) {
  const t = T[lang];
  return new Response(
    `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>${esc(t.gone)}</title>` +
      `<style>:root{color-scheme:light dark}body{margin:0;min-height:100vh;display:grid;place-items:center;font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",system-ui,sans-serif;background:Canvas;color:CanvasText}p{opacity:.7}a{color:inherit}</style>` +
      `<p>${esc(t.gone)} <a href="/groups">${esc(t.more)}</a></p>`,
    { status: 404, headers: { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" } },
  );
}

const COPY = `(()=>{const b=document.getElementById("copy"),p=document.getElementById("text");if(!b||!p)return;b.addEventListener("click",()=>{navigator.clipboard.writeText(p.textContent).then(()=>{const was=b.textContent;b.textContent=b.dataset.done;setTimeout(()=>{b.textContent=was},1600)})})})();`;

// page answers usemagpie.ai/g/<id> and /groups, or null for another path
export async function page(req, env, now = Date.now()) {
  const url = new URL(req.url);
  if (req.method !== "GET" && req.method !== "HEAD") return null;
  const m = url.pathname.match(/^\/g\/([a-z2-7]{12})\/?$/);
  const gallery = url.pathname === "/groups" || url.pathname === "/groups/";
  if (!m && !gallery) return null;
  const { lang, pinned } = langOf(req);
  const t = T[lang];
  const keep = pinned ? `lang=${lang}` : "";
  const db = env.SHARES_DB;
  if (m && !db) return gone(lang);
  if (db) await schema(db);
  const nonce = randomNonce();
  if (gallery) {
    const qy = query(url);
    const got = db ? await listed(db, qy, now) : { groups: [], more: false };
    const link = (o) => {
      const p = new URLSearchParams();
      const v = { ...qy, ...o };
      if (v.sort === "new") p.set("sort", "new");
      if (v.q) p.set("q", v.q);
      if (v.page) p.set("page", String(v.page));
      if (pinned) p.set("lang", lang);
      const s = p.toString();
      return "/groups" + (s ? "?" + s : "");
    };
    const cards = got.groups
      .map((g) => {
        const counts = [fill(t.uses, g.uses), fill(t.likes, g.likes)].map(esc).join(" · ");
        return `<a class="g" href="/g/${g.id}${keep ? "?" + keep : ""}"><b>${esc(g.title)}</b>${g.author ? `<span class="by">${esc(fill(t.by, g.author))}</span>` : ""}<div class="c">${chain(g.members, g.routing, 5)}</div><div class="n">${counts}</div></a>`;
      })
      .join("");
    const body = `<div class="stage"><div class="card wide">
<div class="mark"><div class="tile"><img class="bird" src="/img/magpie-small.svg" alt="magpie"></div></div>
<h1>${esc(t.gallery)}</h1>
<p class="sub">${esc(t.gallerySub)}</p>
<form class="find" method="get" action="/groups" role="search">${qy.sort === "new" ? '<input type="hidden" name="sort" value="new">' : ""}${pinned ? `<input type="hidden" name="lang" value="${lang}">` : ""}<input type="search" name="q" value="${esc(qy.q)}" placeholder="${esc(t.search)}" aria-label="${esc(t.search)}" maxlength="${MAX_QUERY}"><button class="btn ghost" type="submit">${esc(t.go)}</button></form>
<div class="tabs"><a href="${esc(link({ sort: "top", page: 0 }))}"${qy.sort === "top" ? ' aria-current="page"' : ""}>${esc(t.top)}</a><a href="${esc(link({ sort: "new", page: 0 }))}"${qy.sort === "new" ? ' aria-current="page"' : ""}>${esc(t.fresh)}</a></div>
<div class="list">${cards || `<p class="empty">${esc(t.none)}</p>`}</div>
${got.more ? `<a class="next" href="${esc(link({ page: qy.page + 1 }))}">${esc(t.next)}</a>` : ""}
</div></div>
<div class="foot"><a href="/">usemagpie.ai</a></div>`;
    const html = shell({ lang, title: esc(t.gallery) + " · magpie", description: esc(t.gallerySub), nonce, robots: !!(qy.q || qy.page), og: { title: esc(t.gallery), url: `${SITE}/groups` }, body });
    return new Response(req.method === "HEAD" ? null : html, { headers: headers(nonce, "public, max-age=60, no-transform") });
  }
  const id = m[1];
  const r = await one(db, id, now);
  if (!r) return gone(lang);
  const shared = parsed(r.body);
  const g = shared.group || {};
  const members = Array.isArray(g.members) ? g.members.filter((x) => typeof x === "string") : [];
  const match = Array.isArray(g.match) ? g.match.filter((x) => typeof x === "string") : [];
  const routing = typeof g.routing === "string" ? g.routing : "";
  const rules = Array.isArray(g.rules) ? g.rules.filter((x) => x && typeof x === "object") : [];
  const shortChain = members.slice(0, 6).join(" → ") + (members.length > 6 ? " …" : "");
  const description = esc(fill(t.desc, shortChain || match.join(", ")));
  const report = `mailto:${REPORT_TO}?subject=${encodeURIComponent("Report a shared routing group: " + id)}&body=${encodeURIComponent(groupURL(id) + "\n\nWhat's wrong with it:\n")}`;
  const rows = [];
  if (members.length) rows.push(`<div><dt>${esc(t.models)}</dt><dd>${chain(members, routing)}</dd></div>`);
  if (match.length) rows.push(`<div><dt>${esc(t.matches)}</dt><dd>${match.map((p) => `<span class="m">${esc(p)}</span>`).join(" ")}</dd></div>`);
  rows.push(`<div><dt>${esc(t.routing)}</dt><dd>${esc(ROUTING[lang][routing] || routing)}</dd></div>`);
  if (rules.length) rows.push(`<div><dt>${esc(t.rules)}</dt><dd>${rules.map((x) => ruleLine(x, t)).join("")}</dd></div>`);
  const counts = [fill(t.uses, r.uses), fill(t.likes, r.likes)].map(esc).join(" · ");
  const body = `<div class="stage"><div class="card">
<div class="mark"><div class="tile"><img class="bird" src="/img/magpie-small.svg" alt="magpie"></div></div>
<h1>${esc(r.title)}</h1>
${r.author ? `<p class="sub">${esc(fill(t.by, r.author))}</p>\n` : ""}${r.description ? `<p class="desc">${esc(r.description)}</p>\n` : ""}<dl>${rows.join("")}</dl>
<div class="actions"><a class="btn" href="magpie://group?id=${id}"><img class="bird-inv" src="/img/magpie-small.svg" alt="">${esc(t.open)}</a><a class="btn ghost" href="/#get">${esc(t.download)}</a></div>
<p class="counts">${counts}</p>
<details><summary>${esc(t.text)}</summary><pre id="text">${esc(JSON.stringify(shared, null, 2))}</pre><button class="btn ghost" id="copy" type="button" data-done="${esc(t.copied)}">${esc(t.copy)}</button></details>
<p class="fine">${esc(t.fine)} <a href="${esc(report)}">${esc(t.report)}</a></p>
</div></div>
<div class="foot"><a href="/groups${keep ? "?" + keep : ""}">${esc(t.more)}</a> · <a href="/">usemagpie.ai</a></div>`;
  const html = shell({ lang, title: esc(r.title) + " · magpie", description, nonce, robots: !r.public, og: { title: esc(r.title), url: groupURL(id) }, body, script: COPY });
  return new Response(req.method === "HEAD" ? null : html, { headers: headers(nonce, "public, max-age=60, no-transform") });
}
