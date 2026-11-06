// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's member may offer several reasoning levels for the agent to
// pick among (Garydesu on X: "能不能让路由组里的模型设置多个可选推理强度"),
// not only one fixed level or the group's. In the group editor the member's
// reasoning menu has "Several levels…", which opens a menu of the model's
// levels that stays open, each ticked or not. Ticking a second level beside
// a fixed one makes "provider/model:low+high"; the chip says the levels and
// its title that the agent picks; the member's fast setting follows its new
// id; one level left is a fixed effort again, none follows the group. A
// fixed level picked from the first menu still replaces the old one, as
// before. Nothing on the page moves. In every language, Chromium and
// WebKit, at a wide and a narrow window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "OpenAI", icon: "generic", efforts: ["low", "medium", "high", "xhigh"], canFast: true },
  { id: "rl/glm-5", name: "glm-5", providerName: "Relay", icon: "generic", efforts: ["low", "high"] },
];
const groups = (members) => ({
  models, pools: [],
  groups: [{ id: "sol", name: "Sol", members, fast: ["oa/gpt-6.1-sol:high"], routing: "order", ready: true,
    memberInfo: members.map((id) => ({ id, ready: true, fast: id === "oa/gpt-6.1-sol:high", canFast: id.startsWith("oa/") })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", several: "Several levels…", follow: "Follow the group", one: "high reasoning", two: "low / high reasoning", three: "low / high / xhigh reasoning", ends: "low / xhigh reasoning", fixed: "medium reasoning",
    title: "The agent picks among low / high reasoning, each sent at the model's nearest level" },
  zh: { edit: "编辑", save: "保存", several: "多个强度…", follow: "跟随分组" },
  "zh-TW": { edit: "編輯", save: "儲存", several: "多個強度…", follow: "跟隨分組" },
  ja: { edit: "編集", save: "保存", several: "複数のレベル…", follow: "グループに従う" },
  de: { edit: "Bearbeiten", save: "Speichern", several: "Mehrere Stufen…", follow: "Der Gruppe folgen" },
};

function serve(lang, posts, members) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
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
    if (url.pathname === "/api/groups") return json(groups(members));
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups(members));
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, width, members, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 1600 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-group-member-levels.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts, members));
  await page.goto("http://magpie.test/?view=groups");
  const card = page.locator(".rt-group", { hasText: "Sol" });
  await card.waitFor();
  return { page, card, errors, posts };
}

const keys = ["Several levels…", "the agent picks among them", "The agent picks among {levels} reasoning, each sent at the model's nearest level"];
const option = (page, text) => page.locator("#pop:not([hidden]) #list li:not(.group)", { has: page.locator(".v", { hasText: new RegExp("^" + text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "$") }) });

