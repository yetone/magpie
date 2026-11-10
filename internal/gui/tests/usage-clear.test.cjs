// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page clears its figures (CrazyMouse_TT on X: a local model
// deployed again, benchmarked from nothing). A model's "…" (or a
// right-click on its row) offers "Clear usage…", and "Clear all usage…"
// sits under the lists. Each asks first, in an alert that says what goes,
// that key limits and allowances still count it, and that it can't be
// undone; Cancel sends nothing, Clear POSTs /api/usage/clear and the page
// is read again. Every language, a wide window, a narrow one and magpie
// web on a phone; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const G = (id, name, tokens, extra) => ({ id, name, icon: "generic", calls: 10, input: tokens - 100, output: 100, cache_read: 0, cache_write: 0, cost: 0, ...extra });
const QWEN = G("ollama/qwen3:32b", "qwen3:32b", 900000, { provider: "ollama", model: "qwen3:32b", sub: "Ollama" });
const LLAMA = G("ollama/llama4", "llama4", 300000, { provider: "ollama", model: "llama4", sub: "Ollama" });
const usageOf = (models) => {
  const total = models.reduce((n, g) => n + g.input + g.output, 0);
  return {
    calls: 10 * models.length, errors: 0, input: total - 100 * models.length, output: 100 * models.length, reasoning: 0, cache_read: 0, cache_write: 0, unpriced: 0, cost: 0, bucket: "day",
    series: [], path: "~/.config/magpie/usage.jsonl",
    agents: models.length ? [G("pi", "Pi", total)] : [],
    models,
  };
};

function server(lang, web, saved, posts) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    const settings = () => ({ lang, theme: "light" });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], clients: [{ id: "pi", name: "Pi", icon: "pi" }], profiles: [], settings: settings(), fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/clear") {
      const body = JSON.parse(req.postData() || "{}");
      posts.push(body);
      saved.models = body.model ? saved.models.filter((g) => g.model !== body.model) : [];
      return json({ ok: true });
    }
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json(usageOf(saved.models));
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const L = {
  en: { item: "Clear usage…", all: "Clear all usage…", title: "Clear the usage of qwen3:32b?", allTitle: "Clear all usage?", go: "Clear", cancel: "Cancel", undo: "can't be undone", keys: "spending limits" },
  zh: { item: "清除用量…", all: "清除全部用量…", title: "清除 qwen3:32b 的用量？", allTitle: "清除全部用量？", go: "清空", cancel: "取消", undo: "无法撤销", keys: "消费上限" },
  "zh-TW": { item: "清除用量…", all: "清除全部用量…", title: "清除 qwen3:32b 的用量？", allTitle: "清除全部用量？", go: "清空", cancel: "取消", undo: "無法復原", keys: "消費上限" },
  ja: { item: "使用量を消去…", all: "すべての使用量を消去…", title: "qwen3:32b の使用量を消去しますか？", allTitle: "すべての使用量を消去しますか？", go: "クリア", cancel: "キャンセル", undo: "取り消せません", keys: "利用上限" },
  de: { item: "Nutzung löschen…", all: "Gesamte Nutzung löschen…", title: "Nutzung von qwen3:32b löschen?", allTitle: "Gesamte Nutzung löschen?", go: "Leeren", cancel: "Abbrechen", undo: "nicht rückgängig", keys: "Ausgabenlimits" },
};
const SIZES = [{ width: 1200, web: false }, { width: 560, web: false }, { width: 390, web: true }];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Usage page clears a model's usage, and all of it, after asking", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      for (const { width, web } of SIZES) {
        const w = L[lang];
        await t.test(`${lang} ${width}${web ? " web" : ""}`, async () => {
          const errors = [];
          const saved = { models: [QWEN, LLAMA] }, posts = [];
          const ctx = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce", hasTouch: web, isMobile: web && engine === "chromium" });
          const p = await ctx.newPage();
          p.setDefaultTimeout(5000);
          p.setDefaultNavigationTimeout(60000);
          p.on("pageerror", (e) => errors.push(e.message));
          await p.route("**/*", server(lang, web, saved, posts));
          await p.goto("http://magpie.test/");
          const nav = p.locator('[data-view="usage"]:visible').first();
          if (await nav.count()) await nav.click();
          else await p.evaluate(() => show("usage"));
          await p.locator("#usageModels .row.stat").first().waitFor();
          const ids = () => p.locator("#usageModels .row.stat").evaluateAll((rs) => rs.map((r) => r.dataset.id));
          assert.deepEqual(await ids(), [QWEN.id, LLAMA.id]);
          assert.equal(await p.locator("select").count(), 0, "no native select");

          const row = p.locator(`#usageModels .row.stat[data-id="${QWEN.id}"]`);
          await reader.inView(p, row);
          if (!web) await row.hover();
          const more = row.locator(".us-more");
          await more.click();
          const item = p.locator(".row-menu .rm-item");
          // Set price… above it (usage-model-price.test.cjs)
          assert.deepEqual((await item.allTextContents()).map((s) => s.trim()).slice(1), [w.item]);
          await item.nth(1).click();
          const dialog = p.locator("dialog.action-confirm");
          await dialog.waitFor();
          assert.equal(await dialog.getAttribute("role"), "alertdialog");
          assert.equal((await dialog.locator("h2").textContent()).trim(), w.title);
          const said = await dialog.locator("p").textContent();
          assert(said.includes(w.undo) && said.includes(w.keys) && said.includes("qwen3:32b"), "the alert says what goes and that it stays: " + said);
          // Cancel sends nothing
          await dialog.locator("button", { hasText: w.cancel }).click();
          await dialog.waitFor({ state: "detached" });
          assert.deepEqual(posts, []);
          assert.deepEqual(await ids(), [QWEN.id, LLAMA.id]);

          // a right-click opens the same menu; Clear sends the model
          if (!web) await row.click({ button: "right" });
          else await more.click();
          await p.locator(".row-menu .rm-item", { hasText: w.item }).click();
          await dialog.waitFor();
          await dialog.locator("button.danger-fill").click();
          await p.waitForFunction((id) => !document.querySelector(`#usageModels .row.stat[data-id="${CSS.escape(id)}"]`), QWEN.id);
          assert.deepEqual(posts, [{ provider: "ollama", model: "qwen3:32b" }]);
          assert.deepEqual(await ids(), [LLAMA.id]);

          // Clear all usage…, under the lists
          const all = p.locator("#usageClear");
          await reader.inView(p, all);
          assert.equal((await all.textContent()).trim(), w.all);
          await all.click();
          await dialog.waitFor();
          assert.equal((await dialog.locator("h2").textContent()).trim(), w.allTitle);
          assert.equal((await dialog.locator("button.danger-fill").textContent()).trim(), w.go);
          await dialog.locator("button.danger-fill").click();
          await p.waitForFunction(() => document.querySelector("#stats")?.classList.contains("empty"));
          assert.deepEqual(posts.at(-1), {});
          assert.equal(await all.isHidden(), true, "nothing left to clear: the button goes");

          const wide = await p.evaluate(() => { const v = document.querySelector("#view-usage"); return [v.scrollWidth, v.clientWidth, document.documentElement.scrollWidth, window.innerWidth]; });
          assert(wide[0] <= wide[1] + 1 && wide[2] <= wide[3] + 1, "no sideways scroll: " + wide);
          assert.deepEqual(errors, []);
          await ctx.close();
        });
      }
    }
  });
}
