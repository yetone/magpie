// Run with Node's test runner and Playwright on the module path; see README.md.
// refeccd on Discord (fourth report, magpie 0.1.3014, macOS 27.2): with Tap
// to click, a tap inside a dialog, a sheet or a menu almost never takes, in
// the tray panel and the window. WebKit before bug 329642 (fixed upstream
// 2026-11-04, "Dispatch mouse events to the page in the order sent to the
// input method") gives the page each mouse event as the input method answers
// for it, and while a text field has focus (a dialog focuses its first one)
// some answer out of order, so a tap, its two events a few ms apart, comes
// let go first and then pressed: no click, and the next tap's release pairs
// with the left press. Playwright sends the same order here (mouse.up, then
// mouse.down), and the page must click the tapped control once a tap, never
// twice, while a tap in order, and a press in order after a stray release,
// still click once. Every language, Chromium and WebKit, 1100 and 390 wide.
// No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings();
const tr = (lang, s) => (lang === "en" ? s : I18N[lang][s]) || s;
const HOME = "/Users/rf";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });

function server(lang, web) {
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: ["claude:Claude Code","codex:Codex","gemini:Gemini CLI","cursor:Cursor","opencode:OpenCode","kimi:Kimi Code","droid:Droid","amp:Amp","qwen:Qwen Code","crush:Crush","pi:Pi","zed:Zed","trae:Trae","iflow:iFlow","goose:Goose","cline:Cline","roo:Roo","kilo:Kilo","aider:Aider","warp:Warp"].map((x) => agent(...x.split(":"))), mcpHub: true, hubPath: "/mcp",
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
    servers: [{ name: "magpie-web-search", transport: "stdio", command: "/Applications/magpie.app/Contents/MacOS/magpie", args: ["mcp", "search"], env: {}, icon: "https://usemagpie.ai/favicon.png", agents: ["claude", "codex", "gemini"] }],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp/check") return route.fulfill({ json: { servers: {} } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    if (url.hostname === "usemagpie.ai") {
      return route.fulfill({ body: await fs.readFile(path.join(assets, "favicon.png")).catch(() => Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64")), contentType: "image/png" });
    }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a tap that comes let go before it is pressed clicks its control once", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 390]) {
        await t.test(`${lang} ${width}`, async () => {
          const w = (s) => tr(lang, s);
          const ctx = await browser.newContext({ viewport: { width, height: 800 } });
          await ctx.addInitScript(() => {
            try { localStorage.setItem("magpie.libTab", "mcp"); } catch {}
            window.__clicks = 0;
            document.addEventListener("click", (e) => { if (e.target.closest?.("#modal")) window.__clicks++; });
          });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          await page.route("**/*", server(lang, width < 600));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          const ed = page.locator("#modal .lib-editor");
          await page.locator("#view-library .lib-server .name", { hasText: "magpie-web-search" }).click();
          await ed.waitFor();
          await page.waitForTimeout(600); // the dialog has stopped growing in
          const pick = (name) => ed.locator(".lib-viewpick .opt", { hasText: new RegExp("^" + name + "$") });
          const json = ed.locator("textarea.lib-json");
          // one tap, its events in the order given, and the clicks the dialog saw
          const tap = async (loc, order) => {
            const b = await loc.boundingBox();
            await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
            await page.waitForTimeout(50);
            await page.evaluate(() => { window.__clicks = 0; });
            for (const step of order) step === "hold" ? await page.waitForTimeout(20) : await page.mouse[step]();
            await page.waitForTimeout(400);
            return page.evaluate(() => window.__clicks);
          };
          const view = async (on, why) => {
            assert.equal(await pick(on === "JSON" ? "JSON" : w("Form")).getAttribute("aria-pressed"), "true", why);
            assert.equal(await json.count(), on === "JSON" ? 1 : 0, why);
          };
          // in order, as a physical press sends it
          assert.equal(await tap(pick("JSON"), ["down", "up"]), 1, "a tap in order clicks once");
          await view("JSON", "a tap in order picks JSON");
          assert.equal(await tap(pick(w("Form")), ["down", "up"]), 1, "a tap in order clicks once");
          await view("Form", "a tap in order picks Form");
          // a stray release, then a press let go in order: the browser's one click
          assert.equal(await tap(pick("JSON"), ["up", "down", "hold", "up"]), 1, "a press in order after a stray release clicks once");
          await view("JSON", "a press in order after a stray release picks JSON");
          // and on what is no control, the dialog's own padding: the browser's
          // click is kept, not dropped for one the page would make
          const box = await ed.boundingBox();
          await page.mouse.move(box.x + 4, box.y + 4);
          await page.waitForTimeout(50);
          await page.evaluate(() => { window.__clicks = 0; window.__at = ""; document.addEventListener("click", (e) => { window.__at = e.target.closest('button, a[href], summary, label, .opt, [role="button"], [role="tab"], [role="switch"], [role="checkbox"], [role="radio"], [role="menuitem"], [role="option"]') ? "a control" : "no control"; }, { once: true }); });
          await page.mouse.up();
          await page.mouse.down();
          await page.waitForTimeout(20);
          await page.mouse.up();
          await page.waitForTimeout(400);
          assert.deepEqual(await page.evaluate(() => [window.__clicks, window.__at]), [1, "no control"], "a press in order after a stray release on no control clicks it once");
          // let go, then pressed, three times: each picks its option, once
          assert.equal(await tap(pick(w("Form")), ["up", "down"]), 1, "the first tap let go first clicks Form once");
          await view("Form", "the first tap let go first picks Form");
          assert.equal(await tap(pick("JSON"), ["up", "down"]), 1, "a second such tap clicks JSON once, not where the two meet as well");
          await view("JSON", "a second such tap picks JSON");
          assert.equal(await tap(pick(w("Form")), ["up", "down"]), 1, "a third such tap clicks Form once");
          await view("Form", "a third such tap picks Form");
          // a plain button too: the Cancel at the dialog's foot closes it
          await tap(ed.locator("button", { hasText: new RegExp("^" + w("Cancel") + "$") }).first(), ["up", "down"]);
          await page.locator("#modal").waitFor({ state: "hidden", timeout: 1500 });
          await ctx.close();
        });
      }
    }
  });
}
