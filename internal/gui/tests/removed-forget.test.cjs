// Run with Node's test runner and Playwright on the module path; see README.md.
// #694: a subscription removed from magpie sat under "Removed from magpie —
// still signed in" with Add it back as its only way out, so its account
// could never go: added back, the old account came with it. Its row now
// has Add it back and Sign out… side by side (mintonight: no menu to open
// first, two buttons and no "Don't show here"); Sign out… asks first and
// then has the backend sign its accounts out (POST /api/provider/forget),
// the page left where it was, no native select. The Providers page's
// reminder line offers Sign out too. English and Chinese; the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const qoderCN = { id: "qoder-cn", name: "Qoder CN", icon: "qoder", models: [], agents: [], key: {}, account: { agent: "qoder-cn", agentName: "Qoder CN", agentIcon: "qoder", user: "b@q", logins: [{ user: "b@q", active: true, on: true }] } };
// the removed Qoder, named by its provider as the backend now names it
const removed = (quiet) => ({ agent: "plugin", provider: "qoder-plugin", name: "Qoder", why: "You removed it from magpie.", quiet, agentName: "Qoder", agentIcon: "qoder" });

function server(lang, asked, quiet) {
  let gone = false;
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const state = () => ({ providers: [qoderCN], presets: [], excluded: gone ? [] : [removed(quiet)], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(state());
    if (url.pathname.startsWith("/api/provider/")) {
      asked.push([url.pathname.slice("/api/provider/".length), route.request().postDataJSON()]);
      if (url.pathname.endsWith("/forget")) gone = true;
      return json(state());
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { back: "Add it back", out: "Sign out…", ask: "Sign Qoder out of magpie?", go: "Sign out", line: "Sign out" },
  zh: { back: "加回来", out: "退出登录…", ask: "在 magpie 里退出 Qoder？", go: "退出登录", line: "退出登录" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a removed subscription can be signed out for good", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang + ": the add sheet's row", async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 520 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, asked, true));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#addProvider").click();
        const sheet = page.locator("#addSheet");
        const row = sheet.locator(`.tile[data-pick="removed:qoder-plugin"]`);
        await row.waitFor();
        await sheet.evaluate((el) => Promise.all(el.getAnimations().map((a) => a.finished)));
        assert.equal((await row.locator(".n").innerText()).trim(), "Qoder", "named by its provider, not \"plugin\"");

        // its two ways out, side by side on the row: nothing else, no menu
        const buttons = row.locator("button");
        assert.deepEqual((await buttons.allInnerTexts()).map((s) => s.trim()), [w.back, w.out]);
        const [a, b] = [await buttons.nth(0).boundingBox(), await buttons.nth(1).boundingBox()];
        assert.ok(Math.abs(a.y - b.y) < 2 && b.x > a.x + a.width - 1, "the buttons sit side by side");
        assert.equal(await page.locator("select").count(), 0);
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `removed-forget-${engine}-${lang}.png`) });

        // Sign out asks first, then signs it out by its provider id; the
        // click leaves the page where it was
        await row.scrollIntoViewIfNeeded();
        const y = await page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join(","));
        await buttons.nth(1).click();
        const ask = page.locator("#modal .forget-ask");
        await ask.waitFor();
        assert.match(await ask.locator(".ehead b").innerText(), new RegExp(w.ask.replace(/[?？]/g, ".")));
        assert.deepEqual(asked, [], "nothing signed out before it is confirmed");
        assert.equal(await page.locator(".proto-menu").count(), 0, "no menu on the way");
        assert.equal(await page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join(",")), y, "the click moved the page");
        await ask.locator("button", { hasText: new RegExp("^" + w.go + "$") }).click();
        await page.locator("#modal").waitFor({ state: "hidden" });
        assert.deepEqual(asked, [["forget", { id: "qoder-plugin" }]]);
        assert.deepEqual(errors, []);
      });

      await t.test(lang + ": Add it back is still there", async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 700 } })).newPage();
        page.setDefaultTimeout(5000);
        const asked = [];
        await page.route("**/*", server(lang, asked, true));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#addProvider").click();
        const row = page.locator(`#addSheet .tile[data-pick="removed:qoder-plugin"]`);
        await row.locator("button", { hasText: new RegExp("^" + w.back + "$") }).click();
        await new Promise((r) => setTimeout(r, 200));
        assert.equal(await page.locator(".proto-menu").count(), 0, "one click, no menu");
        assert.deepEqual(asked, [["show", { id: "qoder-plugin" }]]);
      });

      await t.test(lang + ": the Providers page's reminder", async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 700 } })).newPage();
        page.setDefaultTimeout(5000);
        const asked = [];
        await page.route("**/*", server(lang, asked, false));
        await page.goto("http://magpie.test/?view=providers");
        const line = page.locator("#excluded .excluded");
        await line.waitFor();
        assert.match(await line.innerText(), /Qoder/);
        await line.locator("button.link", { hasText: new RegExp("^" + w.line + "$") }).click();
        const ask = page.locator("#modal .forget-ask");
        await ask.waitFor();
        await ask.locator("button", { hasText: new RegExp("^" + w.go + "$") }).click();
        await page.locator("#modal").waitFor({ state: "hidden" });
        assert.deepEqual(asked, [["forget", { id: "qoder-plugin" }]]);
      });
    }
  });
}
