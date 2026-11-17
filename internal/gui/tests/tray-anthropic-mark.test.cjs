// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → Menu bar Claude logo (qingnianxiaozhe on X: Anthropic's A\ in
// the menu bar): on a Mac with a Claude card in the menu bar and its logos
// shown, a row of two logos, Claude's and the A\; the A\ posts
// trayAnthropicMark and Claude's takes it back; a save of another setting
// keeps it; no row without a Claude card, with the logos off, nor off a
// Mac. Every language, wide and narrow; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const quotas = [
  { provider: "claude", name: "Claude Code", icon: "claude-color", user: "a@b.c", plan: "Max",
    windows: [{ name: "5-hour", used: 42 }, { name: "Weekly", used: 18 }] },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "x@y.z", plan: "Pro",
    windows: [{ name: "5-hour", used: 8 }, { name: "Weekly", used: 30 }] },
];

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "claude|a@b.c", trayUsages: ["claude|a@b.c", "codex|x@y.z"], trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, posts, over) {
  let cur = settingsPayload({ lang, ...over });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        cur = { ...cur, ...body, trayUsage: (body.trayUsages || [])[0] || "" };
      }
      return json(cur);
    }
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(browser, lang, posts, platform, over, width = 900) {
  const context = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
  await context.addInitScript((p) => Object.defineProperty(Navigator.prototype, "platform", { get: () => p }), platform);
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  await page.route("**/*", server(lang, posts, over));
  await page.goto("http://magpie.test/?view=usage");
  await page.locator("#prefs").click();
  await openPart(page, "usage");
  await page.locator("#trayUsagePick button").waitFor();
  return { context, page };
}

const posted = async (page, posts, sent) => {
  for (let i = 0; i < 50 && posts.length === sent; i++) await page.waitForTimeout(50);
  assert(posts.length > sent, "nothing was posted");
  return posts.at(-1);
};

const names = {
  en: "Menu bar Claude logo",
  zh: "菜单栏 Claude 图标",
  "zh-TW": "選單列 Claude 圖示",
  ja: "メニューバーの Claude アイコン",
  de: "Claude-Logo in der Menüleiste",
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Claude's menu bar logo or Anthropic's A\\", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-tray-anthropic-mark-${i}.png`) }).catch(() => {});
      }
      await browser.close();
    });

    for (const [lang, name] of Object.entries(names)) for (const width of [900, 400]) {
      await t.test(lang + " " + width, async () => {
        const errors = [], posts = [];
        const { context, page } = await open(browser, lang, posts, "MacIntel", {}, width);
        pages.push(page);
        page.on("pageerror", (e) => errors.push(e.message));
        const row = page.locator("#trayClaudeRow");
        await row.waitFor();
        assert.equal((await row.locator(".name").textContent()).trim(), name);
        const sub = (await row.locator(".sub").textContent()).trim();
        assert.match(sub, /A\\/, "the sub names the A\\ mark: " + sub);
        const opts = row.locator("#trayClaudeSegs .opt");
        assert.equal(await opts.count(), 2);
        assert.deepEqual(await opts.evaluateAll((bs) => bs.map((b) => b.querySelector("[aria-label]")?.getAttribute("aria-label"))), ["Claude", "Anthropic"]);
        // each a logo drawn, the A\ in the text's colour
        for (const i of [0, 1]) {
          const box = await opts.nth(i).locator(".ic").boundingBox();
          assert(box && box.width >= 12 && box.height >= 12, "logo " + i + " drawn: " + JSON.stringify(box));
        }
        assert.equal(await opts.nth(1).locator("path").first().evaluate((p) => p.getAttribute("fill")), "currentColor");
        // Claude's logo, as before
        assert.equal(await opts.nth(0).evaluate((b) => b.classList.contains("on")), true, "Claude's logo by default");
        // narrow, the row's control stays in the row and the page doesn't scroll sideways
        await row.scrollIntoViewIfNeeded();
        const [rb, sb] = await Promise.all([row.boundingBox(), page.locator("#trayClaudeSegs").boundingBox()]);
        assert(sb.x >= rb.x - 1 && sb.x + sb.width <= rb.x + rb.width + 1, `the logos ${JSON.stringify(sb)} past the row ${JSON.stringify(rb)}`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");

        // the page is drawn again once a save is in; a control from before
        // that still carries the choices from before (savePrefs)
        const mark = () => page.evaluate(() => { document.querySelector("#currencySegs .opt").stale = true; });
        const redrawn = () => page.waitForFunction(() => !document.querySelector("#currencySegs .opt")?.stale);
        await mark();
        await opts.nth(1).click();
        assert.equal((await posted(page, posts, 0)).trayAnthropicMark, true, "the A\\ posts trayAnthropicMark");
        await redrawn();
        assert.equal(await page.locator("#trayClaudeSegs .opt").nth(1).evaluate((b) => b.classList.contains("on")), true, "the A\\ shown as chosen");

        // another setting saved keeps it
        let sent = posts.length;
        await mark();
        await page.locator("#currencySegs .opt").nth(1).evaluate((b) => b.click());
        const other = await posted(page, posts, sent);
        assert.equal(other.currency, "cny");
        await redrawn();
        assert.equal(other.trayAnthropicMark, true, "a save of another setting keeps the A\\");

        // and Claude's takes it back
        sent = posts.length;
        await row.scrollIntoViewIfNeeded();
        await page.locator("#trayClaudeSegs .opt").nth(0).click();
        assert.equal((await posted(page, posts, sent)).trayAnthropicMark, false, "Claude's logo posts it back");
        assert.deepEqual(errors, []);
        await context.close();
      });
    }

    await t.test("no row", async () => {
      // no Claude card, the logos off, no card at all, or not a Mac
      for (const [platform, over] of [
        ["MacIntel", { trayUsage: "codex|x@y.z", trayUsages: ["codex|x@y.z"] }],
        ["MacIntel", { trayNoLogos: true }],
        ["MacIntel", { trayUsage: "", trayUsages: [] }],
        ["Win32", {}],
      ]) {
        const { context, page } = await open(browser, "en", [], platform, over);
        await page.waitForTimeout(300);
        assert.equal(await page.locator("#trayClaudeRow").isVisible(), false, platform + " " + JSON.stringify(over));
        await context.close();
      }
      // the account in use stands for Claude's cards too
      const { context, page } = await open(browser, "en", [], "MacIntel", { trayUsage: "claude|*", trayUsages: ["claude|*"] });
      await page.locator("#trayClaudeRow").waitFor();
      await context.close();
    });
  });
}
