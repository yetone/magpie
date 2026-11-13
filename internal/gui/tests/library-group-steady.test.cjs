// Run with Node's test runner and Playwright on the module path; see README.md.
// jinpwng on X: in Library › Skills, opening the first group made the page
// jump, and what was clicked went off the screen, while the others opened
// downwards. The group was the first part of a big group (a folder of a
// repository, drawn in the group's long list): a click draws its heading
// anew, and the only neighbour above the first part is the list's empty
// pad, so the page held the next part's heading, pushed down by the rows
// opened. A heading opened or folded stays where it was clicked, and what it
// opens comes in below it: the groups and their parts in Skills, the
// projects in Skills, MCP and Subagents, and the agents in Instructions,
// the first and one further down, at the top of the page and scrolled. Both
// engines, five languages, wide and narrow; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, instructions: `${HOME}/.${id}/AGENTS.md`, ...more });
const gh = (repo, dir, name) => ({ name, kind: "github", source: `https://github.com/${repo}/tree/main/${dir}/${name}`, description: "d", agents: ["claude"] });
const AGENTS = ["Claude Code", "Codex", "Gemini CLI", "OpenCode"];

function library() {
  // two repositories, 51 skills: past 40 the groups and their parts start
  // folded, and big/pack is split by the folders its skills are in
  const skills = [];
  for (let i = 0; i < 3; i++) skills.push(gh("acme/kit", "skills", "kit-" + i));
  for (const d of ["alpha", "beta", "gamma"]) for (let i = 0; i < 16; i++) skills.push(gh("big/pack", "skills/" + d, d + "-" + i));
  const project = (name) => ({ dir: `${HOME}/code/${name}`, name, copy: false, skills: { "kit-0": ["claude"], "kit-1": ["claude"] }, placed: [], servers: { fetch: ["claude"] }, subagents: { reviewer: ["claude"] }, wrote: {} });
  const ids = ["claude", "codex", "gemini", "opencode"];
  return {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: ids.map((id, i) => agent(id, AGENTS[i], { projectSkills: `.${id}/skills`, projectMCP: `.${id}/mcp.json`, subagents: `${HOME}/.${id}/agents`, projectSubagents: `.${id}/agents` })),
    instructions: {
      shared: "Be brief.",
      sets: [{ id: "default", name: "", active: true, text: "Be brief." }],
      agents: ids.map((id, i) => ({ agent: id, name: AGENTS[i], icon: "", path: `${HOME}/.${id}/AGENTS.md`, on: true, extra: "" })),
    },
    foundServers: [], foundSkills: [], problems: [],
    servers: [{ name: "fetch", command: "uvx", args: ["mcp-server-fetch"], agents: ["claude"] }],
    subagents: [{ name: "reviewer", description: "Reviews a change", agents: ["claude"] }],
    projects: ["app", "web", "api", "docs"].map(project),
    skills,
  };
}

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(library());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a group opened or folded stays where it was clicked", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of (process.env.LANGS ? process.env.LANGS.split(",") : LANGS)) for (const width of [980, 420]) {
      await t.test(lang + " " + width, async () => {
        const height = 760;
        const ctx = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        const v = page.locator("#view-library");
        let tab = "skills";
        const open = async (id) => {
          tab = id;
          await page.goto("http://magpie.test/");
          await page.evaluate((id) => { localStorage.clear(); localStorage.setItem("magpie.libTab", id); }, id);
          await page.reload();
          await page.locator('button[data-view="library"]').click();
        };
        const top = (head) => head.evaluate((h) => h.getBoundingClientRect().top);
        const settle = () => page.evaluate(() => new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(ok))));
        // the heading put at a point in the window by the reader's own wheel
        // (which also ends the hold of the click before)
        const place = async (head, at) => {
          await page.mouse.move(width / 2, height / 2);
          const by = (await top(head())) - at * height;
          if (Math.abs(by) > 2) await page.mouse.wheel(0, by);
          await page.waitForTimeout(300);
          await settle();
          // narrow, the library's own heading sticks at the top once
          // scrolled, and the heading is kept clear below it
          const under = await page.evaluate(() => Math.max(0, ...[...document.querySelectorAll("#view-library .lib-head")].map((h) => h.getBoundingClientRect().bottom)));
          const y = await top(head());
          if (y < under + 16) {
            await page.mouse.wheel(0, y - under - 40);
            await page.waitForTimeout(300);
            await settle();
          }
        };
        // a click on its name, as the reader's pointer would (Playwright's
        // own click scrolls first), and where the heading, drawn anew, is
        // once the page has settled
        const flip = async (head, what) => {
          const was = await head().evaluate((h) => h.classList.contains("open"));
          const y = await top(head());
          assert.ok(y > 0 && y < height - 30, `${tab}, ${what}: the heading is in the window (${Math.round(y)})`);
          const b = await head().locator(".name").first().boundingBox();
          const cx = b.x + Math.min(12, b.width / 2), cy = b.y + b.height / 2;
          await page.mouse.click(cx, cy);
          // drawn anew, so the heading is found again until it has turned
          for (const t0 = Date.now(); Date.now() - t0 < 4000;) {
            if (await head().evaluate((h) => h.classList.contains("open")).catch(() => was) !== was) break;
            await page.waitForTimeout(50);
          }
          await page.waitForTimeout(300);
          await settle();
          assert.equal(await head().evaluate((h) => h.classList.contains("open")), !was, `${tab}, ${what}: it ${was ? "folded" : "opened"}`);
          const after = await top(head());
          assert.ok(Math.abs(after - y) < 1.5, `${tab}, ${what} ${was ? "folded" : "opened"}: the heading moved from ${Math.round(y)} to ${Math.round(after)}`);
        };

        // Skills: the groups, then the parts of the big one
        await open("skills");
        const group = (i) => () => v.locator(".lib-grouphead").nth(i);
        const part = (i) => () => v.locator(".lib-subhead").nth(i);
        await group(0)().waitFor();
        assert.equal(await v.locator(".lib-grouphead").count(), 2);
        const big = await v.locator(".lib-grouphead").evaluateAll((hs) => hs.findIndex((h) => h.textContent.includes("big/pack")));
        const small = 1 - big;
        await place(group(0), 0.2);
        await flip(group(0), "the first group");
        await flip(group(0), "the first group");
        await place(group(small), 0.3);
        await flip(group(small), "the small group");
        await flip(group(small), "the small group");
        await place(group(big), 0.25);
        await flip(group(big), "the big group");
        await v.locator(".lib-subhead").nth(2).waitFor();
        assert.equal(await v.locator(".lib-subhead").count(), 3);
        // the first part: at the top of its list, its only neighbour above
        // the list's empty pad (the report), near the top and lower down
        for (const at of [0.2, 0.55]) {
          await place(part(0), at);
          await flip(part(0), "the first part");
          await flip(part(0), "the first part");
        }
        await place(part(1), 0.4);
        await flip(part(1), "the second part");
        await flip(part(1), "the second part");
        // one already open below while the first opens
        await flip(part(1), "the second part");
        await place(part(0), 0.3);
        await flip(part(0), "the first part");
        await flip(part(0), "the first part");

        // the projects, in each tab that lists them
        for (const id of ["skills", "mcp", "subagents"]) {
          await open(id);
          const project = (i) => () => v.locator(".lib-projhead").nth(i);
          await project(0)().waitFor();
          assert.equal(await v.locator(".lib-projhead").count(), 4);
          for (const i of [0, 2]) {
            await place(project(i), 0.3);
            await flip(project(i), "project " + (i + 1));
            await flip(project(i), "project " + (i + 1));
          }
        }

        // Instructions: an agent's row opens its file below it
        await open("instructions");
        const row = (name) => () => v.locator(".lib-row.click", { has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) }).first();
        await row(AGENTS[0])().waitFor();
        for (const name of [AGENTS[0], AGENTS[2]]) {
          await place(row(name), 0.3);
          await flip(row(name), name);
          await flip(row(name), name);
        }

        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the page scrolls sideways");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
