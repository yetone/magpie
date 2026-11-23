// Run with Node's test runner and Playwright on the module path; see README.md.
// #1618 (sxwedo): an MCP server an agent has of its own, listed under "In
// your agents", could only be brought into the library. Its row has a
// Remove (trash) beside Bring in now, as a found skill's has (#1303): it
// asks first, saying which agents' MCP settings it leaves, that each file is
// copied to the backups first, and that another server by its name stays;
// then it posts servers/remove-found, and the row goes. Cancel posts
// nothing. A server the agent's app puts there itself (own) isn't listed.
// In English, Chinese, Japanese and German, and at 440px wide. No backend:
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/sx";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const srv = (name, agents, more = {}) => ({ server: { name, transport: "stdio", command: name + "-mcp", agents }, ...more });
const found = [
  srv("github", ["claude", "codex"], { others: ["gemini"] }),
  srv("figma", ["goose"]),
  srv("node_repl", ["codex"], { own: true }),
];
const lib = (foundServers) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("gemini", "Gemini CLI"), agent("goose", "Goose")],
  instructions: { agents: [] }, servers: [], skills: [], foundSkills: [], projects: [], foundServers,
});

function server(lang, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(found) });
    if (url.pathname === "/api/library/servers/remove-found") {
      const { name } = req.postDataJSON();
      posts.push(name);
      return route.fulfill({ json: { ...lib(found.filter((f) => f.server.name !== name)), result: { changed: ["claude", "codex"] } } });
    }
    if (url.pathname === "/api/library/mcp/check") return route.fulfill({ json: { servers: {} } });
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

const said = {
  en: {
    head: "Remove github?",
    github: ["It is taken out of the MCP settings of Claude Code, Codex. Each file is copied to magpie's backups before it changes.", "Gemini CLI has another server by this name; that one stays."],
    figma: ["It is taken out of the MCP settings of Goose. Each file is copied to magpie's backups before it changes."],
    removed: "github removed",
  },
  zh: {
    head: "移除 github？",
    github: ["将从 Claude Code, Codex 的 MCP 配置中移除。每个文件改动前都会先复制到 magpie 的备份。", "Gemini CLI 中有另一个同名服务器，那个保持不动。"],
    figma: ["将从 Goose 的 MCP 配置中移除。每个文件改动前都会先复制到 magpie 的备份。"],
    removed: "已移除 github",
  },
  ja: {
    head: "github を削除しますか？",
    github: ["Claude Code, Codex の MCP 設定から外されます。各ファイルは変更前に magpie のバックアップにコピーされます。", "Gemini CLI には同じ名前の別のサーバーがあり、そちらはそのまま残ります。"],
    figma: ["Goose の MCP 設定から外されます。各ファイルは変更前に magpie のバックアップにコピーされます。"],
    removed: "github を削除しました",
  },
  de: {
    head: "github entfernen?",
    github: ["Er wird aus den MCP-Einstellungen von Claude Code, Codex entfernt. Jede Datei wird vorher in die Backups von magpie kopiert.", "Gemini CLI hat einen anderen Server mit diesem Namen; dieser bleibt."],
    figma: ["Er wird aus den MCP-Einstellungen von Goose entfernt. Jede Datei wird vorher in die Backups von magpie kopiert."],
    removed: "github entfernt",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": remove an MCP server an agent has of its own", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, posts, width = 980) => {
      const ctx = await browser.newContext({ viewport: { width, height: 800 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();
      return page;
    };
    const row = (page, name) => page.locator("#view-library .lib-row:not(.lib-hub)").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
    const ask = async (page, name) => {
      await row(page, name).locator("button.lib-icon.danger").click();
      const ed = page.locator(".lib-editor").filter({ has: page.locator(".lib-confirm") });
      await ed.waitFor();
      return { ed, head: (await ed.locator(".ehead b").textContent()).trim(), text: await ed.locator(".lib-confirm").allTextContents() };
    };

    for (const lang of ["en", "zh", "ja", "de"]) {
      await t.test(lang, async () => {
        const posts = [];
        const page = await open(lang, posts);
        assert.equal(await row(page, "node_repl").count(), 0, "an agent's own server isn't listed");
        {
          const { ed, text } = await ask(page, "figma");
          assert.deepEqual(text, said[lang].figma);
          await ed.locator(".bar button:not(.primary)").click(); // Cancel
          await ed.waitFor({ state: "detached" });
          assert.deepEqual(posts, [], "Cancel removes nothing");
        }
        const { ed, head, text } = await ask(page, "github");
        assert.equal(head, said[lang].head);
        assert.deepEqual(text, said[lang].github);
        await ed.locator(".bar button.primary").click();
        await ed.waitFor({ state: "detached" });
        await page.waitForTimeout(200);
        assert.deepEqual(posts, ["github"], "only the one confirmed is removed");
        assert.equal(await row(page, "github").count(), 0, "its row is gone");
        assert.equal(await row(page, "figma").count(), 1, "the other stays");
        assert.equal((await page.locator("#status").textContent()).trim(), said[lang].removed);
      });
    }

    await t.test("440px wide", async () => {
      const page = await open("de", [], 440);
      for (const name of ["github", "figma"]) {
        const r = row(page, name);
        const box = await r.boundingBox(), rm = await r.locator("button.lib-icon.danger").boundingBox(), bring = await r.locator("button.action").boundingBox();
        assert(rm && rm.width >= 16 && rm.height >= 16, name + ": the trash is too small " + JSON.stringify(rm));
        assert(rm.x + rm.width <= box.x + box.width + 0.5 && bring.x >= box.x - 0.5, name + ": a button sticks out of the row");
      }
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
    });

    assert.deepEqual(errors, []);
  });
}
