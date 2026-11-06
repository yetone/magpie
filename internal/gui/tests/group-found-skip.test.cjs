// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider can be kept out of the groups magpie finds (chenxiang0010 on
// Discord: "自动创建路由组的这里能否设置跳过某个供应商"). Under the found
// groups' switch, a text button says which are left out; it opens the app's
// menu of providers, each ticked or not, and as the menu closes the page
// posts groups/skip with those ticked, draws the groups it gets back and
// names the providers on the button. Unticked again, none is left out.
// Nothing moves the page, nothing runs past a phone's width, and every
// string has its words, in each language, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m", name: "m", providerName: "Alpha", icon: "generic" },
  { id: "b/m", name: "m", providerName: "Trae CN", icon: "generic" },
  { id: "c/m", name: "m", providerName: "Gamma", icon: "generic" },
];
const skippable = [{ id: "a", name: "Alpha" }, { id: "b", name: "Trae CN" }, { id: "c", name: "Gamma" }];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const groups = (skip) => {
  const members = ["a/m", "b/m", "c/m"].filter((id) => !skip.includes(id.split("/")[0]));
  return { models, pools: [], deciders: [], found: true, skip, skippable,
    groups: [{ id: "auto-m", name: "Model M", members, auto: true, ready: true, memberInfo: info(members) }] };
};

const keys = ["Their models join no found group; the groups you made or changed keep them", "Left out: {names}",
  "Leave out a provider…", "Leave out of found groups", "Found groups changed"];
const words = {
  en: { none: "Leave out a provider…", head: "Leave out of found groups", left: "Left out: Trae CN", said: "Found groups changed" },
  zh: { none: "排除供应商…", head: "不参与自动成组", left: "不参与自动成组：Trae CN", said: "自动路由组已更新" },
  "zh-TW": { none: "排除供應商…", head: "不參與自動成組", left: "不參與自動成組：Trae CN", said: "自動路由組已更新" },
  ja: { none: "プロバイダを除外…", head: "自動グループから除外", left: "自動グループから除外：Trae CN", said: "自動グループを更新しました" },
  de: { none: "Anbieter ausnehmen…", head: "Aus automatischen Gruppen ausnehmen", left: "Ausgenommen: Trae CN", said: "Automatische Gruppen geändert" },
};

function serve(lang, posts) {
  let skip = [];
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups(skip));
    if (url.pathname === "/api/groups/skip") {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push(body);
      skip = body.skip;
      return json(groups(skip));
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 390]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a provider left out of found groups`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-group-found-skip.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=groups");
        const card = page.locator(".rt-group", { hasText: "Model M" });
        await card.waitFor();
        const pick = page.locator(".rt-gsec .rt-gfound .rt-skip-pick");
        assert.equal((await pick.textContent()).trim(), w.none);
        assert.equal(await pick.getAttribute("aria-haspopup"), "menu");

        await pick.evaluate((x) => x.scrollIntoView({ block: "center" })); // as the reader would
        await page.waitForTimeout(200);
        const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";"));
        const before = await at();

        await pick.click();
        const menu = page.locator(".pop.proto-menu.rt-skip-menu");
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), w.head);
        assert.deepEqual(await menu.locator(".pm-item .pm-name").allTextContents(), ["Alpha", "Trae CN", "Gamma"]);
        // in sight, within the window
        const box = await menu.boundingBox();
        assert(box.x >= 0 && box.x + box.width <= width, `the menu runs past the window: ${JSON.stringify(box)}`);
        await menu.locator(".pm-item", { hasText: "Trae CN" }).click();
        assert.equal(await menu.locator(".pm-item", { hasText: "Trae CN" }).getAttribute("aria-checked"), "true");
        assert.deepEqual(posts, [], "nothing is saved while the menu is open");
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });
        await page.waitForFunction(() => document.querySelector(".rt-skip-pick")?.textContent.includes("Trae CN"));
        assert.deepEqual(posts, [{ skip: ["b"] }]);
        assert.equal((await page.locator(".rt-skip-pick").textContent()).trim(), w.left);
        await page.waitForFunction(() => !document.querySelector(".rt-group .mem")?.textContent.includes("Trae CN"));
        await page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), w.said);
        const over = await page.locator(".rt-gfound").evaluate((e) => e.scrollWidth > e.clientWidth + 1);
        assert.equal(over, false, "the found groups' row runs past its width");
        const border = await page.locator(".rt-skip-pick").evaluate((e) => { const c = getComputedStyle(e); return c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor; });
        assert.equal(border, false, "no left-border accent");

        // let back in
        await page.locator(".rt-skip-pick").click();
        await menu.waitFor();
        await menu.locator(".pm-item", { hasText: "Trae CN" }).click();
        await page.locator(".rt-skip-pick").click(); // closes it
        await page.waitForFunction((m) => document.querySelector(".rt-skip-pick")?.textContent.trim() === m, w.none);
        assert.deepEqual(posts, [{ skip: ["b"] }, { skip: [] }]);
        await page.waitForFunction(() => document.querySelector(".rt-group .mem")?.textContent.includes("Trae CN"));
        assert.equal(await at(), before, "the clicks moved nothing");

        if (lang !== "en") {
          const missing = await page.evaluate(([l, ks]) => ks.filter((k) => !I18N[l][k]), [lang, keys]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
