// node --test site/: shared artifacts (shares.js), on a D1 stood in for by
// node:sqlite and R2 by r2stub.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";
import { api, page, content, prune, cleanPath, RATE, MAX_BYTES, CONTENT } from "./shares.js";
import worker from "./worker.js";
import { bucket } from "./r2stub.mjs";
import { d1 } from "./d1stub.mjs";

function setup() {
  const env = { SHARES_DB: d1(), SHARES: bucket(), SHARE_ADMIN: "admin-secret-token" };
  const waits = [];
  const ctx = { waitUntil: (p) => waits.push(p) };
  const settle = () => Promise.all(waits.splice(0));
  return { env, ctx, settle };
}

function form(meta, files) {
  const f = new FormData();
  f.set("meta", JSON.stringify({ ...meta, files: files.map(([p]) => p) }));
  files.forEach(([, body], i) => f.set(String(i), new Blob([body])));
  return f;
}

function req(method, path, { body, token, ip = "198.51.100.7", host = "usemagpie.ai", dest } = {}) {
  const headers = { "CF-Connecting-IP": ip };
  if (token) headers.Authorization = "Bearer " + token;
  if (dest) headers["Sec-Fetch-Dest"] = dest;
  return new Request(`https://${host}${path}`, { method, body, headers });
}

const PAGE = `<!doctype html><title>Q3</title><link rel="stylesheet" href="app.css"><p>hello</p>`;

async function share(s, files = [["index.html", PAGE], ["app.css", "p{color:red}"]], meta = { title: "Q3 report", description: "Numbers" }) {
  const r = await api(req("POST", "/api/shares", { body: form(meta, files) }), s.env, s.ctx);
  assert.equal(r.status, 201, await r.clone().text());
  return r.json();
}

test("shares: a shared page shows in the frame of its link, its files from the sandboxed host", async () => {
  const s = setup();
  const got = await share(s);
  assert.match(got.id, /^[a-z2-7]{16}$/);
  assert.equal(got.url, `https://usemagpie.ai/a/${got.id}`);
  assert.equal(got.version, 1);
  assert.ok(got.token.length >= 40);
  // the token is kept only hashed
  const row = s.env.SHARES_DB.raw.prepare("SELECT token FROM shares").get();
  assert.notEqual(row.token, got.token);
  assert.equal(JSON.stringify(s.env.SHARES_DB.raw.prepare("SELECT * FROM shares").all()).includes(got.token), false);

  const w = await page(req("GET", `/a/${got.id}`), s.env);
  assert.equal(w.status, 200);
  const html = await w.text();
  assert.match(html, /<title>Q3 report<\/title>/);
  assert.ok(html.includes(`src="https://${CONTENT}/${got.id}/1/"`), html);
  assert.match(html, /sandbox="allow-scripts allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox allow-downloads"/);
  assert.doesNotMatch(html, /allow-same-origin/);
  assert.match(html, />Report</);
  assert.match(w.headers.get("Content-Security-Policy"), /frame-src https:\/\/a\.usemagpie\.ai/);
  // Cloudflare's beacon is injected into HTML a worker answers unless it says no-transform
  assert.match(w.headers.get("Cache-Control"), /no-transform/);

  const p = await content(req("GET", `/${got.id}/1/`, { host: CONTENT, dest: "iframe" }), s.env);
  assert.equal(p.status, 200);
  assert.equal(await p.text(), PAGE);
  assert.equal(p.headers.get("Content-Type"), "text/html; charset=utf-8");
  const csp = p.headers.get("Content-Security-Policy");
  assert.match(csp, /^sandbox allow-scripts/);
  assert.doesNotMatch(csp, /allow-same-origin/);
  assert.equal(p.headers.get("X-Content-Type-Options"), "nosniff");
  assert.match(p.headers.get("Cache-Control"), /no-transform/);
  const css = await content(req("GET", `/${got.id}/1/app.css`, { host: CONTENT, dest: "style" }), s.env);
  assert.equal(await css.text(), "p{color:red}");
  assert.equal(css.headers.get("Content-Type"), "text/css; charset=utf-8");
  // a file the page names that isn't there
  assert.equal((await content(req("GET", `/${got.id}/1/nope.js`, { host: CONTENT, dest: "script" }), s.env)).status, 404);
});

