// node --test site/: shared artifacts (shares.js), on a D1 stood in for by
// node:sqlite and R2 by r2stub.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync } from "node:fs";
import { api, page, content, prune, cleanPath, RATE, MAX_BYTES, CONTENT, MAX_BLOBS, BLOB_RATE } from "./shares.js";
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
  // the bar shows magpie's bird beside its name, from the site, which the CSP lets in
  const bird = html.match(/<a class="brand" href="https:\/\/usemagpie\.ai\/"[^>]*><img src="(https:\/\/usemagpie\.ai)(\/img\/magpie-small\.svg)" alt="">magpie<\/a>/);
  assert.ok(bird, html);
  assert.ok(existsSync(new URL("./public" + bird[2], import.meta.url)));
  assert.match(w.headers.get("Content-Security-Policy"), new RegExp(`img-src ${bird[1]};`));
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
  // the bar's own bird is the one <img>
  assert.doesNotMatch(html, /<script>|<img src=x/);
  assert.equal(html.match(/<img/g).length, 1);
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

// #7649: the page's assets go with its share, and the content host
// serves each at /_blob/<id>, the address the page wrote, by its id alone
const AID = "a".repeat(31) + "b", AID2 = "c".repeat(32);
function blobReq(method, id, aid, { token, body, type, ip } = {}) {
  const r = req(method, `/api/shares/${id}/blobs${aid === undefined ? "" : "/" + aid}`, { token, body, ip });
  if (type) r.headers.set("Content-Type", type);
  return r;
}

test("shares: a shared page's assets go up with its token, and show at /_blob/<id> on the content host", async () => {
  const s = setup();
  const got = await share(s);
  const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 1, 2, 3]);
  // the token, or nothing
  assert.equal((await api(blobReq("PUT", got.id, AID, { body: png, type: "image/png" }), s.env, s.ctx)).status, 403);
  assert.equal((await api(blobReq("PUT", got.id, AID, { token: "nope", body: png, type: "image/png" }), s.env, s.ctx)).status, 403);
  assert.equal((await api(blobReq("GET", got.id, undefined), s.env, s.ctx)).status, 403);
  // a type an asset may be, a valid id, a body
  assert.equal((await api(blobReq("PUT", got.id, AID, { token: got.token, body: png, type: "text/html" }), s.env, s.ctx)).status, 415);
  assert.equal((await api(blobReq("PUT", got.id, "x", { token: got.token, body: png, type: "image/png" }), s.env, s.ctx)).status, 400);
  assert.equal((await api(blobReq("PUT", got.id, AID, { token: got.token, body: new Uint8Array(), type: "image/png" }), s.env, s.ctx)).status, 400);
  assert.equal((await api(blobReq("PUT", got.id, AID, { token: got.token, body: new Uint8Array(2 << 20 | 1), type: "image/svg+xml" }), s.env, s.ctx)).status, 413);
  const put = await api(blobReq("PUT", got.id, AID, { token: got.token, body: png, type: "image/png" }), s.env, s.ctx);
  assert.equal(put.status, 201, await put.clone().text());
  // again is an update, not a second
  assert.equal((await api(blobReq("PUT", got.id, AID, { token: got.token, body: png, type: "image/png" }), s.env, s.ctx)).status, 200);
  const list = await (await api(blobReq("GET", got.id, undefined, { token: got.token }), s.env, s.ctx)).json();
  assert.deepEqual(list.blobs, [{ id: AID, type: "image/png", bytes: png.length }]);
  assert.equal(list.maxFiles, MAX_BLOBS);

  // served as uploaded, sandboxed when opened as a document
  const b = await content(req("GET", `/_blob/${AID}`, { host: CONTENT, dest: "image" }), s.env);
  assert.equal(b.status, 200);
  assert.deepEqual(new Uint8Array(await b.arrayBuffer()), png);
  assert.equal(b.headers.get("Content-Type"), "image/png");
  assert.equal(b.headers.get("X-Content-Type-Options"), "nosniff");
  assert.match(b.headers.get("Content-Security-Policy"), /^sandbox; default-src 'none'/);
  // opened by itself in a tab too: the page may link to it
  assert.equal((await content(req("GET", `/_blob/${AID}`, { host: CONTENT, dest: "document" }), s.env)).status, 200);
  assert.equal((await content(req("GET", `/_blob/${AID2}`, { host: CONTENT, dest: "image" }), s.env)).status, 404);
  // through the worker, on the content host only
  assert.equal((await worker.fetch(req("GET", `/_blob/${AID}`, { host: CONTENT, dest: "image" }), s.env, s.ctx)).status, 200);

  // deleted: gone there
  const del = await (await api(blobReq("DELETE", got.id, AID, { token: got.token }), s.env, s.ctx)).json();
  assert.equal(del.deleted, true);
  assert.equal((await content(req("GET", `/_blob/${AID}`, { host: CONTENT, dest: "image" }), s.env)).status, 404);
  assert.equal(s.env.SHARES.objs.has(`${got.id}/_blob/${AID}`), false);
  assert.equal((await (await api(blobReq("DELETE", got.id, AID, { token: got.token }), s.env, s.ctx)).json()).deleted, false);
});

