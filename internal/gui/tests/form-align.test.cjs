// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner (11-12, a second time): "没有水平对齐。这个事情发生过不止一次了，
// 要彻底避免。" Free for me's and Redaction's names sat 4-5px under their
// ticks: every editor label had padding-top: 5px, guessed for a 29px input,
// and a tick, a row of options, a list or the icon box each sat off by its
// own amount. The rows now set name and control on their first baseline.
// For every label beside its control in a provider editor (a key's, a local
// one's with its ticks and time windows, a subscription's with accounts),
// a group editor (new and existing, Advanced open) and Gateway's Connect,
// the middle of the label's first line is within 2px of the middle of the
// control's first line, in every language, both engines, wide and narrow.
// Where a phone's layout puts the label above its control, there is no row.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { formRows } = require("./form-align.cjs");
const { fixture } = require("./fixtures/caller-keys.cjs");

const assets = path.resolve(__dirname, "../assets");
const base = { icon: "generic", chat: "", responses: "", anthropic: "", catalog: "", routing: "", models: [{ id: "m1", name: "m1", on: true }, { id: "m2", name: "m2", on: true }], agents: [], fallback: [], headers: {}, key: { set: false, masked: "" }, keyList: [], proxy: "", balanceToken: { takes: false, set: false }, searches: false, unredacted: false };
const keyed = { ...base, id: "a", name: "Acme", chat: "https://api.acme.example/v1", key: { set: true, masked: "sk-…1234" }, ready: true };
const local = { ...base, id: "local", name: "Local Box", chat: "http://localhost:11434/v1", countFree: true, rateWindows: [{ days: [1], start: "22:00", end: "06:00", rate: "0.5" }], ready: true };
const codex = { ...base, id: "codex", name: "Codex", icon: "codex-color", models: [{ id: "gpt-6", name: "GPT-6", on: true }],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }, { user: "b@example.com", plan: "PLUS", on: true }] } };
const members = ["a/m1", "a/m2"];
const groups = { models: members.map((id) => ({ id, name: id.slice(2), providerName: "Acme", icon: "generic" })), pools: [],
  groups: [{ id: "g", name: "Glim", members, match: ["a/*"], fast: [], off: [], routing: "", ready: true, memberInfo: members.map((id) => ({ id, ready: true })) }] };

function serve(lang, web) {
  const providers = { providers: [keyed, local, codex], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "pi", name: "Pi", path: "/test/pi", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/local-hosts") return json({ local: ["localhost"] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

// every <details> fold of the editor opened, so its rows are measured too
const openFolds = (page, root) => page.evaluate((root) => {
  for (const d of document.querySelectorAll(`${root} details`)) d.open = true;
}, root);
const settle = (page) => page.evaluate(() => new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(ok))));
const far = (rows) => rows.filter((r) => Math.abs(r.off) > 2);

const SIZES = [[false, 1000], [false, 560], [true, 1000], [true, 420]];
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: every form label is level with its control's first line`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      for (const [web, width] of SIZES) {
        const at = `${web ? "web " : ""}${width}px`;
        const ctx = await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(`${at}: ${e.message}`));
        await page.route("**/*", serve(lang, web));
        const wide = width >= 900;

        // the provider editors
        for (const name of ["Acme", "Local Box", "Codex"]) {
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: name }).first().click();
          await page.locator(".editor .bar").first().waitFor();
          await openFolds(page, ".editor");
          await settle(page);
          const rows = await formRows(page);
          if (wide) assert.ok(rows.length >= 8, `${at} ${name}: the editor's rows: ${JSON.stringify(rows)}`);
          assert.deepEqual(far(rows), [], `${at} ${name}: each label is level with its control`);
          // the ticks the owner pointed at are among the rows measured
          if (wide && name === "Local Box") assert.ok(rows.filter((r) => r.what.startsWith("label.tick")).length >= 2, `${at}: the tick rows are measured: ${JSON.stringify(rows)}`);
        }

        // a group's editor, new and existing
        const w = await page.evaluate((lang) => Object.fromEntries(["New group", "Edit"].map((k) => [k, lang === "en" ? k : I18N[lang][k]])), lang);
        for (const existing of [false, true]) {
          await page.goto("http://magpie.test/?view=groups");
          if (existing) await page.locator(".rt-group", { hasText: "Glim" }).locator("button", { hasText: w.Edit }).click();
          else await page.locator("button", { hasText: w["New group"] }).first().click();
          await page.locator(".rt-gedit").waitFor();
          await openFolds(page, ".rt-gedit");
          if (existing) await page.locator(".rt-gedit .rt-pats .fbrow").first().waitFor();
          await settle(page);
          const rows = await formRows(page, ".rt-gedit");
          if (wide) assert.ok(rows.length >= 8, `${at} group: the editor's rows: ${JSON.stringify(rows)}`);
          assert.deepEqual(far(rows), [], `${at} ${existing ? "a" : "a new"} group: each label is level with its control`);
        }
        await ctx.close();

        // Gateway's Connect card
        const cctx = await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" });
        const cpage = await cctx.newPage();
        cpage.setDefaultTimeout(6000);
        cpage.setDefaultNavigationTimeout(60000);
        await cpage.addInitScript(() => { try { localStorage.setItem("magpie.gwConnectFolded", "0"); } catch {} });
        cpage.on("pageerror", (e) => errors.push(`${at} connect: ${e.message}`));
        await cpage.route("**/*", fixture(lang, "light", [], { lan: true }));
        await cpage.goto("http://magpie.test/?view=gateway");
        await cpage.locator("#connect .snip").waitFor();
        await settle(cpage);
        const rows = await formRows(cpage, ".connect");
        if (width >= 560) assert.ok(rows.length >= 2, `${at} connect: its rows: ${JSON.stringify(rows)}`);
        assert.deepEqual(far(rows), [], `${at} connect: each label is level with its value`);
        await cctx.close();
      }
      assert.deepEqual(errors, []);
    });
  }
}