test("shares: the content host opened at the top goes to the link, with the bar", async () => {
  const s = setup();
  const got = await share(s, [["index.html", PAGE], ["docs/more.html", "<p>more"]]);
  const top = await content(req("GET", `/${got.id}/1/`, { host: CONTENT, dest: "document" }), s.env);
  assert.equal(top.status, 302);
  assert.equal(top.headers.get("Location"), got.url);
  const sub = await content(req("GET", `/${got.id}/1/docs/more.html?x=1`, { host: CONTENT, dest: "document" }), s.env);
  assert.equal(sub.headers.get("Location"), `${got.url}/docs/more.html?x=1`);
  const bare = await content(req("GET", `/${got.id}`, { host: CONTENT }), s.env);
  assert.equal(bare.headers.get("Location"), got.url);
  // and the link's page frames that file
  const html = await (await page(req("GET", `/a/${got.id}/docs/more.html?x=1`), s.env)).text();
  assert.ok(html.includes(`src="https://${CONTENT}/${got.id}/1/docs/more.html?x=1"`), html);
});

test("shares: an update shows at the same link, and the version before is served no more", async () => {
  const s = setup();
  const got = await share(s);
  const up = await api(req("PUT", `/api/shares/${got.id}`, { token: got.token, body: form({ title: "Q3 report, v2" }, [["index.html", "<p>two"]]) }), s.env, s.ctx);
  assert.equal(up.status, 200, await up.clone().text());
  assert.deepEqual(await up.json(), { id: got.id, url: got.url, version: 2 });
  await s.settle();
  assert.ok(!(await page(req("GET", `/a/${got.id}`), s.env).then((r) => r.text())).includes("/1/"));
  assert.match(await page(req("GET", `/a/${got.id}`), s.env).then((r) => r.text()), /Q3 report, v2/);
  assert.equal(await content(req("GET", `/${got.id}/2/`, { host: CONTENT, dest: "iframe" }), s.env).then((r) => r.text()), "<p>two");
  assert.equal((await content(req("GET", `/${got.id}/1/`, { host: CONTENT, dest: "iframe" }), s.env)).status, 404);
  assert.deepEqual([...s.env.SHARES.objs.keys()], [`${got.id}/2/index.html`]);
});

