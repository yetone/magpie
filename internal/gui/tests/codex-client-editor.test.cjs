// Run with Node's test runner and Playwright on the module path; see README.md.
// The Codex client (which Codex magpie's ChatGPT requests say they come
// from) is set in Agents › Codex, which gateway mode has not, so the Codex
// provider's editor has it under Advanced too: the same app menu (no
// native select), posted to settings/codex-client on its own, the button
// saying the pick at once. In the app and in gateway mode, every language,
// Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const codex = () => ({
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "", maxConcurrency: null,
});
const claude = () => ({
  ...codex(), id: "claude", name: "Claude", icon: "claude-color", models: [{ id: "claude-x", name: "Claude X", on: true }],
  account: { agent: "claude", agentName: "Claude Code", user: "me@example.com", plan: "MAX", logins: [{ user: "me@example.com", plan: "MAX", active: true, on: true }] },
});

function serve(lang, gateway, posted) {
  let cur = { theme: "light", lang, version: "0.1.400", titleModels: [], codexClient: "", ...(gateway ? { web: true, gatewayOn: true } : {}) };
  const providers = { providers: [codex(), claude()], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${gateway}${gateway ? ",gateway:true" : ""}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/settings/codex-client") {
      const body = req.postDataJSON();
      posted.push(body);
      cur = { ...cur, codexClient: body.client };
      return json(cur);
    }
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
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
  en: { name: "Codex client", cli: "Codex CLI", desktop: "Codex Desktop" },
  zh: { name: "Codex 客户端", cli: "Codex 命令行", desktop: "Codex 桌面版" },
  "zh-TW": { name: "Codex 用戶端", cli: "Codex 命令列", desktop: "Codex 桌面版" },
  ja: { name: "Codex クライアント", cli: "Codex CLI", desktop: "Codex Desktop" },
  de: { name: "Codex-Client", cli: "Codex CLI", desktop: "Codex Desktop" },
};
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = process.env.LANGS ? process.env.LANGS.split(",") : Object.keys(W);
const exact = (s) => new RegExp("^\\s*" + s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "\\s*$");

for (const engine of engines) {
  for (const lang of langs) {
    for (const gateway of [false, true]) {
      test(`${engine} ${lang}${gateway ? " gateway mode" : ""}: the Codex provider's Advanced picks the Codex client`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width: 900, height: 720 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(10000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posted = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, gateway, posted));
        const advanced = async (id) => {
          await page.goto(`http://magpie.test/?view=providers&edit=${id}`);
          const adv = page.locator(".editor details.advanced");
          await adv.waitFor({ timeout: 60000 });
          if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
          return adv;
        };
        const label = (adv) => adv.locator("label", { hasText: exact(w.name) });

        let adv = await advanced("codex");
        await label(adv).waitFor();
        const pick = adv.locator(".codex-client button");
        await pick.scrollIntoViewIfNeeded();
        assert.equal((await pick.textContent()).trim(), w.cli, "Codex CLI by default");
        assert.equal(await page.locator("select").count(), 0, "a native select");
        const cancel = page.locator(".editor > .bar .ed-cancel"), clean = await cancel.textContent();
        await pick.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.deepEqual((await menu.locator(".pm-item .pm-name").allTextContents()).map((s) => s.trim()), [w.cli, w.desktop]);
        await menu.locator(".pm-item").filter({ has: page.locator(".pm-name", { hasText: exact(w.desktop) }) }).click();
        await adv.locator(".codex-client button", { hasText: exact(w.desktop) }).waitFor();
        assert.deepEqual(posted, [{ client: "desktop" }]);
        // applied at once: nothing for the editor's Save to send
        await page.waitForTimeout(100);
        assert.equal(await cancel.textContent(), clean, "the pick left the editor with something to save");

        // only Codex's editor has it
        adv = await advanced("claude");
        await adv.locator("label").first().waitFor();
        assert.equal(await label(adv).count(), 0, "the Codex client in Claude's editor");
        assert.deepEqual(errors, []);
      });
    }
  }
}
