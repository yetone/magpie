// Run with Node's test runner and Playwright on the module path; see README.md.
// (#7249, jinpwng on X) A project's Pi packages: a row each with an on/off
// switch and a remove button, the package the project declares itself with
// Adopt, and a field to add one. In English and Chinese.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const DIR = HOME + "/code/app";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const plugin = (name, more) => ({ name, version: "1.0.0", description: "", agents: [], skills: [], mcp: [], hooks: [], subagents: [], ...more });
const lib = (pi, own) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [
    agent("claude", "Claude Code", { projectSkills: ".claude/skills", projectMCP: ".mcp.json", projectSubagents: ".claude/agents", projectHooks: ".claude/settings.local.json" }),
    agent("codex", "Codex", { projectMCP: ".codex/config.toml", projectSubagents: ".codex/agents", projectHooks: ".codex/hooks.json" }),
    agent("pi", "Pi", { projectSkills: ".agents/skills", projectMCP: ".pi/mcp.json" }),
    agent("goose", "Goose"),
  ],
  instructions: { agents: [] }, servers: [], subagents: [], skills: [], foundServers: [], foundSkills: [],
  hooks: [{ name: "bark", event: "turn-end", command: "say bark", agents: [] }], hookAgents: [{ id: "claude", file: `${HOME}/.claude/settings.json`, events: ["Stop"] }, { id: "codex", file: `${HOME}/.codex/hooks.json`, events: ["Stop"] }], plugins: [], claudeInstalled: [{ id: "fmt@acme", on: false }, { id: "lint@acme", on: true }],
  projects: [{ dir: DIR, name: "app", copy: false, skills: {}, placed: [], servers: {}, subagents: {}, plugins: {}, hooks: {}, wrote: {}, pi, own: { pi: own } }],
});

function server(lang, posts) {
  let pi = [], own = [{ name: "npm:pi-own", path: ".pi/settings.json", agent: "pi" }];
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(pi, own) });
    if (url.pathname === "/api/library/projects/pi") {
      const b = req.postDataJSON();
      posts.push(b);
      const name = (s) => s.replace(/^npm:/, "");
      if (b.how === "adopt") { own = own.filter((o) => o.name !== b.name); pi = [...pi, { source: b.name, name: name(b.name), state: "on", adopted: true }]; }
      else if (b.how === "removed") pi = pi.filter((x) => x.source !== b.name);
      else if (pi.some((x) => x.source === b.name)) pi = pi.map((x) => (x.source === b.name ? { ...x, state: b.how } : x));
      else pi = [...pi, { source: b.name, name: name(b.name), state: b.how }];
      return route.fulfill({ json: { ...lib(pi, own), result: { changed: [], problems: [] } } });
    }
    if (url.pathname === "/api/library/native") return route.fulfill({ json: [] });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const scrolled = (page) => page.evaluate(() => [window.scrollX, window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop || e.scrollLeft).map((e) => `${e.className}:${e.scrollTop},${e.scrollLeft}`)].join(" "));
// a click where the element is, as the reader's: Playwright's own click
// first scrolls it into view
const press = async (page, loc) => {
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};
// a chip: the chips fan out under the pointer first, as a reader's hand
// does, and the chip is pressed where it is then
// (a render after the last write may replace it under the pointer, so it
// is found again until it stays)
const box = async (loc) => {
  for (;;) {
    await loc.waitFor({ state: "visible" });
    const b = await loc.boundingBox();
    if (b) return b;
  }
};
const chip = async (page, loc) => {
  const a = await box(loc);
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2);
  await page.waitForTimeout(350);
  const b = await box(loc);
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};


const words = { en: "Pi packages", zh: "Pi 包" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a project's Pi packages are added, switched, adopted and removed", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const posts = [];
        const ctx = await browser.newContext({ viewport: { width: 860, height: 800 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "plugins"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const card = page.locator("#view-library .lib-project");
        await card.waitFor();
        await press(page, card.locator(".lib-projhead"));
        await card.locator(".lib-projpiadd").waitFor();
        assert.equal((await card.locator(".lib-projpi .lib-projownhead").textContent()).trim(), words[lang]);
        // the project's own, with Adopt
        const own = card.locator(".lib-projpiown");
        assert.match(await own.textContent(), /npm:pi-own/);
        // add one: Enter in the field
        const inp = card.locator(".lib-projpiadd input");
        await inp.fill("npm:pi-demo");
        await inp.press("Enter");
        const row = card.locator('.lib-projpirow[data-source="npm:pi-demo"]');
        await row.waitFor();
        assert.deepEqual(posts.at(-1), { dir: DIR, name: "npm:pi-demo", how: "on" });
        // off, then on
        await row.locator(".lib-switch").click();
        await card.locator('.lib-projpirow.off[data-source="npm:pi-demo"]').waitFor();
        assert.equal(posts.at(-1).how, "off");
        await card.locator(".lib-projpirow .lib-switch").click();
        await card.locator('.lib-projpirow:not(.off)[data-source="npm:pi-demo"]').waitFor();
        assert.equal(posts.at(-1).how, "on");
        // adopt the project's own
        await own.locator("button").click();
        await card.locator('.lib-projpirow[data-source="npm:pi-own"]').waitFor();
        assert.equal(posts.at(-1).how, "adopt");
        assert.equal(await card.locator(".lib-projpiown").count(), 0);
        // remove
        await card.locator('.lib-projpirow[data-source="npm:pi-demo"] .lib-icon').click();
        await card.locator('.lib-projpirow[data-source="npm:pi-demo"]').waitFor({ state: "detached" });
        assert.equal(posts.at(-1).how, "removed");
        await ctx.close();
      });
    }
  });
}
