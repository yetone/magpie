// Run with Node's test runner and Playwright on the module path; see README.md.
// A group that relays one task across vendors sends each member the level
// the vendor means (LuckymanJC on Discord: one vendor's high is another's
// xhigh). In the group editor a member's reasoning menu has "Map levels…":
// a level asked, then the one sent for it, from the model's own levels or
// "As asked". The chip says the member is mapped and its title the pairs;
// the save carries effortMaps for that member only; a fixed level picked
// later drops the map, and so does "Follow the group". A member fixed at a
// level that gets a map follows the group again. Nothing on the page
// moves. In every language, Chromium and WebKit, at a wide and a narrow
// window.
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
const groups = (members, effortMaps) => ({
  models, pools: [],
  groups: [{ id: "sol", name: "Sol", members, effortMaps, routing: "order", ready: true,
    memberInfo: members.map((id) => ({ id, ready: true, canFast: id.startsWith("oa/") })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", map: "Map levels…", follow: "Follow the group", mapped: "Mapped reasoning", asked: "As asked",
    title: "Sent low → medium, high → xhigh; other levels as asked" },
  zh: { edit: "编辑", save: "保存", map: "映射强度…", follow: "跟随分组", mapped: "映射推理强度", asked: "按要求" },
  "zh-TW": { edit: "編輯", save: "儲存", map: "映射強度…", follow: "跟隨分組", mapped: "映射推理強度", asked: "按要求" },
  ja: { edit: "編集", save: "保存", map: "レベルを対応付け…", follow: "グループに従う", mapped: "推論レベルを対応付け", asked: "要求どおり" },
  de: { edit: "Bearbeiten", save: "Speichern", map: "Stufen zuordnen…", follow: "Der Gruppe folgen", mapped: "Zugeordnetes Reasoning", asked: "Wie angefragt" },
};

function serve(lang, posts, members, maps) {
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
    if (url.pathname === "/api/groups") return json(groups(members, maps));
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups(members, maps));
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, width, members, t, maps) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 1600 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-group-member-effort-map.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts, members, maps));
  await page.goto("http://magpie.test/?view=groups");
  const card = page.locator(".rt-group", { hasText: "Sol" });
  await card.waitFor();
  return { page, card, errors, posts };
}

