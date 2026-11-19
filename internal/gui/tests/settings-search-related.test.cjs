// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings' search finds what the words are about (canlee886 on X:
// "可访问性", "监控" and "自动选择模型" each said no setting matched, "缺少
// 搜索时的智能联想"). When no setting has the words themselves, the
// settings they are about are shown under "Settings related to “…”": a
// concept's words in each language (accessibility, monitoring, picking a
// model by itself) lead to the settings it covers, a long run of Chinese is
// read by its two-character words, and a Latin word may have a typo. Words
// a setting has are still found as before, with no such line, and words
// nothing is about still say no setting matches. Nothing is posted, the
// page doesn't scroll, and nothing runs off to the side at 560 x 420 or on
// a phone. Every language, Chromium and WebKit; the API is faked here.
// SEARCH_ASSETS points it at an older assets folder (the base, where it
// fails); ARTIFACT_DIR gets a screenshot of each.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders } = require("./reader.cjs");

const assets = path.resolve(process.env.SEARCH_ASSETS || path.join(__dirname, "../assets"));

const row = (id, name, sub, on, more) => ({ id, name, sub, on, chosen: on, source: "preset", state: on ? "active" : "off", inUse: 0, ...more });
const builtins = [
  row("otel", "Observability", "Send each request's trace to an OpenTelemetry collector", false, { setAt: "otel #otelList" }),
];

