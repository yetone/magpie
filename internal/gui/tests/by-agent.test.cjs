// Run with Node's test runner and Playwright on the module path; see README.md.
// #475: one agent's skills, or MCP servers, were a chip on every row to turn
// off, and an agent hidden on the Agents page had no chips at all, so what it
// was given couldn't be taken out of it. "By agent", beside "In the library",
// opens a sheet listing each agent with how many it has, a "Turn all on" and
// a "Turn all off": each posts that one agent to …/agents-all, and the
// page's chips follow. A hidden agent is listed while it has any (tagged
// Hidden, with no Turn all on), and stays listed once emptied; turning a
// server on is offered only for the ones the agent can reach (no SSE for
// Codex). The clicks scroll nothing; in English and Chinese. No backend: the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const sk = (name, agents) => ({ name, description: name, kind: "folder", agents });
const sv = (name, transport, agents) => ({ name, transport, command: transport === "stdio" ? name : "", url: transport === "stdio" ? "" : "http://localhost:9/" + name, agents });
const lib = (skills, servers) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex", { noSSE: true }), agent("copilot", "Copilot CLI"), agent("goose", "Goose", { skills: "" })],
  instructions: { agents: [] }, servers, skills, foundServers: [], projects: [], foundSkills: [],
});

function server(lang, posts) {
  let skills = [sk("grill-me", ["claude", "copilot"]), sk("tdd", ["copilot"]), sk("pdf", ["codex"])];
  let servers = [sv("fs", "stdio", ["claude", "copilot"]), sv("web", "sse", ["claude"])];
  const can = (s, a) => !(s.transport === "sse" && a === "codex");
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light", agentsHidden: ["copilot"] } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(skills, servers) });
    const every = url.pathname.match(/^\/api\/library\/(skills|servers)\/agents-all$/);
    if (every) {
      const { agents, on } = req.postDataJSON();
      posts.push({ what: every[1], agents, on });
      const put = (x) => ({ ...x, agents: on ? [...new Set([...x.agents, ...agents.filter((a) => every[1] === "skills" || can(x, a))])].sort() : x.agents.filter((a) => !agents.includes(a)) });
      if (every[1] === "skills") skills = skills.map(put);
      else servers = servers.map(put);
      return route.fulfill({ json: { ...lib(skills, servers), result: { changed: agents } } });
    }
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
// a click where the button is, as the reader's: Playwright's own click first
// scrolls the button into view
const press = async (page, loc) => {
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};
// the sheet grows out of the button that opened it: a click at a button's
// place lands once it has
const settled = (page) => page.waitForFunction(() => !document.querySelector("#modal").getAnimations({ subtree: true }).length);
const lit = (page, id) => page.locator(`#view-library .lib-ag.on[data-agent="${id}"]`).count();

const words = {
  en: {
    by: "By agent", skillsHead: "Skills by agent", serversHead: "MCP servers by agent", hidden: "Hidden", done: "Done",
    copilot: "2 of 3 skills", copilotNone: "0 of 3 skills", codex: "1 of 3 skills", codexAll: "3 of 3 skills",
    offCopilot: "Every skill is off for Copilot CLI", onCodex: "3 skills are on for Codex",
    srvCodex: "0 of 2 servers", srvOnCodex: "1 server is on for Codex", srvOffCopilot: "Every server is off for Copilot CLI",
  },
  zh: {
    by: "按 Agent", skillsHead: "按 Agent 管理技能", serversHead: "按 Agent 管理 MCP 服务器", hidden: "已隐藏", done: "完成",
    copilot: "2 / 3 个技能", copilotNone: "0 / 3 个技能", codex: "1 / 3 个技能", codexAll: "3 / 3 个技能",
    offCopilot: "已从 Copilot CLI 中关闭全部技能", onCodex: "已为 Codex 启用 3 个技能",
    srvCodex: "0 / 2 个服务器", srvOnCodex: "已为 Codex 启用 1 个服务器", srvOffCopilot: "已从 Copilot CLI 中关闭全部服务器",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": one agent's library entries on or off at once, a hidden one's too", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, posts, tab) => {
      const ctx = await browser.newContext({ viewport: { width: 860, height: 700 } });
      await ctx.addInitScript((tab) => { try { localStorage.setItem("magpie.libTab", tab); } catch {} }, tab);
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-row").first().waitFor();
      return page;
    };
    const row = (sheet, id) => sheet.locator(`.lib-byagent-row[data-agent="${id}"]`);

    for (const lang of ["en", "zh"]) {
      await t.test(lang + ": skills", async () => {
        const w = words[lang], posts = [];
        const page = await open(lang, posts, "skills");
        const by = page.locator("#view-library .row-head button.lib-byagent");
        assert.equal((await by.textContent()).trim(), w.by);
        assert.equal(await lit(page, "copilot"), 0, "the hidden agent has no chips");
        const before = await scrolled(page);

        await press(page, by);
        const sheet = page.locator("#modal .lib-byagent-sheet");
        await sheet.waitFor();
        await settled(page);
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w.skillsHead);
        assert.deepEqual(await sheet.locator(".lib-byagent-row").evaluateAll((rs) => rs.map((r) => r.dataset.agent)), ["claude", "codex", "copilot"], "goose takes no skills");
        const cp = row(sheet, "copilot");
        assert.equal((await cp.locator(".lib-tag").textContent()).trim(), w.hidden);
        assert.equal((await cp.locator(".sub").textContent()).trim(), w.copilot);
        assert.equal(await cp.locator("button.lib-agenton").isDisabled(), true, "a hidden agent isn't given more");

        await press(page, cp.locator("button.lib-agentoff"));
        await page.waitForFunction(() => document.querySelector('#modal .lib-byagent-row[data-agent="copilot"] .sub')?.textContent.match(/^0/));
        assert.deepEqual(posts, [{ what: "skills", agents: ["copilot"], on: false }]);
        assert.equal((await page.locator("#status").textContent()).trim(), w.offCopilot);
        assert.equal((await cp.locator(".sub").textContent()).trim(), w.copilotNone, "emptied, it stays listed");
        assert.equal(await cp.locator("button.lib-agentoff").isDisabled(), true);

        const cx = row(sheet, "codex");
        assert.equal((await cx.locator(".sub").textContent()).trim(), w.codex);
        await press(page, cx.locator("button.lib-agenton"));
        await page.waitForFunction(() => document.querySelector('#modal .lib-byagent-row[data-agent="codex"] .sub')?.textContent.match(/^3/));
        assert.deepEqual(posts[1], { what: "skills", agents: ["codex"], on: true });
        assert.equal((await page.locator("#status").textContent()).trim(), w.onCodex);
        assert.equal(await cx.locator("button.lib-agenton").isDisabled(), true, "nothing left to turn on");
        assert.equal(await lit(page, "codex"), 3, "every row's Codex chip is lit");
        assert.equal(await lit(page, "claude"), 1, "Claude Code keeps what it had");

        await sheet.locator(".bar button", { hasText: w.done }).click();
        await sheet.waitFor({ state: "detached" });
        assert.equal(posts.length, 2);
        assert.equal(await scrolled(page), before, "the clicks scroll nothing");
      });

      await t.test(lang + ": servers", async () => {
        const w = words[lang], posts = [];
        const page = await open(lang, posts, "mcp");
        const by = page.locator("#view-library .row-head button.lib-byagent");
        const before = await scrolled(page);
        await press(page, by);
        const sheet = page.locator("#modal .lib-byagent-sheet");
        await sheet.waitFor();
        await settled(page);
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w.serversHead);
        assert.deepEqual(await sheet.locator(".lib-byagent-row").evaluateAll((rs) => rs.map((r) => r.dataset.agent)), ["claude", "codex", "copilot", "goose"]);

        const cx = row(sheet, "codex");
        assert.equal((await cx.locator(".sub").textContent()).trim(), w.srvCodex);
        await press(page, cx.locator("button.lib-agenton"));
        await page.waitForFunction(() => document.querySelector('#modal .lib-byagent-row[data-agent="codex"] .sub')?.textContent.match(/^1/));
        assert.deepEqual(posts, [{ what: "servers", agents: ["codex"], on: true }]);
        assert.equal((await page.locator("#status").textContent()).trim(), w.srvOnCodex);
        assert.equal(await cx.locator("button.lib-agenton").isDisabled(), true, "the SSE one it can't reach isn't offered");

        await press(page, row(sheet, "copilot").locator("button.lib-agentoff"));
        await page.waitForFunction(() => document.querySelector('#modal .lib-byagent-row[data-agent="copilot"] .sub')?.textContent.match(/^0/));
        assert.deepEqual(posts[1], { what: "servers", agents: ["copilot"], on: false });
        assert.equal((await page.locator("#status").textContent()).trim(), w.srvOffCopilot);
        assert.equal(await lit(page, "claude"), 2, "Claude Code keeps both");

        await page.keyboard.press("Escape");
        await sheet.waitFor({ state: "detached" });
        assert.equal(await scrolled(page), before, "the clicks scroll nothing");
      });
    }

    assert.deepEqual(errors, []);
  });
}
