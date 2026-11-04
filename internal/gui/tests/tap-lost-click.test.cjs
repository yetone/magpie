// Run with Node's test runner and Playwright on the module path; see README.md.
// refeccd on Discord: Library > MCP servers > magpie-web-search > Edit, the
// Form | JSON switch: the first click changed the text size but not the
// view; it took a second click. A trackpad's tap lifts a few pixels from
// where it came down: here a press near the top of the option that slides 8px
// up, off the switch, in under 300ms. The view must change on the first one,
// both ways, in every language, in Chromium and WebKit, at 1100 and 390
// wide. No backend: the API is faked.
// refeccd on Discord (third report): Library > MCP servers > magpie-web-search >
// Edit, the Form | JSON switch: on a Mac with Tap to click the first tap only
// shrinks the button (:active) and does nothing; a second tap works. A
// physical press works. No physical trackpad has been used for this test, and
// the engine-level cause is not proven. What is common to the reports is a
// press whose mouseup the page sees and whose click it never does. This test
// makes the engine lose the click of the first tap (a capture listener,
// installed before the page's, swallows the first trusted click that follows
// a mouseup) and holds the page to a click on the control, for the switch and
// for a plain button, with a tap that has no hold at all. No backend: the API
// is faked.
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
  test(engine + ": a tap whose click the engine loses still presses the control", async (t) => {
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
            // the engine that sends the mouseup and no click: the next
            // trusted click after a mouseup is lost, once per arming
            let up = false;
            window.__lose = 0;
            addEventListener("mouseup", (e) => { if (e.isTrusted && window.__lose > 0) up = true; }, true);
            addEventListener("click", (e) => {
              if (!e.isTrusted || !up) return;
              up = false;
              window.__lose--;
              e.stopImmediatePropagation();
            }, true);
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
          // a trackpad's tap: down and up with no hold, the pointer already there
          const tap = async (loc) => {
            const b = await loc.boundingBox();
            await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
            await page.waitForTimeout(100);
            await page.evaluate(() => { window.__lose = 1; });
            await page.mouse.down();
            await page.mouse.up();
          };
          await tap(pick("JSON"));
          await ed.locator("textarea.lib-json").waitFor({ timeout: 1500 });
          assert.equal(await pick("JSON").getAttribute("aria-pressed"), "true", "JSON is picked by the first tap");
          await tap(pick(w("Form")));
          await ed.locator("textarea.lib-json").waitFor({ state: "detached", timeout: 1500 });
          assert.equal(await pick(w("Form")).getAttribute("aria-pressed"), "true", "Form is picked by the first tap");
          // a plain button too: the Cancel at the dialog's foot closes it
          await tap(ed.locator("button", { hasText: new RegExp("^" + w("Cancel") + "$") }).first());
          await page.locator("#modal").waitFor({ state: "hidden", timeout: 1500 });
          await ctx.close();
        });
      }
    }
  });
}
