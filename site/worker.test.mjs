// node --test site/: the update feed's notes follow the app's language
// (freecss on Discord). GitHub and the edge cache are stood in for.
import { test } from "node:test";
import assert from "node:assert/strict";
import worker, { served, goTarget, sponsorTarget } from "./worker.js";
import { bucket, sha } from "./r2stub.mjs";

const EN = "### Features\n\n- One thing (#1)\n\n### Install\n\nDownload it.";
const ZH = "### 新功能\n\n- 一件事 (#1)";
const BODIES = {
  "v0.1.3": `${EN}\n\n<!-- lang:zh -->\n\n${ZH}\n`,
  "v0.1.2": "### Fixes\n\n- Older, English alone",
  "v0.1.1": "- first",
};

const store = new Map();
globalThis.caches = {
  default: {
    match: async (req) => (store.has(req.url) ? new Response(store.get(req.url)) : undefined),
    put: async (req, res) => void store.set(req.url, await res.text()),
  },
};
// apiDown stands in for GitHub's API limiting the worker's shared IP (#661);
// pagesDown for github.com itself failing too. seen keeps each API ask's
// headers.
let apiDown = false, pagesDown = false;
// downloadsDown: github.com's download address answering 504 while the
// API serves the same files (2026-11-08)
let downloadsDown = false;
const seen = [];
const FEED = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <id>tag:github.com,2008:Repository/1/v0.1.3</id>
    <updated>2026-10-03T08:54:51Z</updated>
    <link rel="alternate" type="text/html" href="https://github.com/yetone/magpie-releases/releases/tag/v0.1.3"/>
    <title>magpie v0.1.3</title>
    <content type="html">&lt;h3&gt;Features&lt;/h3&gt;
&lt;ul&gt;
&lt;li&gt;One thing (#1)&lt;/li&gt;
&lt;/ul&gt;
&lt;h3&gt;Install&lt;/h3&gt;
&lt;p&gt;Download it.&lt;/p&gt;
&lt;h3&gt;新功能&lt;/h3&gt;
&lt;ul&gt;
&lt;li&gt;一件事 (#1)&lt;/li&gt;
&lt;/ul&gt;</content>
  </entry>
  <entry>
    <id>tag:github.com,2008:Repository/1/v0.1.2</id>
    <updated>2026-10-02T08:54:51Z</updated>
    <link rel="alternate" type="text/html" href="https://github.com/yetone/magpie-releases/releases/tag/v0.1.2"/>
    <content type="html">&lt;h3&gt;Fixes&lt;/h3&gt;
&lt;ul&gt;
&lt;li&gt;Older, &lt;strong&gt;English&lt;/strong&gt; alone, &lt;code&gt;a &amp;amp;lt; b&lt;/code&gt; &lt;a href=&quot;https://x.dev/&quot;&gt;link&lt;/a&gt;&lt;/li&gt;
&lt;/ul&gt;</content>
  </entry>
</feed>`;
// what the worker sent PostHog
const captured = [];
// what the worker passed on to PostHog for the pages' snippet (relay)
const relayed = [];
globalThis.fetch = async (u, init = {}) => {
  u = String(u);
  if (u === "https://us.i.posthog.com/i/v0/e/") {
    captured.push(JSON.parse(init.body));
    return new Response("{}");
  }
  if (/^https:\/\/us(-assets)?\.i\.posthog\.com\//.test(u)) {
    relayed.push({ u, method: init.method, headers: new Headers(init.headers), body: init.body && (await new Response(init.body).text()) });
    return new Response("relayed");
  }
  if (u.startsWith("https://api.github.com/")) {
    seen.push(new Headers(init.headers));
    if (apiDown) return new Response('{"message":"API rate limit exceeded"}', { status: 403 });
    const asset = u.match(/\/releases\/assets\/(\d+)$/);
    if (asset) {
      if (new Headers(init.headers).get("Accept") !== "application/octet-stream") return Response.json({ id: +asset[1] });
      return new Response(null, { status: 302, headers: { Location: `https://release-assets.githubusercontent.com/a/${asset[1]}?sig=1` } });
    }
  } else if (pagesDown) return new Response("unicorn", { status: 503 });
  if (downloadsDown && u.includes("/releases/download/")) return new Response("", { status: 504 });
  if (u === "https://release-assets.githubusercontent.com/a/2?sig=1") return new Response("abc  magpie-linux-amd64\n");
  if (u === "https://github.com/yetone/magpie-releases/releases/latest")
    return new Response(null, { status: 302, headers: { Location: "https://github.com/yetone/magpie-releases/releases/tag/v0.1.3" } });
  if (u.endsWith("/releases.atom")) return new Response(FEED);
  const gh = (tag) => ({ tag_name: tag, body: BODIES[tag], html_url: "https://github.com/r/" + tag, published_at: "2026-10-01T00:00:00Z", draft: false, prerelease: false, assets: [
      { name: "magpie-linux-amd64", size: 1, browser_download_url: "https://dl/x", url: "https://api.github.com/repos/yetone/magpie-releases/releases/assets/1" },
      { name: "SHA256SUMS", size: 1, browser_download_url: "https://dl/sums", url: "https://api.github.com/repos/yetone/magpie-releases/releases/assets/2" },
    ] });
  if (u.endsWith("/releases/tags/v0.1.3")) return Response.json(gh("v0.1.3"));
  if (u.endsWith("/releases/latest")) return Response.json(gh("v0.1.3"));
  if (u.includes("/releases?per_page=")) return Response.json(Object.keys(BODIES).map(gh));
  if (u.endsWith("/SHA256SUMS")) return new Response("abc  magpie-linux-amd64\n");
  return new Response("not found", { status: 404 });
};

const ctx = { waitUntil: (p) => p };
const get = async (path) => {
  const res = await worker.fetch(new Request("https://usemagpie.ai" + path), {}, ctx);
  assert.equal(res.status, 200, path);
  return { body: await res.json(), cc: res.headers.get("Cache-Control") };
};

test("latest: the English alone without lang, the Chinese with zh", async () => {
  for (const q of ["", "?lang=en", "?lang=fr"]) {
    const { body, cc } = await get("/api/latest" + q);
    assert.equal(body.version, "0.1.3");
    assert.equal(body.notes, EN, q);
    assert.match(cc, /max-age=/);
  }
  for (const q of ["?lang=zh", "?lang=zh-CN", "?lang=zh_Hans"]) {
    assert.equal((await get("/api/latest" + q)).body.notes, ZH, q);
  }
  // asked in Chinese first, the edge's copy still has both
  assert.equal((await get("/api/latest")).body.notes, EN);
});

test("notes: each release in the language, English where it has no Chinese", async () => {
  const zh = (await get("/api/notes?after=0.1.1&upto=0.1.3&lang=zh")).body.releases;
  assert.deepEqual(zh.map((r) => [r.version, r.notes]), [["0.1.3", ZH], ["0.1.2", BODIES["v0.1.2"]]]);
  const en = (await get("/api/notes?after=0.1.1&upto=0.1.3")).body.releases;
  assert.deepEqual(en.map((r) => [r.version, r.notes]), [["0.1.3", EN], ["0.1.2", BODIES["v0.1.2"]]]);
  const again = (await get("/api/notes?after=0.1.1&upto=0.1.3&lang=zh")).body.releases;
  assert.equal(again[0].notes, ZH);
});

