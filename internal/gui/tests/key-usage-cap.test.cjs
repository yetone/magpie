// Run with Node's test runner and Playwright on the module path; see README.md.
// A key whose usage windows magpie reads (OpenCode Go's 5 hours, week and
// month) takes the usage cap a subscription account does (@wanggenjidev on
// X): the same pill and words (No cap, Cap 99%), the app's own menu, posted
// to provider/accountcap by the key's id, and a key at its cap is marked
// with when it is back. A key of a provider whose windows aren't read has no
// such pill. A click moves nothing; at a phone's width the row stays inside
// the window. In every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fresh = () => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" },
  accountCaps: { k1: 90, k2: 99 },
  keyList: [
    { id: "k1", name: "Main", masked: "sk-…one", on: true, usageCap: true },
    { id: "k2", name: "Team", masked: "sk-…two", on: true, usageCap: true, capHeld: { used: 99, cap: 99, back: new Date(Date.now() + 3 * 36e5).toISOString() } },
    { id: "k3", name: "Spare", masked: "sk-…three", on: true, usageCap: true },
  ],
  balanceToken: { takes: false, set: false }, proxy: "",
});

function serve(lang, posts) {
  const relay = fresh();
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/lanes") return json({});
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      const action = url.pathname.slice("/api/provider/".length), body = route.request().postDataJSON();
      posts.push({ action, body });
      if (action === "accountcap") {
        if (body.cap) relay.accountCaps[body.account] = body.cap;
        else delete relay.accountCaps[body.account];
      }
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { k1: "Cap 90%", k3: "Cap 80%", k2: "Cap 99%", none: "No cap", held: "At its cap · back ", menu: "Usage cap" },
  zh: { k1: "上限 90%", k3: "上限 80%", k2: "上限 99%", none: "不设上限", held: "已达上限 · ", menu: "用量上限" },
  "zh-TW": { k1: "上限 90%", k3: "上限 80%", k2: "上限 99%", none: "不設上限", held: "已達上限 · ", menu: "用量上限" },
  ja: { k1: "上限 90%", k3: "上限 80%", k2: "上限 99%", none: "上限なし", held: "上限に到達 · ", menu: "使用量上限" },
  de: { k1: "Limit 90 %", k3: "Limit 80 %", k2: "Limit 99 %", none: "Kein Limit", held: "Am Limit · wieder ", menu: "Nutzungslimit" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 400]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a key whose windows are read takes the usage cap, marks one at its cap, and is set from the app's menu`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-key-usage-cap.png`) });
          }
          await browser.close();
        });
        page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Relay" }).first().click();
        await page.locator(".editor .accts .acc .acap").first().waitFor({ state: "attached" });
        await page.waitForTimeout(300);
        const row = (id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
        const pill = (id) => row(id).locator(".acap");
        const menu = page.locator(".proto-menu");
        const item = (name) => menu.locator(".pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`) }) });
        const scrolled = () => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
        const posted = async (n) => {
          for (let i = 0; i < 60 && posts.length === n; i++) await new Promise((r) => setTimeout(r, 50));
          assert(posts.length > n, "nothing posted");
          return posts.at(-1);
        };

        assert.equal(await pill("k1").textContent(), w.k1);
        assert.equal(await pill("k2").textContent(), w.k2);
        assert.equal(await pill("k3").textContent(), w.none);
        assert.equal(await page.locator("select").count(), 0, "no native select");
        // one at its cap says so, and when it is back
        const line = row("k2").locator(".key-cap-line");
        assert.equal(await line.count(), 1);
        assert((await line.textContent()).startsWith(w.held), await line.textContent());
        assert.equal(await row("k1").locator(".key-cap-line").count(), 0);
        // a key's cap has no "click its meter" line: a key has no meter to click
        assert(!(await pill("k1").getAttribute("title")).includes("meter"), "a meter is named");
        const over = await page.evaluate(() => [...document.querySelectorAll(".editor .accts .acc")].filter((e) => e.getBoundingClientRect().right > innerWidth + 1).length);
        assert.equal(over, 0, "a key's row past the window's edge");
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth + 1), true, "the page scrolls sideways");

        // k3: capped at 80%
        await row("k3").hover();
        await page.waitForTimeout(150);
        const before = await row("k3").evaluate((e) => Math.round(e.getBoundingClientRect().top)), sc = await scrolled();
        await pill("k3").click();
        await menu.waitFor();
        await page.waitForTimeout(200);
        assert.equal(await row("k3").evaluate((e) => Math.round(e.getBoundingClientRect().top)), before, "the row moved");
        assert.equal(await scrolled(), sc, "a click scrolled");
        assert.equal(await menu.locator(".pm-head").textContent(), w.menu);
        let n = posts.length;
        await item("80%").click();
        let p = await posted(n);
        assert.equal(p.action, "accountcap");
        assert.deepEqual(p.body, { id: "relay", account: "k3", cap: 80 });
        await page.waitForTimeout(250);
        assert.equal(await pill("k3").textContent(), w.k3);

        // k1's cap taken off
        await row("k1").hover();
        await pill("k1").click();
        await menu.waitFor();
        n = posts.length;
        await item(w.none).click();
        p = await posted(n);
        assert.deepEqual(p.body, { id: "relay", account: "k1", cap: 0 });
        await page.waitForTimeout(250);
        assert.equal(await pill("k1").textContent(), w.none);

        const stripes = await page.evaluate(() => [...document.querySelectorAll(".acap, .key-cap-line, .key-cap-line *")].map((e) => getComputedStyle(e)).filter((s) => parseFloat(s.borderLeftWidth) > parseFloat(s.borderRightWidth) || (parseFloat(s.borderLeftWidth) > 0 && s.borderLeftColor !== s.borderRightColor)).length);
        assert.equal(stripes, 0, "no border stripes");
        assert.deepEqual(errors, []);
      });
    }
  }
}
