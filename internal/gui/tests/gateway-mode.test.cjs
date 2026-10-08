// Run with Node's test runner and Playwright on the module path; see README.md.
// Gateway mode (Player on Discord): magpie web on a server that is only
// the gateway for other computers' agents. boot.js says so and the page
// opens on Providers with no Agents or Library tab, an address
// kept for one of them opening Providers; Settings leaves out what is
// written into this computer's agents' files (provider in model names,
// Codex subagents, long conversations, Codex thread titles) and the
// desktop's alerts, and General has the switch — Automatic, On, Off —
// saying why it is on. Off brings the tabs and the rows back at once, the
// Agents page opening again; Automatic takes them away again. magpie web
// not in gateway mode keeps every tab, and the switch is magpie web's
// alone. No click scrolls the page, no native <select>, no left border.
// English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd", textSize: 100,
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "/config/home/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

// agents says whether magpie found agents here: Automatic is on without
function server(lang, posts, { web = true, gateway = true, agents = false } = {}) {
  const why = (mode) => (mode === "on" || mode === "off" ? mode : agents ? "" : "no-agents");
  const on = (mode) => web && (mode === "on" || (mode !== "off" && !agents));
  let cur = settingsPayload({ lang, web, gatewayMode: "", gatewayOn: web && gateway, gatewayWhy: web && gateway ? why("") : "" });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}${web && gateway ? ",gateway:true" : ""}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings/gateway-mode") {
      const { mode } = req.postDataJSON();
      posts.push(["gateway-mode", mode]);
      cur = { ...cur, gatewayMode: mode, gatewayOn: on(mode), gatewayWhy: on(mode) || mode === "off" ? why(mode) : "" };
      return json(cur);
    }
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") { posts.push(["settings", req.postDataJSON()]); cur = { ...cur, ...req.postDataJSON() }; }
      return json(cur);
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const L = {
  en: { mode: "Gateway mode", noAgents: "Now on: no agents on this computer", off: "Now off: agents found on this computer" },
  zh: { mode: "网关模式", noAgents: "当前开启：本机没有 Agent", off: "当前关闭：本机找到了 Agent" },
};
const HIDDEN = ["agents", "library"];
const KEPT = ["providers", "gateway", "routing", "usage", "sessions", "plugins"];

// the tabs shown, the page shown
const tabs = (page) => page.evaluate(() => [...document.querySelectorAll("#nav button")].filter((b) => !b.hidden && b.offsetParent).map((b) => b.dataset.view));
const viewShown = (page) => page.evaluate(() => [...document.querySelectorAll("main.view")].filter((v) => !v.hidden).map((v) => v.id));
// the rows gateway mode leaves out of Settings, each shown or not
const agentRows = (page) => page.evaluate(() => ["#plainNamesSegs", "#codexAgentsV1Segs", "#fullContextSegs", "#codexTitlesPick", "#codexAutoReviewPick", "#usageAlertSegs", "#balanceAlertSegs", "#resetReminderSegs"]
  .map((s) => !document.querySelector(s).closest(".row").hidden));

async function noSelectNoBorder(page, where) {
  const bad = await page.evaluate(() => {
    const out = [];
    for (const s of document.querySelectorAll("select")) if (s.offsetParent) out.push("select " + (s.id || s.className));
    for (const e of [document.querySelector("#gatewayModeRow"), ...document.querySelectorAll("#gatewayModeRow *")]) {
      const cs = getComputedStyle(e);
      if (parseFloat(cs.borderLeftWidth) > 0 && cs.borderLeftStyle !== "none" && cs.borderLeftColor !== cs.borderTopColor) out.push("left border " + (e.id || e.className));
    }
    return out;
  });
  assert.deepEqual(bad, [], where);
}