test("notes: limit keeps the newest that many up to upto", async () => {
  const two = (await get("/api/notes?upto=0.1.3&limit=2")).body.releases;
  assert.deepEqual(two.map((r) => r.version), ["0.1.3", "0.1.2"]);
  const one = (await get("/api/notes?upto=0.1.2&limit=1")).body.releases;
  assert.deepEqual(one.map((r) => r.version), ["0.1.2"]);
  for (const q of ["", "&limit=0", "&limit=x", "&limit=-1"]) {
    assert.equal((await get("/api/notes?upto=0.1.3" + q)).body.releases.length, 3, q);
  }
});

test("i18n: every string the home page marks has its Chinese, and no other", async () => {
  const { readFile } = await import("node:fs/promises");
  const { LANGS } = await import("./i18n.js");
  const html = await readFile(new URL("./public/index.html", import.meta.url), "utf8");
  const keys = new Set();
  for (const [, k] of html.matchAll(/data-i18n="([^"]+)"/g)) keys.add(k);
  for (const [, pairs] of html.matchAll(/data-i18n-attr="([^"]+)"/g))
    for (const p of pairs.split(",")) keys.add(p.split(":")[1]);
  for (const [lang, { dict }] of Object.entries(LANGS)) {
    assert.deepEqual([...keys].filter((k) => !(k in dict)), [], `${lang} lacks`);
    assert.deepEqual(Object.keys(dict).filter((k) => !keys.has(k)), [], `${lang} has unused`);
  }
});

test("home: / sends a browser that prefers Chinese, Japanese or German to its page, until a language is picked", async () => {
  const env = { ASSETS: { fetch: async () => new Response("<html></html>", { headers: { "Content-Type": "text/plain" } }) } };
  const home = (headers) => worker.fetch(new Request("https://usemagpie.ai/", { headers }), env, ctx);
  const to = async (headers) => {
    const res = await home(headers);
    assert.match(res.headers.get("Vary") || "", /Accept-Language/);
    return res.status === 302 ? res.headers.get("Location") : null;
  };
  assert.equal(await to({}), null);
  assert.equal(await to({ "Accept-Language": "en-US,en;q=0.9,zh;q=0.8" }), null);
  assert.equal(await to({ "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8" }), "/zh/");
  assert.equal(await to({ "Accept-Language": "zh-TW" }), "/zh/");
  assert.equal(await to({ "Accept-Language": "fr-FR,zh;q=0.5,en;q=0.4" }), "/zh/");
  assert.equal(await to({ "Accept-Language": "en;q=0.2,zh;q=0.8" }), "/zh/");
  assert.equal(await to({ "Accept-Language": "zh-CN", Cookie: "a=b; lang=en" }), null);
  assert.equal(await to({ Cookie: "lang=zh" }), "/zh/");
  assert.equal(await to({ "Accept-Language": "ja-JP,ja;q=0.9,en;q=0.8" }), "/ja/");
  assert.equal(await to({ "Accept-Language": "ja", Cookie: "lang=zh" }), "/zh/");
  assert.equal(await to({ Cookie: "lang=ja" }), "/ja/");
  assert.equal(await to({ "Accept-Language": "de-DE,de;q=0.9,en;q=0.8" }), "/de/");
  assert.equal(await to({ "Accept-Language": "de-AT" }), "/de/");
  assert.equal(await to({ "Accept-Language": "de", Cookie: "lang=en" }), null);
  assert.equal(await to({ Cookie: "lang=de" }), "/de/");
  for (const lang of ["zh", "de"]) {
    const res = await worker.fetch(new Request("https://usemagpie.ai/" + lang), env, ctx);
    assert.equal(res.status, 301);
    assert.equal(new URL(res.headers.get("Location")).pathname, `/${lang}/`);
  }
});

test("docs: the bare docs paths open each language's guide", async () => {
  const env = { ASSETS: { fetch: async () => new Response("asset") } };
  for (const [from, to] of [["/docs", "/docs/start"], ["/docs/zh/", "/docs/zh/start"], ["/docs/ja", "/docs/ja/start"]]) {
    const res = await worker.fetch(new Request("https://usemagpie.ai" + from), env, ctx);
    assert.equal(new URL(res.headers.get("Location"), "https://usemagpie.ai").pathname, to, from);
  }
});

test("site: /favicon.ico goes to the icon the pages link", async () => {
  const env = { ASSETS: { fetch: async () => new Response("asset") } };
  const res = await worker.fetch(new Request("https://usemagpie.ai/favicon.ico"), env, ctx);
  assert.equal(res.status, 301);
  assert.equal(res.headers.get("Location"), "https://usemagpie.ai/favicon.png");
});

test("docs: the MCP hub's old pages go to the MCP router's, keeping the query", async () => {
  const env = { ASSETS: { fetch: async () => new Response("asset") } };
  for (const [from, to] of [["/docs/mcp-hub", "/docs/mcp-router"], ["/docs/mcp-hub.html", "/docs/mcp-router"], ["/docs/zh/mcp-hub/", "/docs/zh/mcp-router"], ["/docs/ja/mcp-hub.html?x=1", "/docs/ja/mcp-router?x=1"]]) {
    const res = await worker.fetch(new Request("https://usemagpie.ai" + from), env, ctx);
    assert.equal(res.status, 301, from);
    const got = new URL(res.headers.get("Location"));
    assert.equal(got.pathname + got.search, to, from);
  }
  const res = await worker.fetch(new Request("https://usemagpie.ai/docs/mcp-router"), env, ctx);
  assert.equal(await res.text(), "asset");
});

// #661: the update window showed no notes while GitHub's API limited the
// worker, and the failure was kept for five minutes.
const ask = (path, env = {}) => worker.fetch(new Request("https://usemagpie.ai" + path), env, ctx);

