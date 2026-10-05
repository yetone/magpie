// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group says which context window agents are told (Mikan on
// Discord: a custom length, or the shortest or longest member's). The
// group editor's "Context" row picks Largest model's (0, as before),
// Smallest model's (-1, followed as the members change) or Custom, with a
// box that takes 300k or 1m; its hint says the length agents are told.
// In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "OpenAI", icon: "generic", context: 1000000 },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", providerName: "Anthropic", icon: "generic", context: 200000 },
  { id: "ds/deepseek-flash", name: "deepseek-flash", providerName: "DeepSeek", icon: "generic", context: 128000 },
];
const members = models.map((m) => m.id);
const saved = { context: 0 };
const groups = () => ({
  models, pools: [],
  groups: [{ id: "sol", name: "Sol", members, routing: "order", ready: true, context: saved.context,
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", label: "Context", largest: "Largest model's", smallest: "Smallest model's", custom: "Custom", n1m: "1,000,000", n128: "128,000", n300: "300,000", bad: "not a length" },
  zh: { edit: "编辑", save: "保存", label: "上下文", largest: "最大模型的", smallest: "最小模型的", custom: "自定义", n1m: "1,000,000", n128: "128,000", n300: "300,000", bad: "不是有效的" },
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
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push({ path: url.pathname, body });
      saved.context = body.context || 0;
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
    test(`${engine} ${lang}: a group's context is its largest, smallest or a named length`, async (t) => {
      saved.context = 0;
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 2000 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-context.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      const card = page.locator(".rt-group", { hasText: "Sol" });
      await card.waitFor();

      const ed = page.locator(".rt-gedit");
      const row = ed.locator("label", { hasText: new RegExp(`^${w.label}$`) }).locator("xpath=following-sibling::div[1]");
      const hint = () => row.locator(".hint").textContent();
      const save = async () => {
        await ed.locator("button.primary", { hasText: w.save }).click();
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        return posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
      };
      const open = async () => { await card.locator("button", { hasText: w.edit }).click(); await row.waitFor().catch((e) => { throw new Error(errors.join(" / ") || e.message); }); };

      await open();
      assert.equal(await row.locator(".opt.on").textContent(), w.largest);
      assert(await row.locator("input").isHidden(), "no box for the largest");
      assert((await hint()).includes(w.n1m), await hint());
      await row.locator(".opt", { hasText: w.smallest }).click();
      assert((await hint()).includes(w.n128), await hint());
      assert.equal((await save()).context, -1);

      await open();
      assert.equal(await row.locator(".opt.on").textContent(), w.smallest, "the editor opens at what was saved");
      await row.locator(".opt", { hasText: w.custom }).click();
      const box = row.locator("input");
      assert.equal(await box.getAttribute("aria-label"), w.label);
      assert.equal(await box.inputValue(), "128k", "custom starts at the smallest");
      await box.fill("abc");
      assert.equal(await box.getAttribute("aria-invalid"), "true");
      assert((await hint()).includes(w.bad), await hint());
      await box.fill("300k");
      assert.equal(await box.getAttribute("aria-invalid"), "false");
      assert.equal(await box.getAttribute("aria-describedby"), await row.locator(".hint").getAttribute("id"));
      assert((await hint()).includes(w.n300), await hint());
      assert.equal((await save()).context, 300000);

      await open();
      assert.equal(await row.locator(".opt.on").textContent(), w.custom);
      assert.equal(await row.locator("input").inputValue(), "300k");
      await row.locator(".opt", { hasText: w.largest }).click();
      assert.equal((await save()).context, 0);

      const missing = await page.evaluate(() => [
        "Largest model's", "Smallest model's", "Custom", "e.g. 200k",
        "Agents are told the group takes {n} tokens; a longer conversation still goes on to a member with room for it.",
        "Agents are told {n} tokens, its smallest model's, so they compact before any member would turn the conversation away.",
        "Agents are told its smallest model's window, so they compact before any member would turn the conversation away.",
        "Agents are told {n} tokens, its largest model's; a conversation too long for one member goes on to one with room for it.",
        "Agents are told its largest model's window; a conversation too long for one member goes on to one with room for it.",
        "{v} is not a length of tokens like 200k or 1m",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
