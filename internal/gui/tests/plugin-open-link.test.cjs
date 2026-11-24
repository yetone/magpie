// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin's page could only tell the reader to copy a package's name and
// paste it into Plugins › Discover › "Have one in mind?" (Token 羊毛's
// 「安装插件」: "页面无法直接跳转安装"). Now a page calls
// magpie.openPlugin(name) and a link is magpie://plugin?name=<package>
// (the window opens on ?view=plugins&plugin=…): either opens the package's
// sheet, with its Install for the reader to press. Neither installs
// anything, a name that isn't an npm package's is refused, and a page
// can't throw sheets up one after another. A package the market doesn't
// list says nobody at magpie reviewed it.
// In Chromium and WebKit, in every language, wide and narrow; the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const bridge = path.resolve(process.env.MAGPIE_BRIDGE || path.join(__dirname, "../pagebridge.js"));

const ZED = "@magpie-community/opencode-zed-auth";
const OTHER = "opencode-someone-else";
const listings = [
  { package: ZED, name: "Zed", icon: "zed", providers: ["zed"], community: true,
    summary: { en: "Claude, GPT, Gemini and Grok hosted by Zed, on your Zed plan." },
    npm: { version: "0.1.9", weekly: 120, license: "MIT", repository: "https://github.com/magpie-community/plugins" } },
];
const TOK = "a".repeat(32);
const pages = [{ spec: "/x/free-tokens", name: "free-tokens", key: "plugin-free-tokens", title: "Token 羊毛", reads: [], url: `/_magpie/plugin/${TOK}/index.html` }];
const html = `<!doctype html><meta charset="utf-8"><script src="_magpie/bridge.js"></script>
<script>window.go = (n) => magpie.openPlugin(n).then((r) => ({ ok: r }), (e) => ({ err: String(e.message) }));</script><p>page</p>`;
const NOTE = {
  en: "nobody at magpie has reviewed it",
  zh: "也没有人审核过它",
  "zh-TW": "也沒有人審核過它",
  ja: "magpie の誰もレビューしていません",
  de: "niemand bei magpie hat es geprüft",
};

function server(lang, asked) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/plugin-pages") return json(pages);
    if (url.pathname === "/api/plugin-panels") return json([]);
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.4.2", plugins: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings });
    if (url.pathname === "/api/plugins/github") return json({ repos: [] });
    if (url.pathname === "/api/plugins/npm") {
      const npm = {};
      for (const n of (url.searchParams.get("names") || "").split(",")) if (n === OTHER) npm[n] = { version: "2.0.0", publisher: "someone", license: "MIT", weekly: 3 };
      return json({ npm });
    }
    if (url.pathname === "/api/plugins/page") return json({ readme: "# " + url.searchParams.get("name") + "\n\nWhat it does.", updated: "2026-11-01T00:00:00Z" });
    if (url.pathname.startsWith("/api/plugins/") && route.request().method() === "POST") { asked.push(url.pathname); return json({}); }
    if (url.pathname.startsWith("/api/")) return json({});
    const m = url.pathname.match(/^\/_magpie\/plugin\/([0-9a-f]{32})\/(.+)$/);
    if (m) {
      const headers = { "X-Content-Type-Options": "nosniff", "Cache-Control": "no-cache" };
      if (m[2] === "_magpie/bridge.js") return route.fulfill({ headers: { ...headers, "Content-Type": "text/javascript" }, body: await fs.readFile(bridge) });
      if (m[2] === "index.html" && m[1] === TOK) return route.fulfill({ headers: { ...headers, "Content-Type": "text/html" }, body: html });
      return route.fulfill({ status: 404, body: "" });
    }
    if (url.host !== "magpie.test" || !/^\/[\w./-]*$/.test(url.pathname)) return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the sheet open, on pkg, inside the window, with an Install that hasn't
// been pressed
async function sheetOn(page, pkg, linked, label) {
  const d = page.locator("#modal .pm-detail");
  await d.waitFor();
  const by = await d.locator(".pm-by").textContent();
  assert.ok(by.includes(pkg), `${label}: the sheet is ${pkg}'s (${by})`);
  await d.locator(".pm-act.get").waitFor();
  assert.equal(await d.locator(".pm-note.warn").filter({ hasText: /magpie/ }).count() > 0, linked, `${label}: ${linked ? "an unlisted package says nobody reviewed it" : "a listed one has no such note"}`);
  const box = await d.boundingBox();
  const w = await page.evaluate(() => innerWidth);
  assert.ok(box.x >= 0 && box.x + box.width <= w + 1, `${label}: the sheet fits the window (${box.x}+${box.width} of ${w})`);
  return d;
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a plugin link and magpie.openPlugin open a package's sheet, and install nothing`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(NOTE)) {
      for (const width of [1100, 480]) {
        const label = `${lang} ${width}px`;
        const asked = [];
        const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        await page.route("**/*", server(lang, asked));

        // magpie://plugin?name=… opens the window here (importlink.go)
        await page.goto("http://magpie.test/?view=plugins&plugin=" + encodeURIComponent(OTHER));
        let d = await sheetOn(page, OTHER, true, label + " link");
        assert.ok((await d.locator(".pm-note.warn").last().textContent()).includes(NOTE[lang]), `${label}: the note is in ${lang}`);
        assert.ok(!(await page.evaluate(() => location.search)).includes("plugin="), `${label}: a reload doesn't open it again`);
        await d.locator(".bar button.text").last().click();
        await page.locator("#modal .pm-detail").waitFor({ state: "detached" });

        // a plugin's page: the sheet comes up over it
        await page.locator("#nav button[data-plugin-page]").waitFor({ state: "attached" });
        await page.evaluate(() => show("plugin-free-tokens"));
        let frame;
        for (let i = 0; i < 100 && !(frame = page.frames().find((x) => x.url().includes(TOK))); i++) await page.waitForTimeout(50);
        assert.ok(frame, `${label}: the page's frame opened`);
        await frame.waitForFunction(() => window.magpie?.theme && window.go);
        // a name that isn't an npm package's: refused, no sheet (each
        // try spends the page's turn, so once, not in every layout)
        for (const bad of lang === "en" && width === 1100 ? ["github:owner/repo", "../../etc", "Zed@latest", "https://evil.test/x.tgz", 42] : []) {
          const r = await frame.evaluate((n) => window.go(n), bad);
          assert.ok(r.err, `${label}: ${bad} refused (${JSON.stringify(r)})`);
          await page.waitForTimeout(1600); // past the gap, so the name is what's refused
        }
        assert.equal(await page.locator("#modal .pm-detail").count(), 0, `${label}: no sheet for a bad name`);
        const r = await frame.evaluate((n) => window.go(n), ZED);
        assert.deepEqual(r, { ok: { opened: true } }, `${label}: openPlugin answers`);
        d = await sheetOn(page, ZED, false, label + " page");
        assert.equal(await page.evaluate(() => view), "plugin-free-tokens", `${label}: the reader stays on the page`);
        // one after another: the second is turned away
        const again = await frame.evaluate((n) => window.go(n), OTHER);
        assert.ok(again.err, `${label}: a second sheet at once is refused`);
        assert.ok((await d.locator(".pm-by").textContent()).includes(ZED), `${label}: the sheet open stays`);
        assert.deepEqual(asked, [], `${label}: nothing was installed`);
        await ctx.close();
      }
    }
  });
}
