// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's member may be switched off (#feedback on Discord: testing
// routing rules meant removing models and adding them back, losing their
// order). The card says "(off)" after it; in the editor each member has a
// switch, on unless the group has it in off. Clicking it moves nothing on
// the page and keeps the member where it is; saved, the members off go as
// off, one whose reasoning is changed stays off under its new id, and the
// card's pick keeps them. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "OpenAI", icon: "generic", efforts: ["low", "medium", "high", "xhigh"], canFast: true },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", providerName: "Anthropic", icon: "generic", canFast: true },
  { id: "rl/glm-5", name: "glm-5", providerName: "Relay", icon: "generic" },
];
const members = ["oa/gpt-6.1-sol:high", "an/claude-opus-5-5", "rl/glm-5"];
const groups = () => ({
  models, pools: [],
  groups: [{ id: "sol", name: "Sol", members, fast: [], off: ["rl/glm-5"], routing: "order", ready: true,
    memberInfo: members.map((id) => ({ id, ready: true, fast: id === "oa/gpt-6.1-sol:high", canFast: !id.startsWith("rl/") })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", off: "glm-5 (off)", up: "Up" },
  zh: { edit: "编辑", save: "保存", off: "glm-5 (关闭)", up: "上移" },
};

function serve(lang, posts) {
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
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups());
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
    test(`${engine} ${lang}: a group's member is switched off`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const card = page.locator(".rt-group", { hasText: "Sol" });
      await card.waitFor();
      assert((await card.locator(".mem").textContent()).includes(w.off), "the card says the member is off");

      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const row = (name) => ed.locator(".fbrow", { has: page.locator(".n", { hasText: name }) });
      const sw = (name) => row(name).locator("button.rt-mon");
      await sw("glm-5").waitFor();
      assert.equal(await sw("glm-5").getAttribute("aria-checked"), "false");
      assert.equal(await sw("claude-opus-5-5").getAttribute("aria-checked"), "true");
      assert.equal(await row("glm-5").evaluate((x) => x.classList.contains("muted")), true);

      await row("claude-opus-5-5").evaluate((x) => x.scrollIntoView({ block: "center" })); // by the test, as the reader would
      await page.waitForTimeout(200);
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + Math.round(document.querySelector(".rt-gedit").getBoundingClientRect().top));
      const before = await at();
      await sw("glm-5").click();
      assert.equal(await sw("glm-5").getAttribute("aria-checked"), "true");
      await sw("gpt-6.1-sol").click();
      assert.equal(await sw("gpt-6.1-sol").getAttribute("aria-checked"), "false");
      assert.equal(await at(), before, "the clicks moved nothing");
      const order = await ed.locator(".fbrow .n > span:first-child").allTextContents();
      assert.deepEqual(order, ["gpt-6.1-sol", "claude-opus-5-5", "glm-5"], "every member kept its place");

      // its reasoning changed: still off, under its new id
      await row("gpt-6.1-sol").locator("button.rt-fixed:not(.rt-fast)").click();
      await page.locator(".pop .opt, .pop [role=option], .pop li", { hasText: /^xhigh/ }).first().click();
      assert.equal(await sw("gpt-6.1-sol").getAttribute("aria-checked"), "false", "off follows the reasoning");

      const b = ed.locator("button.primary", { hasText: w.save });
      await page.mouse.move(550, 600);
      for (let i = 0; i < 10 && (await b.boundingBox()).y > 1400 - 160; i++) {
        await page.mouse.wheel(0, 300);
        await page.waitForTimeout(150);
      }
      const bb = await b.boundingBox();
      await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const sent = posts.find((p) => p.path === "/api/groups/save");
      assert.deepEqual(sent.body.members, ["oa/gpt-6.1-sol:xhigh", "an/claude-opus-5-5", "rl/glm-5"]);
      assert.deepEqual(sent.body.off, ["oa/gpt-6.1-sol:xhigh"]);

      const missing = await page.evaluate(() => [
        "off", "Off: kept in its place, sent nothing. Click to switch it on",
        "On: requests may go to it. Click to switch it off and keep its place",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
