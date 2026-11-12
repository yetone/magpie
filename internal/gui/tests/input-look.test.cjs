// Run with Node's test runner and Playwright on the module path; see README.md.
// Every text field is magpie's own, never the system's (the owner, 11-12:
// Settings › Network's saved-proxy form showed four bare white boxes, "根本
// 就不是我们整个产品的输入框组件"). A field the CSS names nowhere still
// gets the product's look from app.css's base rule, so a new form can't
// fall back to the browser's. Settings' every part (Network with the
// saved-proxy form and each Custom proxy open), a provider's editor and a
// signed-in subscription's (Advanced open, each Custom proxy open) and a
// routing group's editor (Advanced open) are read: no visible field may
// have the browser's inset border, and none is white on the dark theme.
// Every language, Chromium and WebKit, light and dark, wide and narrow; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }, { user: "two@example.com", plan: "PLUS", on: true }] },
  proxy: "",
};
const models = [{ id: "relay/model-a", name: "Model A", provider: "relay", providerName: "Relay", icon: "generic" }];
const groups = {
  models, pools: [], accounts: {}, gatewayKeys: [],
  groups: [{ id: "work", name: "Work", members: ["relay/model-a"], routing: "order", ready: true, accounts: [], memberInfo: [{ id: "relay/model-a", ready: true }] }],
};

function serve(lang, theme) {
  const settings = {
    theme, lang, tray: "panel", currency: "usd", textSize: 100, version: "test", dir: "/test", gateway: "http://127.0.0.1:3425",
    proxy: "", proxyNow: "none", proxySource: "none", pluginProxy: "", updateProxy: "", lanURLs: [], visionModels: [], imageGenModels: [],
    titleModels: [], workbuddyCheckins: [], redactWords: [], trayUsageEvery: 3,
    proxies: [{ id: "px1", name: "Office", url: "http://10.0.0.1:8080" }],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme, web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [relay, codex], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { edit: "Edit" }, zh: { edit: "编辑" }, "zh-TW": { edit: "編輯" }, ja: { edit: "編集" }, de: { edit: "Bearbeiten" },
};

// each visible text field drawn as the browser's: an inset border (both
// engines' default), or white on the dark theme
const bare = (page, where, dark) => page.evaluate(([where, dark]) => {
  const out = [];
  for (const e of document.querySelectorAll("input, textarea")) {
    if (/^(checkbox|radio|range|hidden|file|color|button|submit)$/.test(e.type) || !e.getClientRects().length) continue;
    const s = getComputedStyle(e);
    const sides = [s.borderTopStyle, s.borderRightStyle, s.borderBottomStyle, s.borderLeftStyle];
    if (sides.some((x) => x === "inset" || x === "outset") || (dark && s.backgroundColor === "rgb(255, 255, 255)")) {
      out.push(`${where}: <${e.tagName.toLowerCase()} class="${e.className}" placeholder="${e.placeholder}"> ${sides[0]} ${s.backgroundColor}`);
    }
  }
  return out;
}, [where, dark]);

// presses every Custom of a proxy's options under root, one at a time (a
// press draws the row again)
async function customs(page, root) {
  for (let i = 0; i < 12; i++) {
    const on = await page.evaluate((root) => {
      const b = [...document.querySelectorAll(`${root} .segs > .opt[data-seg="custom"]`)].find((x) => !x.classList.contains("on") && !x.dataset.looked);
      if (!b) return -1;
      const n = document.querySelectorAll(`${root} .segs > .opt.on[data-seg="custom"]`).length;
      b.dataset.looked = "1";
      b.click();
      return n;
    }, root);
    if (on < 0) return;
    await page.waitForFunction(([root, n]) => document.querySelectorAll(`${root} .segs > .opt.on[data-seg="custom"]`).length > n, [root, on]);
  }
}

const unfold = (page, sel) => page.evaluate((sel) => { for (const d of document.querySelectorAll(sel)) d.open = true; }, sel);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: every text field in Settings and the editors is magpie's own`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        t.after(() => browser.close());
        const found = [], errors = [];
        for (const theme of ["dark", "light"]) {
          const dark = theme === "dark";
          const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce", colorScheme: theme })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, theme));
          await watchSettingsReads(page);
          for (const tab of ["general", "usage", "network", "models", "privacy", "otel", "sync", "plugins", "about"]) {
            await page.goto(`http://magpie.test/?view=settings&tab=${tab}`);
            await page.locator(`#setTab-${tab}`).waitFor({ state: "attached" });
            await settingsDrawn(page);
            if (tab === "network") {
              await page.locator("#proxiesList .saved-proxy").first().waitFor();
              // Add proxy: the list's last row, under the saved ones
              await page.evaluate(() => [...document.querySelectorAll("#proxiesList > .row:not(.saved-proxy) button")].pop().click());
              await page.locator("#proxiesList .proxy-form input").first().waitFor();
              await customs(page, "#view-settings");
            }
            found.push(...await bare(page, `${theme} settings/${tab}`, dark));
          }
          for (const id of ["relay", "codex"]) {
            await page.goto(`http://magpie.test/?view=providers&edit=${id}`);
            await page.locator(".editor details.advanced").waitFor({ state: "attached" });
            await unfold(page, ".editor details");
            await customs(page, ".editor");
            await unfold(page, ".editor details");
            found.push(...await bare(page, `${theme} provider ${id}`, dark));
          }
          await page.goto("http://magpie.test/?view=groups");
          const card = page.locator(".rt-group", { hasText: "Work" });
          await card.waitFor();
          await card.locator("button", { hasText: words[lang].edit }).first().evaluate((b) => b.click());
          await page.locator(".rt-gedit .rt-gadv").waitFor({ state: "attached" });
          await unfold(page, ".rt-gedit details");
          found.push(...await bare(page, `${theme} group editor`, dark));
          // the probe itself: a field nothing styles is caught here
          if (await page.evaluate(() => { const i = document.createElement("input"); i.className = "nothing-names-this"; document.querySelector(".rt-gedit").append(i); const s = getComputedStyle(i).borderTopStyle; i.remove(); return s === "inset"; })) found.push(`${theme}: a field nothing names falls back to the browser's look`);
        }
        assert.deepEqual(found, [], "fields drawn as the browser's:\n" + found.join("\n"));
        assert.deepEqual(errors, []);
      });
    }
  }
}