test("shares: an asset id another share holds is refused, and a takedown takes the assets with it", async () => {
  const s = setup();
  const one = await share(s), two = await share(s);
  const body = new TextEncoder().encode("a,b\n1,2\n");
  assert.equal((await api(blobReq("PUT", one.id, AID, { token: one.token, body, type: "text/csv" }), s.env, s.ctx)).status, 201);
  // first claim wins: another share can't put over it, nor take it down
  assert.equal((await api(blobReq("PUT", two.id, AID, { token: two.token, body: new TextEncoder().encode("x"), type: "text/plain" }), s.env, s.ctx)).status, 409);
  assert.equal((await api(blobReq("DELETE", two.id, AID, { token: two.token }), s.env, s.ctx)).status, 409);
  assert.equal(await (await content(req("GET", `/_blob/${AID}`, { host: CONTENT, dest: "empty" }), s.env)).text(), "a,b\n1,2\n");

  const down = await api(req("DELETE", `/api/shares/${one.id}`, { token: one.token }), s.env, s.ctx);
  assert.equal(down.status, 200);
  await s.settle();
  assert.equal((await content(req("GET", `/_blob/${AID}`, { host: CONTENT, dest: "image" }), s.env)).status, 404);
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT COUNT(*) AS n FROM blobs").get().n, 0);
  assert.equal([...s.env.SHARES.objs.keys()].some((k) => k.startsWith(`${one.id}/`)), false);
  // the id is free again
  assert.equal((await api(blobReq("PUT", two.id, AID, { token: two.token, body, type: "text/csv" }), s.env, s.ctx)).status, 201);
  // a share gone: its assets' calls are 404s
  assert.equal((await api(blobReq("PUT", one.id, AID2, { token: one.token, body, type: "text/csv" }), s.env, s.ctx)).status, 404);
});

// The content host serves /_blob/<id> by the id alone: an id a share's
// page still names after its asset went (its data, an older copy, one
// refused for its size) must never be another share's to fill, or its
// page shows a stranger's image, or runs their script or SVG.
test("shares: an id a share took down or was refused stays that share's, while it is up", async () => {
  const s = setup();
  const one = await share(s), two = await share(s);
  const js = new TextEncoder().encode("parent.postMessage('owned', '*')");
  const ok = new TextEncoder().encode("console.log(1)");
  const put = (sh, aid, body, type = "text/javascript") => api(blobReq("PUT", sh.id, aid, { token: sh.token, body, type }), s.env, s.ctx);
  assert.equal((await put(one, AID, ok)).status, 201);
  assert.equal((await (await api(blobReq("DELETE", one.id, AID, { token: one.token }), s.env, s.ctx)).json()).deleted, true);
  // taken down: not served, not listed, and not another share's to take
  assert.equal((await content(req("GET", `/_blob/${AID}`, { host: CONTENT, dest: "script" }), s.env)).status, 404);
  assert.deepEqual((await (await api(blobReq("GET", one.id, undefined, { token: one.token }), s.env, s.ctx)).json()).blobs, []);
  assert.equal((await put(two, AID, js)).status, 409);
  assert.equal((await api(blobReq("DELETE", two.id, AID, { token: two.token }), s.env, s.ctx)).status, 409);
  assert.equal((await content(req("GET", `/_blob/${AID}`, { host: CONTENT, dest: "script" }), s.env)).status, 404);
  // refused for its type or size: the same
  assert.equal((await put(one, AID2, ok, "text/html")).status, 415);
  assert.equal((await put(two, AID2, js)).status, 409);
  const id3 = "d".repeat(32);
  assert.equal((await put(one, id3, new Uint8Array(2 << 20 | 1), "image/svg+xml")).status, 413);
  assert.equal((await put(two, id3, js, "image/svg+xml")).status, 409);
  // the share itself puts it up again
  assert.equal((await put(one, AID, ok)).status, 201);
  assert.equal(await (await content(req("GET", `/_blob/${AID}`, { host: CONTENT, dest: "script" }), s.env)).text(), "console.log(1)");
  // a takedown lets them go
  assert.equal((await api(req("DELETE", `/api/shares/${one.id}`, { token: one.token }), s.env, s.ctx)).status, 200);
  await s.settle();
  assert.equal(s.env.SHARES_DB.raw.prepare("SELECT COUNT(*) AS n FROM blobs").get().n, 0);
});

test("shares: a share's assets keep to its budget and an address's hourly asks", async () => {
  const s = setup();
  const got = await share(s);
  s.env.SHARES_DB.raw.prepare("INSERT INTO blobs (id, share, type, bytes, created) VALUES (?, ?, 'text/plain', ?, 0)").run(AID2, got.id, (100 << 20) - 3);
  const four = new Uint8Array(4);
  const r = await api(blobReq("PUT", got.id, AID, { token: got.token, body: four, type: "text/plain" }), s.env, s.ctx);
  assert.equal(r.status, 413);
  assert.match((await r.json()).error, /at most 256 files, 100 MB together/);
  // replacing one counts its new size, not both
  assert.equal((await api(blobReq("PUT", got.id, AID2, { token: got.token, body: four, type: "text/plain" }), s.env, s.ctx)).status, 200);
  assert.equal((await api(blobReq("PUT", got.id, AID, { token: got.token, body: four, type: "text/plain" }), s.env, s.ctx)).status, 201);

  for (let i = 0; i < BLOB_RATE; i++) await api(blobReq("DELETE", got.id, AID2, { token: got.token, ip: "203.0.113.9" }), s.env, s.ctx);
  assert.equal((await api(blobReq("DELETE", got.id, AID2, { token: got.token, ip: "203.0.113.9" }), s.env, s.ctx)).status, 429);
});
