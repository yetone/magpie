// Run with Node's test runner and Playwright on the module path; see README.md.
// refeccd on Discord: Library > MCP servers > magpie-web-search > Edit, the
// Form | JSON switch: the first click changed the text size but not the
// view; it took a second click. A trackpad's tap lifts a few pixels from
// where it came down: here a press near the top of the option that slides 8px
// up, off the switch, in under 300ms. The view must change on the first one,
// both ways, in every language, in Chromium and WebKit, at 1100 and 390
// wide. No backend: the API is faked.
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
  test(engine + ": the Form | JSON switch takes the first press", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 390]) {
        await t.test(`${lang} ${width}`, async () => {
          const w = (s) => tr(lang, s);
          const ctx = await browser.newContext({ viewport: { width, height: 800 } });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
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
          // a press like a trackpad tap: down, a slide of a few px, up soon
          const press = async (loc, slide) => {
            const b = await loc.boundingBox();
            const x = b.x + b.width / 2, y = b.y + 3;
            await page.mouse.move(x, y);
            await page.mouse.down();
            await page.waitForTimeout(60);
            if (slide) await page.mouse.move(x, y - slide);
            await page.waitForTimeout(40);
            await page.mouse.up();
          };
          for (const slide of [0, 8]) {
            await press(pick("JSON"), slide);
            await ed.locator("textarea.lib-json").waitFor({ timeout: 1500 });
            assert.equal(await pick("JSON").getAttribute("aria-pressed"), "true", "JSON is picked at the first press, slide " + slide);
            await press(pick(w("Form")), slide);
            await ed.locator("textarea.lib-json").waitFor({ state: "detached", timeout: 1500 });
            assert.equal(await pick(w("Form")).getAttribute("aria-pressed"), "true", "Form is picked at the first press, slide " + slide);
          }
          await ctx.close();
        });
      }
    }
  });
}