test("latest: with the API limited, the notes come from the releases feed, kept briefly", async () => {
  store.clear();
  apiDown = true;
  try {
    const res = await ask("/api/latest");
    assert.equal(res.status, 200);
    const body = await res.json();
    assert.equal(body.version, "0.1.3");
    assert.match(body.notes, /One thing \(#1\)/);
    assert.doesNotMatch(body.notes, /一件事/);
    assert.equal(body.assets["magpie-linux-amd64"].sha256, "abc");
    assert.equal(res.headers.get("Cache-Control"), "public, max-age=60");
    const zh = await (await ask("/api/latest?lang=zh")).json();
    assert.match(zh.notes, /^### 新功能\n\n- 一件事 \(#1\)$/);
  } finally {
    apiDown = false;
  }
});

test("notes: with the API limited, the releases feed's notes, in either language", async () => {
  store.clear();
  apiDown = true;
  try {
    const res = await ask("/api/notes?after=0.1.1&upto=0.1.3&lang=zh");
    assert.equal(res.status, 200);
    assert.equal(res.headers.get("Cache-Control"), "public, max-age=60");
    const list = (await res.json()).releases;
    assert.deepEqual(list.map((r) => r.version), ["0.1.3", "0.1.2"]);
    assert.equal(list[0].notes, "### 新功能\n\n- 一件事 (#1)");
    assert.equal(list[1].notes, "### Fixes\n\n- Older, **English** alone, `a &lt; b` [link](https://x.dev/)");
    assert.equal(list[0].url, "https://github.com/yetone/magpie-releases/releases/tag/v0.1.3");
  } finally {
    apiDown = false;
  }
});

test("notes and latest: GitHub failing altogether is an error never kept, then the last whole answer", async () => {
  store.clear();
  apiDown = pagesDown = true;
  try {
    for (const path of ["/api/notes?upto=0.1.3", "/api/latest"]) {
      const res = await ask(path);
      assert.equal(res.status, 503, path);
      assert.equal(res.headers.get("Cache-Control"), "no-store", path);
    }
    // a whole answer once, then GitHub fails again: that one, kept briefly
    apiDown = pagesDown = false;
    assert.equal((await ask("/api/notes?upto=0.1.3")).headers.get("Cache-Control"), "public, max-age=300");
    assert.equal((await ask("/api/latest")).status, 200);
    for (const k of ["https://usemagpie.ai/__v2/releases", "https://usemagpie.ai/__v2/latest"]) store.delete(k);
    apiDown = pagesDown = true;
    const res = await ask("/api/notes?upto=0.1.3");
    assert.equal(res.status, 200);
    assert.equal(res.headers.get("Cache-Control"), "public, max-age=60");
    assert.equal((await res.json()).releases[0].notes, EN);
    const lat = await ask("/api/latest");
    assert.equal(lat.status, 200);
    assert.equal((await lat.json()).notes, EN);
  } finally {
    apiDown = pagesDown = false;
  }
});

test("api: a GITHUB_TOKEN secret is sent to GitHub's API, and none without it", async () => {
  store.clear();
  seen.length = 0;
  await ask("/api/notes?upto=0.1.3", { GITHUB_TOKEN: "ghp_x" });
  assert.equal(seen.at(-1).get("Authorization"), "Bearer ghp_x");
  store.clear();
  await ask("/api/notes?upto=0.1.3");
  assert.equal(seen.at(-1).get("Authorization"), null);
});

test("cache: what an older worker kept, in its own shape, is never read as this one's", async () => {
  store.clear();
  store.set("https://usemagpie.ai/__latest", JSON.stringify({ version: "0.1.1", notes: "", assets: {} }));
  store.set("https://usemagpie.ai/__releases", JSON.stringify([{ version: "0.1.1", notes: "old" }]));
  store.set("https://usemagpie.ai/__v2/latest", JSON.stringify({ version: "0.1.1" }));
  store.set("https://usemagpie.ai/__v2/releases", JSON.stringify([{ version: "0.1.1" }]));
  const notes = await ask("/api/notes?upto=0.1.3");
  assert.equal(notes.status, 200);
  assert.equal((await notes.json()).releases[0].version, "0.1.3");
  const lat = await ask("/api/latest");
  assert.equal(lat.status, 200);
  assert.equal((await lat.json()).version, "0.1.3");
});

test("download: a platform's link goes to that asset of the latest release", async () => {
  const res = await ask("/download/linux");
  assert.equal(res.status, 302);
  assert.equal(res.headers.get("Location"), "https://dl/x");
  assert.equal((await ask("/download/nothing-like-it")).status, 404);
});

test("download: each link followed is counted, with the platform, the country and a browser or a command", async () => {
  captured.length = 0;
  const req = new Request("https://usemagpie.ai/download/linux", { headers: { "User-Agent": "curl/8.7.1" } });
  req.cf = { country: "BR" };
  assert.equal((await worker.fetch(req, {}, ctx)).status, 302);
  await new Promise((ok) => setTimeout(ok, 0));
  assert.equal(captured.length, 1);
  assert.equal(captured[0].event, "magpie site download");
  assert.deepEqual(captured[0].properties, { link: "linux", file: "magpie-linux-amd64", client: "command", country: "BR", $process_person_profile: false });
  captured.length = 0;
  await worker.fetch(new Request("https://usemagpie.ai/download/linux", { headers: { "User-Agent": "Mozilla/5.0 (Macintosh)" } }), {}, ctx);
  await new Promise((ok) => setTimeout(ok, 0));
  assert.equal(captured[0].properties.client, "browser");
  // a HEAD, or a file the release doesn't have, isn't counted
  captured.length = 0;
  await worker.fetch(new Request("https://usemagpie.ai/download/linux", { method: "HEAD" }), {}, ctx);
  await ask("/download/nothing-like-it");
  await new Promise((ok) => setTimeout(ok, 0));
  assert.deepEqual(captured, []);
});

test("install.sh: each fetch is counted", async () => {
  const env = { ASSETS: { fetch: async () => new Response("#!/bin/sh\n", { headers: { "Content-Type": "text/x-sh" } }) } };
  captured.length = 0;
  const res = await worker.fetch(new Request("https://usemagpie.ai/install.sh", { headers: { "User-Agent": "curl/8.7.1" } }), env, ctx);
  assert.equal(await res.text(), "#!/bin/sh\n");
  await new Promise((ok) => setTimeout(ok, 0));
  assert.equal(captured.length, 1);
  assert.equal(captured[0].event, "magpie site install script");
  assert.equal(captured[0].properties.client, "command");
});

test("relay: the snippet's requests reach PostHog with the visitor's address and without the site's cookies", async () => {
  relayed.length = 0;
  const post = new Request("https://usemagpie.ai/r/e/?ip=0&ver=1.442.1", {
    method: "POST",
    body: "gzipped",
    headers: { Cookie: "lang=zh", "CF-Connecting-IP": "203.0.113.7", "Content-Type": "text/plain", "User-Agent": "Mozilla/5.0" },
  });
  assert.equal(await (await worker.fetch(post, {}, ctx)).text(), "relayed");
  await worker.fetch(new Request("https://usemagpie.ai/r/static/array.js"), {}, ctx);
  await worker.fetch(new Request("https://usemagpie.ai/r/array/phc_x/config.js"), {}, ctx);
  await worker.fetch(new Request("https://usemagpie.ai/r/flags/?v=2"), {}, ctx);
  assert.deepEqual(relayed.map((r) => r.u), [
    "https://us.i.posthog.com/e/?ip=0&ver=1.442.1",
    "https://us-assets.i.posthog.com/static/array.js",
    "https://us-assets.i.posthog.com/array/phc_x/config.js",
    "https://us.i.posthog.com/flags/?v=2",
  ]);
  const [e] = relayed;
  assert.equal(e.method, "POST");
  assert.equal(e.body, "gzipped");
  assert.equal(e.headers.get("X-Forwarded-For"), "203.0.113.7");
  assert.equal(e.headers.get("Cookie"), null);
  assert.equal(e.headers.get("User-Agent"), "Mozilla/5.0");
});


test("docs: an English docs page sends a browser that prefers Chinese or Japanese to its own, until a language is picked", async () => {
  const have = new Set(["/docs/start", "/docs/zh/start", "/docs/ja/start", "/docs/only-en"]);
  const env = { ASSETS: { fetch: async (r) => new Response(have.has(new URL(r.url).pathname) ? "<html></html>" : "nf", { status: have.has(new URL(r.url).pathname) ? 200 : 404, headers: { "Content-Type": "text/plain" } }) } };
  const to = async (path, headers) => {
    const res = await worker.fetch(new Request("https://usemagpie.ai" + path, { headers }), env, ctx);
    assert.match(res.headers.get("Vary") || "", /Accept-Language/);
    return res.status === 302 ? res.headers.get("Location") : res.status;
  };
  assert.equal(await to("/docs/start", {}), 200);
  assert.equal(await to("/docs/start", { "Accept-Language": "en-US,en;q=0.9,zh;q=0.8" }), 200);
  assert.equal(await to("/docs/start", { "Accept-Language": "zh-CN,zh;q=0.9" }), "/docs/zh/start");
  assert.equal(await to("/docs/start?omarchy=1", { "Accept-Language": "ja-JP" }), "/docs/ja/start?omarchy=1");
  assert.equal(await to("/docs/start", { "Accept-Language": "zh-CN", Cookie: "lang=en" }), 200);
  assert.equal(await to("/docs/start", { Cookie: "lang=ja" }), "/docs/ja/start");
  // German has no docs: English
  assert.equal(await to("/docs/start", { "Accept-Language": "de-DE" }), 200);
  // a page not written in Chinese yet
  assert.equal(await to("/docs/only-en", { "Accept-Language": "zh-CN" }), 200);
  // a language's own page is never moved
  const res = await worker.fetch(new Request("https://usemagpie.ai/docs/zh/start", { headers: { Cookie: "lang=en" } }), env, ctx);
  assert.equal(res.status, 200);
});

// /api/partners is partners.js, cached ten minutes; every entry keeps the
// rules the app checks (internal/provider/partners.go), so none is dropped
// there unseen.
test("partners", async () => {
  const { PARTNERS } = await import("./partners.js");
  const res = await ask("/api/partners");
  assert.equal(res.status, 200);
  assert.equal(res.headers.get("Cache-Control"), "public, max-age=600");
  const { partners } = await res.json();
  assert.deepEqual(partners, served(PARTNERS, Date.now()));
  assert(partners.length <= 12);
  const ids = new Set();
  for (const p of PARTNERS) {
    assert.match(p.id, /^[a-z0-9][a-z0-9-]{1,40}$/, p.id);
    assert(!ids.has(p.id), p.id + " twice");
    ids.add(p.id);
    assert(p.name?.trim(), p.id + " has no name");
    assert(p.chat || p.responses || p.anthropic || p.regions?.length, p.id + " has no endpoint");
    const urls = [p.chat, p.responses, p.anthropic, p.website, p.keysUrl, p.iconUrl, ...(p.regions || []).flatMap((r) => [r.chat, r.responses, r.anthropic, r.keysUrl, r.website])];
    for (const u of urls.filter(Boolean)) assert.equal(new URL(u).protocol, "https:", p.id + ": " + u);
    for (const t of [p.from, p.until].filter(Boolean)) assert(!isNaN(Date.parse(t)), p.id + ": " + t);
  }
});

// The website and key links the app gets go through /go, which counts the
// click and sends the browser on; one whose term ended leaves the list but
// its links, kept in the providers added from it, still go.
const LIST = [
  { id: "acme", name: "Acme", chat: "https://api.acme.example/v1", website: "https://acme.example/", keysUrl: "https://acme.example/keys", iconUrl: "https://acme.example/i.png",
    regions: [{ id: "cn", name: "China", chat: "https://cn.acme.example/v1", keysUrl: "https://cn.acme.example/keys" }, { id: "us east", name: "US", chat: "https://us.acme.example/v1" }] },
  { id: "plain", name: "Plain", chat: "https://api.plain.example/v1" },
  { id: "gone", name: "Gone", chat: "https://api.gone.example/v1", keysUrl: "https://gone.example/keys", until: "2026-01-01T00:00:00Z" },
];
const NOW = Date.parse("2026-10-10T00:00:00Z");

test("partners: links go through /go", () => {
  const out = served(LIST, NOW);
  assert.deepEqual(out.map((p) => p.id), ["acme", "plain"]);
  const [acme, plain] = out;
  assert.equal(acme.website, "https://usemagpie.ai/go/acme/site");
  assert.equal(acme.keysUrl, "https://usemagpie.ai/go/acme/keys");
  assert.equal(acme.iconUrl, LIST[0].iconUrl);
  assert.equal(acme.chat, LIST[0].chat);
  assert.equal(acme.regions[0].keysUrl, "https://usemagpie.ai/go/acme/keys/cn");
  assert.equal(acme.regions[0].website, undefined);
  // the editor names a /go website by the host it sends the browser to
  assert.equal(acme.siteHost, "acme.example");
  assert.equal(acme.regions[0].siteHost, undefined);
  assert.equal(plain.siteHost, undefined);
  assert.equal(acme.regions[1].keysUrl, undefined);
  assert.deepEqual(plain, LIST[1]);
  // partners.js itself is left as it is
  assert.equal(LIST[0].keysUrl, "https://acme.example/keys");
  assert.equal(served([{ ...LIST[0], until: "2026-10-10T00:00:01Z" }], NOW).length, 1);
});

test("partners: /go sends the browser on, saying it came from magpie", () => {
  assert.equal(goTarget(LIST, "acme", "keys"), "https://acme.example/keys?ref=magpie");
  assert.equal(goTarget(LIST, "acme", "site"), "https://acme.example/?ref=magpie");
  assert.equal(goTarget(LIST, "acme", "keys", "cn"), "https://cn.acme.example/keys?ref=magpie");
  // a region without its own page has the partner's
  assert.equal(goTarget(LIST, "acme", "keys", "us east"), "https://acme.example/keys?ref=magpie");
  assert.equal(goTarget(LIST, "acme", "site", "cn"), "https://acme.example/?ref=magpie");
  // ended, still goes
  assert.equal(goTarget(LIST, "gone", "keys"), "https://gone.example/keys?ref=magpie");
  // the partner's own query and fragment stay; its own ref is its referral code and stays as it is
  const own = [{ id: "own", keysUrl: "https://own.example/keys?aff=7#signup", website: "https://own.example/?ref=abc" }];
  assert.equal(goTarget(own, "own", "keys"), "https://own.example/keys?aff=7&ref=magpie#signup");
  assert.equal(goTarget(own, "own", "site"), "https://own.example/?ref=abc");
  for (const [id, what, region] of [["nobody", "keys"], ["acme", "chat"], ["acme", "keys", "mars"], ["plain", "keys"], ["acme", "constructor"]])
    assert.equal(goTarget(LIST, id, what, region), "", [id, what, region].join(" "));
});

test("partners: /go counts the click", async () => {
  const { PARTNERS } = await import("./partners.js");
  const p = PARTNERS.find((x) => x.keysUrl || x.website);
  if (!p) {
    // nobody listed: every /go is not found, and nothing is sent
    captured.length = 0;
    for (const path of ["/go/acme/keys", "/go/%E0/keys", "/go/"]) assert.equal((await ask(path)).status, 404, path);
    assert.deepEqual(captured, []);
    return;
  }
  const what = p.keysUrl ? "keys" : "site";
  captured.length = 0;
  const res = await worker.fetch(new Request("https://usemagpie.ai/go/" + p.id + "/" + what), {}, ctx);
  assert.equal(res.status, 302);
  assert.equal(res.headers.get("Location"), goTarget(PARTNERS, p.id, what));
  assert.equal(new URL(res.headers.get("Location")).searchParams.has("ref"), true);
  await Promise.resolve();
  assert.equal(captured.length, 1);
  assert.equal(captured[0].event, "magpie partner go");
  assert.equal(captured[0].properties.id, p.id);
  assert.equal(captured[0].properties.what, what);
  assert.equal(captured[0].properties.$process_person_profile, false);
  // a HEAD goes on but isn't counted
  captured.length = 0;
  const head = await worker.fetch(new Request("https://usemagpie.ai/go/" + p.id + "/" + what, { method: "HEAD" }), {}, ctx);
  assert.equal(head.status, 302);
  assert.deepEqual(captured, []);
  assert.equal((await ask("/go/nobody-at-all/keys")).status, 404);
  assert.equal((await ask("/go/%E0/keys")).status, 404);
});

test("partners: the list's fetches are counted, one in ten", async () => {
  const { PARTNERS } = await import("./partners.js");
  const anyLive = PARTNERS.some((p) => (!p.from || Date.parse(p.from) <= Date.now()) && (!p.until || Date.now() < Date.parse(p.until)));
  const random = Math.random;
  try {
    for (const [r, sent] of [[0.05, anyLive], [0.5, false]]) {
      Math.random = () => r;
      captured.length = 0;
      const res = await ask("/api/partners");
      assert.equal(res.status, 200);
      await new Promise((ok) => setTimeout(ok, 0));
      assert.equal(captured.length, sent ? 1 : 0, "random " + r);
      if (sent) {
        assert.equal(captured[0].event, "magpie partners fetch");
        assert.equal(captured[0].properties.weight, 10);
      }
    }
  } finally {
    Math.random = random;
  }
});

// /api/icon fetches a picture for the app so its host sees Cloudflare, not
// the user: nothing of the asker's request goes on, and only a picture
// comes back.
test("icon proxy passes on a picture and nothing of the asker", async () => {
  const PNG = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3]);
  const was = globalThis.fetch;
  const asked = [];
  globalThis.fetch = async (u, init = {}) => {
    asked.push({ u: String(u), init });
    if (String(u) === "https://author.example/logo.png") return new Response(PNG);
    if (String(u) === "https://author.example/page") return new Response("<html><script>x</script></html>");
    if (String(u) === "https://author.example/big.png") return new Response(new Uint8Array(2 << 20).fill(0x89));
    if (String(u) === "https://author.example/gone.png") return new Response("no", { status: 404 });
    throw new Error("a test reached " + u); // never the real network
  };
  try {
    const req = new Request("https://usemagpie.ai/api/icon?url=" + encodeURIComponent("https://author.example/logo.png"), {
      headers: { Cookie: "lang=zh", "User-Agent": "magpie/0.1.1168", "X-Forwarded-For": "203.0.113.9", "CF-Connecting-IP": "203.0.113.9" },
    });
    const res = await worker.fetch(req, {}, { waitUntil() {} });
    assert.equal(res.status, 200);
    assert.equal(res.headers.get("Content-Type"), "image/png");
    assert.deepEqual(new Uint8Array(await res.arrayBuffer()), PNG);
    const sent = asked.find((a) => a.u === "https://author.example/logo.png");
    const h = new Headers(sent.init.headers);
    assert.deepEqual([...h.keys()].sort(), ["accept", "user-agent"]);
    assert.doesNotMatch(h.get("user-agent"), /0\.1\.1168/);

    const ask = async (u) => (await worker.fetch(new Request("https://usemagpie.ai/api/icon?url=" + encodeURIComponent(u)), {}, { waitUntil() {} })).status;
    assert.equal(await ask("https://author.example/page"), 415);
    assert.equal(await ask("https://author.example/big.png"), 413);
    assert.equal(await ask("https://author.example/gone.png"), 502);
    assert.equal(await ask("http://author.example/logo.png"), 400);
    assert.equal(await ask("https://u:p@author.example/logo.png"), 400);
    assert.equal(await ask("https://author.example:8443/logo.png"), 400);
    assert.equal(await ask("not a url"), 400);
    assert.equal(await ask("https://usemagpie.ai/api/icon?url=x"), 400);
    assert.equal(await ask("https://www.usemagpie.ai/logo.png"), 400);
    assert.ok(asked.every((a) => !new URL(a.u).hostname.endsWith("usemagpie.ai")), "the worker asked itself");
  } finally {
    globalThis.fetch = was;
  }
});

test("sponsors: /go/<id>/home and /readme send the browser to the sponsor's own link, counted per place", async () => {
  const { SPONSORS } = await import("./sponsors.js");
  const list = [{ id: "acme", home: "https://acme.example/r/HOME", readme: "https://acme.example/r/README?x=1" }, { id: "solo", home: "https://solo.example/" }];
  assert.equal(sponsorTarget(list, "acme", "home"), "https://acme.example/r/HOME");
  assert.equal(sponsorTarget(list, "acme", "readme"), "https://acme.example/r/README?x=1");
  for (const [id, where] of [["solo", "readme"], ["acme", "keys"], ["acme", "id"], ["nobody", "home"]])
    assert.equal(sponsorTarget(list, id, where), "", id + " " + where);
  for (const s of SPONSORS) {
    for (const where of ["home", "readme"]) {
      if (!s[where]) continue;
      assert.equal(new URL(s[where]).protocol, "https:");
      captured.length = 0;
      const res = await worker.fetch(new Request(`https://usemagpie.ai/go/${s.id}/${where}`), {}, ctx);
      assert.equal(res.status, 302);
      assert.equal(res.headers.get("Location"), s[where]);
      await Promise.resolve();
      assert.equal(captured.length, 1);
      assert.equal(captured[0].event, "magpie sponsor go");
      assert.equal(captured[0].properties.id, s.id);
      assert.equal(captured[0].properties.where, where);
      // a HEAD goes on but isn't counted
      captured.length = 0;
      assert.equal((await worker.fetch(new Request(`https://usemagpie.ai/go/${s.id}/${where}`, { method: "HEAD" }), {}, ctx)).status, 302);
      assert.deepEqual(captured, []);
    }
    assert.equal((await ask(`/go/${s.id}/home/x`)).status, 404);
  }
  assert.equal((await ask("/go/nobody-at-all/home")).status, 404);
});

test("sponsors: the home page and the READMEs link every sponsor through /go", async () => {
  const { readFileSync } = await import("node:fs");
  const { SPONSORS } = await import("./sponsors.js");
  const pages = { home: [new URL("./public/index.html", import.meta.url)], readme: [new URL("../README.md", import.meta.url), new URL("../README.zh-CN.md", import.meta.url)] };
  for (const [where, files] of Object.entries(pages))
    for (const f of files) {
      const text = readFileSync(f, "utf8");
      for (const s of SPONSORS) {
        if (s[where]) assert.equal(text.includes(s[where]), false, `${f.pathname} links ${s.id} directly`);
        // the home page links its own /go; a README, on GitHub, the site's
        const go = (where === "home" ? "" : "https://usemagpie.ai") + `/go/${s.id}/${where}`;
        // as an <a href> or a Markdown link (Kimi's thanks line, e6b9d1199)
        if (text.includes(`/go/${s.id}/`)) assert.ok(s[where] && (text.includes(`href="${go}"`) || text.includes(`](${go})`)), `${f.pathname}: ${go}`);
      }
    }
  const home = readFileSync(pages.home[0], "utf8");
  for (const s of SPONSORS.filter((s) => s.home)) assert.ok(home.includes(`href="/go/${s.id}/home"`), s.id);
});

// HTMLRewriter is Cloudflare's; this stands in for the two uses here: a
// selector's inner content set, and the body appended to
class FakeRewriter {
  constructor() { this.h = []; }
  on(sel, h) { this.h.push([sel, h]); return this; }
  transform(res) {
    const hs = this.h;
    return new Response(new ReadableStream({
      async start(c) {
        let t = await res.text();
        for (const [sel, h] of hs) {
          let inner, tail = "";
          h.element({ setInnerContent: (x) => (inner = x), append: (x) => (tail += x) });
          if (sel === "#wall" && inner !== undefined) t = t.replace(/(<div id="wall">)[\s\S]*?(<\/div>)/, (_, a, b) => a + inner + b);
          if (sel === "body") t = t.replace("</body>", tail + "</body>");
        }
        c.enqueue(new TextEncoder().encode(t));
        c.close();
      },
    }), res);
  }
}

test("contributors: each language's page gets its own table, and keeps its line without one", async () => {
  const { readFileSync } = await import("node:fs");
  const { renderWall } = await import("./credits.mjs");
  const data = { people: [{ name: "packinggoodsllc_07668", via: ["Discord"], releases: [{ v: "0.1.2581", what: ["a contributors page"] }] }] };
  const page = (p) => new URL(`./public${p}.html`, import.meta.url);
  let fragments = true;
  const asked = [];
  const env = {
    ASSETS: {
      fetch: async (r) => {
        const p = new URL(r.url).pathname.replace(/\.html$/, "");
        asked.push(p);
        const m = p.match(/^\/contributors\/(en|zh|ja)$/);
        if (m) return fragments ? new Response(renderWall(data, m[1]), { headers: { "Content-Type": "text/html" } }) : new Response("nf", { status: 404 });
        try { return new Response(readFileSync(page(p), "utf8"), { headers: { "Content-Type": "text/html; charset=utf-8" } }); } catch { return new Response("nf", { status: 404 }); }
      },
    },
  };
  const was = globalThis.HTMLRewriter;
  globalThis.HTMLRewriter = FakeRewriter;
  try {
    const get = async (path) => (await worker.fetch(new Request(`https://usemagpie.ai${path}`, { headers: { Cookie: "lang=en" } }), env, ctx)).text();
    for (const [path, lang, head] of [["/docs/contributors", "en", "What came of it"], ["/docs/zh/contributors", "zh", "带来的改动"], ["/docs/ja/contributors.html", "ja", "そこから生まれた"]]) {
      const html = await get(path);
      assert.match(html, /<div id="wall"><div class="table-wrap"><table class="cw">/, path);
      assert.ok(html.includes(head), `${path} has the ${lang} table`);
      assert.ok(html.includes("packinggoodsllc_07668") && html.includes("v0.1.2581"), path);
      assert.match(html, /posthog\.init\(/, `${path} keeps the analytics`);
      assert.doesNotMatch(html, /cloudflareinsights/, `${path} leaves Cloudflare's beacon to Cloudflare`);
    }
    fragments = false;
    const bare = await get("/docs/zh/contributors");
    assert.ok(!bare.includes('<table class="cw">'));
    assert.match(bare, /<div id="wall"><p>名单在每次部署网站时/);
    // no other page asks for a table
    asked.length = 0;
    await get("/docs/start");
    assert.deepEqual(asked, ["/docs/start"]);
  } finally {
    globalThis.HTMLRewriter = was;
  }
});

test("contributors: the pages say how to be left off, and every docs footer links them", async () => {
  const { readFileSync, readdirSync } = await import("node:fs");
  const dir = (d) => new URL(`./public/docs/${d}`, import.meta.url);
  for (const [pre, file] of [["/docs/", "contributors.html"], ["/docs/zh/", "zh/contributors.html"], ["/docs/ja/", "ja/contributors.html"]]) {
    const html = readFileSync(dir(file), "utf8");
    assert.match(html, /<div id="wall">/, file);
    assert.ok(html.includes("site/contributors-exclude.txt"), file);
    for (const l of ["en", "zh", "ja"]) assert.ok(html.includes(`hreflang="${l}" href="https://usemagpie.ai/docs/${l === "en" ? "" : l + "/"}contributors"`), `${file} ${l}`);
    assert.ok(!/privacy" hreflang=|og:url" content="[^"]*privacy/.test(html), `${file} still points at the page it was made from`);
  }
  for (const sub of ["", "zh/", "ja/"]) {
    const pre = `/docs/${sub}`;
    for (const f of readdirSync(dir(sub)).filter((f) => f.endsWith(".html"))) {
      const foot = readFileSync(dir(sub + f), "utf8").split("<footer")[1];
      assert.ok(foot.includes(`<a href="${pre}contributors">`), `${sub}${f}'s footer`);
    }
  }
  assert.ok(readFileSync(new URL("./public/index.html", import.meta.url), "utf8").includes('<a href="/docs/contributors" data-i18n="foot.contrib">'));
});

test("seo: the sitemap lists the home pages and every docs page, each its own canonical", async () => {
  const { readFile, readdir } = await import("node:fs/promises");
  const { LANGS } = await import("./i18n.js");
  const pub = new URL("./public/", import.meta.url);
  const xml = await readFile(new URL("sitemap.xml", pub), "utf8");
  const listed = [...xml.matchAll(/<loc>([^<]+)<\/loc>/g)].map((m) => m[1]);
  const want = ["https://usemagpie.ai/", ...Object.keys(LANGS).map((l) => `https://usemagpie.ai/${l}/`)];
  for (const dir of ["docs/", "docs/zh/", "docs/ja/"])
    for (const f of (await readdir(new URL(dir, pub))).filter((f) => f.endsWith(".html")).sort())
      want.push(`https://usemagpie.ai/${dir}${f.slice(0, -5)}`);
  assert.deepEqual([...listed].sort(), [...want].sort());
  const home = await readFile(new URL("index.html", pub), "utf8");
  assert.match(home, /<link rel="canonical" data-i18n-attr="href:meta.url" href="https:\/\/usemagpie.ai\/">/);
  for (const [l, { dict }] of Object.entries(LANGS)) assert.equal(dict["meta.url"], `https://usemagpie.ai/${l}/`);
  for (const u of listed.filter((u) => u.includes("/docs/"))) {
    const html = await readFile(new URL(u.slice("https://usemagpie.ai/".length) + ".html", pub), "utf8");
    const canon = [...html.matchAll(/<link rel="canonical" href="([^"]+)">/g)].map((m) => m[1]);
    assert.deepEqual(canon, [u], `${u} canonical`);
    assert.ok(html.includes(`<link rel="alternate" hreflang="x-default" href="https://usemagpie.ai/docs/${u.split("/").pop()}">`), `${u} x-default`);
    assert.ok(!/name="robots" content="[^"]*noindex/.test(html), `${u} is noindex`);
    // every alternate is a page that exists, so none is listed as a 404
    for (const [, alt] of html.matchAll(/<link rel="alternate" hreflang="[a-z-]+" href="([^"]+)">/g))
      assert.ok(listed.includes(alt), `${u}: ${alt}`);
  }
});

