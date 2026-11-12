// Run with Node's test runner and Playwright on the module path; see README.md.
// Sign-in pages kept out of the default browser (max2020 on Discord: they
// sign in from a separate, fingerprint browser, and magpie opening the page
// in the default one shows the vendor that browser). Settings → Privacy →
// Sign-in pages has "Open sign-in pages in the browser", On by default; Off
// saves signInLinkOnly. A sign-in the server says it kept to its link
// (linkOnly) opens no page in a browser tab either, says so, and offers its
// link to copy and Open (not "Open again"); one without it opens its page as
// before, and "as set in Settings" leads to the setting. Every language,
// Chromium and WebKit, a narrow window; no click
// moves the page. No backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { openPart, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const LINK = "https://accounts.example/o/oauth2/auth?client_id=x&redirect_uri=http%3A%2F%2F127.0.0.1%3A51234%2Fcallback&state=" + "s".repeat(80);

const W = {
  en: { settings: "Settings", head: "Sign-in pages", name: "Open sign-in pages in the browser", anyway: "Sign in anyway", said: "magpie didn't open the sign-in page", open: "Open in browser", again: "Open again", copy: "Copy link" },
  zh: { settings: "设置", head: "登录页", name: "在浏览器中打开登录页", anyway: "仍然登录", said: "magpie 按设置没有打开登录页", open: "在浏览器中打开", again: "重新打开", copy: "复制链接" },
  "zh-TW": { settings: "設定", head: "登入頁", name: "在瀏覽器中開啟登入頁", anyway: "仍然登入", said: "magpie 依設定沒有開啟登入頁", open: "在瀏覽器中開啟", again: "重新開啟", copy: "複製連結" },
  ja: { settings: "設定", head: "サインインページ", name: "サインインページをブラウザで開く", anyway: "それでもサインイン", said: "設定に従い、magpie はサインインページを開いていません", open: "ブラウザで開く", again: "もう一度開く", copy: "リンクをコピー" },
  de: { settings: "Einstellungen", head: "Anmeldeseiten", name: "Anmeldeseiten im Browser öffnen", anyway: "Trotzdem anmelden", said: "magpie hat die Anmeldeseite wie in den Einstellungen festgelegt nicht geöffnet", open: "Im Browser öffnen", again: "Erneut öffnen", copy: "Link kopieren" },
};

function settingsPayload(lang, over) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsages: [], trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, web, sign, posts) {
  let cur = settingsPayload(lang);
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") { posts.push(req.postDataJSON()); cur = { ...cur, ...req.postDataJSON() }; }
      return json(cur);
    }
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/signin" || url.pathname.startsWith("/api/signin/")) return json(sign);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function newPage(browser, width, route) {
  const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  // a tab the page opens is counted, not opened
  await page.addInitScript(() => {
    window.__opened = [];
    window.open = (u) => { window.__opened.push(String(u)); return {}; };
  });
  await page.route("**/*", route);
  return { context, page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": sign-in pages kept to their link", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(W)) {
      const w = W[lang];
      for (const width of [900, 360]) {
        await t.test(`${lang} ${width}: the setting`, async () => {
          const posts = [];
          const { context, page, errors } = await newPage(browser, width, server(lang, false, {}, posts));
          await page.goto("http://magpie.test/?view=usage");
          await page.locator("#prefs").click();
          await openPart(page, "privacy");
          const box = page.locator("#signInOpenList");
          await box.locator(".opt").first().waitFor();
          assert.equal((await box.evaluate((b) => b.previousElementSibling.querySelector(".label").textContent)).trim(), w.head);
          assert.equal((await box.locator(".name").textContent()).trim(), w.name);
          const opts = box.locator(".opt");
          assert.equal(await opts.nth(1).evaluate((b) => b.classList.contains("on")), true, "On by default");
          const fit = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1);
          assert(fit, "no sideways scroll");
          await reach(page, opts.nth(0)); // by the wheel, as a reader does: magpie puts back a scroll no one asked for
          await page.waitForTimeout(200);
          const top = await page.locator("#view-settings").evaluate((v) => v.scrollTop);
          await opts.nth(0).click();
          for (let i = 0; i < 40 && !posts.length; i++) await page.waitForTimeout(50);
          assert.equal(posts.at(-1)?.signInLinkOnly, true, "Off saves signInLinkOnly");
          await page.waitForFunction(() => document.querySelectorAll("#signInOpenList .opt")[0]?.classList.contains("on"));
          await page.waitForTimeout(200);
          assert.equal(await page.locator("#view-settings").evaluate((v) => v.scrollTop), top, "the click didn't move the page");
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
      for (const linkOnly of [true, false]) {
        await t.test(`${lang}: a sign-in ${linkOnly ? "kept to its link" : "opened"} in a browser tab`, async () => {
          const sign = { id: "s1", agent: "factory", state: "waiting", url: LINK, ...(linkOnly ? { linkOnly: true } : {}) };
          const { context, page, errors } = await newPage(browser, 900, server(lang, true, sign, []));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator("#addProvider").click();
          const sheet = page.locator("#addSheet");
          await sheet.locator('.tile[data-pick="Factory"]').click();
          const box = sheet.locator(".signing");
          await box.locator("button", { hasText: w.anyway }).click();
          await box.locator(".signlink").waitFor();
          assert.equal(await box.locator(".signlink code").textContent(), LINK);
          assert.equal(await box.locator(".signlink button.copy").getAttribute("title"), w.copy);
          const said = await box.locator(".tt > .s").first().textContent();
          const opened = await page.evaluate(() => window.__opened);
          if (linkOnly) {
            assert.deepEqual(opened, [], "no page opened");
            assert(said.startsWith(w.said), said);
            assert.equal(await box.locator(".signlink button", { hasText: new RegExp("^" + w.open + "$") }).count(), 1, "Open in browser, beside the link");
            // Copy first, Open in browser to its right
            const at = await box.locator(".signlink").evaluate((r) => [...r.children].map((c) => c.tagName + "." + c.className));
            assert.deepEqual(at, ["CODE.", "BUTTON.copy", "BUTTON.link"], "link, Copy, Open in browser");
            assert.equal(await box.locator("button", { hasText: w.again }).count(), 0, "not Open again");
            // "as set in Settings" leads to the setting
            const go = box.locator(".tt > .s button");
            assert.equal((await go.textContent()).trim(), w.settings + " ›");
            await go.click();
            await page.locator("#view-settings #signInOpenRow.flash").waitFor();
          } else {
            assert.deepEqual(opened, [LINK], "the page opened once");
            assert(!said.startsWith(w.said), said);
            assert.equal(await box.locator(".tt > .s button").count(), 0, "nothing to lead to");
            assert.equal(await box.locator(".signlink button", { hasText: w.again }).count(), 1, "Open again, beside Copy");
          }
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    }
  });
}
