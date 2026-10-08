// node --test site/: the update feed's notes follow the app's language
// (freecss on Discord). GitHub and the edge cache are stood in for.
import { test } from "node:test";
import assert from "node:assert/strict";
import worker from "./worker.js";

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
globalThis.fetch = async (u, init = {}) => {
  u = String(u);
  if (u.startsWith("https://api.github.com/")) {
    seen.push(new Headers(init.headers));
    if (apiDown) return new Response('{"message":"API rate limit exceeded"}', { status: 403 });
  } else if (pagesDown) return new Response("unicorn", { status: 503 });
  if (u === "https://github.com/yetone/magpie-releases/releases/latest")
    return new Response(null, { status: 302, headers: { Location: "https://github.com/yetone/magpie-releases/releases/tag/v0.1.3" } });
  if (u.endsWith("/releases.atom")) return new Response(FEED);
  const gh = (tag) => ({ tag_name: tag, body: BODIES[tag], html_url: "https://github.com/r/" + tag, published_at: "2026-10-01T00:00:00Z", draft: false, prerelease: false, assets: [{ name: "magpie-linux-amd64", size: 1, browser_download_url: "https://dl/x" }] });
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
  assert.deepEqual(await res.json(), { partners: PARTNERS });
  assert(PARTNERS.length <= 12);
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