test("seo: robots.txt names the sitemap and keeps crawlers off the counted links", async () => {
  const { readFile } = await import("node:fs/promises");
  const txt = await readFile(new URL("./public/robots.txt", import.meta.url), "utf8");
  assert.match(txt, /^Sitemap: https:\/\/usemagpie.ai\/sitemap.xml$/m);
  for (const p of ["/api/", "/go/", "/r/", "/download"]) assert.match(txt, new RegExp(`^Disallow: ${p}$`, "m"));
});

test("seo: llms.txt lists every English docs page and links only to listed pages", async () => {
  const { readFile, readdir } = await import("node:fs/promises");
  const pub = new URL("./public/", import.meta.url);
  const txt = await readFile(new URL("llms.txt", pub), "utf8");
  const xml = await readFile(new URL("sitemap.xml", pub), "utf8");
  const listed = new Set([...xml.matchAll(/<loc>([^<]+)<\/loc>/g)].map((m) => m[1]));
  assert.match(txt, /^# magpie\n\n> /);
  const links = [...txt.matchAll(/\]\((https:\/\/usemagpie\.ai\/[^)]*)\)/g)].map((m) => m[1]);
  for (const u of links) assert.ok(listed.has(u), `llms.txt links ${u}, which the sitemap doesn't list`);
  const docs = (await readdir(new URL("docs/", pub))).filter((f) => f.endsWith(".html") && f !== "contributors.html");
  for (const f of docs) assert.ok(links.includes(`https://usemagpie.ai/docs/${f.slice(0, -5)}`), `llms.txt misses docs/${f}`);
});

