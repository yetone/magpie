// Discover's counts (the app's internal/stats/plugins.go): how many
// computers use each plugin Discover lists, and how many like it.
//
// Each app that lets its usage stats go reports once a day which of
// Discover's plugins are installed, under its random install id, never an
// account or address. A plugin's users are the ids that reported it in
// the last seven days. A like is one per id, can be taken back, and comes
// only from an app that has the plugin (the app checks; here a like also
// counts as that id having it). The caller's address is kept only hashed,
// for an hour, to hold each one to RATE asks an hour.
//
// D1 (wrangler.jsonc's PLUGINS) holds three tables; the quarter-hour
// cron prunes what is old.

const ID = /^[0-9a-f]{32}$/;
const NPM = /^(@[a-z0-9][a-z0-9._~-]*\/)?[a-z0-9][a-z0-9._~-]*$/;
const GIT = /^github:[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;
export const MOST = 50; // plugins in one report, as the app's most
export const RATE = 60; // asks an address makes in an hour
const WEEK = 7 * 864e5;
const KEEP = 30 * 864e5;

const SCHEMA = [
  "CREATE TABLE IF NOT EXISTS installs (device TEXT NOT NULL, plugin TEXT NOT NULL, day TEXT NOT NULL, PRIMARY KEY (device, plugin))",
  "CREATE INDEX IF NOT EXISTS installs_day ON installs (day)",
  "CREATE TABLE IF NOT EXISTS likes (device TEXT NOT NULL, plugin TEXT NOT NULL, PRIMARY KEY (device, plugin))",
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

export function validName(n) {
  return typeof n === "string" && n.length <= 214 && (NPM.test(n) || GIT.test(n));
}

const day = (t) => new Date(t).toISOString().slice(0, 10);

function answer(body, status = 200, headers = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store", ...headers },
  });
}

async function who(req) {
  const ip = req.headers.get("CF-Connecting-IP") || "";
  const h = await crypto.subtle.digest("SHA-256", new TextEncoder().encode("magpie-plugins:" + ip));
  return [...new Uint8Array(h).slice(0, 12)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// held reports whether the caller has made RATE asks this hour already
async function held(db, req, now) {
  const row = await db
    .prepare("INSERT INTO asks (who, hour, n) VALUES (?1, ?2, 1) ON CONFLICT (who, hour) DO UPDATE SET n = n + 1 RETURNING n")
    .bind(await who(req), Math.floor(now / 36e5))
    .first();
  return (row?.n ?? 0) > RATE;
}

async function body(req) {
  if (req.method !== "POST") return null;
  try {
    const text = await req.text();
    if (text.length > 64 << 10) return null;
    return JSON.parse(text);
  } catch {
    return null;
  }
}

// handle answers /api/plugins/*, or null for any other path.
export async function handle(req, env, ctx, now = Date.now()) {
  const path = new URL(req.url).pathname;
  if (!["/api/plugins/report", "/api/plugins/like", "/api/plugins/stats"].includes(path)) return null;
  const db = env.PLUGINS;
  if (!db) return answer({ error: "counts aren't kept here" }, 503);
  await schema(db);
  if (path === "/api/plugins/stats") {
    if (req.method !== "GET" && req.method !== "HEAD") return answer({ error: "GET" }, 405);
    return stats(req, db, ctx, now);
  }
  const b = await body(req);
  if (!b || !ID.test(b.id ?? "")) return answer({ error: "a report needs the app's id" }, 400);
  if (await held(db, req, now)) return answer({ error: "too many asks; try again in an hour" }, 429);
  if (path === "/api/plugins/report") {
    const names = Array.isArray(b.plugins) ? [...new Set(b.plugins)] : null;
    if (!names || names.length > MOST || !names.every(validName)) return answer({ error: "plugins: names Discover lists" }, 400);
    const today = day(now);
    const marks = names.map((_, i) => `?${i + 2}`).join(",");
    const gone = db.prepare(`DELETE FROM installs WHERE device = ?1${names.length ? ` AND plugin NOT IN (${marks})` : ""}`).bind(b.id, ...names);
    const put = names.map((n) =>
      db
        .prepare("INSERT INTO installs (device, plugin, day) VALUES (?1, ?2, ?3) ON CONFLICT (device, plugin) DO UPDATE SET day = ?3")
        .bind(b.id, n, today),
    );
    await db.batch([gone, ...put]);
    return answer({ ok: true });
  }
  // a like, or one taken back
  if (!validName(b.plugin) || typeof b.on !== "boolean") return answer({ error: "which plugin, and on or off?" }, 400);
  const steps = b.on
    ? [
        db.prepare("INSERT INTO likes (device, plugin) VALUES (?1, ?2) ON CONFLICT DO NOTHING").bind(b.id, b.plugin),
        db
          .prepare("INSERT INTO installs (device, plugin, day) VALUES (?1, ?2, ?3) ON CONFLICT (device, plugin) DO UPDATE SET day = ?3")
          .bind(b.id, b.plugin, day(now)),
      ]
    : [db.prepare("DELETE FROM likes WHERE device = ?1 AND plugin = ?2").bind(b.id, b.plugin)];
  await db.batch(steps);
  const row = await db.prepare("SELECT COUNT(*) AS n FROM likes WHERE plugin = ?1").bind(b.plugin).first();
  return answer({ likes: row?.n ?? 0 });
}

// stats is every plugin's users and likes, held at the edge for an hour
async function stats(req, db, ctx, now) {
  const key = new Request(new URL("/api/plugins/stats", req.url).toString());
  const hit = await caches.default.match(key);
  if (hit) return hit;
  const [users, likes] = await db.batch([
    db.prepare("SELECT plugin, COUNT(*) AS n FROM installs WHERE day >= ?1 GROUP BY plugin").bind(day(now - WEEK)),
    db.prepare("SELECT plugin, COUNT(*) AS n FROM likes GROUP BY plugin"),
  ]);
  const plugins = {};
  for (const r of users.results) (plugins[r.plugin] ??= {}).users = r.n;
  for (const r of likes.results) (plugins[r.plugin] ??= {}).likes = r.n;
  const res = answer({ plugins }, 200, { "Cache-Control": "public, max-age=3600" });
  ctx.waitUntil(caches.default.put(key, res.clone()));
  return res;
}

// prune drops installs not reported in a month and addresses' old counts
export async function prune(env, now = Date.now()) {
  const db = env.PLUGINS;
  if (!db) return;
  await schema(db);
  await db.batch([
    db.prepare("DELETE FROM installs WHERE day < ?1").bind(day(now - KEEP)),
    db.prepare("DELETE FROM asks WHERE hour < ?1").bind(Math.floor(now / 36e5) - 1),
  ]);
}
