// Run with Node's test runner and Playwright on the module path; see README.md.
// #868 (模型名称太长了…路由组的名称也很长。会看不到后面代表什么): an agent's
// narrow model menu cut "Long name · routing group" short, and the setting
// that drops the suffix (Settings › Provider in model names, #335) wasn't
// found. Model settings says what the agents' lists will show ("My Sol ·
// Relay") and where the setting is. Its three choices were in there too, and
// read as this provider's own (joe on Discord), so the editor now says it is
// one setting for every provider and opens Settings at it. For the routing groups it is one setting for them
// all, so it sits over the groups on Routing, not in one group's editor (PAMI
// on Discord: in there it read as that group's alone); the editor only says
// what its group will be called ("Fast · routing group") and where that is
// set. A pick posts settings/plain-names once, an open editor's line follows
// it (its unsaved name kept), and the click doesn't scroll the page. English
// and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { names: "Model settings", edit: "Edit", general: "General", global: "“Provider in model names” is one setting for every provider", change: "Change in Settings", own: "Not on names I set", on: "On", off: "Off", label: "In agents’ lists",
    shows: (l) => `Agents’ lists show “${l}”`, above: " · “· routing group” is set for every group, under the list", setting: "Names in agents’ lists" },
  zh: { names: "模型设置", edit: "编辑", general: "常规", global: "「模型名带供应商」是全局设置，对所有供应商生效", change: "在设置中修改", own: "自定义名称不带供应商", on: "开启", off: "关闭", label: "Agent 列表里的名称",
    shows: (l) => `Agent 的模型列表里显示为「${l}」`, above: " · 是否带「· routing group」在列表下方统一设置，对所有路由组生效", setting: "Agent 列表里的名称" },
};

