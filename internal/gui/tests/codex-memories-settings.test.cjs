// Run with Node's test runner and Playwright on the module path; see README.md.
// Codex's "Codex memories model" (#985, Hu9956: the model Codex writes its
// memories with was only the notebook square on Codex's card, beside the
// subagent squares, and wasn't found), in its settings on its opened row
// (Settings had them till the owner, 10-20). The row sits under "Codex auto-review
// model" with its name alone (1ooogle on X); unset it is
// the same as the session (cybercaius). It opens the app's model picker (no native select), and
// a pick posts Codex's memories field, and Default puts the session's back.
// The card has no square for it beside the row, the same setting twice (the
// owner, 10-25). No Codex with the field: no row, and Claude Code's settings
// haven't it. No click
// scrolls the page. Chromium and WebKit, en/zh/ja/de, 440px and wide; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, openAgentSettings } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const models = [{ value: "gpt-5.5", label: "GPT-5.5", ref: "openai/gpt-5.5" }, { value: "gpt-5.4-mini", label: "GPT-5.4 mini", ref: "openai/gpt-5.4-mini" }];
const agents = (codex) => codex ? [{
  id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true,
  fields: [
    { key: "model", label: "model", value: "gpt-5.5", options: models },
    { key: "subagent", label: "subagents", value: "", options: models },
    { key: "memories", label: "memories", value: "", options: models },
  ],
}] : [{ id: "claude", name: "Claude Code", path: "/test/settings.json", icon: "claude-color", wired: true, fields: [{ key: "model", label: "model", value: "relay/m1", options: [{ value: "relay/m1", label: "m1", ref: "relay/m1" }] }] }];

const settings = (lang) => ({
  theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
  dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
  codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
  trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
  version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
  proxyNow: "none", proxySource: "none", login: false,
  visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [], titleModels: [],
  fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
});

function server(lang, sets, codex) {
  let cur = { agents: agents(codex), profiles: [] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...cur, settings: { lang, theme: "light" } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      cur.agents.find((a) => a.id === body.agent).fields.find((f) => f.key === body.field).value = body.value;
      return json({ ...cur, settings: { lang, theme: "light" } });
    }
    if (url.pathname === "/api/settings") return json(settings(lang));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try {
      await route.fulfill({ body: await fs.readFile(file), contentType });
    } catch {
      await route.fulfill({ status: 404, body: "" });
    }
  };
}

// the words as i18n.js has them
const W = {
  en: { name: "Codex memories model", own: "Same as the session", def: "Default" },
  zh: { name: "Codex 记忆整理模型", own: "跟随当前会话", def: "默认" },
  ja: { name: "Codex メモリ整理のモデル", own: "セッションと同じ", def: null },
  de: { name: "Codex-Modell für Erinnerungen", own: "Wie die Sitzung", def: null },
};
const view = (page) => page.locator("#view-agents").evaluate((v) => v.scrollTop);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [440, 1100]) {
    for (const lang of ["en", "zh", "ja", "de"]) {
      test(`${engine} ${width}px ${lang}: Codex's settings pick the model it writes its memories with, no square beside them`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 520 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], sets = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, sets, true));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-codex-memories-settings.png`) });
          }
          await browser.close();
        });
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=agents");
        await openAgentSettings(page);
        const row = page.locator("#codexMemoriesRow");
        const pick = page.locator("#codexMemoriesPick button");
        await pick.waitFor();
        // beside the other two of Codex's background models
        assert.equal(await page.evaluate(() => document.querySelector("#codexAutoReviewRow").nextElementSibling?.id), "codexMemoriesRow");
        assert.equal(await row.locator("select").count(), 0, "no native select");
        assert.equal((await row.locator(".name").textContent()).trim(), w.name);
        if (w.own) assert.equal((await pick.textContent()).trim(), w.own, "the session's by default (cybercaius)");
        assert.equal(await row.locator(".sub").count(), 0, "the row says its name alone, its button the choice (1ooogle on X)");
        // the title, its line and the button fit the row
        const fit = await row.evaluate((r) => {
          const box = r.getBoundingClientRect();
          return [...r.querySelectorAll(".name, .sub, button")].every((e) => {
            const b = e.getBoundingClientRect();
            return b.width > 0 && b.left >= box.left - 1 && b.right <= box.right + 1;
          }) && document.documentElement.scrollWidth <= innerWidth;
        });
        assert(fit, "the row overflows");

        // scrolled by a wheel till the row is mid-view, so a click that
        // moved the page would show
        const box = await page.locator("#view-agents").boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        const top = () => pick.evaluate((e) => e.getBoundingClientRect().top);
        for (let i = 0; i < 60 && (await top()) > 200; i++) { await page.mouse.wheel(0, 100); await page.waitForTimeout(30); }
        const before = await view(page);
        assert(before > 0, "the agents list must scroll to the row");

        // the app's model picker, Default first
        await pick.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        assert.equal(await page.locator("#pop").evaluate((e) => e.classList.contains("model-picker")), true);
        assert.equal(await page.locator("select").count(), 0, "a native select");
        if (w.def) assert((await page.locator("#pop #list li").first().innerText()).includes(w.def));
        await page.locator("#pop #list li", { hasText: "GPT-5.4 mini" }).first().click();
        await page.locator("#codexMemoriesPick button", { hasText: "GPT-5.4 mini" }).waitFor();
        assert.deepEqual(sets, [{ agent: "codex", field: "memories", value: "gpt-5.4-mini" }]);
        assert.equal(await row.locator(".sub").count(), 0);
        await page.waitForTimeout(300);
        assert.equal(await view(page), before, "picking a model scrolled the page");

        // the card has no square for it beside the row: that was the same
        // setting twice (the owner, 10-25). Default is picked on the row.
        assert.equal(await page.locator('.row.agent[data-id="codex"] .field[data-key="memories"]').count(), 0, "a memories square on Codex's card");
        await pick.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        await page.locator("#pop #list li").first().click();
        for (let i = 0; i < 100 && sets.length < 2; i++) await page.waitForTimeout(50);
        assert.deepEqual(sets.at(-1), { agent: "codex", field: "memories", value: "" });

        // and the row shows the session's again
        if (w.own) await page.locator("#codexMemoriesPick button", { hasText: w.own }).waitFor();
        assert.equal(await row.locator(".sub").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }

  test(`${engine}: no Codex, no memories row`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const page = await browser.newPage({ viewport: { width: 900, height: 520 } });
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", server("en", [], false));
    t.after(() => browser.close());
    await watchSettingsReads(page);
    await page.goto("http://magpie.test/?view=agents");
    await openAgentSettings(page, "claude");
    await page.locator('.row.agent[data-id="claude"] .ag-prefs #fullContextRow').waitFor();
    // Long conversations, and Claude Code's own size under it
    await page.locator('.row.agent[data-id="claude"] .ag-prefs .agent-compact').waitFor();
    assert.equal(await page.locator(".ag-prefs .row.pref:not(.agent-compact)").count(), 1, "Claude Code's settings are Long conversations alone");
    assert.equal(await page.locator("#codexMemoriesRow").isHidden(), true);
    assert.deepEqual(errors, []);
  });
}
