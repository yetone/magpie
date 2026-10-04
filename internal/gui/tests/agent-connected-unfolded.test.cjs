// Run with Node's test runner and Playwright on the module path; see README.md.
// #726 (EZN7L2C3): the Agents page put a connected Codex, starting on its
// last choice with no model set, under 未设置 (Not set up), while
// Antigravity and DeepSeek Harness, not connected, stayed up top for a model
// of their own read from their files. An agent with 「接入」's switch is set
// up when it is connected and only then: connected ones are in view, those
// not connected fold under Not set up whatever their own files say; one
// with no switch (Cursor) still goes by what is set on it. While none is
// connected nothing folds: Cursor on its own auto doesn't fold the rest
// away on a fresh magpie. Chromium and WebKit, English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const ref = (v) => ({ value: v, label: v, ref: v });
const own = (v) => ({ value: v, label: v });
// an agent 「接入」 can connect: its model picker lists magpie's models
const agent = (id, name, { wired = false, value = "" } = {}) => ({
  id, name, path: "/test/" + id, wired,
  fields: [{ key: "model", label: "model", value, options: [ref("sub/gpt-6.1-sol"), ref("sub/opus-5.5")] }],
});
const AGENTS = [
  agent("claude", "Claude Code", { wired: true, value: "sub/gpt-6.1-sol" }),
  agent("agy", "Antigravity CLI", { value: "gemini-3.5-pro" }), // its own model, not connected
  agent("dsh", "DeepSeek Harness", { value: "deepseek-v4" }), // likewise
  agent("opencode", "OpenCode", { wired: true, value: "sub/opus-5.5" }),
  agent("codex", "Codex", { wired: true, value: "" }), // connected, on its last choice
  agent("kimi", "Kimi Code"), // nothing at all
  // no switch: what is set on it decides, as before
  { id: "cursor", name: "Cursor", path: "/test/cursor", fields: [{ key: "model", label: "model", value: "auto", options: [own("auto")] }] },
];

function serve(lang, agents = AGENTS) {
  const state = { agents, profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: state });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true, window: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const cap = { en: "Not set up", zh: "未设置" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": connected agents are in view, the rest under Not set up", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const page = await (await browser.newContext({ viewport: { width: 1240, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/");
        await page.locator('.row.agent[data-id="claude"]').waitFor();
        const rows = () => page.evaluate(() => {
          const ids = (sel) => [...document.querySelectorAll(sel)].map((r) => r.dataset.id);
          return {
            top: ids("#agents .row.agent[data-id]").filter((id) => !document.querySelector(`.agent-fold .row.agent[data-id="${id}"]`)),
            fold: ids(".agent-fold .row.agent[data-id]"),
            caps: [...document.querySelectorAll(".agent-fold-cap")].map((c) => c.textContent.trim()),
          };
        });
        const r = await rows();
        assert.deepEqual(r.top, ["claude", "opencode", "codex", "cursor"], "the connected ones, and Cursor for its own model");
        assert.deepEqual(r.fold, ["agy", "dsh", "kimi"], "the ones not connected fold away");
        assert.deepEqual(r.caps, [cap[lang]]);
        // every row in view is connected or has no switch; every folded one's switch is off
        const sw = await page.evaluate(() => [...document.querySelectorAll("#agents .row.agent[data-id]")].map((r) => [r.dataset.id, !!r.closest(".agent-fold"), r.querySelector(".ag-conn")?.getAttribute("aria-checked") ?? null]));
        for (const [id, folded, on] of sw) {
          if (on === null) continue;
          assert.equal(on === "true", !folded, `${id}: switch ${on} ${folded ? "folded" : "in view"}`);
        }
        assert.deepEqual(errors, []);
        await page.close();
      });
    }
  });

  test(engine + ": with none connected every agent is in view", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1240, height: 900 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve("en", AGENTS.map((a) => ({ ...a, wired: false }))));
    await page.goto("http://magpie.test/");
    await page.locator('.row.agent[data-id="claude"]').waitFor();
    const r = await page.evaluate(() => ({
      rows: [...document.querySelectorAll("#agents .row.agent[data-id]")].map((x) => x.dataset.id),
      fold: document.querySelectorAll(".agent-fold").length,
    }));
    assert.deepEqual(r.rows, AGENTS.map((a) => a.id));
    assert.equal(r.fold, 0, "nothing folded");
    assert.deepEqual(errors, []);
  });
}
