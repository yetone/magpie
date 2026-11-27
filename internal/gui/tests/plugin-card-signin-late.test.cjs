// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider plugin installed from its Discover card offers its sign-in on
// that card without the page being opened again (the owner, 11-27: 「已安装
// 之后，能否在这里直接显示去登录或者是否登录的按钮？」). The host lists a new
// plugin's subscriptions in the background (plugin.Cached), so the reads
// right after the install have none and say the plugins haven't answered
// (named false); the card said Installed until the page was opened again.
// The page now asks again till they have, and the card becomes Sign in,
// which opens the plugin's sign-in in the Providers add sheet. A GitHub
// repository, as the owner's was, in every language, wide and narrow, both
// engines; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const SPEC = "github:someone/today-plugin";
const repo = { spec: SPEC, repo: "someone/today-plugin", url: "https://github.com/someone/today-plugin", owner: "someone",
  description: "Today as a magpie provider.", stars: 2, kind: "provider" };

function server(lang, asked) {
  const installed = [];
  const subs = [];
  // reads of the providers left before the plugins answer
  let unanswered = 0;
  const answered = () => unanswered <= 0;
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") {
      unanswered--;
      return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: answered() ? subs : [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    const state = () => ({ bun: true, bunVersion: "1.3.0",
      plugins: installed.map((e) => ({ ...e, named: answered(), providers: answered() ? ["Today"] : [] })) });
    if (url.pathname === "/api/plugins") return json(state());
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins/market") return json({ listings: [], state: state() });
    if (url.pathname === "/api/plugins/github") return json({ repos: [repo], topic: "magpie-plugin" });
    if (url.pathname === "/api/plugins/npm") return json({ npm: {} });
    if (url.pathname === "/api/plugins/add") {
      asked.push(["add", route.request().postDataJSON()]);
      installed.push({ spec: SPEC, version: "0.1.1", commit: "abc1234", head: "abc1234" });
      subs.push({ id: "today-plugin", pid: "today", name: "Today", spec: SPEC, signedIn: false, models: 2,
        methods: [{ type: "api", label: "Phone and SMS code" }] });
      // the refresh after the install and two of the page's asks find none
      unanswered = 3;
      return json({});
    }
    if (url.pathname === "/api/plugin-signin/prompt") { asked.push(["signin", route.request().postDataJSON()]); return json({}); }
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.hostname !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a provider plugin installed from its card offers Sign in there once the plugins answer", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1180, 440]) {
      await t.test(`${lang} ${width}`, async () => {
        const asked = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(10000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.addInitScript(() => { try { localStorage.setItem("magpie.pluginTab", "discover"); } catch {} });
        await page.route("**/*", server(lang, asked));
        await page.goto("http://magpie.test/?view=plugins");
        const card = page.locator(`#view-plugins .pm-card[data-pkg="${SPEC}"]`).first();
        await card.locator(".pm-act.get").click();
        for (let i = 0; i < 100 && !asked.some(([k]) => k === "add"); i++) await new Promise((r) => setTimeout(r, 20));
        assert.deepEqual(asked.find(([k]) => k === "add")[1], { spec: SPEC });
        // Installed while the plugins haven't answered, then Sign in by itself
        await card.locator(".pm-act.go").waitFor({ timeout: 8000 });
        assert.equal(await card.locator(".pm-act.done").count(), 0);
        await card.locator(".pm-act.go").click();
        for (let i = 0; i < 100 && !asked.some(([k]) => k === "signin"); i++) await new Promise((r) => setTimeout(r, 20));
        // the plugin's own sign-in, in the Providers add sheet
        assert.equal(asked.find(([k]) => k === "signin")?.[1]?.provider, "today");
        await page.locator("#view-providers #addSheet").waitFor();
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
