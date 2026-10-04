// Run with Node's test runner and Playwright on the module path; see README.md.
// A server's or a skill's All chip (Discord: MCP 服务器 SKILL 都得一个个选):
// one click gives the item to every agent shown that can take it — not one
// whose chip is greyed out (an SSE server for an agent without SSE), not one
// hidden on the Agents page, which keeps what it has — in a single write;
// an agent that couldn't be given it is named in the toast with how many
// have it; a second click takes it from all. A greyed-out chip's click says
// why it is grey and changes nothing. The click moves nothing. In English and Chinese. No
// backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/loosheng";
const agent = (id, name, icon, more = {}) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const base = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  // Pi has no MCP file, Droid no skills folder; Codex and Droid no SSE
  agents: [
    agent("claude", "Claude Code", "claudecode-color"),
    agent("codex", "Codex", "codex-color", { noSSE: true }),
    agent("zcode", "ZCode", "zcode"),
    agent("pi", "Pi", "pi", { mcp: "" }), // skills only
    agent("gemini", "Gemini CLI", "gemini-color"), // hidden on the Agents page
    agent("droid", "Droid", "factory", { noSSE: true, skills: "" }),
  ],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [],
  servers: [
    { name: "files", transport: "stdio", command: "npx", args: ["files-mcp"], agents: ["claude", "gemini"] },
    { name: "events", transport: "sse", url: "https://example.com/sse", agents: [] },
  ],
  skills: [{ name: "pdf", kind: "folder", description: "d", source: `${HOME}/skills/pdf`, agents: ["codex"] }],
});