test("seo: llms-full.txt is current and holds every English docs page", async () => {
  const { readFile, readdir } = await import("node:fs/promises");
  const { build } = await import("./llms-full.mjs");
  const pub = new URL("./public/", import.meta.url);
  const txt = await readFile(new URL("llms-full.txt", pub), "utf8");
  assert.equal(txt, await build(), "public/llms-full.txt is stale: run `node llms-full.mjs` in site/");
  const docs = (await readdir(new URL("docs/", pub))).filter((f) => f.endsWith(".html") && f !== "contributors.html");
  for (const f of docs) assert.ok(txt.includes(`Source: https://usemagpie.ai/docs/${f.slice(0, -5)}\n`), `llms-full.txt misses docs/${f}`);
  assert.doesNotMatch(txt.replace(/^```\n[\s\S]*?\n```$/gm, ""), /<\/?(p|div|span|li|td|code|a)\b/, "llms-full.txt has HTML left outside its code blocks");
});

test("seo: the home page's structured data parses and its title is in every language", async () => {
  const { readFile } = await import("node:fs/promises");
  const { LANGS } = await import("./i18n.js");
  const html = await readFile(new URL("./public/index.html", import.meta.url), "utf8");
  const ld = html.match(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/);
  const app = JSON.parse(ld[1])["@graph"].find((n) => n["@type"] === "SoftwareApplication");
  assert.equal(app.offers.price, "0");
  assert.match(html, /<title data-i18n="meta.title">[^<]{20,70}<\/title>/);
  for (const [l, { dict }] of Object.entries(LANGS)) assert.ok(dict["meta.title"].startsWith("magpie"), l);
});

