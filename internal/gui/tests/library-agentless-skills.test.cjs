// Run with Node's test runner and Playwright on the module path; see README.md.
// fenglian980510 on X: in magpie's Docker gateway the Library had MCP and
// no Skills. A magpie on another computer that adds this one as a provider,
// with Use its skills and prompts on, takes its skills and prompt sets
// (GET /v1/magpie/library), so with no agent here, or in gateway mode, the
// Library has Instructions and Skills beside MCP: the sets and the skills,
// the line saying who takes them, the box to install one, and nothing of
// agents (no chips, no agents' own skills, no projects, no Agents list).
// The page still opens on MCP unless the reader picked another tab. In
// every language, both engines, the app with no agents at 1100px and a
// browser in gateway mode at 420px: no sideways scroll, no native select.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/srv/mg";
const fixture = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  mcpHub: false, hubPath: "/mcp/magpie/hub/",
  // in gateway mode this computer's agents are there, and not shown
  agents: st.gateway ? [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json`, instructions: `${HOME}/.claude/CLAUDE.md` }] : [],
  instructions: {
    agents: st.gateway ? [{ agent: "claude", name: "Claude Code", on: false, file: `${HOME}/.claude/CLAUDE.md` }] : [],
    sets: [{ id: "default", name: "", active: true, text: "Be brief." }],
  },
  skills: [{ name: "pdf", description: "PDFs", kind: "github", agents: [], source: "https://github.com/owner/repo/tree/main/pdf" }],
  foundSkills: st.gateway ? [{ name: "agent-own", description: "Own", agents: ["claude"] }] : [],
  projects: st.gateway ? [{ path: `${HOME}/code/app`, skills: { pdf: ["claude"] } }] : [],
  problems: [], foundServers: [], servers: [],
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
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${st.web}${st.gateway ? ",gateway:true" : ""}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", gateway: "http://127.0.0.1:3425" } });
    if (url.pathname === "/api/library") return json(fixture(st));
    if (url.pathname.startsWith("/api/library/market/")) return json({ items: [] });
    if (url.pathname === "/api/library/skills/check") return json({ ...fixture(st) });
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

const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-tabs, #view-library .lib-intro, #view-library .lib-row, #view-library .lib-sets")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

const tabName = async (v) => (await v.locator(".lib-tabs .opt.on").textContent()).trim().replace(/ · \d+$/, "");
// the page's tabs, or the side nav's at a wide window
const tabs = (page) => page.locator(":is(#view-library .lib-tabs .opt, .nav-s):visible");
const pick = async (page, name) => {
  await page.waitForFunction(() => !document.querySelector(".nav-sub .nav-s") || window.navSections);
  // a tab's count follows its name: "Skills · 1", or "Skills1" in the side nav
  const names = (await tabs(page).allTextContents()).map((x) => x.trim().replace(/\s*(· )?\d+$/, ""));
  const i = names.indexOf(name);
  assert.ok(i >= 0, `no ${name} tab in ${JSON.stringify(names)}`);
  await tabs(page).nth(i).click();
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": skills and prompts with no agent", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const width of [1100, 420]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const web = width < 800;
        const st = { web, gateway: web };
        const where = web ? "gateway mode" : "no agents";
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        await tabs(page).first().waitFor();

        // the tabs a client takes from, opened on MCP with none picked
        assert.deepEqual((await v.locator(".lib-tabs .opt").allTextContents()).map((x) => x.trim().replace(/ · \d+$/, "")), [w("Instructions"), w("MCP servers"), w("Skills")], `${where}: the tabs`);
        assert.equal(await tabName(v), w("MCP servers"), `${where}: opened on`);

        // Skills: the skill, who takes it, and nothing of agents
        await pick(page, w("Skills"));
        await page.waitForFunction(() => !!document.querySelector("#view-library .lib-row:not(.lib-hub)"));
        assert.equal(await tabName(v), w("Skills"));
        assert.equal((await v.locator(".lib-intro").first().textContent()).trim(), w("A magpie on another computer that adds this one as a provider, with Use its skills and prompts on, takes these skills and gives them to its agents."), where);
        assert.ok((await v.textContent()).includes("pdf"), `${where}: the skill isn't listed`);
        assert.equal(await v.locator(".lib-ag").count(), 0, `${where}: agent chips`);
        let text = await v.textContent();
        for (const gone of [w("In your agents"), w("Project skills"), "Claude Code", "agent-own"]) assert.ok(!text.includes(gone), `${where}: ${gone} is shown`);
        assert.ok(await v.locator('.mk[data-market="skills"]').count(), `${where}: no Discover`);
        assert.equal(await page.locator("select").count(), 0);
        assert.deepEqual(await sideways(page), [], `${where}: sideways scroll, skills`);

        // Instructions: the sets, who takes them, and no Agents list
        await pick(page, w("Instructions"));
        await v.locator(".lib-sets").waitFor();
        assert.equal((await v.locator(".lib-intro").first().textContent()).trim(), w("A magpie on another computer that adds this one as a provider, with Use its skills and prompts on, takes these sets, and its agents read them."), where);
        text = await v.textContent();
        for (const gone of [w("None of your agents reads a user-wide instructions file magpie knows."), w("Project instructions"), "Claude Code"]) assert.ok(!text.includes(gone), `${where}: ${gone} is shown`);
        assert.deepEqual(await sideways(page), [], `${where}: sideways scroll, instructions`);

        // the pick is kept: the page opens on it again
        await page.reload();
        // the Library may be the view the page comes back on already
        await page.locator('button[data-view="library"]').waitFor();
        if (!(await v.isVisible())) await page.locator('button[data-view="library"]').click();
        await v.locator(".lib-sets").waitFor();
        assert.equal(await tabName(v), w("Instructions"), `${where}: the pick wasn't kept`);
        // the tabs shown, the side nav's at a wide window, are the same three
        if (!web) {
          await page.waitForFunction(() => !document.querySelector(".nav-sub .nav-s") || window.navSections);
          await tabs(page).first().waitFor();
          const side = (await tabs(page).allTextContents()).map((x) => x.trim().replace(/\s*(· )?\d+$/, ""));
          for (const n of [w("Instructions"), w("MCP servers"), w("Skills")]) assert.ok(side.includes(n), `${where}: no ${n} tab: ${side}`);
        }

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