function server(lang, posts, web) {
  const cur = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", textSize: 100,
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, builtins,
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (req.method() !== "GET" && url.pathname !== "/api/update/using" && url.pathname !== "/api/page-away") posts.push(url.pathname);
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web},textSize:100,featuresOff:["otel"]};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", textSize: 100 }, fx: cur.fx });
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/plugins/builtin") return json(builtins);
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["codex"], { codex: "ChatGPT" }));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the line over the parts, and whether it is the related one (only when no
// setting has the words themselves)
const line = (page) => page.evaluate(() => {
  const n = document.querySelector("#setNone");
  return n.getClientRects().length ? (n.classList.contains("set-near") ? "near: " : "none: ") + n.textContent : "";
});
const seen = (page, sel) => page.locator(sel).first().evaluate((e) => !!e.getClientRects().length && getComputedStyle(e).visibility !== "hidden").catch(() => false);
const overflow = (page) => page.evaluate(() => {
  const out = [];
  const de = document.documentElement, v = document.querySelector("#view-settings");
  if (de.scrollWidth > de.clientWidth) out.push("page " + de.scrollWidth + ">" + de.clientWidth);
  if (v.scrollWidth > v.clientWidth) out.push("view " + v.scrollWidth + ">" + v.clientWidth);
  const n = document.querySelector("#setNone").getBoundingClientRect(), r = v.getBoundingClientRect();
  if (n.width && (n.left < r.left - 0.5 || n.right > r.right + 0.5)) out.push("line " + n.left + "-" + n.right);
  return out;
});

// what a reader in each language types for the three asks
const ASKS = {
  en: ["accessibility", "monitoring", "auto select model"],
  zh: ["可访问性", "监控", "自动选择模型"],
  "zh-TW": ["無障礙", "監控", "自動選擇模型"],
  ja: ["アクセシビリティ", "監視", "モデル自動選択"],
  de: ["barrierefreiheit", "überwachung", "automatische modellwahl"],
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Settings' search finds what the words are about", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-settings-related-${name}.png`), buf);
      }
      await browser.close();
    });
    const open = async (lang, posts, { width = 560, height = 420, web = false } = {}) => {
      const context = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.errors = [];
      page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; page.errors.push(e.message); });
      await page.route("**/*", server(lang, posts, web));
      await page.goto("http://magpie.test/?view=settings&tab=general");
      await page.locator("#themeSegs .opt").first().waitFor();
      return page;
    };
    // typed, then the search settled: the lazy finder and built-ins in
    const type = async (page, s) => {
      await page.locator("#setQ").fill(s);
      await page.waitForFunction(() => window.setFind && window.setFeatsFind && document.querySelector("#view-settings").classList.contains("set-searching") === !!document.querySelector("#setQ").value.trim());
      await page.waitForTimeout(150);
    };

    for (const lang of Object.keys(ASKS)) {
      await t.test(lang, async () => {
        const posts = [];
        const page = await open(lang, posts);
        const tr = (s, vars) => page.evaluate(([s, vars]) => t(s, vars), [s, vars]);
        const [a11y, monitor, autoModel] = ASKS[lang];

        // accessibility: the settings that make it easier to see and use
        await type(page, a11y);
        for (const sel of ["#textSizeSegs", "#themeSegs"]) assert(await seen(page, sel), `"${a11y}" shows ${sel}`);
        assert(await seen(page, "#view-settings .row:has(> .who > .name[data-en='Reduce motion'])"), `"${a11y}" shows Reduce motion`);
        assert.equal(await seen(page, "#currencySegs"), false, "not Currency");
        assert.equal(await line(page), "near: " + await tr("Settings related to “{q}”", { q: a11y }));
        assert.deepEqual(await overflow(page), []);
        shots.push([lang + "-a11y", await page.screenshot()]);

        // monitoring: Observability, a built-in switched off here
        await type(page, monitor);
        const feats = await page.locator("#setFeats .feat-find:visible .name").allTextContents();
        assert(feats.includes(await tr("Observability")), `"${monitor}" finds Observability: ${feats}`);
        assert.match(await line(page), /^near: /);
        assert.deepEqual(await overflow(page), []);
        shots.push([lang + "-monitor", await page.screenshot()]);

        // a model picked by itself: Models' Routing settings
        await type(page, autoModel);
        assert(await seen(page, "#busyLoadRow"), `"${autoModel}" shows Busy models last`);
        assert(await seen(page, "#contextCapRow"), `"${autoModel}" shows Longest context, under the same Routing`);
        assert.equal(await seen(page, "#themeSegs"), false, "not Appearance");
        // with a setting that has the words themselves (English's web
        // search row says "auto", "select" and "model"), no line
        assert.match(await line(page), lang === "en" ? /^$/ : /^near: /);
        assert.deepEqual(await overflow(page), []);
        shots.push([lang + "-auto-model", await page.screenshot()]);

        // a word a setting has is found as before, with no line
        await type(page, "currency");
        assert(await seen(page, "#currencySegs"));
        assert.equal(await line(page), "");
        // and a concept's settings come with the words' own: "proxy" has
        // its own rows, and "vpn" (in no setting's words) leads to them
        await type(page, "vpn");
        assert(await seen(page, "#view-settings .row:has(> .who > .name[data-en='Proxy'])"), "vpn finds Proxy");
        // a typo is forgiven
        await type(page, "currancy");
        assert(await seen(page, "#currencySegs"), "currancy finds Currency");
        assert.equal(await line(page), "near: " + await tr("Settings related to “{q}”", { q: "currancy" }));
        // and words nothing is about still say so
        await type(page, "zzqqxx");
        assert.equal(await line(page), "none: " + await tr("No setting matches “{q}”", { q: "zzqqxx" }));
        // Escape puts it all away
        await page.locator("#setQ").focus();
        await page.keyboard.press("Escape");
        await page.waitForTimeout(100);
        assert.equal(await line(page), "");
        assert(await seen(page, "#setTabs"), "the tabs back");

        assert.equal(await page.locator("#view-settings").evaluate((v) => v.scrollTop), 0, "searching scrolls nothing");
        assert.deepEqual(posts, [], "searching posts nothing");
        assert.deepEqual(page.errors, []);
        await page.context().close();
      });
    }

    // a long run of Chinese is read by its two-character words, and a
    // Latin word with a typo by the word it is near
    await t.test("pieces and typos", async () => {
      for (const [lang, q] of [["zh", "文字大小调整"], ["en", "text sise"], ["zh-TW", "文字大小調整"]]) {
        const page = await open(lang, []);
        await type(page, q);
        assert(await seen(page, "#textSizeSegs"), `${lang} "${q}" finds Text size`);
        assert.match(await line(page), /^near: /, q);
        assert.deepEqual(page.errors, []);
        await page.context().close();
      }
    });

    await t.test("magpie web on a phone", async () => {
      const page = await open("zh", [], { width: 390, height: 760, web: true });
      await type(page, "可访问性");
      assert.equal(await line(page), "near: 与“可访问性”相关的设置");
      assert.deepEqual(await overflow(page), []);
      shots.push(["web-phone", await page.screenshot()]);
      assert.deepEqual(page.errors, []);
      await page.context().close();
    });
  });
}