// 2026-11-08: github.com answered 504 for every release file for hours,
// while GitHub's API served the same ones. The feed then reads the
// checksums through the API and sends each file by the site's /dl, which
// an app too old to know the api field follows; it is kept a minute, so
// github.com's own address comes back as soon as it answers.
test("latest: github.com's downloads failing, the files go by /dl, through GitHub's API", async () => {
  store.clear();
  downloadsDown = true;
  try {
    const res = await ask("/api/latest");
    assert.equal(res.status, 200);
    assert.equal(res.headers.get("Cache-Control"), "public, max-age=60");
    const a = (await res.json()).assets["magpie-linux-amd64"];
    assert.deepEqual(a, {
      url: "https://usemagpie.ai/dl/v0.1.3/magpie-linux-amd64",
      api: "https://api.github.com/repos/yetone/magpie-releases/releases/assets/1",
      size: 1,
      sha256: "abc",
    });
    seen.length = 0;
    // each ask's writes to the edge's cache are waited for before the next
    const pending = [];
    const keeping = { waitUntil: (p) => void pending.push(p) };
    for (let i = 0; i < 2; i++) {
      const dl = await worker.fetch(new Request("https://usemagpie.ai/dl/v0.1.3/magpie-linux-amd64"), {}, keeping);
      await Promise.all(pending);
      assert.equal(dl.status, 302);
      assert.equal(dl.headers.get("Location"), "https://release-assets.githubusercontent.com/a/1?sig=1");
      assert.equal(dl.headers.get("Cache-Control"), "no-store");
    }
    // the release's files once, where one is once: the second is the edge's
    assert.equal(seen.length, 2);
    assert.equal(seen[1].get("Accept"), "application/octet-stream");
    assert.equal((await ask("/dl/v0.1.3/magpie-nothing")).status, 404);
  } finally {
    downloadsDown = false;
  }
  // github.com answering again: its own address, and api beside it
  store.clear();
  const res = await ask("/api/latest");
  assert.equal(res.headers.get("Cache-Control"), "public, max-age=300");
  const a = (await res.json()).assets["magpie-linux-amd64"];
  assert.equal(a.url, "https://dl/x");
  assert.equal(a.api, "https://api.github.com/repos/yetone/magpie-releases/releases/assets/1");
});