test("shares: only the page's token (or the site's admin token) updates or takes it down", async () => {
  const s = setup();
  const a = await share(s), b = await share(s);
  const body = () => form({ title: "x" }, [["index.html", "<p>x"]]);
  for (const token of [undefined, "", "wrong", b.token]) {
    assert.equal((await api(req("PUT", `/api/shares/${a.id}`, { token, body: body() }), s.env, s.ctx)).status, 403, `PUT with ${token}`);
    assert.equal((await api(req("DELETE", `/api/shares/${a.id}`, { token }), s.env, s.ctx)).status, 403, `DELETE with ${token}`);
  }
  assert.equal((await page(req("GET", `/a/${a.id}`), s.env)).status, 200);
  const del = await api(req("DELETE", `/api/shares/${a.id}`, { token: a.token }), s.env, s.ctx);
  assert.deepEqual(await del.json(), { deleted: true });
  await s.settle();
  const gone = await page(req("GET", `/a/${a.id}`), s.env);
  assert.equal(gone.status, 404);
  assert.match(await gone.text(), /isn't shared any more/);
  assert.equal((await content(req("GET", `/${a.id}/1/`, { host: CONTENT, dest: "iframe" }), s.env)).status, 404);
  assert.ok([...s.env.SHARES.objs.keys()].every((k) => k.startsWith(b.id + "/")));
  // a page reported is taken down with the admin token
  assert.equal((await api(req("DELETE", `/api/shares/${b.id}`, { token: "admin-secret-token" }), s.env, s.ctx)).status, 200);
  await s.settle();
  assert.equal(s.env.SHARES.objs.size, 0);
  // one taken down is gone for its token too
  assert.equal((await api(req("DELETE", `/api/shares/${a.id}`, { token: a.token }), s.env, s.ctx)).status, 404);
});

test("shares: a taken-down page's files left in R2 are served no more", async () => {
  const s = setup();
  const got = await share(s);
  s.env.SHARES_DB.raw.prepare("DELETE FROM shares").run();
  assert.equal((await content(req("GET", `/${got.id}/1/`, { host: CONTENT, dest: "iframe" }), s.env)).status, 404);
});

test("shares: what isn't a page and its plain relative files is refused, and nothing kept", async () => {
  const s = setup();
  const cases = [
    [{ title: "x" }, [["app.css", "a"]], 400], // no page
    [{ title: "" }, [["index.html", "a"]], 400],
    [{ title: "x" }, [["index.html", "a"], ["../up.js", "b"]], 400],
    [{ title: "x" }, [["index.html", "a"], ["/abs.js", "b"]], 400],
    [{ title: "x" }, [["index.html", "a"], ["a//b.js", "b"]], 400],
    [{ title: "x" }, [["index.html", "a"], ["a\\b.js", "b"]], 400],
    [{ title: "x" }, [["index.html", "a"], ["index.html", "b"]], 400],
    [{ title: "x" }, [["index.html", "a"], ["big.bin", new Uint8Array(MAX_BYTES)]], 413],
  ];
  for (const [meta, files, status] of cases) {
    const r = await api(req("POST", "/api/shares", { body: form(meta, files), ip: "203.0.113." + status }), s.env, s.ctx);
    assert.equal(r.status, status, JSON.stringify([meta, files.map((f) => f[0])]));
    assert.ok((await r.json()).error);
  }
  const missing = new FormData();
  missing.set("meta", JSON.stringify({ title: "x", files: ["index.html"] }));
  assert.equal((await api(req("POST", "/api/shares", { body: missing }), s.env, s.ctx)).status, 400);
  assert.equal((await api(req("POST", "/api/shares", { body: "not a form" }), s.env, s.ctx)).status, 400);
  assert.equal((await api(req("GET", "/api/shares"), s.env, s.ctx)).status, 405);
  assert.equal(s.env.SHARES.objs.size, 0);
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT count(*) n FROM shares").get().n, 0);
  for (const p of ["a/b.js", "img/x y.png", "日本.css"]) assert.equal(cleanPath(p), p);
  for (const p of ["", ".", "a/./b", "a/../b", "/a", "a/", "a\0b"]) assert.equal(cleanPath(p), "", p);
});

test("shares: an address makes RATE shares or updates an hour; another address isn't held", async () => {
  const s = setup();
  const t0 = Date.UTC(2026, 10, 24, 10, 5);
  const one = () => form({ title: "x" }, [["index.html", "a"]]);
  let first;
  for (let i = 0; i < RATE; i++) {
    const r = await api(req("POST", "/api/shares", { body: one() }), s.env, s.ctx, t0);
    assert.equal(r.status, 201, `share ${i + 1}`);
    first ??= await r.json();
  }
  assert.equal((await api(req("POST", "/api/shares", { body: one() }), s.env, s.ctx, t0)).status, 429);
  assert.equal((await api(req("PUT", `/api/shares/${first.id}`, { token: first.token, body: one() }), s.env, s.ctx, t0)).status, 429);
  // taking one down is never held
  assert.equal((await api(req("DELETE", `/api/shares/${first.id}`, { token: first.token }), s.env, s.ctx, t0)).status, 200);
  assert.equal((await api(req("POST", "/api/shares", { body: one(), ip: "192.0.2.1" }), s.env, s.ctx, t0)).status, 201);
  assert.equal((await api(req("POST", "/api/shares", { body: one() }), s.env, s.ctx, t0 + 36e5)).status, 201);
  // the counts are kept hashed, and pruned after the hour after
  const who = s.env.SHARES_DB.raw.prepare("SELECT who FROM asks").all().map((r) => r.who);
  assert.ok(who.every((w) => /^[0-9a-f]{24}$/.test(w)));
  await prune(s.env, t0 + 2 * 36e5);
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT count(*) n FROM asks").get().n, 1);
});

test("shares: a title is text in the link's page, never markup", async () => {
  const s = setup();
  const got = await share(s, [["index.html", "a"]], { title: `</title><script>alert(1)</script>"'`, description: `"><img src=x onerror=alert(1)>` });
  const html = await (await page(req("GET", `/a/${got.id}`), s.env)).text();
  assert.doesNotMatch(html, /<script>|<img/);
  assert.match(html, /&lt;\/title&gt;&lt;script&gt;/);
});

test("shares: the worker sends the content host, /a/ and /api/shares to shares.js", async () => {
  const s = setup();
  const r = await worker.fetch(req("POST", "/api/shares", { body: form({ title: "Via worker" }, [["index.html", "<p>w"]]) }), s.env, s.ctx);
  assert.equal(r.status, 201);
  const got = await r.json();
  assert.match(await (await worker.fetch(req("GET", `/a/${got.id}`), s.env, s.ctx)).text(), /Via worker/);
  const file = await worker.fetch(req("GET", `/${got.id}/1/`, { host: CONTENT, dest: "iframe" }), s.env, s.ctx);
  assert.equal(await file.text(), "<p>w");
  // the content host serves nothing of the site's
  assert.equal((await worker.fetch(req("GET", "/", { host: CONTENT }), s.env, s.ctx)).status, 404);
  assert.equal((await worker.fetch(req("GET", "/api/latest", { host: CONTENT }), s.env, s.ctx)).status, 404);
});
