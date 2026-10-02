// Run with Node's test runner and Playwright on the module path; see README.md.
// The Plugins page is drawn in parts (#488): Installed from /api/plugins at
// once, Discover's cards as soon as the plugins suggested are there, and
// what npm says of them (versions, downloads, an update out) after, from a
// request of its own — never waiting on the whole market, which this test
// never answers. English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ZED = "@magpie-community/opencode-zed-auth";

function server(lang, asked, npmHeld) {
  const installed = [{ spec: "opencode-copilot-auth", providers: ["GitHub Copilot"], version: "0.0.7", moved: [] }];
  const listings = [
    { package: ZED, name: "Zed", icon: "zed", providers: ["zed"], community: true, summary: { en: "Zed's plan.", zh: "Zed 的套餐。" } },
    { package: "opencode-copilot-auth", name: "Copilot", icon: "githubcopilot", providers: ["github-copilot"], community: true, summary: { en: "Copilot.", zh: "Copilot。" } },
  ];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/plugins")) asked.push(url.pathname + url.search);
    // the whole market, npm and all, is slow: it never answers here
    if (url.pathname === "/api/plugins/market") return new Promise(() => {});
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.3.0", plugins: installed, movable: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings });
    if (url.pathname === "/api/plugins/npm") {
      await npmHeld;
      return json({ npm: { [ZED]: { version: "0.2.0", weekly: 1234 }, "opencode-copilot-auth": { version: "0.0.9", weekly: 52000 } } });
    }
    if (url.pathname === "/api/plugins/updates") return json({ waiting: [], updated: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { installed: "Installed", discover: "Discover", install: "Install", week: "1.2k/week", out: "v0.0.9 out" },
  zh: { installed: "已安装", discover: "发现", install: "安装", week: "1.2k/周", out: "v0.0.9 可用" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Plugins page in parts", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const asked = [];
        let release;
        const npmHeld = new Promise((r) => { release = r; });
        const page = await (await browser.newContext({ viewport: { width: 980, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, asked, npmHeld));

        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        await view.waitFor({ state: "visible" });

        // Installed, while npm (and the whole market) hasn't answered
        await view.locator(".lib-tabs .opt", { hasText: w.installed }).click();
        const row = view.locator(".pm-row").first();
        await row.waitFor();
        assert.equal((await row.locator(".name span").first().innerText()).trim(), "Copilot");
        assert.match(await row.innerText(), /v0\.0\.7/);
        assert.equal(await view.locator(".pm-card.ghost").count(), 0, "no skeleton left");
        assert.equal(await row.locator(".pm-chip.up").count(), 0, "no update until npm says so");

        // Discover: the cards, their buttons waiting on npm
        await view.locator(".lib-tabs .opt", { hasText: w.discover }).click();
        const card = view.locator(`.pm-card[data-pkg="${ZED}"]`);
        await card.waitFor();
        assert.equal(await card.locator(".pm-dl").count(), 0);
        const act = card.locator(".pm-act");
        assert.equal(await act.isDisabled(), true, "the button waits for npm");
        assert.equal((await act.innerText()).trim(), "");

        // npm answers: downloads, the version, Install, and the update out
        release();
        await card.locator(".pm-dl", { hasText: w.week }).waitFor();
        assert.equal((await card.locator(".pm-ver").innerText()).trim(), "v0.2.0");
        assert.equal((await act.innerText()).trim(), w.install);
        assert.equal(await act.isDisabled(), false);
        await view.locator(".lib-tabs .opt", { hasText: w.installed }).click();
        await view.locator(".pm-row .pm-chip.up", { hasText: w.out }).waitFor();

        assert.ok(!asked.includes("/api/plugins/market"), "the whole market isn't asked for: " + asked.join(" "));
        const npm = asked.find((a) => a.startsWith("/api/plugins/npm?"));
        assert.ok(npm, "npm is asked apart");
        assert.deepEqual(new URLSearchParams(npm.split("?")[1]).get("names").split(",").sort(), [ZED, "opencode-copilot-auth"].sort());
        assert.deepEqual(errors, []);
      });
    }
  });
}