test("dl: a redirect GitHub's API gives anywhere but its asset host is not passed on", async () => {
  store.clear();
  const was = globalThis.fetch;
  globalThis.fetch = async (u, init = {}) =>
    /\/releases\/assets\/\d+$/.test(String(u)) ? new Response(null, { status: 302, headers: { Location: "https://evil.example/x" } }) : was(u, init);
  try {
    const res = await ask("/dl/v0.1.3/magpie-linux-amd64");
    assert.equal(res.status, 502);
    assert.equal(res.headers.get("Cache-Control"), "no-store");
  } finally {
    globalThis.fetch = was;
  }
});

// R2 holding a file under the hash the release gives, as the cron leaves it
const holding = async (b, tag, name, body, hash = sha(body)) =>
  b.put(`${tag}/${name}`, body, { httpMetadata: { contentType: "application/octet-stream" }, customMetadata: { sha256: hash } });

test("r2: the feed and /download name the site's /files for a file R2 holds, with github.com's address beside it", async () => {
  store.clear();
  const env = { RELEASES: bucket() };
  // the fixture's SHA256SUMS says "abc"
  await holding(env.RELEASES, "v0.1.3", "magpie-linux-amd64", "bytes", "abc");
  const res = await ask("/api/latest", env);
  assert.equal(res.status, 200);
  assert.deepEqual((await res.json()).assets["magpie-linux-amd64"], {
    url: "https://usemagpie.ai/files/v0.1.3/magpie-linux-amd64",
    api: "https://api.github.com/repos/yetone/magpie-releases/releases/assets/1",
    size: 1,
    sha256: "abc",
    github: "https://github.com/yetone/magpie-releases/releases/download/v0.1.3/magpie-linux-amd64",
  });
  const dl = await ask("/download/linux", env);
  assert.equal(dl.status, 302);
  assert.equal(dl.headers.get("Location"), "https://usemagpie.ai/files/v0.1.3/magpie-linux-amd64");
  const file = await ask("/files/v0.1.3/magpie-linux-amd64", env);
  assert.equal(file.status, 200);
  assert.equal(await file.text(), "bytes");
  // a tag R2 no longer keeps goes to GitHub
  const old = await ask("/files/v0.1.1/magpie-linux-amd64", env);
  assert.equal(old.status, 302);
  assert.equal(old.headers.get("Location"), "https://github.com/yetone/magpie-releases/releases/download/v0.1.1/magpie-linux-amd64");
});

