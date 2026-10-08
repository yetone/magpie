// Run with Node's test runner and Playwright on the module path; see README.md.
// #1027 (emo172): the Library's MCP servers page had only By agent and the
// order, each under an "In the library" heading of its own, so with both
// the page showed the heading twice; turning every server on or off, or
// removing them, was a chip or a row at a time. It now has the skills
// page's: one heading holding the order, Select, Turn all on, Turn all off,
// By agent and Remove all. Turn all on posts every agent shown (not a
// hidden one) to servers/agents-all; Turn all off asks first, in the page,
// and Cancel posts nothing. Select puts a box on each row, a row's click
// ticks it rather than opening the editor, and the bar's chips post the
// servers picked to servers/agents-some; an agent that can reach none of
// them (Codex, no SSE) has its chip greyed and a click posts nothing.
// Remove all asks first, saying who loses them, the sign-ins forgotten and
// that the agents' own servers stay, then posts every name to
// servers/remove-all. At 440px wide in en, zh, ja and de: no sideways
// scroll, and what each click pressed stays where it was on the screen.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const sv = (name, transport, agents, more = {}) => ({ name, transport, command: transport === "stdio" ? name + "-mcp" : "", url: transport === "stdio" ? "" : "http://localhost:9/" + name, agents, ...more });
const lib = (servers) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex", { noSSE: true }), agent("copilot", "Copilot CLI"), agent("gemini", "Gemini CLI")],
  instructions: { agents: [] }, servers, skills: [], foundServers: [], projects: [], foundSkills: [], problems: [],
});

let I18N;
async function loadI18N() {
  const src = await fs.readFile(path.join(assets, "i18n.js"), "utf8");
  I18N = new Function(src.slice(0, src.indexOf("\n};") + 3) + "; return I18N;")();
}
// the words the page says in lang; a string missing from a language fails
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang, state, posts) {
  const can = (s, a) => !(s.transport === "sse" && a === "codex");
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", agentsHidden: ["copilot"] } });
    if (url.pathname === "/api/library") return json(lib(state.servers));
    // every server answers, as a real check does: one without a result is
    // checked again on each drawing, which moves the rows under a click
    if (url.pathname === "/api/library/mcp/check") return json({ servers: Object.fromEntries(req.postDataJSON().names.map((n) => [n, { state: "ok", tools: 2 }])) });
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST" && !url.pathname.endsWith("/reveal")) {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      const put = (x, agents, on) => ({ ...x, agents: on ? [...new Set([...x.agents, ...agents.filter((a) => can(x, a))])].sort() : x.agents.filter((a) => !agents.includes(a)) });
      if (what === "servers/agents-all") state.servers = state.servers.map((x) => put(x, body.agents, body.on));
      if (what === "servers/agents-some") state.servers = state.servers.map((x) => (body.names.includes(x.name) ? put(x, body.agents, body.on) : x));
      if (what === "servers/remove-all") state.servers = state.servers.filter((x) => !body.names.includes(x.name));
      return json({ ...lib(state.servers), result: { changed: ["claude"] } });
    }
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