function server(lang, posts, fail, fixture = base()) {
  let lib = fixture;
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, agentsHidden: ["gemini"] } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    const m = /^\/api\/library\/(servers|skills)\/agents$/.exec(url.pathname);
    if (m) {
      const body = req.postDataJSON();
      posts.push({ path: m[1], ...body });
      lib = structuredClone(lib);
      const item = lib[m[1]].find((x) => x.name === body.name);
      item.agents = body.agents;
      const what = (m[1] === "servers" ? "mcp:" : "skill:") + body.name;
      const problems = fail && body.agents.includes(fail.agent) ? [{ agent: fail.agent, what, error: fail.error }] : [];
      item.problems = Object.fromEntries(problems.map((p) => [p.agent, p.error]));
      return route.fulfill({ json: { ...lib, problems, result: { changed: body.agents.filter((a) => !problems.some((p) => p.agent === a)), problems } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    all: "All", on: "files is on for all 4 agents", off: "files is off for every agent",
    partial: "events is on for 1 of 2 agents — ZCode: config.json: permission denied",
    skill: "pdf is on for all 4 agents",
    why: {
      "claude-desktop": "Claude Desktop runs only a command from its settings — add a remote server in its Connectors instead",
      codex: "Codex can't reach a server over SSE — only a command or streamable HTTP",
    },
  },
  zh: {
    all: "全部", on: "已为全部 4 个 Agent 启用 files", off: "已从所有 Agent 中移除 files",
    partial: "events 已在 2 个 Agent 中的 1 个启用 — ZCode：config.json: permission denied",
    skill: "已为全部 4 个 Agent 启用 pdf",
    why: {
      "claude-desktop": "Claude Desktop 的配置文件只支持命令启动的服务器 — 远程服务器请在它的「连接器」里添加",
      codex: "Codex 不支持 SSE 服务器 — 只支持命令或 Streamable HTTP",
    },
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a server's or skill's All chip", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, tab, posts, fail, { width = 980, fixture } = {}) => {
      const ctx = await browser.newContext({ viewport: { width, height: 800 } }); // the MCP tab fits, its "In projects" too
      await ctx.addInitScript((tab) => { try { localStorage.setItem("magpie.libTab", tab); } catch {} }, tab);
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("http://magpie.test/**", server(lang, posts, fail, fixture));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-row").first().waitFor();
      return page;
    };
    const row = (page, name) => page.locator("#view-library .lib-row").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
    const toast = (page, text) => page.waitForFunction((text) => document.querySelector("#status").textContent === text, text).catch(async () => {
      assert.equal(await page.locator("#status").textContent(), text);
    });
    // clicked where it is, and the page not moved by it
    const click = async (page, loc) => {
      const top = await page.evaluate(() => document.querySelector("#view-library").scrollTop);
      await loc.click();
      assert.equal(await page.evaluate(() => document.querySelector("#view-library").scrollTop), top);
    };
    const lit = (r) => r.locator(".lib-ag[data-agent]").evaluateAll((cs) => cs.filter((c) => c.getAttribute("aria-pressed") === "true").map((c) => c.dataset.agent));

    for (const lang of ["en", "zh"]) {
      for (const width of [560, 980]) {
        await t.test(`${lang}: All stays under a stationary pointer with 14 agents at ${width}px`, async () => {
          const fixture = base(), posts = [];
          fixture.agents = Array.from({ length: 14 }, (_, i) => agent("a" + i, "Agent " + i, "codex-color"));
          fixture.servers = [{ name: "files", transport: "stdio", command: "codex", args: ["mcp-server"], agents: [] }];
          const page = await open(lang, "mcp", posts, null, { width, fixture });
          await page.mouse.move(0, 0);
          const r = row(page, "files"), all = r.locator(".lib-ag.all");
          await r.evaluate(async (r) => {
            await Promise.all(r.parentElement.getAnimations({ subtree: true }).map((a) => a.finished.catch(() => {})));
          });
          const before = await all.boundingBox();
          const point = { x: before.x + before.width / 2, y: before.y + before.height / 2 };
          await page.mouse.move(point.x, point.y);
          const samples = await all.evaluate(async (c, point) => {
            const samples = [];
            for (let i = 0; i < 60; i++) {
              await new Promise(requestAnimationFrame);
              const a = c.getBoundingClientRect(), row = c.closest(".lib-row").getBoundingClientRect();
              samples.push({ x: a.x, y: a.y, height: row.height, hit: document.elementFromPoint(point.x, point.y)?.closest("button") === c });
            }
            return samples;
          }, point);
          assert.ok(samples.every((s) => s.hit), "All moved out from under the stationary pointer");
          assert.ok(samples.every((s) => Math.abs(s.x - before.x) <= 1 && Math.abs(s.y - before.y) <= 1), "All moved as the chips spread: " + JSON.stringify({ before, samples: samples.filter((s) => Math.abs(s.x - before.x) > 1 || Math.abs(s.y - before.y) > 1).slice(0, 3) }));
          assert.ok(Math.max(...samples.map((s) => s.height)) - Math.min(...samples.map((s) => s.height)) <= 1, "the row kept wrapping and unwrapping");
          assert.equal(posts.length, 0, "hover must not change agent assignments");
          await page.mouse.click(point.x, point.y);
          await page.waitForFunction(() => document.querySelector(".lib-ag.all").getAttribute("aria-pressed") === "true");
          assert.equal(posts.length, 1);
          assert.equal(posts[0].agents.length, 14);
          await page.close();
        });
      }

      await t.test(lang + ": All gives a server to every agent that can take it, and takes it from all", async () => {
        const posts = [];
        const page = await open(lang, "mcp", posts);
        const r = row(page, "files");
        const all = r.locator(".lib-ag.all");
        assert.equal(await all.textContent(), words[lang].all);
        assert.equal(await all.getAttribute("aria-pressed"), "false");
        // it comes first, and no chip covers it
        assert.equal(await r.locator(".lib-agents > :first-child").evaluate((c) => c.classList.contains("all")), true);
        const [a, next] = await r.locator(".lib-agents > .lib-ag").evaluateAll((cs) => cs.slice(0, 2).map((c) => c.getBoundingClientRect().toJSON()));
        assert.ok(next.left >= a.right, `the first chip overlaps All: ${a.right} > ${next.left}`);

        await click(page, all);
        // Gemini CLI, hidden, keeps it; Pi has no MCP
        await toast(page, words[lang].on);
        assert.deepEqual(posts.map((p) => [p.path, p.name, [...p.agents].sort()]), [["servers", "files", ["claude", "codex", "droid", "gemini", "zcode"]]]);
        assert.deepEqual(await lit(r), ["claude", "codex", "zcode", "droid"]);
        assert.equal(await all.getAttribute("aria-pressed"), "true");

        await click(page, all);
        assert.equal(await all.getAttribute("aria-pressed"), "false");
        await toast(page, words[lang].off);
        assert.deepEqual(posts[1].agents, ["gemini"]);
        assert.deepEqual(await lit(r), []);
        await page.close();
      });

      await t.test(lang + ": an SSE server's All leaves out an agent without SSE, and names one that couldn't take it", async () => {
        const posts = [];
        const page = await open(lang, "mcp", posts, { agent: "zcode", error: "config.json: permission denied" });
        const r = row(page, "events");
        await click(page, r.locator(".lib-ag.all"));
        await toast(page, words[lang].partial);
        assert.deepEqual([...posts[0].agents].sort(), ["claude", "zcode"]);
        assert.equal(await r.locator('.lib-ag[data-agent="zcode"]').evaluate((c) => c.classList.contains("warn")), true);
        assert.equal(await r.locator('.lib-ag[data-agent="codex"]').evaluate((c) => c.getAttribute("aria-disabled") === "true" && c.getAttribute("aria-pressed") === "false"), true);
        await page.close();
      });

      // 蓝猫 on Discord: Claude Desktop's chip for a remote server was grey,
      // and clicking it did nothing — no tooltip on a disabled button in
      // WebKit, nothing on a click. Now the click says why, and gives
      // nothing to the agent.
      await t.test(lang + ": a greyed-out chip's click says why, and changes nothing", async () => {
        const posts = [], fixture = base();
        fixture.agents.push(agent("claude-desktop", "Claude Desktop", "claude-color", { noRemote: true }));
        fixture.servers.push({ name: "docs", transport: "http", url: "https://example.com/mcp", agents: [] });
        const page = await open(lang, "mcp", posts, null, { fixture });
        for (const [name, id] of [["docs", "claude-desktop"], ["events", "codex"]]) {
          const c = row(page, name).locator(`.lib-ag[data-agent="${id}"]`);
          assert.equal(await c.getAttribute("aria-disabled"), "true");
          assert.equal(await c.evaluate((c) => c.disabled), false);
          // a person's click reaches it; Playwright's own waits for
          // aria-disabled to go, so it is forced
          const top = await page.evaluate(() => document.querySelector("#view-library").scrollTop);
          await c.click({ force: true });
          assert.equal(await page.evaluate(() => document.querySelector("#view-library").scrollTop), top);
          await toast(page, words[lang].why[id]);
          assert.equal(await c.getAttribute("aria-pressed"), "false");
        }
        assert.deepEqual(posts, []);
        // All passes over it: the HTTP server goes to the rest
        await click(page, row(page, "docs").locator(".lib-ag.all"));
        await page.waitForFunction(() => document.querySelector("#status").textContent !== "");
        assert.equal(posts.length, 1);
        assert.ok(!posts[0].agents.includes("claude-desktop"), JSON.stringify(posts[0].agents));
        await page.close();
      });

      await t.test(lang + ": a skill's All gives it to every agent with a skills folder", async () => {
        const posts = [];
        const page = await open(lang, "skills", posts);
        const r = row(page, "pdf");
        await click(page, r.locator(".lib-ag.all"));
        await toast(page, words[lang].skill);
        assert.deepEqual(posts.map((p) => [p.path, p.name, [...p.agents].sort()]), [["skills", "pdf", ["claude", "codex", "pi", "zcode"]]]);
        // the row's own click (open the SKILL.md) wasn't set off
        assert.equal(await page.locator("#modal").isHidden(), true);
        await page.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
