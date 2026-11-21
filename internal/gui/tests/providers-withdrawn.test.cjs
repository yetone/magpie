// Run with Node's test runner and Playwright on the module path; see README.md.
// Antigravity's plugin is withdrawn (the owner, 11-15: 正在使用的继续正常，
// 加个提示，新用户不让下载): it keeps working for whoever has it, and a
// line over the Providers list tells them Google can suspend the account
// and leads to Antigravity (local agy CLI) on the Plugins page. Without it
// installed, no line. On beside agy-cli signed in with its accounts, the
// line says so and turns it off, not "Use agy-cli" (the owner's
// screenshot, 11-21: agy-cli in use and still told to use it). Every language, Chromium and WebKit, at 1100 and 420
// wide, nothing past the window's edge.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, kept, offs = []) {
  const settings = { lang, theme: "light", lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString() } };
  const full = settings;
  const w = { id: "antigravity", name: "Antigravity", instead: "Antigravity (local agy CLI)", insteadId: "agy-cli", package: "@magpie-community/opencode-agy-cli-auth", spec: "@magpie-community/opencode-antigravity-auth@latest" };
  const withdrawn = () => (kept === "beside" ? (offs.length ? undefined : [{ ...w, beside: true }]) : kept ? [w] : undefined);
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false,textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(full);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings: [{ name: "Antigravity (local agy CLI)", package: "@magpie-community/opencode-agy-cli-auth", providers: ["agy-cli"], community: true, summary: "Your own agy CLI" }] });
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins/off") { offs.push(JSON.parse(req.postData())); return json({ plugins: [] }); }
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "relay", name: "Relay", icon: "generic", host: "relay.example", chat: "https://relay.example/v1", models: [], agents: [], key: { set: true, masked: "sk-…1234" } }], presets: [], excluded: [], withdrawn: withdrawn() });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const fits = (page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: a withdrawn plugin's user is told the risk and led to agy`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const open = async (kept, offs) => {
          const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(6000);
          page.setDefaultNavigationTimeout(60000);
          page.errors = [];
          page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; page.errors.push(e.message); });
          await page.route("**/*", serve(lang, kept, offs));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator("#view-providers .row", { hasText: "Relay" }).first().waitFor();
          return page;
        };
        const tr = (page, s, v) => page.evaluate(([s, v]) => t(s, v), [s, v]);

        // without it: no line
        let page = await open(false);
        assert.equal(await page.locator("#movable .withdrawn").count(), 0, "no line");
        assert.deepEqual(page.errors, []);

        // with it: the risk, translated, and the way to agy-cli
        page = await open(true);
        const line = page.locator("#movable .withdrawn");
        await line.waitFor();
        assert(await page.locator("#movable").isVisible(), "shown");
        const head = await tr(page, "{name}'s plugin can get your account suspended", { name: "Antigravity" });
        assert.equal((await line.locator(".dep-head").textContent()).trim(), head);
        if (lang !== "en") assert.notEqual(head, "Antigravity's plugin can get your account suspended", "translated");
        assert.equal((await line.locator(".dep-why").textContent()).trim(), await tr(page, "Google suspends accounts it sees used outside its own app. {name} runs your own agy instead.", { name: "Antigravity (local agy CLI)" }));
        assert(await fits(page), "fits");
        const go = line.locator("button.action");
        assert.equal((await go.textContent()).trim(), await tr(page, "Use {name}", { name: "Antigravity (local agy CLI)" }));
        const gb = await go.boundingBox(), lb = await line.boundingBox();
        assert(gb.x >= lb.x - 1 && gb.x + gb.width <= lb.x + lb.width + 1, "inside the line");
        await go.click();
        await page.waitForFunction(() => [...document.querySelectorAll("#view-plugins input[type=search]")].some((q) => q.value === "agy"));
        assert.deepEqual(page.errors, []);

        // on beside agy-cli, which has its accounts: said so, and turned off
        const offs = [];
        page = await open("beside", offs);
        const beside = page.locator("#movable .withdrawn");
        await beside.waitFor();
        assert.equal((await beside.locator(".dep-head").textContent()).trim(), await tr(page, "{name}'s plugin is still on beside {instead}", { name: "Antigravity", instead: "Antigravity (local agy CLI)" }));
        assert.equal((await beside.locator(".dep-why").textContent()).trim(), await tr(page, "Google suspends accounts it sees used outside its own app. {instead} has your accounts now, so {name}'s plugin can be turned off.", { name: "Antigravity", instead: "Antigravity (local agy CLI)" }));
        if (lang !== "en") assert.notEqual(await tr(page, "Turn off {name}'s plugin", { name: "Antigravity" }), "Turn off Antigravity's plugin", "translated");
        assert(await fits(page), "fits beside");
        const off = beside.locator("button.action");
        assert.equal((await off.textContent()).trim(), await tr(page, "Turn off {name}'s plugin", { name: "Antigravity" }));
        await off.click();
        await page.locator("#movable .withdrawn").waitFor({ state: "detached" });
        assert.deepEqual(offs, [{ spec: "@magpie-community/opencode-antigravity-auth@latest", off: true }]);
        assert.deepEqual(page.errors, []);
      });
    }
  }
}