// a click where the button is, as the reader's (Playwright's own click first
// scrolls the button into view), and what was clicked stays where it was on
// the screen while the click redraws around it: a click never scrolls the
// page (app.js's guard holds it). The window is tall enough for the whole
// list, as the pick bar stays over the last rows of a longer one.
const press = async (page, loc) => {
  const b = await loc.boundingBox();
  const sel = String(loc);
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
  await page.waitForTimeout(250);
  if (!(await loc.count())) return; // gone with what it did (Clear, Done)
  const a = await loc.boundingBox();
  if (a && a.width) assert(Math.abs(a.y - b.y) <= 1, `${sel} moved on the screen from ${Math.round(b.y)} to ${Math.round(a.y)}`);
};
const settled = (page) => page.waitForFunction(() => !document.querySelector("#modal").getAnimations({ subtree: true }).length);
// nothing wider than the window: the page, the Library, its heading and bar
const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  const v = document.querySelector("#view-library");
  if (v.scrollWidth > v.clientWidth + 1) over.push("library " + v.scrollWidth + ">" + v.clientWidth);
  for (const e of document.querySelectorAll("#view-library .lib-serverhead > *, #view-library .lib-pickbar > *, #view-library .lib-pickbar")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the servers page's Select, Turn all on/off and Remove all", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    await loadI18N();
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "ja", "de"]) {
      await t.test(lang, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const state = { servers: [sv("fs", "stdio", ["claude", "copilot"]), sv("web", "sse", ["claude"]), sv("docs", "http", [], { signIn: { signedIn: true } })] };
        const posts = [], errors = [];
        const ctx = await browser.newContext({ viewport: { width: 440, height: 1000 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
        await page.route("**/*", server(lang, state, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        await v.locator(".lib-server").first().waitFor();
        const row = (name) => v.locator(".lib-server").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name) }) });
        const agentsOf = (name) => state.servers.find((s) => s.name === name)?.agents;

        // one heading over the list, holding every action
        const heads = await v.locator(".row-head .label").allTextContents();
        assert.deepEqual(heads.filter((x) => x === w("In the library")), [w("In the library")], "one In the library heading: " + heads);
        const head = v.locator(".row-head.lib-serverhead");
        assert.equal((await head.locator(".lib-select").textContent()).trim(), w("Select"));
        assert.equal((await head.locator(".lib-everyon").textContent()).trim(), w("Turn all on"));
        assert.equal((await head.locator(".lib-everyoff").textContent()).trim(), w("Turn all off"));
        assert.equal((await head.locator(".lib-byagent").textContent()).trim(), w("By agent"));
        assert.equal((await head.locator(".lib-everyrm").textContent()).trim(), w("Remove all"));
        assert.equal(await head.locator(".lib-everyon").getAttribute("title"), w("Give every server to all {n} agents, each the ones it can reach", { n: 3 }));
        assert.equal(await head.locator(".lib-everyrm").getAttribute("title"), w("Take all {n} servers out of the library", { n: 3 }));
        assert.deepEqual(await sideways(page), [], "sideways scroll with the heading");

        // Turn all on: every agent shown, the hidden Copilot CLI left alone
        await press(page, head.locator(".lib-everyon"));
        await page.locator("#status").filter({ hasText: w("{n} servers are on for all {m} agents", { n: 3, m: 3 }) }).waitFor();
        assert.deepEqual(posts.shift(), { what: "servers/agents-all", body: { agents: ["claude", "codex", "gemini"], on: true } });
        assert.deepEqual(agentsOf("web"), ["claude", "gemini"], "SSE is no server for Codex");
        await page.waitForFunction(() => document.querySelector("#view-library .lib-serverhead .lib-everyon")?.disabled);

        // Turn all off asks first; Cancel posts nothing
        await press(page, v.locator(".lib-serverhead .lib-everyoff"));
        const sheet = page.locator("#modal .lib-editor");
        await sheet.waitFor();
        await settled(page);
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w("Turn off all {n} servers?", { n: 3 }));
        assert.equal((await sheet.locator(".lib-confirm").textContent()).trim(), w("Every server is taken out of {agents}. They stay in the library, to turn on again.", { agents: "Claude Code, Codex, Gemini CLI" }));
        await sheet.locator(".bar button", { hasText: w("Cancel") }).click();
        await sheet.waitFor({ state: "detached" });
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "Cancel posts nothing");
        await press(page, v.locator(".lib-serverhead .lib-everyoff"));
        await sheet.waitFor();
        await settled(page);
        await sheet.locator(".bar button.danger-fill").click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "servers/agents-all", body: { agents: ["claude", "codex", "gemini"], on: false } });
        await page.locator("#status").filter({ hasText: w("{n} servers are off for all {m} agents", { n: 3, m: 3 }) }).waitFor();
        assert.deepEqual(agentsOf("fs"), ["copilot"], "the hidden agent keeps its own");

        // Select: a box on each row, which a row's click ticks
        assert.equal(await v.locator(".lib-pickbox").count(), 0);
        await press(page, v.locator(".lib-serverhead .lib-select"));
        assert.equal((await v.locator(".lib-serverhead .lib-select").textContent()).trim(), w("Done"));
        assert.equal(await v.locator(".lib-server .lib-pickbox").count(), 3);
        await v.locator(".lib-pickbar .lib-pickn.none").filter({ hasText: w("Pick servers to turn them on or off together") }).waitFor();
        await press(page, row("fs").locator(".sub"));
        assert.equal(await row("fs").locator(".lib-pickbox").isChecked(), true);
        assert.equal(await row("fs").evaluate((r) => r.classList.contains("picked")), true);
        assert.equal(await page.locator("#modal .lib-editor").count(), 0, "the click opened the server");
        await press(page, row("web").locator(".lib-pickbox"));
        await v.locator(".lib-pickbar .lib-pickn").filter({ hasText: w("{n} selected", { n: 2 }) }).waitFor();
        assert.deepEqual(await sideways(page), [], "sideways scroll with the bar");

        // the bar's chips: Codex is given the one of the two it can reach
        const chip = (id) => v.locator(`.lib-pickbar .lib-ag[data-agent="${id}"]`);
        assert.equal(await chip("copilot").count(), 0, "a hidden agent has a chip");
        assert.equal(await chip("codex").getAttribute("title"), w("Give {agent} all of these servers", { agent: "Codex" }));
        await press(page, chip("codex"));
        await page.locator("#status").filter({ hasText: w("{n} servers are on for {agents}", { n: 2, agents: "Codex" }) }).waitFor();
        assert.deepEqual(posts.shift(), { what: "servers/agents-some", body: { names: ["fs", "web"], agents: ["codex"], on: true } });
        assert.deepEqual(agentsOf("fs"), ["codex", "copilot"]);
        assert.deepEqual(agentsOf("docs"), [], "a server not picked was given");
        // the bar drawn again from magpie's answer: the chip's title, not
        // only its lit state, which a click sets before the answer
        await page.waitForFunction((tip) => document.querySelector('#view-library .lib-pickbar .lib-ag[data-agent="codex"]')?.title === tip,
          w("{agent} has all of these servers — click to take them away", { agent: "Codex" }));
        assert.equal(await row("web").locator(".lib-pickbox").isChecked(), true, "the picks were lost");
        // and taken back
        await press(page, chip("codex"));
        await page.locator("#status").filter({ hasText: w("{n} servers are off for {agents}", { n: 2, agents: "Codex" }) }).waitFor();
        assert.deepEqual(posts.shift(), { what: "servers/agents-some", body: { names: ["fs", "web"], agents: ["codex"], on: false } });

        // web alone: Codex can't reach it, and a click says so, posting nothing
        await press(page, v.locator(".lib-pickclear"));
        await v.locator(".lib-pickbar .lib-pickn.none").waitFor();
        await press(page, row("web").locator(".lib-pickbox"));
        await v.locator(".lib-pickbar .lib-pickn").filter({ hasText: w("{n} selected", { n: 1 }) }).waitFor();
        assert.equal(await chip("codex").getAttribute("aria-disabled"), "true");
        await press(page, chip("codex"));
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "a greyed chip posted");

        // the bar's box picks every server
        await press(page, v.locator(".lib-pickall input"));
        await v.locator(".lib-pickbar .lib-pickn").filter({ hasText: w("{n} selected", { n: 3 }) }).waitFor();
        assert.equal((await v.locator(".lib-pickall").textContent()).trim(), w("All {n}", { n: 3 }));
        assert.deepEqual(await sideways(page), [], "sideways scroll with every server picked");

        // Done takes the boxes away; a row opens its server again
        await press(page, v.locator(".lib-pickdone"));
        assert.equal(await v.locator(".lib-pickbox").count(), 0);
        assert.equal(await v.locator(".lib-pickbar").count(), 0);
        assert.equal((await v.locator(".lib-serverhead .lib-select").textContent()).trim(), w("Select"));

        // Remove all asks first, saying what goes; Cancel posts nothing
        await press(page, v.locator(".lib-serverhead .lib-everyrm"));
        await sheet.waitFor();
        await settled(page);
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w("Remove all {n} servers?", { n: 3 }));
        const said = (await sheet.locator(".lib-confirm").allTextContents()).map((x) => x.trim());
        assert.deepEqual(said, [
          // only the hidden Copilot CLI has one now, after Turn all off
          w("They are taken out of the library and out of {agents}.", { agents: "Copilot CLI" }),
          w("magpie's sign-in to {n} of them is forgotten too.", { n: 1 }),
          w("Servers your agents have that aren't in the library stay as they are."),
        ]);
        await sheet.locator(".bar button", { hasText: w("Cancel") }).click();
        await sheet.waitFor({ state: "detached" });
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "Cancel posts nothing");
        assert.equal(await v.locator(".lib-server").count(), 3);

        await press(page, v.locator(".lib-serverhead .lib-everyrm"));
        await sheet.waitFor();
        await settled(page);
        const go = sheet.locator(".bar button.danger-fill");
        assert.equal((await go.textContent()).trim(), w("Remove all {n}", { n: 3 }));
        await go.click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "servers/remove-all", body: { names: ["fs", "web", "docs"] } }, "one post, with every server");
        await page.locator("#status").filter({ hasText: w("{n} servers removed", { n: 3 }) }).waitFor();
        assert.equal(await v.locator(".lib-server").count(), 0, "a server is left");
        assert.equal(await v.locator(".lib-serverhead").count(), 0);

        assert.deepEqual(posts, []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
