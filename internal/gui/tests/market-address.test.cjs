// Run with Node's test runner and Playwright on the module path; see README.md.
// lc on Discord: https://github.com/crystaldba/postgres-mcp pasted into
// Library › MCP › Discover said only "Nothing matches". An address the market
// finds nothing at now says so — over the servers found by its name, when
// there are any — and offers to add it by hand, the form filled in with what
// the address says: the repository's name, an endpoint's URL. A search that
// isn't an address is as it was. In English and Chinese. No backend: the API
// is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/tester";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const someone = { id: "io.github.someone/postgres-mcp", name: "postgres", title: "postgres", publisher: "someone", description: "Another Postgres server.", transport: "stdio", runs: "npx" };

function serve(lang, asked) {
  const view = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [agent("claude", "Claude Code", "claudecode-color"), agent("codex", "Codex", "codex-color")],
    instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") return json(view);
    if (url.pathname === "/api/library/market/servers") {
      const q = url.searchParams.get("q");
      asked.push(q);
      if (q.includes("crystaldba")) return json({ items: [someone], custom: { name: "postgres", transport: "stdio" } });
      if (q.includes("nowhere")) return json({ items: [], custom: { name: "nowhere", transport: "http", url: q } });
      if (q === "zzz") return json({ items: [] });
      return json({ items: [{ id: "fetch", name: "fetch", title: "Fetch", description: "Read the web.", transport: "stdio", runs: "uvx", featured: true }] });
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
  en: { add: "＋ Add it yourself…", byName: "None of these is at", none: "Nothing listed is at", nothing: "Nothing matches “zzz”.", cancel: "Cancel" },
  zh: { add: "＋ 自己添加…", byName: "以下都不在", none: "没有列出位于", nothing: "没有与“zzz”匹配的结果。", cancel: "取消" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: an address nothing is listed at can be added by hand`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [], asked = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, asked));
      await page.addInitScript(() => { localStorage.setItem("magpie.libTab", "mcp"); });
      t.after(() => browser.close());
      await page.goto("http://magpie.test/?view=library");
      const view = page.locator("#view-library");
      const box = view.locator('.mk[data-market="mcp"]');
      const search = box.locator('input[data-lib="market-mcp"]');
      await box.locator('.mk-card[data-id="fetch"]').waitFor();
      const w = words[lang];
      const ed = page.locator(".lib-editor");

      await t.test("a repository: the servers by its name, and one to add named for it", async () => {
        const q = "https://github.com/crystaldba/postgres-mcp";
        await search.fill(q);
        await search.press("Enter");
        const msg = box.locator(".mk-custom");
        await msg.waitFor();
        assert.match(await msg.textContent(), new RegExp(w.byName));
        assert.ok((await msg.textContent()).includes(q));
        assert.equal(await box.locator(`.mk-card[data-id="${someone.id}"]`).count(), 1, "the ones found by its name are shown");
        assert.equal(asked.at(-1), q);
        await msg.getByRole("button", { name: w.add, exact: true }).click();
        await ed.waitFor();
        assert.equal(await ed.locator('input[placeholder="e.g. github"], input[placeholder="例如 github"]').inputValue(), "postgres");
        assert.equal(await ed.locator('input[placeholder="npx -y @modelcontextprotocol/server-github"]').inputValue(), "");
        await ed.getByRole("button", { name: w.cancel, exact: true }).click();
        await ed.waitFor({ state: "detached" });
      });

      await t.test("an endpoint: the form filled in with its URL", async () => {
        const q = "https://mcp.nowhere.dev/mcp";
        await search.fill(q);
        await search.press("Enter");
        const msg = box.locator(".mk-custom");
        await msg.filter({ hasText: w.none }).waitFor();
        assert.equal(await box.locator(".mk-card").count(), 0);
        await msg.getByRole("button", { name: w.add, exact: true }).click();
        await ed.waitFor();
        assert.equal(await ed.locator('input[placeholder="https://example.com/mcp"]').inputValue(), q);
        assert.equal(await ed.locator('input[placeholder="e.g. github"], input[placeholder="例如 github"]').inputValue(), "nowhere");
        await ed.getByRole("button", { name: w.cancel, exact: true }).click();
        await ed.waitFor({ state: "detached" });
      });

      await t.test("words that match nothing say so, as before", async () => {
        await search.fill("zzz");
        await search.press("Enter");
        await box.locator(".mk-msg", { hasText: w.nothing }).waitFor();
        assert.equal(await box.locator(".mk-custom").count(), 0);
      });

      assert.deepEqual(errors, []);
    });
  }
}