test("r2: bytes R2 holds under another hash than the release's are not named; github.com's address is", async () => {
  store.clear();
  const env = { RELEASES: bucket() };
  await holding(env.RELEASES, "v0.1.3", "magpie-linux-amd64", "older");
  const a = (await (await ask("/api/latest", env)).json()).assets["magpie-linux-amd64"];
  assert.equal(a.url, "https://dl/x");
  assert.equal(a.github, "https://github.com/yetone/magpie-releases/releases/download/v0.1.3/magpie-linux-amd64");
  // R2 failing: the feed as GitHub has it
  store.clear();
  env.RELEASES.fail = true;
  const res = await ask("/api/latest", env);
  assert.equal(res.status, 200);
  assert.equal((await res.json()).assets["magpie-linux-amd64"].url, "https://dl/x");
});

test("r2: the five-minute cron copies the newest release's files from github.com, by GitHub's API when that fails", async () => {
  const body = "the linux build";
  const was = globalThis.fetch;
  const asked = [];
  globalThis.fetch = async (u, init = {}) => {
    u = String(u);
    if (u.endsWith("/SHA256SUMS") || u === "https://release-assets.githubusercontent.com/a/2?sig=1") return new Response(`${sha(body)}  magpie-linux-amd64\n`);
    if (u === "https://github.com/yetone/magpie-releases/releases/download/v0.1.3/magpie-linux-amd64") {
      asked.push("github.com");
      return downloadsDown ? new Response("", { status: 504 }) : new Response(body);
    }
    if (u === "https://release-assets.githubusercontent.com/a/1?sig=1") {
      asked.push("api");
      return new Response(body);
    }
    return was(u, init);
  };
  try {
    for (const down of [false, true]) {
      store.clear();
      downloadsDown = down;
      asked.length = 0;
      const env = { RELEASES: bucket() };
      const pending = [];
      await worker.scheduled({ cron: "*/5 * * * *" }, env, { waitUntil: (p) => void pending.push(p) });
      await Promise.all(pending);
      assert.deepEqual(asked, down ? ["github.com", "api"] : ["github.com"]);
      const o = env.RELEASES.objs.get("v0.1.3/magpie-linux-amd64");
      assert.ok(o, "copied");
      assert.equal(new TextDecoder().decode(o.bytes), body);
      assert.equal(o.customMetadata.sha256, sha(body));
      // the quarter-hour cron is the ledger's, and copies nothing
      const other = { RELEASES: bucket(), LEDGER: { get: async () => null, put: async () => {} } };
      const p2 = [];
      await worker.scheduled({ cron: "*/15 * * * *" }, other, { waitUntil: (p) => void p2.push(p.catch(() => {})) });
      await Promise.all(p2);
      assert.equal(other.RELEASES.objs.size, 0);
    }
  } finally {
    globalThis.fetch = was;
    downloadsDown = false;
    store.clear();
  }
});

test("plugins/tagged: the dist branch's list passed on, jsDelivr and the last copy when it fails", async () => {
  const was = globalThis.fetch;
  const list = { version: 1, plugins: [{ repo: "a/b", spec: "github:a/b", stars: 3 }] };
  let rawDown = false, cdnDown = false;
  globalThis.fetch = async (u) => {
    u = String(u);
    if (u.startsWith("https://raw.githubusercontent.com/magpie-community/plugins/dist/")) return rawDown ? new Response("x", { status: 503 }) : Response.json(list);
    if (u.startsWith("https://cdn.jsdelivr.net/gh/magpie-community/plugins@dist/")) return cdnDown ? new Response("x", { status: 503 }) : Response.json(list);
    throw new Error("a test reached " + u);
  };
  try {
    store.delete("https://usemagpie.ai/__v2/tagged");
    const ask = () => worker.fetch(new Request("https://usemagpie.ai/api/plugins/tagged"), {}, ctx);
    let res = await ask();
    assert.equal(res.status, 200);
    assert.deepEqual((await res.json()).plugins, list.plugins);
    store.delete("https://usemagpie.ai/__v2/tagged");
    rawDown = true;
    assert.equal((await (await ask()).json()).plugins[0].repo, "a/b", "jsDelivr stands in");
    store.delete("https://usemagpie.ai/__v2/tagged");
    cdnDown = true;
    assert.equal((await (await ask()).json()).plugins[0].repo, "a/b", "the last whole copy stands in");
    store.delete("https://usemagpie.ai/__v2/tagged-good");
    assert.equal((await ask()).status, 503);
  } finally {
    globalThis.fetch = was;
  }
});

test("plugins/readme: a plugin's kept README passed on, from jsDelivr when raw fails, 404 when none", async () => {
  const was = globalThis.fetch;
  const asked = [];
  let rawDown = false;
  globalThis.fetch = async (u) => {
    u = String(u);
    asked.push(u);
    const raw = "https://raw.githubusercontent.com/magpie-community/plugins/dist/readmes/";
    const cdn = "https://cdn.jsdelivr.net/gh/magpie-community/plugins@dist/readmes/";
    for (const [base, down] of [[raw, rawDown], [cdn, false]]) {
      if (!u.startsWith(base)) continue;
      if (down) return new Response("x", { status: 503 });
      if (u.endsWith("/rsshub-plugin.md")) return new Response("# kept");
      if (u.endsWith("/%40sc/scoped.md")) return new Response("# scoped");
      if (u.endsWith("/github/o/r.md")) return new Response("# repo");
      return new Response("nope", { status: 404 });
    }
    throw new Error("a test reached " + u);
  };
  try {
    const ask = (n) => worker.fetch(new Request("https://usemagpie.ai/api/plugins/readme?name=" + encodeURIComponent(n)), {}, ctx);
    for (const [n, want] of [["rsshub-plugin", "# kept"], ["@sc/scoped", "# scoped"], ["github:o/r", "# repo"]]) {
      store.delete("https://usemagpie.ai/__v2/readme/" + (n.startsWith("github:") ? "github/" + n.slice(7) : n));
      const res = await ask(n);
      assert.equal(res.status, 200, n);
      assert.equal(await res.text(), want);
    }
    const before = asked.length;
    assert.equal(await (await ask("rsshub-plugin")).text(), "# kept");
    assert.equal(asked.length, before, "the edge keeps it");
    store.delete("https://usemagpie.ai/__v2/readme/rsshub-plugin");
    rawDown = true;
    assert.equal(await (await ask("rsshub-plugin")).text(), "# kept", "jsDelivr stands in");
    assert.equal((await ask("missing-one")).status, 404);
    for (const bad of ["../x", "github:../../x", "", "A B"]) assert.equal((await ask(bad)).status, 400, bad);
  } finally {
    globalThis.fetch = was;
  }
});
