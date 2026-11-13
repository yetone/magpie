// Run with Node's test runner and Playwright on the module path; see README.md.
// The skill router, on the Library's Skills tab: a Skill router row under
// the list with a switch. Off, no skill has a router button. On (posts
// skills/router {on: true}), each skill an agent may pick itself gets a
// button that keeps it out of the router (skills/router-direct), and a
// skill only for the user to type gets none. A skill kept out shows "Not
// through the router", and the same button lists it again. An agent that
// keeps its own skills is named under the row with why. In every
// language, both engines, wide and narrow: no sideways scroll.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/mg";
const fixture = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  skillRouter: st.on,
  agents: [
    { id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, ...(st.on ? { skillRouter: "on" } : {}) },
    { id: "pi", name: "Pi", icon: "", skills: `${HOME}/.agents/skills`, ...(st.on ? { skillRouter: "shared" } : {}) },
  ],
  instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [{ name: "mine", description: "Its own", agents: ["claude"] }], problems: [],
  skills: [
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

        // off: the row and its switch, no router buttons, no why
        assert.equal((await row.locator(".name").textContent()).trim(), w("Skill router"));
        const sw = row.locator(".lib-switch");
        assert.equal(await sw.getAttribute("aria-checked"), "false");
        assert.equal(await v.locator(".lib-routedirect").count(), 0);
        assert.equal(await v.locator(".lib-hub-why").count(), 0);
        assert.deepEqual(await sideways(page), []);
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
        assert.equal(await v.locator(".lib-hub-why").count(), 2);
        assert.deepEqual(await sideways(page), []);
        await shot(page, `${engine}-${lang}-${width}-on`);

        // pdf kept out of the router
        await pdf.locator(".lib-routedirect").click();
        await v.locator(".lib-tag.lib-direct").waitFor();
        assert.deepEqual(st.sent.shift(), ["direct", { name: "pdf", on: true }]);
        assert.equal((await v.locator(".lib-tag.lib-direct").textContent()).trim(), w("Not through the router"));
        assert.equal(await v.locator(".lib-routedirect.on").getAttribute("aria-pressed"), "true");
        assert.deepEqual(await sideways(page), []);
        await shot(page, `${engine}-${lang}-${width}-direct`);

        // the same button lists it again
        await v.locator(".lib-routedirect.on").click();
        await v.locator(".lib-tag.lib-direct").waitFor({ state: "detached" });
        assert.deepEqual(st.sent.shift(), ["direct", { name: "pdf", on: false }]);

        // off again: no router buttons
        await v.locator(".lib-skillrouter .lib-switch").click();
        await v.locator(".lib-routedirect").first().waitFor({ state: "detached" });
        assert.deepEqual(st.sent.shift(), ["router", { on: false }]);
        assert.deepEqual(st.sent, []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
