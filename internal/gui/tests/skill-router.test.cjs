// Run with Node's test runner and Playwright on the module path; see README.md.
// The skill router, on the Library's Skills tab: a Skill router row at the
// top, over the install card and the list, with a switch. Off, no skill has a router button. On (posts
// skills/router {on: true}), each skill an agent may pick itself gets a
// button that keeps it out of the router (skills/router-direct), and a
// skill only for the user to type gets none. A skill kept out shows "Not
// through the router", and the same button lists it again. An agent that
// keeps its own skills is named under the row with why, and one whose
// router description has no room for every name with how many are only in
// its list. In every
// language, both engines, wide and narrow: no sideways scroll.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");
// the sidebar's Skills star, as navicons.js draws it
const SKILLS_STAR = require("node:fs").readFileSync(path.join(__dirname, "../assets/navicons.js"), "utf8").match(/\n    skills: "([^"]+)"/)[1];

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/mg";
const fixture = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  skillRouter: st.on,
  agents: [
    { id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, ...(st.on ? { skillRouter: "on", routerUnnamed: 37 } : {}) },
    { id: "codex", name: "Codex", icon: "", skills: `${HOME}/.codex/skills`, ...(st.on ? { skillRouter: "on", routerUnnamed: 1 } : {}) },
    { id: "opencode", name: "OpenCode", icon: "", skills: `${HOME}/.config/opencode/skills`, ...(st.on ? { skillRouter: "on", routerUnnamed: 37 } : {}) },
    { id: "pi", name: "Pi", icon: "", skills: `${HOME}/.agents/skills`, ...(st.on ? { skillRouter: "shared" } : {}) },
  ],
  instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [{ name: "mine", description: "Its own", agents: ["claude"] }], problems: [],
  skills: st.empty ? [] : [
    { name: "pdf", description: "Read PDFs", kind: "folder", agents: ["claude", "pi"], ...(st.direct ? { router: "direct" } : {}) },
    { name: "deploy", description: "Ship it", kind: "folder", agents: ["claude"], manual: true },
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
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(fixture(st));
    if (url.pathname === "/api/library/skills/router") {
      const body = req.postDataJSON();
      st.sent.push(["router", body]);
      st.on = body.on;
      return json({ ...fixture(st), result: { changed: ["claude"] } });
    }
    if (url.pathname === "/api/library/skills/router-direct") {
      const body = req.postDataJSON();
      st.sent.push(["direct", body]);
      st.direct = body.on;
      return json({ ...fixture(st), result: { changed: ["claude"] } });
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

const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-skillrouter, #view-library .lib-skillrouter .lib-switch, #view-library .lib-hub-why, #view-library .lib-direct, #view-library .lib-routedirect")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push(e.className + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

// two cards one over the other in the Library's body, with nothing between,
// keep room between them (the owner, 11-23: the install card sat on the
// Skill router's): every pair of boxed neighbours is at least 10px apart
const crowded = (page) => page.evaluate(() => {
  const boxed = (e) => { const c = getComputedStyle(e); return parseFloat(c.borderTopWidth) > 0 || c.backgroundColor !== "rgba(0, 0, 0, 0)"; };
  const kids = [...document.querySelectorAll("#view-library .lib-body:not(.lib-skel) > *")].filter((e) => e.getBoundingClientRect().height);
  const out = [];
  for (let i = 1; i < kids.length; i++) {
    if (!boxed(kids[i - 1]) || !boxed(kids[i])) continue;
    const gap = kids[i].getBoundingClientRect().top - kids[i - 1].getBoundingClientRect().bottom;
    if (gap < 10) out.push(`${kids[i - 1].className} | ${kids[i].className}: ${gap.toFixed(1)}px`);
  }
  return { n: kids.length, out };
});

// a control the list's length has put under the Library's sticky head
// (four lines over the list in German at 420px) is brought to the middle
// first, as a reader scrolls to it
const press = async (loc) => {
  await loc.evaluate((e) => e.scrollIntoView({ block: "center" }));
  await loc.click();
};

const shot = async (page, name) => {
  if (!process.env.ARTIFACT_DIR) return;
  await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
  await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `skill-router-${name}.png`), fullPage: true });
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the skill router on the Skills tab", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const width of [1100, 420]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const st = { on: false, direct: false, sent: [], web: width < 800 };
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const row = v.locator(".lib-skillrouter");
        await row.waitFor();

        // at the top: over the install card and the skill list
        assert.equal(await page.evaluate(() => {
          const v = document.querySelector("#view-library");
          const r = v.querySelector(".lib-skillrouter"), c = v.querySelector(".lib-install"), l = v.querySelector(".lib-groups");
          return !!(r.compareDocumentPosition(c) & Node.DOCUMENT_POSITION_FOLLOWING) && !!(r.compareDocumentPosition(l) & Node.DOCUMENT_POSITION_FOLLOWING);
        }), true, "the Skill router row is above the install card and the list");

        assert.equal(await v.locator(".lib-skillrouter-empty").count(), 0, "with library skills, no line on importing them");
        // its mark is the sidebar Skills star, not a skill's page
        assert.equal(await row.locator(".lib-glyph svg path").getAttribute("d"), SKILLS_STAR, "the Skill router row has the sidebar's Skills icon");
        // off: the row and its switch, no router buttons, no why
        assert.equal((await row.locator(".name").textContent()).trim(), w("Skill router"));
        const sw = row.locator(".lib-switch");
        assert.equal(await sw.getAttribute("aria-checked"), "false");
        assert.equal(await v.locator(".lib-routedirect").count(), 0);
        assert.equal(await v.locator(".lib-hub-why").count(), 0);
        assert.deepEqual(await sideways(page), []);
        { const c = await crowded(page); assert.ok(c.n > 2, "the body is measured"); assert.deepEqual(c.out, [], "cards apart, off"); }
        await shot(page, `${engine}-${lang}-${width}-off`);

        // on
        await sw.click();
        await v.locator(".lib-routedirect").first().waitFor();
        assert.deepEqual(st.sent.shift(), ["router", { on: true }]);
        assert.equal(await row.locator(".lib-switch").getAttribute("aria-checked"), "true");
        // a button on pdf, none on deploy (only for the user to type)
        const pdf = v.locator(".lib-row", { has: page.locator(".lib-routedirect") });
        assert.equal(await pdf.count(), 1);
        assert.match(await pdf.textContent(), /pdf/);
        assert.equal(await pdf.locator(".lib-routedirect").getAttribute("aria-pressed"), "false");
        // Pi keeps its own skills, and the page says why
        const why = v.locator('.lib-hub-why[data-why="shared"]');
        assert.equal((await why.textContent()).trim(), w("{agents} reads ~/.agents/skills, which other agents read too, so it keeps each skill of its own.", { agents: "Pi" }));
        // Claude Code has a skill of its own the library lacks: it stays in
        // its prompt, the router can't list it, and the page says so
        const found = v.locator('.lib-hub-why[data-why="found"]');
        assert.equal((await found.textContent()).trim(), w("{agents} has skills of its own that aren't in the library, so the router doesn't list them: bring them in below.", { agents: "Claude Code" }));
        // the router's description has no room for every name (niuniu_8964
        // on X): how many are only in its list, the agents with the same
        // count on one line
        const unnamed = v.locator('.lib-hub-why[data-why="unnamed"]');
        assert.deepEqual((await unnamed.allTextContents()).map((s) => s.trim()), [
          w("{agents} sees {n} of the router's skills only once it opens the router: its description has no room to name them.", { n: 37, agents: "Claude Code, OpenCode" }),
          w("{agents} sees 1 of the router's skills only once it opens the router: its description has no room to name it.", { agents: "Codex" }),
        ]);
        assert.equal(await v.locator(".lib-hub-why").count(), 4);
        assert.deepEqual(await sideways(page), []);
        { const c = await crowded(page); assert.ok(c.n > 2, "the body is measured"); assert.deepEqual(c.out, [], "cards apart, on"); }
        await shot(page, `${engine}-${lang}-${width}-on`);

        // pdf kept out of the router
        await press(pdf.locator(".lib-routedirect"));
        await v.locator(".lib-tag.lib-direct").waitFor();
        assert.deepEqual(st.sent.shift(), ["direct", { name: "pdf", on: true }]);
        assert.equal((await v.locator(".lib-tag.lib-direct").textContent()).trim(), w("Not through the router"));
        assert.equal(await v.locator(".lib-routedirect.on").getAttribute("aria-pressed"), "true");
        assert.deepEqual(await sideways(page), []);
        await shot(page, `${engine}-${lang}-${width}-direct`);

        // the same button lists it again
        await press(v.locator(".lib-routedirect.on"));
        await v.locator(".lib-tag.lib-direct").waitFor({ state: "detached" });
        assert.deepEqual(st.sent.shift(), ["direct", { name: "pdf", on: false }]);

        // off again: no router buttons
        await press(v.locator(".lib-skillrouter .lib-switch"));
        await v.locator(".lib-routedirect").first().waitFor({ state: "detached" });
        assert.deepEqual(st.sent.shift(), ["router", { on: false }]);
        assert.deepEqual(st.sent, []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}

