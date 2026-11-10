// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Network and sharing › Another magpie (xuanwo on X: a magpie on a
// server, its routing and usage, opened from the desktop app). The address
// and the key are posted to /api/remote, which opens the window; an error
// from it is said in the page's language; once kept, the key field shows it
// is kept (never the key) and Forget posts /api/remote/forget. A browser on
// the page (web) has no such section and never asks for it. Nothing wider
// than a 360px window. Every language, Chromium and WebKit; no backend, the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const KEY = "0123456789abcdef0123456789abcdef";

function server(lang, web, posts, gets) {
  let kept = { url: "", hasKey: false };
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web},sideGateway:{url:"http://127.0.0.1:3425",running:true,boot:true}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, web });
    if (url.pathname === "/api/remote") {
      if (req.method() === "GET") { gets.n++; return json(kept); }
      const body = JSON.parse(req.postData());
      posts.push(body);
      if (body.key === "wrong-wrong-wrong-wrong") return json({ error: "that magpie turned the key away: use the key in the link it printed (MAGPIE_WEB_KEY, or the k= in it)" }, 400);
      if (/^http:\/\/magpie\.example\.com/.test(body.url)) return json({ error: "magpie.example.com is a name on the internet: open it over https://, or the key crosses the network unencrypted" }, 400);
      kept = { url: body.url.replace(/\?k=.*/, ""), hasKey: true };
      return json(kept);
    }
    if (url.pathname === "/api/remote/forget") { posts.push("forget"); kept = { url: "", hasKey: false }; return json(kept); }
    if (url.pathname === "/api/web-page") return json({ mode: "", port: 3430, serving: false });
    if (url.pathname === "/api/settings") return json({ lang, theme: "light", gateway: "http://127.0.0.1:3425", lan: false, lanURLs: [], version: "0.1.900", dir: "/config/magpie", fx: { rate: 7.2, at: new Date().toISOString(), stale: false } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Another magpie opens a server's magpie with its key", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-another-magpie-${i}.png`), fullPage: true });
      }
      await browser.close();
    });

    const open = async (lang, web, width = 900) => {
      const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
      pages.push(page);
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [], gets = { n: 0 };
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, web, posts, gets));
      await watchSettingsReads(page);
      await page.goto("http://localhost:3430/?view=settings&tab=network");
      await settingsDrawn(page);
      const tr = (s, vars) => page.evaluate(([s, vars]) => t(s, vars), [s, vars]);
      return { page, errors, posts, gets, tr };
    };
    // posts are counted, not a time waited
    const until = async (page, f) => { for (let i = 0; i < 250 && !f(); i++) await page.waitForTimeout(20); assert(f()); };

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const { page, errors, posts, tr } = await open(lang, false);
        const head = page.locator("#remoteHead");
        const list = page.locator("#remoteList");
        await list.locator(".row.pref").first().waitFor();
        assert.equal((await head.textContent()).trim(), await tr("Another magpie"));
        if (lang !== "en") assert.notEqual(await tr("Another magpie"), "Another magpie", "translated");
        assert.equal(await list.locator(".row.pref").count(), 2);
        const addr = list.locator("input.remote-url"), key = list.locator("input.remote-key");
        assert.equal(await key.getAttribute("type"), "password");
        assert.equal(await list.getByRole("button", { name: await tr("Forget") }).count(), 0, "nothing kept: no Forget");

        // a name on the internet over http: said in the page's language
        await reach(page, addr);
        await addr.fill("http://magpie.example.com:3430");
        await key.fill(KEY);
        const openBtn = list.getByRole("button", { name: await tr("Open"), exact: true });
        await openBtn.click();
        await page.locator("#status").filter({ hasText: await tr("{host} is a name on the internet: open it over https://, or the key crosses the network unencrypted", { host: "magpie.example.com" }) }).waitFor();
        // a wrong key
        await addr.fill("https://magpie.example.com");
        await key.fill("wrong-wrong-wrong-wrong");
        await openBtn.click();
        await page.locator("#status").filter({ hasText: await tr("That magpie turned the key away: use the key in the link it printed") }).waitFor();
        // the link it printed, with Enter
        await key.fill("");
        await addr.fill("https://magpie.example.com/?k=" + KEY);
        await addr.press("Enter");
        await until(page, () => posts.some((p) => p.url === "https://magpie.example.com/?k=" + KEY));
        assert.deepEqual(posts.at(-1), { url: "https://magpie.example.com/?k=" + KEY, key: "" });
        const forget = list.getByRole("button", { name: await tr("Forget") });
        await forget.waitFor();
        assert.equal(await addr.inputValue(), "https://magpie.example.com/", "kept without its key");
        assert.equal(await key.inputValue(), "", "the key is never put back");
        assert.equal(await key.getAttribute("placeholder"), await tr("Kept"));
        assert.equal(await page.locator("#setPage-network select").count(), 0, "no native select");
        await forget.click();
        await until(page, () => posts.includes("forget"));
        await page.waitForFunction(() => !document.querySelector("#remoteList input.remote-url").value);
        assert.deepEqual(errors, []);
      });
    }

    await t.test("360px wide, German", async () => {
      const { page, errors, tr } = await open("de", false, 360);
      const list = page.locator("#remoteList");
      await list.locator("input.remote-url").waitFor();
      await list.locator("input.remote-url").fill("https://magpie.example.com/?k=" + KEY);
      await list.getByRole("button", { name: await tr("Open"), exact: true }).click();
      await list.getByRole("button", { name: await tr("Forget") }).waitFor();
      const over = await list.evaluate((l) => [...l.querySelectorAll(".row.pref")].filter((r) => r.scrollWidth > r.clientWidth + 1 || r.getBoundingClientRect().right > innerWidth + 1).map((r) => r.textContent.slice(0, 40)));
      assert.deepEqual(over, [], "rows wider than the window");
      assert.deepEqual(errors, []);
    });

    await t.test("a browser on the page has no Another magpie", async () => {
      const { page, errors, gets } = await open("en", true);
      await page.locator("#portList .row").first().waitFor();
      assert.equal(await page.locator("#remoteHead").isVisible(), false);
      assert.equal(await page.locator("#remoteList .row").count(), 0);
      assert.equal(gets.n, 0, "never asks /api/remote");
      assert.deepEqual(errors, []);
    });
  });
}
