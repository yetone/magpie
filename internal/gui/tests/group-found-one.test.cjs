// Run with Node's test runner and Playwright on the module path; see README.md.
// A model one provider alone serves can be a found group too (realLemonawa
// on X: "Could there be an option so a group is created automatically even
// when only one provider serves it?"). The switch is under Advanced below
// the groups' settings, folded and off by default; switched on, the page
// posts groups/ofone and draws the group of one it gets back; off, it goes.
// With found groups off there is no such switch. Nothing moves the page,
// nothing runs past a phone's width, and every string has its words, in
// each language, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m", name: "m", providerName: "Alpha", icon: "generic" },
  { id: "b/m", name: "m", providerName: "Beta", icon: "generic" },
  { id: "a/solo", name: "solo", providerName: "Alpha", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const groups = (found, ofOne) => {
  const gs = [{ id: "auto-m", name: "Model M", members: ["a/m", "b/m"], auto: true, ready: true, memberInfo: info(["a/m", "b/m"]) }];
  if (ofOne) gs.push({ id: "auto-solo", name: "Solo Model", members: ["a/solo"], auto: true, ready: true, memberInfo: info(["a/solo"]) });
  return { models, pools: [], deciders: [], found, ofOne, skip: [], skippable: [{ id: "a", name: "Alpha" }, { id: "b", name: "Beta" }], groups: found ? gs : [] };
};

const keys = ["Groups of one provider", "A model only one of your providers serves becomes a group too (auto/…), so agents keep that name when a second provider comes or one goes.", "Found groups changed", "Advanced"];
const words = {
  en: { adv: "Advanced", label: "Groups of one provider", said: "Found groups changed" },
  zh: { adv: "高级", label: "单个供应商也成组", said: "自动路由组已更新" },
  "zh-TW": { adv: "進階", label: "單一供應商也成組", said: "自動路由組已更新" },
  ja: { adv: "詳細設定", label: "1 つのプロバイダでもグループに", said: "自動グループを更新しました" },
  de: { adv: "Erweitert", label: "Gruppen aus einem Anbieter", said: "Automatische Gruppen geändert" },
};

function serve(lang, posts, start) {
  let { found, ofOne } = start;
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
    if (url.pathname === "/api/groups") return json(groups(found, ofOne));
    if (url.pathname === "/api/groups/ofone") {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push(body);
      ofOne = body.on;
      return json(groups(found, ofOne));
    }
    if (url.pathname === "/api/groups/found") {
      const body = JSON.parse(r.request().postData() || "{}");
      found = body.on;
      return json(groups(found, ofOne));
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, width, t, start, name) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}.png`), fullPage: true });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts, start));
  await page.goto("http://magpie.test/?view=groups");
  await page.locator(".rt-group", { hasText: "Model M" }).waitFor();
  return { page, errors, posts };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 390]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: groups of one provider, under Advanced`, async (t) => {
        const { page, errors, posts } = await open(engine, lang, width, t, { found: true, ofOne: false }, "group-found-one");
        const more = page.locator(".rt-gsec .rt-gmore");
        assert.equal(await more.isVisible(), true);
        assert.equal(await more.evaluate((d) => d.open), false, "Advanced starts folded");
        const sum = more.locator("summary");
        assert.equal((await sum.textContent()).trim(), w.adv);
        await sum.evaluate((x) => x.scrollIntoView({ block: "center" })); // as the reader would
        await page.waitForTimeout(200);
        const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";"));
        const before = await at();
        await sum.click();
        const sw = more.locator(".rt-gone .lib-switch");
        await sw.waitFor();
        assert.equal((await more.locator(".rt-gone b").textContent()).trim(), w.label);
        assert.equal(await sw.getAttribute("aria-checked"), "false");
        assert.equal(await sw.getAttribute("aria-label"), w.label);
        assert.equal(await page.locator(".rt-group", { hasText: "Solo Model" }).count(), 0);

        await sw.click();
        await page.locator(".rt-group", { hasText: "Solo Model" }).waitFor();
        assert.deepEqual(posts, [{ on: true }]);
        await page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), w.said);
        assert.equal(await page.locator(".rt-gmore .rt-gone .lib-switch").getAttribute("aria-checked"), "true");
        assert.equal(await more.evaluate((d) => d.open), true, "the fold closed as it was drawn again");
        const over = await page.locator(".rt-gone").evaluate((e) => e.scrollWidth > e.clientWidth + 1);
        assert.equal(over, false, "the row runs past its width");
        const box = await page.locator(".rt-gone .lib-switch").boundingBox();
        assert(box.x >= 0 && box.x + box.width <= width, `the switch runs past the window: ${JSON.stringify(box)}`);

        await page.locator(".rt-gmore .rt-gone .lib-switch").click();
        await page.locator(".rt-group", { hasText: "Solo Model" }).waitFor({ state: "detached" });
        assert.deepEqual(posts, [{ on: true }, { on: false }]);
        assert.equal(await page.locator(".rt-gmore .rt-gone .lib-switch").getAttribute("aria-checked"), "false");
        assert.equal(await at(), before, "the clicks moved nothing");

        if (lang !== "en") {
          const missing = await page.evaluate(([l, ks]) => ks.filter((k) => !I18N[l][k]), [lang, keys]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: on, Advanced starts open; found groups off, it goes`, async (t) => {
    const { page, errors } = await open(engine, "en", 1100, t, { found: true, ofOne: true }, "group-found-one-on");
    const more = page.locator(".rt-gsec .rt-gmore");
    assert.equal(await more.evaluate((d) => d.open), true);
    assert.equal(await more.locator(".rt-gone .lib-switch").getAttribute("aria-checked"), "true");
    await page.locator(".rt-gfound .lib-switch").click();
    await page.waitForFunction(() => document.querySelector(".rt-gsec .rt-gmore")?.hidden === true);
    assert.equal(await more.isVisible(), false);
    assert.deepEqual(errors, []);
  });
}
