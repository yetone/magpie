// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's models may be found by patterns (#766). The card says each
// pattern with how many models it matches, and one matching nothing says so.
// In the editor the matched models follow those named, marked "by pattern",
// with an off switch and no Remove; a pattern is added and removed in its
// own box, its models coming and going with it. Saved, the group sends the
// models it names and its patterns, never what they matched. In English and
// Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "or/a:free", name: "a:free", providerName: "OpenRouter", icon: "generic" },
  { id: "or/b", name: "b", providerName: "OpenRouter", icon: "generic" },
  { id: "or/c:free", name: "c:free", providerName: "OpenRouter", icon: "generic" },
  { id: "zen/d-free", name: "d-free", providerName: "Zen", icon: "generic" },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", providerName: "Anthropic", icon: "generic" },
];
const members = ["an/claude-opus-5-5", "or/a:free", "or/c:free"];
const groups = () => ({
  models, pools: [],
  groups: [{ id: "free", name: "Free", members, match: ["or/*:free", "nope/*"], matched: ["or/a:free", "or/c:free"], fast: [], off: [], routing: "order", ready: true,
    patterns: [{ pattern: "or/*:free", models: 2 }, { pattern: "nope/*", models: 0 }],
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", add: "Add pattern", remove: "Remove", two: "or/*:free · 2 models", none: "nope/* · matches nothing now", by: "by pattern" },
  zh: { edit: "编辑", save: "保存", add: "添加模式", remove: "移除", two: "or/*:free · 2 个模型", none: "nope/* · 目前没有匹配的模型", by: "按模式匹配" },
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
    test(`${engine} ${lang}: a group finds models by patterns`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      const card = page.locator(".rt-group", { hasText: "Free" });
      await card.waitFor();
      const pats = (await card.locator(".mem.pat").allTextContents()).map((x) => x.trim());
      assert.deepEqual(pats, [w.two, w.none], "each pattern with its count");
      assert.equal(await card.locator(".mem.pat.none").count(), 1, "the one matching nothing stands out");

      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const names = () => ed.locator(".fallback:not(.rt-pats) .fbrow .n > span:first-child").allTextContents();
      const row = (name) => ed.locator(".fallback:not(.rt-pats) .fbrow", { has: page.locator(".n > span:first-child", { hasText: new RegExp("^" + name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "$") }) });
      await row("a:free").waitFor();
      assert.deepEqual(await names(), ["claude-opus-5-5", "a:free", "c:free"]);
      assert((await row("a:free").textContent()).includes(w.by), "a matched model says so");
      assert.equal(await row("a:free").locator("button", { hasText: w.remove }).count(), 0, "a matched model has no Remove");
      assert.equal(await row("a:free").locator("button.rt-mon").count(), 1, "but can be switched off");
      assert.equal(await row("claude-opus-5-5").locator("button", { hasText: w.remove }).count(), 1);

      const pbox = ed.locator(".rt-pats");
      assert.equal(await pbox.locator(".fbrow").count(), 2);
      // a pattern added brings its models, after those already in
      await pbox.locator("input").fill("*-free");
      await pbox.locator("button", { hasText: w.add }).click();
      assert.deepEqual(await names(), ["claude-opus-5-5", "a:free", "c:free", "d-free"]);
      // one that is no pattern is refused, the box keeping it
      await pbox.locator("input").fill("or/b");
      await pbox.locator("button", { hasText: w.add }).click();
      assert.equal(await pbox.locator(".fbrow").count(), 3);
      assert.equal(await pbox.locator("input").inputValue(), "or/b");
      await pbox.locator("input").fill("");
      // a pattern removed takes its models with it
      await pbox.locator(".fbrow", { hasText: "or/*:free" }).locator("button", { hasText: w.remove }).click();
      assert.deepEqual(await names(), ["claude-opus-5-5", "d-free"]);
      await row("d-free").locator("button.rt-mon").click();

      const b = ed.locator("button.primary", { hasText: w.save });
      await b.scrollIntoViewIfNeeded();
      await b.click();
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const sent = posts.find((p) => p.path === "/api/groups/save");
      assert.deepEqual(sent.body.members, ["an/claude-opus-5-5"], "only the models named are sent");
      assert.deepEqual(sent.body.match, ["nope/*", "*-free"]);
      assert.deepEqual(sent.body.off, ["zen/d-free"], "a matched model may be off");

      const missing = await page.evaluate(() => [
        "Patterns", "Add pattern", "by pattern", "matches nothing now", "e.g. openrouter/*:free or re:…",
        "Every model this pattern matches is in the group, as providers list them",
        "No model magpie serves matches this pattern now",
        "In the group by a pattern: switch it off to send it nothing",
        "A pattern has a * in it, or starts with re:",
        "{pattern} is not a regular expression magpie can read",
        "Every model a pattern matches is in the group, now and as providers list new ones, after the models above: * is any run of characters in provider/model, re: starts a regular expression.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