const keys = ["Map levels…", "a level in place of the one asked", "Mapped reasoning", "Sent {pairs}; other levels as asked", "As asked"];
const option = (page, text) => page.locator("#pop:not([hidden]) #list li:not(.group)", { has: page.locator(".v", { hasText: new RegExp("^" + text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "$") }) });
const shut = (page) => page.waitForFunction(() => document.querySelector("#pop").hidden);

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
      test(`${engine} ${lang} ${width}px: a group's member maps the levels asked`, async (t) => {
        const { page, card, errors, posts } = await open(engine, lang, width, ["oa/gpt-6.1-sol", "rl/glm-5"], t);
        await card.locator("button", { hasText: w.edit }).click();
        const ed = page.locator(".rt-gedit");
        const row = (name) => ed.locator(".fbrow", { has: page.locator(".n", { hasText: name }) });
        const chip = () => row("gpt-6.1-sol").locator("button.rt-fixed:not(.rt-fast)");
        await chip().waitFor();
        await chip().evaluate((x) => x.scrollIntoView({ block: "center" })); // by the test, as the reader would
        await page.waitForTimeout(200);
        const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop && !e.closest("#pop")).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + Math.round(document.querySelector(".rt-gedit").getBoundingClientRect().top));
        const before = await at();

        const pair = async (from, to) => {
          await chip().click();
          await option(page, w.map).click();
          // every level that can be asked
          await option(page, from).waitFor();
          assert.deepEqual(await page.locator("#pop #list li:not(.group) .v").allTextContents(), ["none", "minimal", "low", "medium", "high", "xhigh", "max"]);
          await option(page, from).click();
          // then as asked, or the model's own others
          await option(page, w.asked).waitFor();
          assert.deepEqual(await page.locator("#pop #list li:not(.group) .v").allTextContents(), [w.asked, ...["low", "medium", "high", "xhigh"].filter((v) => v !== from)]);
          await option(page, to).click();
          await shut(page);
        };
        await pair("high", "xhigh");
        await pair("low", "medium");
        await pair("max", "xhigh");
        await pair("max", w.asked); // back to as asked
        assert.equal((await chip().textContent()).trim(), w.mapped);
        assert.match(await chip().getAttribute("class"), /\bon\b/);
        if (lang === "en") assert.equal(await chip().getAttribute("title"), w.title);
        assert((await chip().getAttribute("title")).includes("high → xhigh"));
        assert.equal(await at(), before, "the picks moved nothing");

        // the first menu has "map" as what it is now; the next menu says each pair
        await chip().click();
        await option(page, w.map).waitFor();
        assert.match(await option(page, w.map).getAttribute("class"), /\bcur\b/);
        await option(page, w.map).click();
        await option(page, "high").waitFor();
        assert.equal((await option(page, "high").locator(".n, .note").first().textContent()).trim(), "→ xhigh");
        await page.keyboard.press("Escape");
        await shut(page);

        const fits = await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);
        assert(fits, "no sideways scroll");
        const missing = await page.evaluate((keys) => keys.filter((k) => !I18N.zh[k] || !I18N["zh-TW"][k] || !I18N.ja[k] || !I18N.de[k]), keys);
        assert.deepEqual(missing, [], "every string has each language");

        await save(page, w);
        const sent = posts.find((p) => p.path === "/api/groups/save");
        assert.deepEqual(sent.body.members, ["oa/gpt-6.1-sol", "rl/glm-5"]);
        assert.deepEqual(sent.body.effortMaps, { "oa/gpt-6.1-sol": { high: "xhigh", low: "medium" } });
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang}: a fixed level or the group drops a map, and a map frees a fixed one`, async (t) => {
      const w = words[lang];
      const { page, card, errors, posts } = await open(engine, lang, 1100, ["oa/gpt-6.1-sol", "rl/glm-5:high", "oa/gpt-6.1-sol:low"], t,
        { "oa/gpt-6.1-sol": { high: "xhigh" } });
      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const chips = ed.locator("button.rt-fixed:not(.rt-fast)");
      await chips.first().waitFor();
      assert.equal((await chips.nth(0).textContent()).trim(), w.mapped, "the saved map is read");
      // a fixed level drops the map: back on the group, it isn't there
      await chips.nth(0).click();
      await option(page, w.follow).waitFor();
      await page.locator("#pop:not([hidden]) #list li:not(.group)").nth(3).click(); // high
      await shut(page);
      assert.notEqual((await chips.nth(0).textContent()).trim(), w.mapped);
      await chips.nth(0).click();
      await option(page, w.follow).click();
      await shut(page);
      assert.notEqual((await chips.nth(0).textContent()).trim(), w.mapped);
      // a mapped one back on the group's own
      await chips.nth(0).click();
      await option(page, w.map).click();
      await option(page, "high").click();
      await option(page, "xhigh").click();
      await shut(page);
      assert.equal((await chips.nth(0).textContent()).trim(), w.mapped);
      await chips.nth(0).click();
      await option(page, w.follow).click();
      await shut(page);
      assert.notEqual((await chips.nth(0).textContent()).trim(), w.mapped);
      // a member fixed at high, mapped: low to high, and it follows the group
      await chips.nth(1).click();
      await option(page, w.map).click();
      await option(page, "low").click();
      await option(page, w.asked).waitFor();
      await page.locator("#pop:not([hidden]) #list li:not(.group)").nth(1).click(); // the model's other level, high
      await shut(page);
      assert.equal((await chips.nth(1).textContent()).trim(), w.mapped);
      await save(page, w);
      const sent = posts.find((p) => p.path === "/api/groups/save");
      assert.deepEqual(sent.body.members, ["oa/gpt-6.1-sol", "rl/glm-5", "oa/gpt-6.1-sol:low"]);
      assert.deepEqual(sent.body.effortMaps, { "rl/glm-5": { low: "high" } });
      assert.deepEqual(errors, []);
    });
  }
}
