// Run with Node's test runner and Playwright on the module path; see README.md.
// The groups magpie finds on its own (auto-<model>) can be turned off all
// at once (蓝猫 on Discord: "路由分组会自动创建，可以关闭掉吗"), not only
// removed one by one. A switch by the routing groups' list, with what it
// does in words, says whether they are found; clicked off, it posts
// groups/found {on:false}, the found groups leave the list and the user's
// stay, and the page says which agent was moved off a found group to its
// model from one provider. Clicked on again, they are back. Neither click
// moves the page, and the switch has no left-border accent. In English and
// Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "b/m", name: "m", providerName: "B", icon: "generic" },
  { id: "a/x", name: "x", providerName: "A", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const found = { id: "auto-m", name: "Model M", members: ["a/m", "b/m"], auto: true, ready: true, memberInfo: info(["a/m", "b/m"]) };
const mine = { id: "mine", name: "Mine", members: ["a/x", "b/m"], ready: true, memberInfo: info(["a/x", "b/m"]) };
const groups = (on, moved) => ({ models, pools: [], deciders: [], found: on, groups: on ? [mine, found] : [mine], ...(moved ? { moved } : {}) });

const words = {
  en: { label: "Find groups on their own", onHint: "becomes a group of them", offHint: "only the groups you made or changed",
    moved: "Claude Code moved to a/m", back: "Found groups are on" },
  zh: { label: "自动创建路由组", onHint: "会自动组成一个路由组", offHint: "只列出、只提供你自己建的或改过的组",
    moved: "Claude Code 已改用 a/m", back: "已开启自动创建路由组" },
};

function serve(lang, posts) {
  let on = true;
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
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
    if (url.pathname === "/api/groups") return json(groups(on));
    if (url.pathname === "/api/groups/found") {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push(body);
      on = body.on;
      return json(groups(on, on ? null : [{ agent: "Claude Code", field: "model", from: "group/auto-m", to: "a/m" }]));
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: found groups turned off and on`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-found-off.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      const card = (name) => page.locator(".rt-group", { hasText: name });
      await card("Model M").waitFor();
      const box = page.locator(".rt-gsec .rt-gfound");
      const sw = box.locator("button[role=switch]");
      assert.equal(await sw.getAttribute("aria-label"), w.label);
      assert.equal(await sw.getAttribute("aria-checked"), "true");
      assert((await box.textContent()).includes(w.label));
      assert((await box.textContent()).includes(w.onHint), "it says what found groups are");
      // by the list: right under the routing groups' head, over the cards
      const order = await page.evaluate(() => {
        const s = document.querySelector(".rt-gsec");
        return [...s.children].map((c) => c.className);
      });
      assert.deepEqual(order.slice(0, 3), ["row-head", "rt-gfound", "list rt-groups"]);
      const border = await sw.evaluate((e) => { const c = getComputedStyle(e); return c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor; });
      assert.equal(border, false, "no left-border accent");

      await box.evaluate((x) => x.scrollIntoView({ block: "center" })); // by the test, as the reader would
      await page.waitForTimeout(200);
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";"));
      const before = await at();

      await sw.click();
      await page.waitForFunction(() => document.querySelector(".rt-gfound button[role=switch]")?.getAttribute("aria-checked") === "false");
      assert.deepEqual(posts, [{ on: false }]);
      await card("Model M").waitFor({ state: "detached" }); // the found group left the list
      assert.equal(await card("Mine").count(), 1, "the user's group stays");
      assert((await box.textContent()).includes(w.offHint), "it says what is listed now");
      await page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), w.moved);
      assert.equal(await at(), before, "the click moved nothing");

      await box.locator("button[role=switch]").click();
      await card("Model M").waitFor();
      assert.deepEqual(posts, [{ on: false }, { on: true }]);
      assert.equal(await box.locator("button[role=switch]").getAttribute("aria-checked"), "true");
      await page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), w.back);
      assert.equal(await at(), before, "nor did the second");

      const missing = await page.evaluate(() => [
        "Find groups on their own", "Found groups are on", "Found groups are off: only yours are listed and served",
        "A model two or more of your providers serve becomes a group of them (auto-…). Switch it off to list and serve only the groups you made or changed.",
        "Off: only the groups you made or changed are listed and served. An agent set to a found group is moved to its model from one provider, and a request still naming one goes there too.",
        "No group yet. New group makes one of any models you like.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