// clicks what at, and says the page didn't move
async function clickStill(page, at, where) {
  const before = await page.evaluate(() => { const v = [...document.querySelectorAll("main.view")].find((x) => !x.hidden); return [v.id, v.scrollTop, scrollY]; });
  await page.locator(at).click();
  await page.waitForTimeout(250);
  const after = await page.evaluate((id) => [id, document.getElementById(id).scrollTop, scrollY], before[0]);
  assert.deepEqual(after, before, `${where}: the click scrolled`);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: gateway mode`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      const where = `${engine} ${lang}`;
      const context = await browser.newContext({ viewport: { width: 1100, height: 560 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      assert.deepEqual(await tabs(page), KEPT, `${where}: the tabs`);
      assert.deepEqual(await viewShown(page), ["view-providers"], `${where}: opens on Providers`);
      assert.equal(await page.locator("#nav button.on").getAttribute("data-view"), "providers", where);

      // an address kept for a page left out opens Providers
      for (const v of HIDDEN) {
        await page.goto(`http://magpie.test/?view=${v}`);
        await page.waitForSelector("#nav button.on");
        assert.deepEqual(await viewShown(page), ["view-providers"], `${where}: ?view=${v}`);
      }

      // Settings: the switch, why it is on, and the agents' rows gone
      await clickStill(page, "#prefs", `${where}: Settings`);
      await page.waitForSelector("#gatewayModeSegs .opt");
      assert.ok(await page.locator("#gatewayModeRow").isVisible(), `${where}: the switch`);
      assert.equal((await page.locator("#gatewayModeRow .name").textContent()).trim(), L[lang].mode, where);
      assert.match(await page.locator("#gatewayModeSub").textContent(), new RegExp(L[lang].noAgents), where);
      assert.equal(await page.locator("#gatewayModeSegs .opt.on").evaluate((b) => [...b.parentElement.children].filter((x) => x.matches(".opt")).indexOf(b)), 0, `${where}: Automatic picked`);
      assert.deepEqual(await agentRows(page), [false, false, false, false, false, false, false, false], `${where}: agents' rows in gateway mode`);
      await noSelectNoBorder(page, where);

      // Off: the tabs and the rows are back, Settings stays open
      await clickStill(page, "#gatewayModeSegs .opt:nth-child(4)", `${where}: Off`);
      await page.waitForFunction(() => !document.querySelector('#nav button[data-view="agents"]').hidden);
      assert.deepEqual(posts.filter((p) => p[0] === "gateway-mode"), [["gateway-mode", "off"]], where);
      assert.deepEqual(await tabs(page), ["agents", "providers", "gateway", "routing", "usage", "sessions", "library", "plugins"], `${where}: off`);
      assert.deepEqual(await viewShown(page), ["view-settings"], `${where}: Settings stays`);
      assert.deepEqual(await agentRows(page), [true, true, true, true, true, true, true, true], `${where}: rows back`);
      assert.doesNotMatch(await page.locator("#gatewayModeSub").textContent(), new RegExp(L[lang].noAgents), where);
      await clickStill(page, '#nav button[data-view="agents"]', `${where}: Agents`);
      assert.deepEqual(await viewShown(page), ["view-agents"], `${where}: the Agents page opens`);

      // Automatic again: gone again
      await clickStill(page, "#prefs", `${where}: Settings again`);
      await clickStill(page, "#gatewayModeSegs .opt:nth-child(2)", `${where}: Automatic`);
      await page.waitForFunction(() => document.querySelector('#nav button[data-view="agents"]').hidden);
      assert.deepEqual(posts.filter((p) => p[0] === "gateway-mode").at(-1), ["gateway-mode", ""], where);
      assert.deepEqual(await tabs(page), KEPT, `${where}: automatic`);
      assert.deepEqual(await agentRows(page), [false, false, false, false, false, false, false, false], where);
      // the Settings page's own saves never send it
      assert.ok(posts.filter((p) => p[0] === "settings").every((p) => !("gatewayMode" in p[1])), where);
      assert.deepEqual(errors, [], where);
      await context.close();
    }

    // magpie web with agents here, and the app's window: every tab; the
    // switch is magpie web's alone
    for (const [web, name] of [[true, "web with agents"], [false, "window"]]) {
      for (const lang of ["en", "zh"]) {
        const where = `${engine} ${lang} ${name}`;
        const context = await browser.newContext({ viewport: { width: 1100, height: 560 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, [], { web, gateway: false, agents: true }));
        await page.goto("http://magpie.test/");
        await page.waitForSelector("#nav button.on");
        assert.deepEqual(await tabs(page), ["agents", "providers", "gateway", "routing", "usage", "sessions", "library", "plugins"], where);
        assert.deepEqual(await viewShown(page), ["view-agents"], where);
        await page.locator("#prefs").click();
        await page.waitForSelector("#langSegs .opt");
        assert.equal(await page.locator("#gatewayModeRow").isVisible(), web, `${where}: the switch`);
        if (web) assert.match(await page.locator("#gatewayModeSub").textContent(), new RegExp(L[lang].off), where);
        assert.deepEqual(errors, [], where);
        await context.close();
      }
    }
  });
}
