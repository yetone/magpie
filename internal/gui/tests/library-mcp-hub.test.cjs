// Run with Node's test runner and Playwright on the module path; see README.md.
// #2679 (@minggang123 on X): the Library's MCP section has a switch that
// gives each agent one MCP server, magpie, in place of the library's
// servers. Off, it says what it would do; a click posts servers/hub on,
// says the agents are given magpie's one server, and the page then says so.
// Each agent that keeps each server's own entry is named under it with why
// (Claude Desktop reaches no server over HTTP; Codex has a "magpie" of its
// own). On, it counts what it serves (servers, tools, the agents given it)
// and shows the one address any other app connects at, with Connect…: a
// sheet of two tabs. Agents: every agent magpie found that takes MCP
// servers, in groups (not connected, connected, keeping each server's own
// entry), each a switch that posts servers/agents-all, or a word for why it
// keeps its entries with the reason in full behind (i); a Connect all in
// the bar gives the rest at once. With 25 agents the connected and the
// kept fold to a row each, a search narrows them, and the sheet still fits
// the window. Other apps: the address and a row per app (Cursor when it
// isn't one of the agents, VS Code, Windsurf, any JSON app) whose Copy
// entry copies what adds it. Cursor and VS Code get an Add in the app (it
// posts servers/hub-open with the app's name, never a link) and none in a
// browser. No agent is told to the user as a command to run. The address
// is the public one in a browser and the gateway in the app. A click again
// turns it off. Other apps also get the address that gives the tools on
// demand (?tools=on-demand, Passings_z on X), with what it does, while
// Tools on demand is on; with every tool whole it isn't offered. In every
// language, both engines, a wide window in the app
// and a narrow one in a browser: no sideways scroll, and the switch beside
// its text.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/mg";
// 21 more agents, for a sheet with 25: the first 17 are connected
const MANY = Array.from({ length: 21 }, (_, i) => ({ id: "agent" + (i + 1), name: "Agent " + String(i + 1).padStart(2, "0"), icon: "generic", mcp: `${HOME}/.agent${i + 1}/mcp.json` }));
const fixture = (on, st = {}) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  mcpHub: on, hubPath: "/mcp/magpie/hub/",
  hubOnDemandQuery: "tools=on-demand", hubOnDemandOver: 10, hubAllTools: !!st.allTools,
  agents: [
    { id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json`, ...(on ? { hub: "on" } : {}) },
    { id: "codex", name: "Codex", icon: "codex-color", mcp: `${HOME}/.codex/config.toml`, ...(on ? { hub: "own" } : {}) },
    { id: "claude-desktop", name: "Claude Desktop", icon: "claude-color", mcp: `${HOME}/Library/Application Support/Claude/claude_desktop_config.json`, noRemote: true, ...(on ? { hub: "no-remote" } : {}) },
    // given none of the library's servers yet: Connect gives it them
    ...(st.cursor ? [{ id: "cursor", name: "Cursor", icon: "cursor", mcp: `${HOME}/.cursor/mcp.json`, ...(on && st.cursorOn ? { hub: "on" } : {}) }] : []),
    ...(st.many ? MANY.map((a, i) => ({ ...a, ...(on && (i < 17 || st.manyOn) ? { hub: "on" } : {}) })) : []),
  ],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
  servers: [
    { name: "my-tools", transport: "stdio", command: "node", args: [`${HOME}/code/my-tools/index.js`], agents: ["claude", "codex", "claude-desktop", ...(st.cursorOn ? ["cursor"] : [])] },
    { name: "notes", transport: "stdio", command: "uvx", args: ["notes-mcp"], agents: ["claude", "codex", ...(st.cursorOn ? ["cursor"] : [])] },
    // magpie's own: in the hub too, saving into the calling agent's project
    { name: "magpie-image", transport: "stdio", command: "/Applications/magpie.app/Contents/MacOS/magpie", args: ["mcp", "image"], agents: ["claude"] },
  ],
});

let I18N;
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${st.web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", gateway: "http://127.0.0.1:3425", publicURL: "https://mac.tail0.ts.net" } });
    if (url.pathname === "/api/library") return json(fixture(st.on, st));
    if (url.pathname === "/api/library/servers/hub") {
      const body = req.postDataJSON();
      st.sent.push(body);
      st.on = body.on;
      return json({ ...fixture(st.on, st), result: { changed: ["claude"] } });
    }
    if (url.pathname === "/api/library/servers/hub-open") {
      st.opened.push(req.postDataJSON());
      return json(fixture(st.on, st));
    }
    if (url.pathname === "/api/library/servers/agents-all") {
      const body = req.postDataJSON();
      st.all.push(body);
      if (body.agents.includes("cursor")) st.cursorOn = body.on;
      if (body.agents.includes("agent18")) st.manyOn = body.on;
      return json({ ...fixture(st.on, st), result: { changed: body.agents } });
    }
    if (url.pathname === "/api/copy") {
      st.copied.push(req.postDataJSON().text);
      return json({});
    }
    if (url.pathname === "/api/library/mcp/check") return json({ servers: Object.fromEntries(req.postDataJSON().names.map((n) => [n, { state: "ok", tools: 2 }])) });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// ARTIFACT_DIR keeps a picture of the sheet at each step
const shot = async (page, name) => {
  if (!process.env.ARTIFACT_DIR) return;
  await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
  await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => {}))));
  await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `hub-${name}.png`) });
};

const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-hub, #view-library .lib-hub .lib-switch, #view-library .lib-hub-why, #view-library .lib-hubat, #view-library .lib-hubat .lib-hubconnect, #view-library .lib-hubat .copy, .lib-hubsheet, .lib-hubsheet .segs, .lib-hubsheet .lib-hubroute, .lib-hubsheet .lib-hubsearch, .lib-hubsheet .lib-hubagent, .lib-hubsheet .lib-hubagent .lib-switch, .lib-hubsheet .lib-hubinfo, .lib-hubsheet .lib-hubfolded, .lib-hubsheet .lib-hubaddr, .lib-hubsheet .lib-hubapp, .lib-hubsheet .lib-hubpill, .lib-hubsheet .bar button")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": one MCP server for every agent", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const width of [1100, 420]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const web = width < 800;
        // Cursor is one of the agents in the app, and not in a browser
        // every tool whole in a browser: no address for the tools on demand
        const st = { on: false, sent: [], opened: [], all: [], copied: [], web, cursor: !web, allTools: web };
        const url = (web ? "https://mac.tail0.ts.net" : "http://127.0.0.1:3425") + "/mcp/magpie/hub";
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const hub = v.locator(".lib-hub");
        await hub.waitFor();

        // off: what it would do, and no reasons yet
        assert.equal((await hub.locator(".name").textContent()).trim(), w("MCP router"));
        assert.equal((await hub.locator(".sub.wraps").textContent()).trim(), w("Serve every server through magpie: each agent is given one server, magpie, in place of these, and any other app connects at one address."));
        assert.equal(await v.locator(".lib-hubstats, .lib-hubat").count(), 0);
        const sw = hub.locator(".lib-switch");
        assert.equal(await sw.getAttribute("role"), "switch");
        assert.equal(await sw.getAttribute("aria-checked"), "false");
        assert.equal(await v.locator(".lib-hub-why").count(), 0);
        // at the top: over the servers and Add server, not among them
        const above = await page.evaluate(() => {
          const rows = [...document.querySelectorAll("#view-library .lib-server, #view-library .after-list")];
          const hub = document.querySelector("#view-library .lib-hub");
          return rows.length > 1 && rows.every((r) => hub.getBoundingClientRect().bottom <= r.getBoundingClientRect().top);
        });
        assert.ok(above, "the hub's row isn't above the servers");
        assert.deepEqual(await sideways(page), [], "sideways scroll, off");

        // on
        await sw.click();
        await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("The agents are given magpie's one MCP server — restart them to use it"));
        assert.deepEqual(st.sent, [{ on: true }]);
        await page.waitForFunction(() => document.querySelector("#view-library .lib-hub .lib-switch")?.getAttribute("aria-checked") === "true");
        assert.equal((await v.locator(".lib-hub .sub.wraps").textContent()).trim(), w("Each agent is given one server, magpie, in place of these. magpie runs each server once for all of them and hands each call to it, as server__tool. Restart an agent for it to read the change."));
        // what it serves: three servers, magpie-image among them, 2 tools each once checked, in Claude Code
        await page.waitForFunction((x) => document.querySelector("#view-library .lib-hubstats")?.textContent === x,
          [w("{n} servers", { n: 3 }), w("{n} tools", { n: 6 }), w("in {agents}", { agents: "Claude Code" })].join(" · "));
        // the hub's mark is magpie's bird (the owner didn't like a made-up glyph),
        // its tail three routes out to three ends (the owner's pick, 10-31),
        // drawn by a clip the page has
        assert.equal(await v.locator('.lib-hub > .lib-hubmark use[href="#bird"][clip-path="url(#bird-notail)"]').count(), 1);
        assert.equal(await v.locator(".lib-hub > .lib-hubmark path").count(), 3);
        assert.equal(await v.locator(".lib-hub > .lib-hubmark circle").count(), 3);
        assert.equal(await page.locator("clipPath#bird-notail").count(), 1);
        // the address any other app connects at
        const at = v.locator(".lib-hubat");
        // the row says what it is (the owner: "Other apps" over an address read as nonsense)
        assert.equal((await at.locator(".name").textContent()).trim(), w("Router address"));
        assert.equal((await at.locator(".lib-hubURL").textContent()).trim(), url);
        assert.equal((await at.locator(".lib-hubconnect").textContent()).trim(), w("Connect…"));
        const why = v.locator(".lib-hub-why");
        assert.deepEqual(await why.evaluateAll((ps) => ps.map((p) => p.dataset.why)), ["no-remote", "own"]);
        assert.equal((await v.locator('.lib-hub-why[data-why="no-remote"]').textContent()).trim(), w("{agents} reaches no MCP server over HTTP, so it keeps each server's own entry.", { agents: "Claude Desktop" }));
        assert.equal((await v.locator('.lib-hub-why[data-why="own"]').textContent()).trim(), w("{agents} has an MCP server called magpie of its own, which magpie leaves as it is, so it keeps each server's own entry.", { agents: "Codex" }));
        // the switch sits beside its text, inside the row
        const fits = await page.evaluate(() => {
          const row = document.querySelector("#view-library .lib-hub").getBoundingClientRect();
          const s = document.querySelector("#view-library .lib-hub .lib-switch").getBoundingClientRect();
          return s.width > 0 && s.left >= row.left && s.right <= row.right + 0.5 && s.top >= row.top && s.bottom <= row.bottom + 0.5;
        });
        assert.ok(fits, "the switch is outside its row");
        assert.deepEqual(await sideways(page), [], "sideways scroll, on");

        // Connect…: the agents, in their groups, each with where it stands
        await at.locator(".lib-hubconnect").click();
        const sheet = page.locator(".lib-hubsheet");
        await sheet.waitFor();
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w("Connect an app"));
        const tabs = sheet.locator(".ebody > .segs .opt");
        assert.deepEqual((await tabs.allTextContents()).map((x) => x.trim()), [w("Agents"), w("Other apps")]);
        const lead = () => sheet.locator(".lib-hublead").textContent().then((x) => x.trim());
        // one agent that can be: named, not counted
        assert.equal(await lead(), web ? w("{agent} uses the library's servers through magpie", { agent: "Claude Code" }) : w("{on} of {all} agents use the library's servers through magpie", { on: 1, all: 2 }));
        assert.equal((await sheet.locator(".lib-hubsub").textContent()).trim().replace(/\u00a0/g, " "),
          [web ? w("Each server runs once, for all of them") : w("{n} more can use them", { n: 1 }), w("{n} keep each server's own entry", { n: 2 })].join(" · "));
        // the diagram's servers are the library's three, magpie-image's mark too (the owner: why isn't its logo there?)
        assert.equal(await sheet.locator(".lib-hubroute > .lib-hubstack").last().locator(":scope > *").count(), 3);
        assert.equal(await sheet.locator('.ehead > .lib-hubmark use[href="#bird"]').count(), 1);
        // a few agents: no search, nothing folded
        assert.equal(await sheet.locator(".lib-hubsearch, .lib-hubfolded, .lib-hubfold").count(), 0);
        const groups = () => sheet.locator(".lib-hubgrp").evaluateAll((gs) => gs.map((g) => [g.dataset.group, [...g.querySelectorAll(".lib-hubagent")].map((r) => r.dataset.agent)]));
        assert.deepEqual(await groups(), [...(web ? [] : [["off", ["cursor"]]]), ["on", ["claude"]], ["kept", ["codex", "claude-desktop"]]]);
        assert.deepEqual((await sheet.locator(".lib-hubsec > span:first-child").allTextContents()).map((x) => x.trim()),
          [...(web ? [] : [w("Not using the library")]), w("Using the library"), w("Keep each server's own entry")]);
        const rows = sheet.locator(".lib-hubagent");
        const states = () => rows.evaluateAll((rs) => rs.map((r) => [r.dataset.agent, r.dataset.state]));
        assert.deepEqual(await states(), [...(web ? [] : [["cursor", "none"]]), ["claude", "on"], ["codex", "own"], ["claude-desktop", "no-remote"]]);
        const agent = (id) => sheet.locator(`.lib-hubagent[data-agent="${id}"]`);
        assert.equal(await agent("claude").locator(".lib-switch").getAttribute("aria-checked"), "true");
        // the kept: a word for why, the reason in full behind (i)
        assert.equal(await agent("codex").locator(".lib-switch").count(), 0);
        assert.equal((await agent("codex").locator(".lib-hubtag").textContent()).trim(), w("Has its own magpie"));
        assert.equal((await agent("claude-desktop").locator(".lib-hubtag").textContent()).trim(), w("No HTTP"));
        assert.equal(await sheet.locator(".lib-hubwhy").count(), 0);
        await agent("claude-desktop").locator(".lib-hubinfo").click();
        assert.equal((await sheet.locator('.lib-hubwhy[data-why="no-remote"]').textContent()).trim(), w("It reaches no MCP server over HTTP, so it keeps each server's own entry"));
        assert.equal(await agent("claude-desktop").locator(".lib-hubinfo").getAttribute("aria-expanded"), "true");
        await agent("codex").locator(".lib-hubinfo").click();
        assert.equal((await sheet.locator('.lib-hubwhy[data-why="own"]').textContent()).trim(), w("It has a server called magpie of its own, which magpie leaves as it is"));
        assert.doesNotMatch(await sheet.textContent(), /mcp add/);
        await shot(page, `${engine}-${lang}-${width}-agents`);
        const bar = sheet.locator(".bar");
        if (web) {
          // nothing to connect: Done is the one primary
          assert.equal(await bar.locator(".lib-huball").count(), 0);
          assert.equal((await bar.locator("button.primary").textContent()).trim(), w("Done"));
        } else {
          // one switch on, then off, then Connect all
          assert.equal((await bar.locator(".lib-huball").textContent()).trim(), w("Connect all"));
          await agent("cursor").locator(".lib-switch").click();
          await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("{agent} is connected — restart it to use magpie's tools", { agent: "Cursor" }));
          await page.waitForFunction(() => document.querySelector('.lib-hubagent[data-agent="cursor"]')?.dataset.state === "on");
          assert.deepEqual(await groups(), [["on", ["claude", "cursor"]], ["kept", ["codex", "claude-desktop"]]]);
          assert.equal(await lead(), w("{on} of {all} agents use the library's servers through magpie", { on: 2, all: 2 }));
          assert.equal(await bar.locator(".lib-huball").count(), 0);
          assert.equal((await bar.locator(".lib-hubnote").textContent()).trim(), w("Restart an agent for it to read the change"));
          await agent("cursor").locator(".lib-switch").click();
          await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("{agent} no longer has the library's servers — restart it to read the change", { agent: "Cursor" }));
          await page.waitForFunction(() => document.querySelector('.lib-hubagent[data-agent="cursor"]')?.dataset.state === "none");
          await bar.locator(".lib-huball").click();
          await page.waitForFunction(() => document.querySelector('.lib-hubagent[data-agent="cursor"]')?.dataset.state === "on");
          assert.deepEqual(st.all, [{ agents: ["cursor"], on: true }, { agents: ["cursor"], on: false }, { agents: ["cursor"], on: true }]);
          // the sheet stays open over the page drawn again
          assert.ok(await sheet.isVisible());
        }
        // each agent's icon beside its name, at every width
        const beside = await rows.evaluateAll((rs) => rs.filter((r) => {
          const i = r.querySelector(":scope > .ic").getBoundingClientRect(), n = r.querySelector(".name").getBoundingClientRect();
          return i.right > n.left || i.bottom < n.top || i.top > n.bottom;
        }).map((r) => r.dataset.agent));
        assert.deepEqual(beside, [], "an agent's icon isn't beside its name");
        assert.deepEqual(await sideways(page), [], "sideways scroll, agents");

        // Other apps: the address, and a row per app with what adds it
        await tabs.last().click();
        assert.deepEqual((await sheet.locator(".lib-hubaddr code").allTextContents()).map((x) => x.trim()), web ? [url] : [url, url + "?tools=on-demand"]);
        if (!web) {
          // its own copy gives the address with the query
          await sheet.locator(".lib-hubaddr-lazy .copy").click();
          for (let i = 0; i < 250 && st.copied.at(-1) !== url + "?tools=on-demand"; i++) await page.waitForTimeout(20);
          assert.equal(st.copied.at(-1), url + "?tools=on-demand");
        }
        const apps = sheet.locator(".lib-hubapp");
        // Cursor is set up by hand only when magpie doesn't look after it
        assert.deepEqual(await apps.evaluateAll((rs) => rs.map((r) => r.dataset.app)), [...(web ? ["cursor"] : []), "vscode", "windsurf", "json"]);
        const app = (id) => sheet.locator(`.lib-hubapp[data-app="${id}"]`);
        const copied = async (id, name) => {
          await app(id).locator(".lib-hubcopy").click();
          await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("{app}'s entry copied", { app: name }));
          return JSON.parse(st.copied.at(-1));
        };
        if (web) {
          assert.equal(await sheet.locator(".lib-hubadd").count(), 0);
          assert.equal((await app("cursor").locator(".sub").textContent()).trim(), w("Or add it to ~/.cursor/mcp.json"));
          assert.deepEqual(await copied("cursor", "Cursor"), { mcpServers: { magpie: { url } } });
          assert.equal((await app("vscode").locator(".sub").textContent()).trim(), w("Or add it to .vscode/mcp.json, or to your user mcp.json"));
        } else {
          assert.equal((await app("vscode").locator(".sub").textContent()).trim(), w("{app} asks you to confirm, then it's ready", { app: "VS Code" }));
          assert.equal((await app("vscode").locator(".lib-hubadd").textContent()).trim(), w("Add"));
          await app("vscode").locator(".lib-hubadd").click();
          await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("{app} asks you to confirm adding magpie", { app: "VS Code" }));
        }
        assert.deepEqual(st.opened, web ? [] : [{ app: "vscode" }]);
        assert.deepEqual(await copied("vscode", "VS Code"), { servers: { magpie: { type: "http", url } } });
        assert.equal((await app("windsurf").locator(".sub").textContent()).trim(), w("Paste it into ~/.codeium/windsurf/mcp_config.json"));
        assert.deepEqual(await copied("windsurf", "Windsurf"), { mcpServers: { magpie: { serverUrl: url } } });
        assert.deepEqual(await copied("json", w("Other")), { mcpServers: { magpie: { type: "http", url } } });
        assert.deepEqual((await sheet.locator(".lib-hubcap").allTextContents()).map((x) => x.trim()), [w("From another computer, use the address magpie is reached at there, with a gateway key: Authorization: Bearer <key>."),
          ...(web ? [] : [w("For an app that doesn't search its tools itself: past {n} tools, it is given one that finds a tool and one that runs it.", { n: 10 })])]);
        assert.deepEqual(await sideways(page), [], "sideways scroll, apps");
        await shot(page, `${engine}-${lang}-${width}-apps`);
        await bar.locator("button.primary").click();
        await sheet.waitFor({ state: "hidden" });

        // 25 agents: the connected and the kept fold, a search narrows
        // them, and the sheet still fits the window
        st.many = true;
        await page.reload();
        await page.locator('button[data-view="library"]').click();
        await v.locator(".lib-hubconnect").click();
        await sheet.waitFor();
        await tabs.first().click();
        const can = web ? 22 : 23, on = web ? 18 : 19;
        assert.equal(await lead(), w("{on} of {all} agents use the library's servers through magpie", { on, all: can }));
        assert.equal(await sheet.locator(".lib-hubsearch input").count(), 1);
        // the owner: the glass and the placeholder sat above the field's
        // middle. Both are centred in the box, the placeholder's own line too
        const mid = await sheet.locator(".lib-hubsearch").evaluate((b) => {
          const c = (r) => (r.top + r.bottom) / 2, box = b.getBoundingClientRect(), inp = b.querySelector("input");
          const ir = inp.getBoundingClientRect(), cs = getComputedStyle(inp);
          // where the text's line sits inside the input: its padding and line box
          const line = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.2;
          const top = ir.top + parseFloat(cs.borderTopWidth) + parseFloat(cs.paddingTop);
          const inner = ir.height - parseFloat(cs.borderTopWidth) - parseFloat(cs.borderBottomWidth) - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom);
          return { box: c(box), glass: c(b.querySelector("svg").getBoundingClientRect()), input: c(ir), text: top + inner / 2, inTop: ir.top - box.top, inBottom: box.bottom - ir.bottom, line, inner };
        });
        for (const k of ["glass", "input", "text"]) assert.ok(Math.abs(mid[k] - mid.box) <= 0.5, `the search's ${k} is ${(mid[k] - mid.box).toFixed(2)}px off the box's middle: ${JSON.stringify(mid)}`);
        assert.ok(mid.inTop >= 0 && mid.inBottom >= 0, `the input runs out of its box: ${JSON.stringify(mid)}`);
        assert.deepEqual(await sheet.locator(".lib-hubgrp").evaluateAll((gs) => gs.map((g) => [g.dataset.group, g.querySelectorAll(".lib-hubagent").length, g.querySelectorAll(".lib-hubfolded").length])),
          [["off", 4, 0], ["on", 0, 1], ["kept", 0, 1]]);
        assert.equal((await sheet.locator('.lib-hubgrp[data-group="on"] .lib-hubfolded .name').textContent()).trim(),
          w("{first} and {n} more", { first: web ? "Claude Code, Agent 01" : "Claude Code, Cursor", n: on - 2 }));
        const inWindow = await page.evaluate(() => {
          const d = document.querySelector(".lib-hubsheet").getBoundingClientRect();
          return d.top >= 0 && d.bottom <= innerHeight;
        });
        assert.ok(inWindow, "the sheet is taller than the window");
        assert.deepEqual(await sideways(page), [], "sideways scroll, 25 agents");
        await shot(page, `${engine}-${lang}-${width}-many`);
        // unfold the connected: every one, each a switch
        await sheet.locator('.lib-hubgrp[data-group="on"] .lib-hubfold').click();
        assert.equal(await sheet.locator('.lib-hubgrp[data-group="on"] .lib-hubagent .lib-switch[aria-checked="true"]').count(), on);
        assert.equal((await sheet.locator('.lib-hubgrp[data-group="on"] .lib-hubfold').textContent()).trim(), w("Hide"));
        // search: across every group, folded or not, and none
        await sheet.locator(".lib-hubsearch input").fill("agent 2");
        assert.deepEqual(await rows.evaluateAll((rs) => rs.map((r) => r.dataset.agent)), ["agent20", "agent21"]);
        assert.equal(await sheet.locator(".lib-hubsearch input").evaluate((i) => i === document.activeElement), true);
        await shot(page, `${engine}-${lang}-${width}-search`);
        await sheet.locator(".lib-hubsearch input").fill("zzz");
        assert.equal((await sheet.locator(".lib-hubnone").textContent()).trim(), w("No agent called “{q}”", { q: "zzz" }));
        await sheet.locator(".lib-hubsearch input").fill("");
        // Connect all: the four given nothing yet, in one post
        st.all.length = 0;
        await bar.locator(".lib-huball").click();
        await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("{n} agents are connected — restart them to use magpie's tools", { n: 4 }));
        assert.deepEqual(st.all, [{ agents: ["agent18", "agent19", "agent20", "agent21"], on: true }]);
        assert.equal(await bar.locator(".lib-huball").count(), 0);
        await bar.locator("button.primary").click();
        await sheet.waitFor({ state: "hidden" });
        st.many = false;

        // off again
        await v.locator(".lib-hub .lib-switch").click();
        await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("The agents are given each server's own entry again — restart them to use it"));
        assert.deepEqual(st.sent, [{ on: true }, { on: false }]);
        await page.waitForFunction(() => document.querySelector("#view-library .lib-hub .lib-switch")?.getAttribute("aria-checked") === "false");
        assert.equal(await v.locator(".lib-hub-why").count(), 0);
        assert.equal(await v.locator(".lib-hubstats, .lib-hubat").count(), 0);

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
