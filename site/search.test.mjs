// The docs' search: the committed indexes are what search-index.mjs builds,
// every docs page in every language is in its language's index, and the
// finding in public/docs-search.js ranks and cuts as its comments say.
import test from "node:test";
import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import { createRequire } from "node:module";
import { LANGS, build, page } from "./search-index.mjs";

const pub = new URL("./public/", import.meta.url);
const { find, snippet } = createRequire(import.meta.url)("./public/docs-search.js");

test("search: each committed index is current and holds every docs page", async () => {
  const built = await build();
  for (const [lang, dir] of Object.entries(LANGS)) {
    const have = await readFile(new URL(`search/${lang}.json`, pub), "utf8");
    assert.equal(have, built[lang], `public/search/${lang}.json is stale: run \`node search-index.mjs\` in site/`);
    const idx = JSON.parse(have);
    const paths = new Set(idx.map((p) => p[0]));
    const files = (await readdir(new URL(dir, pub))).filter((f) => f.endsWith(".html") && f !== "contributors.html");
    for (const f of files) assert.ok(paths.has("/" + dir + f.slice(0, -5)), `${lang} index misses ${dir}${f}`);
    const src = new Map();
    for (const f of files) src.set("/" + dir + f.slice(0, -5), await readFile(new URL(dir + f, pub), "utf8"));
    for (const [path, title, secs] of idx) {
      assert.ok(title, `${path} has no title`);
      assert.ok(secs.length, `${path} has no sections`);
      for (const [id, head, body] of secs) {
        // a tag left in the text is one the page writes as text (&lt;…),
        // an HTML sample in a code block, never the page's own markup
        for (const [, tag] of (head + " " + body).matchAll(/<(\/?[a-z][\w-]*)/gi)) assert.ok(src.get(path).includes("&lt;" + tag), `${path}#${id} keeps the page's <${tag}>`);
        if (head) assert.ok(id, `${path}: section "${head}" has no id to link to`);
      }
    }
  }
});

test("search: the new Download source section is found in each language", async () => {
  const q = { en: "download source mirror", zh: "下载源 镜像", ja: "ダウンロード元 ミラー" };
  for (const lang of Object.keys(LANGS)) {
    const idx = JSON.parse(await readFile(new URL(`search/${lang}.json`, pub), "utf8"));
    const hits = find(idx, q[lang]);
    assert.ok(hits.slice(0, 3).some((h) => h.path.endsWith("/mirrors") && h.id === "settings"), `${lang}: ${JSON.stringify(hits.slice(0, 3).map((h) => h.path + "#" + h.id))}`);
  }
});

test("search: page reads sections, not the page's chrome", () => {
  const named = '<nav><div class="links"><a href="/docs/x">Other</a><a class="toc-only" href="/docs/mirrors">Mirrors &amp; proxies</a></div></nav><aside class="toc">\n  <p>On this page</p></aside><article><h1>Mirrors in China: faster</h1></article>';
  assert.equal(page(named, "/docs/mirrors").title, "Mirrors & proxies", "the nav's name for the page");
  assert.equal(page(named, "/docs/y").title, "Mirrors in China: faster", "the h1 when the nav has no link and the contents' heading is generic");
  assert.equal(page(named.replace("On this page", "Mirrors"), "/docs/y").title, "Mirrors", "the contents' heading");
  const p = page('<nav>Nav words</nav><article><p class="eyebrow">Docs</p><h1>Mirrors &amp; more</h1><p>Intro <code>x</code>.</p><button>Copy</button><h2 id="a">First</h2><p>One</p><h3>Sub</h3><ol class="steps"><li><b>Open</b>Settings</li></ol><h2 id="b">Second</h2><p>Two</p></article>');
  assert.deepEqual(p, { title: "Mirrors & more", sections: [["", "", "Intro x."], ["a", "First", "One Sub Open Settings"], ["b", "Second", "Two"]] });
});

test("search: find needs every term, ranks headings first and caps each page", () => {
  const idx = [
    ["/docs/a", "Alpha", [["", "", "mirror text here, a mirror"], ["s1", "Mirror settings", "how to set it"], ["s2", "Other", "mirror once"], ["s3", "More", "mirror again"]]],
    ["/docs/b", "Beta mirror", [["", "", "nothing else"], ["x", "Proxy", "a proxy and a mirror"]]],
    ["/docs/c", "镜像", [["", "", "下载源可以设成镜像"]]],
  ];
  const hits = find(idx, "mirror");
  assert.equal(hits[0].path + "#" + hits[0].id, "/docs/a#s1", "a heading match comes first");
  assert.ok(hits.filter((h) => h.path === "/docs/a").length <= 3, "at most three sections a page");
  assert.deepEqual(find(idx, "MIRROR proxy").map((h) => h.path + "#" + h.id), ["/docs/b#x"], "every term, any case");
  assert.deepEqual(find(idx, "下载源 镜像").map((h) => h.path), ["/docs/c"], "Chinese terms match inside text");
  assert.deepEqual(find(idx, "   "), []);
  assert.deepEqual(find(idx, "nowhere"), []);
  assert.equal(find(idx, "a", 2).length, 2, "the limit holds");
});

test("search: snippet marks the terms around the first one found", () => {
  const long = "x ".repeat(100) + "the Download source row holds a mirror address " + "y ".repeat(100);
  const parts = snippet(long, "mirror download");
  assert.ok(parts[0].startsWith("…"), "cut text starts with an ellipsis");
  assert.ok(parts.at(-1).endsWith("…"));
  assert.deepEqual(parts.filter((_, i) => i % 2), ["Download", "mirror"]);
  assert.deepEqual(snippet("a.b (c)", "(c)"), ["a.b ", "(c)", ""], "regex characters are literal");
  assert.deepEqual(snippet("short", ""), ["short"]);
});