function serve(lang, posts) {
  let cur = { lang, theme: "light", plainNames: false, plainOwnNames: false };
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const models = [
    { id: "sol-2026-preview-long", name: "My Sol", default: "sol-2026-preview-long", on: true, efforts: [], images: false },
  ];
  const provider = { id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example/v1", responses: "", anthropic: "", models, agents: [], key: { set: true, masked: "sk-…1234" }, ready: true };
  const groups = { models: [{ id: "relay/sol-2026-preview-long", name: "My Sol", providerName: "Relay", icon: "generic" }], pools: [],
    groups: [{ id: "fast", name: "Fast", members: ["relay/sol-2026-preview-long"], routing: "order", ready: true, memberInfo: [{ id: "relay/sol-2026-preview-long", ready: true }] }] };
  return async (r) => {
    const req = r.request(), url = new URL(req.url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings/plain-names") {
      const body = req.postDataJSON();
      posts.push(body);
      cur = { ...cur, plainNames: body.mode === "off", plainOwnNames: body.mode === "own" };
      return json(cur);
    }
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/providers") return json({ providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function launch(t, engine, lang, posts) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
  const page = await (await browser.newContext({ viewport: { width: 900, height: 1200 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${t.name.includes("group") ? "group" : "names"}-suffix.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts));
  return { page, errors };
}

// a pick shows as picked at once, and the line under it follows when the
// setting is saved: wait for that, then say what it is
async function says(loc, want) {
  for (let i = 0; i < 100 && (await loc.textContent()) !== want; i++) await new Promise((r) => setTimeout(r, 20));
  assert.equal(await loc.textContent(), want);
}
const scrolls = (page) => page.evaluate(() => [scrollY, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)]);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Model settings says what agents' lists show and opens the setting on Settings`, async (t) => {
      const posts = [];
      const { page, errors } = await launch(t, engine, lang, posts);
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider").click();
      if (!(await page.locator(".mnames:not([hidden])").count())) await page.getByRole("button", { name: w.names, exact: true }).click();
      const sfx = page.locator(".mnames:not([hidden]) .msuffix");
      await sfx.waitFor();
      // no choices in one provider's editor: it says the setting is every
      // provider's, and where it is
      assert.equal(await sfx.locator(".segs").count(), 0, "the provider's editor has the setting for every provider");
      assert.equal(await sfx.locator(".hint").textContent(), w.shows("My Sol · Relay"));
      assert.equal(await sfx.locator(".where").textContent(), w.global);
      assert.equal(await sfx.evaluate((e) => getComputedStyle(e).borderLeftWidth), "0px");
      assert.equal(await page.locator("#view-providers select").count(), 0);
      // the button opens Settings › General at the setting, lit
      await sfx.getByRole("button", { name: w.change, exact: true }).click();
      const row = page.locator("#view-settings .row.pref", { has: page.locator("#plainNamesSegs") });
      await row.waitFor();
      assert.ok(await row.isVisible(), "the setting is shown");
      assert.equal((await page.locator("#view-settings [role=tab][data-set][aria-selected=true]").textContent()).trim(), w.general);
      await page.locator("#view-settings .row.pref.flash #plainNamesSegs").waitFor({ state: "attached" });
      const box = await row.boundingBox(), vh = page.viewportSize().height;
      assert.ok(box.y >= 0 && box.y + box.height <= vh, `the setting is in view (${box.y})`);
      // picked there, it posts once, and the editor's line follows it
      await row.locator(".opt", { hasText: w.own }).click();
      await page.locator("#plainNamesSegs .opt.on", { hasText: w.own }).waitFor();
      assert.deepEqual(posts, [{ mode: "own" }]);
      // back on Providers, its editor says it
      await page.locator("button[data-view=providers]").first().click();
      await page.locator("#view-providers.on, #view-providers:not([hidden])").first().waitFor();
      await page.locator("#view-providers .row.provider:visible, .mnames:not([hidden])").first().waitFor();
      if (!(await page.locator(".mnames:not([hidden])").count())) {
        if (await page.locator(".row.provider").isVisible()) await page.locator(".row.provider").click();
        if (!(await page.locator(".mnames:not([hidden])").count())) await page.getByRole("button", { name: w.names, exact: true }).click();
      }
      await says(page.locator(".mnames:not([hidden]) .msuffix .hint"), w.shows("My Sol"));
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: "· routing group" is set over the groups, for all of them, and a group's editor says what it shows`, async (t) => {
      const posts = [];
      const { page, errors } = await launch(t, engine, lang, posts);
      await page.goto("http://magpie.test/?view=groups");
      const card = page.locator(".rt-group", { hasText: "Fast" });
      await card.waitFor();
      // the setting is under the groups, in the page's Settings (11-06)
      const set = page.locator(".rt-gnames");
      await set.waitFor();
      assert.equal((await set.locator("b").textContent()).trim(), w.setting);
      assert.deepEqual((await set.locator(".segs .opt").allTextContents()).map((s) => s.trim()), [w.off, w.own, w.on]);
      assert.equal((await set.locator(".opt.on").textContent()).trim(), w.on);
      assert.ok(await set.evaluate((s) => !!(s.compareDocumentPosition(document.querySelector(".rt-group")) & Node.DOCUMENT_POSITION_PRECEDING)), "the setting is under the groups");
      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const row = ed.locator("label", { hasText: new RegExp(`^${w.label}$`) }).locator("xpath=following-sibling::div[1]");
      await row.waitFor();
      // not in here: one group's editor has no choices of its own
      assert.equal(await row.locator(".segs").count(), 0, "the group's editor has the setting for every group");
      assert.equal(await row.locator(".hint").textContent(), w.shows("Fast · routing group") + w.above);
      // the name typed is the name shown
      await ed.locator("input").first().fill("Fast lane");
      assert.equal(await row.locator(".hint").textContent(), w.shows("Fast lane · routing group") + w.above);
      // the setting, under the open editor, brought into sight by the wheel
      // first, as a reader would: what is measured is the click alone
      const vp = page.viewportSize();
      await page.mouse.move(vp.width / 2, vp.height / 2);
      for (let i = 0; i < 40; i++) {
        const r = await set.boundingBox();
        if (r.y >= 0 && r.y + r.height <= vp.height) break;
        await page.mouse.wheel(0, r.y + r.height - vp.height + 40);
        await page.waitForTimeout(60);
      }
      // and come to rest: WebKitGTK's wheel at the page's foot runs on and
      // springs back for a while after it, click or no click
      for (let i = 0, last = ""; i < 50; i++) {
        const now = JSON.stringify(await scrolls(page));
        if (now === last) break;
        last = now;
        await page.waitForTimeout(100);
      }
      const before = await scrolls(page);
      await set.locator(".opt", { hasText: w.own }).click();
      await page.locator(".rt-gnames .opt.on", { hasText: w.own }).waitFor();
      // the open editor follows it, its unsaved name kept
      const row2 = page.locator(".rt-gedit label", { hasText: new RegExp(`^${w.label}$`) }).locator("xpath=following-sibling::div[1]");
      await says(row2.locator(".hint"), w.shows("Fast lane") + w.above);
      assert.deepEqual(posts, [{ mode: "own" }]);
      assert.deepEqual(await scrolls(page), before, "the click scrolled the page");
      // nothing native, no stripe down the side
      assert.equal(await page.locator("#view-groups select").count(), 0);
      assert.equal(await set.evaluate((e) => getComputedStyle(e).borderLeftWidth), "0px");
      assert.equal(await row2.evaluate((e) => getComputedStyle(e).borderLeftWidth), "0px");
      const missing = await page.evaluate(() => ["Agents’ lists show “{label}”", "In agents’ lists",
        "“Provider in model names” is one setting for every provider", "Change in Settings", "Not on names I set", "Provider in model names",
        "Names in agents’ lists", "“· routing group” is set for every group, under the list",
        "Whether “· routing group” follows a group’s name in the agents’ lists: one setting for every group, and for the provider after each model’s name, as on Settings."]
        .filter((k) => !I18N.zh[k] || !I18N.ja[k] || !I18N.de[k]));
      assert.deepEqual(missing, [], "every string has its Chinese, Japanese and German");
      assert.deepEqual(errors, []);
    });
  }
}