async function save(page, w) {
  const b = page.locator(".rt-gedit button.primary", { hasText: w.save });
  await b.scrollIntoViewIfNeeded();
  await b.click();
  await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a group's member offers several levels`, async (t) => {
        const { page, card, errors, posts } = await open(engine, lang, width, ["oa/gpt-6.1-sol:high", "rl/glm-5"], t);
        await card.locator("button", { hasText: w.edit }).click();
        const ed = page.locator(".rt-gedit");
        const row = (name) => ed.locator(".fbrow", { has: page.locator(".n", { hasText: name }) });
        const chip = () => row("gpt-6.1-sol").locator("button.rt-fixed:not(.rt-fast)");
        await chip().waitFor();
        const said = async () => (await chip().textContent()).trim();
        const one = await said();
        assert(one.includes("high"), `the chip says the fixed level: ${one}`);

        await chip().evaluate((x) => x.scrollIntoView({ block: "center" })); // by the test, as the reader would
        await page.waitForTimeout(200);
        const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop && !e.closest("#pop")).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + Math.round(document.querySelector(".rt-gedit").getBoundingClientRect().top));
        const before = await at();

        // the first menu: follow the group, each level, and several
        await chip().click();
        await option(page, w.several).waitFor();
        assert.equal(await option(page, w.follow).count(), 1);
        await option(page, w.several).click();
        // the levels, the fixed one ticked; the menu stays open as each is picked
        await option(page, "low").waitFor();
        assert.deepEqual(await page.locator("#pop #list li:not(.group) .v").allTextContents(), ["low", "medium", "high", "xhigh"]);
        assert.match(await option(page, "high").getAttribute("class"), /\bcur\b/);
        assert.doesNotMatch(await option(page, "low").getAttribute("class"), /\bcur\b/);
        await option(page, "low").click();
        await page.waitForFunction(() => !document.querySelector("#pop").hidden);
        assert.match(await option(page, "low").getAttribute("class"), /\bcur\b/);
        assert.match(await option(page, "high").getAttribute("class"), /\bcur\b/);
        if (lang === "en") assert.equal(await said(), w.two);
        assert((await said()).includes("low / high"), `the chip says both levels: ${await said()}`);
        if (lang === "en") assert.equal(await chip().getAttribute("title"), w.title);
        await option(page, "xhigh").click();
        if (lang === "en") assert.equal(await said(), w.three);
        await option(page, "high").click();
        if (lang === "en") assert.equal(await said(), w.ends);
        assert((await said()).includes("low / xhigh"), `the chip says both levels: ${await said()}`);
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => document.querySelector("#pop").hidden);
        assert.equal(await at(), before, "the picks moved nothing");

        // the first menu again has "several" as what it is now
        await chip().click();
        await option(page, w.several).waitFor();
        assert.match(await option(page, w.several).getAttribute("class"), /\bcur\b/);
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => document.querySelector("#pop").hidden);

        // the chip fits the row at a narrow window, the page doesn't scroll sideways
        const fits = await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);
        assert(fits, "no sideways scroll");

        const missing = await page.evaluate((keys) => keys.filter((k) => !I18N.zh[k] || !I18N["zh-TW"][k] || !I18N.ja[k] || !I18N.de[k]), keys);
        assert.deepEqual(missing, [], "every string has each language");

        await save(page, w);
        const sent = posts.find((p) => p.path === "/api/groups/save");
        assert.deepEqual(sent.body.members, ["oa/gpt-6.1-sol:low+xhigh", "rl/glm-5"]);
        assert.deepEqual(sent.body.fast, ["oa/gpt-6.1-sol:low+xhigh"], "fast follows the member");
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang}: several levels back to one, and one picked as before`, async (t) => {
      const w = words[lang];
      const { page, card, errors, posts } = await open(engine, lang, 1100, ["oa/gpt-6.1-sol:low+high", "rl/glm-5:high"], t);
      // the card says both levels
      assert((await card.locator(".mem").first().textContent()).includes("low / high"));
      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const row = (name) => ed.locator(".fbrow", { has: page.locator(".n", { hasText: name }) });
      const chip = (name) => row(name).locator("button.rt-fixed:not(.rt-fast)");
      await chip("gpt-6.1-sol").waitFor();
      // one left is a fixed effort again
      await chip("gpt-6.1-sol").click();
      await option(page, w.several).click();
      await option(page, "low").click();
      await page.keyboard.press("Escape");
      assert((await chip("gpt-6.1-sol").textContent()).includes("high"));
      assert(!(await chip("gpt-6.1-sol").textContent()).includes("low"));
      // a fixed level picked from the first menu replaces the old one
      await chip("glm-5").click();
      await option(page, w.several).waitFor();
      await page.locator("#pop:not([hidden]) #list li:not(.group)").nth(1).click(); // low, the model's first
      await save(page, w);
      const sent = posts.find((p) => p.path === "/api/groups/save");
      assert.deepEqual(sent.body.members, ["oa/gpt-6.1-sol:high", "rl/glm-5:low"]);
      assert.deepEqual(errors, []);
    });
  }
}
