// Run with Node's test runner and Playwright on the module path; see README.md.
// Copy all keys (@emilochen on X: one provider's keys from this computer to
// a magpie in the cloud): a key provider with more than one key has Copy all
// N keys under Add another key. It asks provider/key with allKeys and copies
// what comes back, one a line, through magpie's clipboard, and says how many
// went and where to paste them. One key has no such row; each row's own
// Copy stays. Add another key is still the one row that adds. The page
// doesn't scroll. Every language, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const KEYS = "sk-one\nsk-two\nsk-three";
const relay = (n) => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "big", name: "", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: [
    { id: "aaaaaaaaaa", name: "Team", masked: "sk-…one", on: true, active: true },
    { id: "bbbbbbbbbb", name: "", masked: "sk-…two", on: false },
    { id: "cccccccccc", name: "", masked: "sk-…ree", on: true },
  ].slice(0, n),
});

function serve(lang, n, posts) {
  const providers = { providers: [relay(n)], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/provider/key" && route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      posts.push({ path: url.pathname, body });
      return json(body.allKeys ? { key: KEYS, count: 3 } : { key: "sk-one" });
    }
    if (url.pathname === "/api/copy" && route.request().method() === "POST") {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return route.fulfill({ status: 204, body: "" });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { all: "Copy all 3 keys", another: "Add another key", note: "3 keys copied, one a line — paste them into Paste several in another magpie" },
  zh: { all: "全部复制（3 个密钥）", another: "添加另一个密钥", note: "已复制 3 个密钥，每行一个 — 在另一台 magpie 的“批量粘贴”里粘贴即可" },
  "zh-TW": { all: "全部複製（3 個金鑰）", another: "新增另一個金鑰", note: "已複製 3 個金鑰，每行一個 — 在另一台 magpie 的「批次貼上」裡貼上即可" },
  ja: { all: "3 個のキーをすべてコピー", another: "別のキーを追加", note: "3 個のキーを 1 行に 1 つずつコピーしました — 別の magpie の「まとめて貼り付け」に貼り付けてください" },
  de: { all: "Alle 3 Schlüssel kopieren", another: "Weiteren Schlüssel hinzufügen", note: "3 Schlüssel kopiert, einer pro Zeile — in einem anderen magpie unter „Mehrere einfügen“ einfügen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const width of [900, 420]) {
      const open = async (t, n, name) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}-key-copy-all.png`) });
          }
          await browser.close();
        });
        page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(15000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, n, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Relay" }).first().click();
        await page.locator(".editor .accts .acc.add").waitFor();
        return { page, errors, posts };
      };
      const scrolled = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.classList[0]}:${e.scrollTop}`)].join(" "));

      test(`${engine} ${lang} ${width}px: Copy all keys copies every key, one a line`, async (t) => {
        const { page, errors, posts } = await open(t, 3, "three");
        const all = page.locator(".editor .accts .acc.keys-copy-all");
        assert.equal(await all.count(), 1);
        assert.equal((await all.textContent()).trim(), w.all);
        // under Add another key, which stays the one row that adds
        assert.equal(await page.locator(".editor .accts .acc.add").count(), 1);
        assert.equal((await page.locator(".editor .accts .acc.add").textContent()).trim(), w.another);
        assert.ok(await all.evaluate((e) => e.previousElementSibling?.classList.contains("add")), "not right under Add another key");
        // the label fits on its row, narrow too
        assert.ok(await all.evaluate((e) => e.scrollWidth <= e.clientWidth + 1), "the label overflows its row");
        // in view first: Playwright scrolls to what it clicks, the page must not
        await all.scrollIntoViewIfNeeded();
        const sc = await scrolled(page);
        await all.click();
        for (let i = 0; i < 200 && posts.length < 2; i++) await new Promise((r) => setTimeout(r, 50));
        assert.deepEqual(posts, [
          { path: "/api/provider/key", body: { id: "relay", allKeys: true } },
          { path: "/api/copy", body: { text: KEYS } },
        ]);
        await page.waitForFunction((note) => document.querySelector("#status")?.textContent.trim() === note, w.note);
        assert.equal(await scrolled(page), sc, "a click scrolled");
        // the label stays: the toast says it went
        assert.equal((await all.textContent()).trim(), w.all);
        assert.deepEqual(errors, []);
      });

      if (width === 900) {
        test(`${engine} ${lang}: one key has no Copy all`, async (t) => {
          const { page, errors } = await open(t, 1, "one");
          assert.equal(await page.locator(".editor .accts .acc.keys-copy-all").count(), 0);
          assert.deepEqual(errors, []);
        });
      }
    }
  }
}