// The owner (11-23): every skill still in the agents' own folders, none in
// the library, and no Skill router row at all. With agents it is there, at
// the top, and says it lists the library's skills, so the agents' own are
// imported for it. The MCP tab's router row shows with no server alike.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the skill router with no skill in the library yet", async (t) => {
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const width of [1100, 420]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const st = { on: false, direct: false, empty: true, sent: [], web: width < 800 };
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const row = v.locator(".lib-skillrouter");
        await row.waitFor();
        assert.equal((await row.locator(".name").textContent()).trim(), w("Skill router"));
        assert.equal((await row.locator(".lib-skillrouter-empty").textContent()).trim(), w("It lists the library's skills. Import the agents' own skills below for it to list them."));
        assert.equal(await page.evaluate(() => {
          const v = document.querySelector("#view-library");
          const r = v.querySelector(".lib-skillrouter"), c = v.querySelector(".lib-install");
          return !!(r.compareDocumentPosition(c) & Node.DOCUMENT_POSITION_FOLLOWING);
        }), true, "the Skill router row is above the install card");
        assert.deepEqual(await sideways(page), []);
        await shot(page, `${engine}-${lang}-${width}-empty`);
        await row.locator(".lib-switch").click();
        await row.locator('.lib-switch[aria-checked="true"]').waitFor();
        assert.deepEqual(st.sent.shift(), ["router", { on: true }]);

        // the MCP tab, agents and no server: its router row is there too,
        // over "No servers in the library yet"
        const mctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await mctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const mp = await mctx.newPage();
        mp.setDefaultTimeout(5000);
        mp.setDefaultNavigationTimeout(60000);
        mp.on("pageerror", (e) => errors.push(e.message));
        await mp.route("**/*", server(lang, st));
        await mp.goto("http://magpie.test/");
        await mp.locator('button[data-view="library"]').click();
        await mp.locator("#view-library .lib-empty").waitFor();
        assert.equal(await mp.locator("#view-library .lib-skillrouter").count(), 0, "the MCP tab, not Skills");
        assert.equal(await mp.evaluate(() => {
          const v = document.querySelector("#view-library");
          const h = v.querySelector(".lib-hub"), e = v.querySelector(".lib-empty");
          return !!h && !!(h.compareDocumentPosition(e) & Node.DOCUMENT_POSITION_FOLLOWING);
        }), true, "the MCP router row is over the empty list");
        assert.deepEqual(await sideways(mp), []);
        await mctx.close();
        assert.deepEqual(await sideways(page), []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
