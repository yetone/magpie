// Run with Node's test runner and Playwright on the module path; see README.md.
// #300 (emo172): a server taken out of the Library kept "✓ Added" on its
// Discover card until the window lost and got back focus — the removal
// redrew the library from its answer but not the market, whose marks are
// read once. Any change to what the library has now clears the mark of one
// gone at once and asks the market again: removing a server or a skill,
// adding one by hand. In English and Chinese. No backend: the API is faked
// here, and nothing focuses the window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/tester";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const pw = { name: "playwright", transport: "stdio", command: "npx", args: ["-y", "@playwright/mcp@latest"], agents: ["claude"] };
const pdf = { name: "pdf", kind: "folder", description: "PDFs", agents: ["claude"], source: `${HOME}/skills/pdf` };

function serve(lang, lib, calls) {
  const view = (result) => ({
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [agent("claude", "Claude Code", "claudecode-color")],
    instructions: { agents: [], sets: [] }, servers: structuredClone(lib.servers), foundServers: [], projects: [], foundSkills: [],
    skills: structuredClone(lib.skills), problems: [], ...(result ? { result } : {}),
  });
  const have = (list, name) => (list.some((x) => x.name === name) ? name : "");
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") { calls.push("library"); return json(view()); }
    if (url.pathname === "/api/library/market/servers") {
      calls.push("market/servers");
      return json({ items: [
        { id: "playwright", name: "playwright", title: "Playwright", publisher: "Microsoft", description: "Drive a real browser.", transport: "stdio", runs: "npx", featured: true, have: have(lib.servers, "playwright") },
        { id: "fetch", name: "fetch", title: "Fetch", description: "Read the web.", transport: "stdio", runs: "uvx", featured: true, have: have(lib.servers, "fetch") },
      ] });
    }
    if (url.pathname === "/api/library/market/skills") {
      calls.push("market/skills");
      return json({ items: [{ id: "acme/skills/pdf", name: "pdf", source: "acme/skills", skillId: "pdf", installs: 1200, description: "PDFs", have: have(lib.skills, "pdf") }] });
    }
    if (url.pathname === "/api/library/servers/remove") {
      const { name } = req.postDataJSON();
      lib.servers = lib.servers.filter((x) => x.name !== name);
      return json(view({ changed: ["claude"], problems: [] }));
    }
    if (url.pathname === "/api/library/servers/save") {
      const { server } = req.postDataJSON();
      lib.servers.push(server);
      return json(view({ changed: ["claude"], problems: [] }));
    }
    if (url.pathname === "/api/library/skills/remove") {
      const { name } = req.postDataJSON();
      lib.skills = lib.skills.filter((x) => x.name !== name);
      return json(view({ changed: ["claude"], problems: [] }));
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const words = {
  en: { remove: "Remove from the library", removeSkill: "Remove", addServer: "＋ Add a server", name: "Name", add: "Add" },
  zh: { remove: "从资源库移除", removeSkill: "移除", addServer: "＋ 添加服务器", name: "名称", add: "添加" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the market's marks follow the library`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [], calls = [];
      const lib = { servers: [structuredClone(pw)], skills: [structuredClone(pdf)] };
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, lib, calls));
      await page.addInitScript(() => { localStorage.setItem("magpie.libTab", "mcp"); });
      // nothing here may count on the window being focused again
      await page.addInitScript(() => {
        window.__focus = 0;
        window.addEventListener("focus", (e) => { if (e.target === window) window.__focus++; }, true);
        document.addEventListener("visibilitychange", () => window.__focus++);
      });
      t.after(() => browser.close());
      await page.goto("http://magpie.test/?view=library");
      const view = page.locator("#view-library");
      const card = (kind, id) => view.locator(`.mk[data-market="${kind}"] .mk-card[data-id="${id}"]`);
      await card("mcp", "playwright").locator(".mk-have").waitFor();
      await card("mcp", "fetch").locator(".mk-add").waitFor();

      await t.test("a server removed is no longer marked added", async () => {
        const top = await view.evaluate((e) => e.scrollTop);
        const before = calls.filter((c) => c === "market/servers").length;
        await view.locator(".lib-row", { hasText: "playwright" }).click();
        await page.getByRole("button", { name: words[lang].remove, exact: true }).click();
        await page.locator("dialog.action-confirm[open] button").last().click();
        await card("mcp", "playwright").locator(".mk-add").waitFor({ timeout: 2000 });
        assert.equal(await card("mcp", "playwright").locator(".mk-have").count(), 0);
        assert.equal(await card("mcp", "playwright").evaluate((e) => e.classList.contains("have")), false);
        assert(calls.filter((c) => c === "market/servers").length > before, "the market was not asked again");
        assert.equal(await view.evaluate((e) => e.scrollTop), top, "the click moved the page");
      });

      await t.test("a server added by hand is marked added", async () => {
        // the library has none now: its empty state's button
        await view.getByRole("button", { name: words[lang].addServer, exact: true }).click();
        const ed = page.locator(".lib-editor");
        await ed.locator('input[placeholder="e.g. github"], input[placeholder="例如 github"]').fill("fetch");
        await ed.locator('input[placeholder="npx -y @modelcontextprotocol/server-github"]').fill("uvx mcp-server-fetch");
        await ed.getByRole("button", { name: words[lang].add, exact: true }).click();
        await card("mcp", "fetch").locator(".mk-have").waitFor({ timeout: 2000 });
      });

      await t.test("a skill removed is no longer marked added", async () => {
        await view.locator(".lib-tabs .opt").nth(2).click();
        await card("skills", "acme/skills/pdf").locator(".mk-have").waitFor();
        await view.locator(".lib-row", { hasText: "pdf" }).locator(".lib-icon.danger").click();
        await page.locator(".lib-editor").getByRole("button", { name: words[lang].removeSkill, exact: true }).click();
        await card("skills", "acme/skills/pdf").locator(".mk-add").waitFor({ timeout: 2000 });
      });

      assert.equal(await page.evaluate(() => window.__focus), 0, "the window was focused again");
      assert.deepEqual(errors, []);
    });
  }
}
