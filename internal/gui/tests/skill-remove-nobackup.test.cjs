// Run with Node's test runner and Playwright on the module path; see README.md.
// @Eternalfate__ on X: removing a skill from the Library always kept a copy
// in magpie's backups. The sheet that asks first now has one tick, "Keep a
// copy in magpie's backups", ticked unless the reader unticked it last time
// (remembered in this browser). Unticked, what the sheet says changes to
// the folder being deleted, and Remove posts noBackup: true; ticked, it
// posts what it did before. A skill linked from a folder of the user's
// has no tick: only the link goes. An agent's own skill with its folder in
// ~/.agents/skills, where most users' skills are, has the tick (the same
// reader saw none on any sheet, v0.1.4594): it leaves that folder either
// way, deleted or kept in the backups as the tick says; one there that is
// a link has none. Remove all counts a skill brought in from the agents as
// going back to them, not as a folder kept or deleted, and with only such
// and linked ones has no tick. Chromium and WebKit, 980px and 440px, in
// en, zh, zh-TW, ja and de; nothing runs off the page sideways.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/ef";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const lib = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("goose", "Goose")],
  instructions: { agents: [] }, servers: [], foundServers: [], projects: [], problems: [],
  skills: st.skills, foundSkills: st.found,
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

function server(lang, st, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib(st));
    if (url.pathname === "/api/library/skills/remove" || url.pathname === "/api/library/skills/remove-found") {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      st.skills = st.skills.filter((s) => s.name !== body.name);
      st.found = st.found.filter((s) => s.name !== body.name);
      return json({ ...lib(st), result: { changed: ["claude"] } });
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
  for (const e of document.querySelectorAll("#modal .lib-editor, #modal .lib-editor .bar > *, #modal .lib-editor .lib-confirm, #modal .lib-keepbox")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a skill removed with no copy kept, when the reader unticks it", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const width of [980, 440]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const st = {
          skills: [
            { name: "notes", kind: "", description: "Notes", agents: ["claude"] },
            { name: "lint", kind: "folder", description: "Lint", source: `${HOME}/.agents/skills/lint`, agents: ["claude"] },
            { name: "brought", kind: "folder", description: "Brought in", source: `${HOME}/.agents/skills/brought`, agents: ["codex"], back: ["codex"] },
          ],
          found: [
            { name: "old", description: "Not wanted", agents: ["claude"] },
            { name: "grilling", description: "Grill a plan", agents: ["goose"], shared: `${HOME}/.agents/skills/grilling` },
            { name: "web", description: "Web", agents: ["claude"], link: `${HOME}/src/web`, shared: `${HOME}/.agents/skills/web` },
          ],
        };
        const posts = [], errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
        await page.route("**/*", server(lang, st, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const row = (name) => page.locator("#view-library .lib-row").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
        await row("notes").waitFor();
        const sheet = page.locator("#modal .lib-editor");
        const ask = async (name) => {
          await row(name).hover();
          await row(name).locator("button.lib-icon.danger").click();
          await sheet.waitFor();
        };
        const lines = async () => (await sheet.locator(".lib-confirm").allTextContents()).map((s) => s.trim());
        const box = sheet.locator("label.lib-keepbox input[type=checkbox]");
        const cancel = async () => {
          await sheet.locator(".bar button:not(.primary)").click();
          await sheet.waitFor({ state: "detached" });
        };
        const removeAll = async () => {
          await page.locator("#view-library .lib-skillshead .lib-barmore").click();
          await page.locator(".row-menu .rm-item.lib-everyrm").click();
          await sheet.waitFor();
        };

        // Remove all: the one brought in goes back, only notes' folder is
        // kept, so the tick is for that one
        await removeAll();
        assert.equal(await box.count(), 1, "no tick with a folder of magpie's");
        assert.equal((await lines()).at(-1), [
          w("{n} go back to the agents they were brought in from, as they were.", { n: 1 }),
          w("{n} folders are moved to magpie's backups (Backups, at the top of the Library), not deleted.", { n: 1 }),
          w("{n} linked from folders of your own are only unlinked: those folders stay where they are.", { n: 1 }),
        ].join(" "));
        await cancel();

        // the library's own folder: ticked at first, as it always was
        await ask("notes");
        assert.equal(await box.count(), 1, "no tick");
        assert.equal(await box.isChecked(), true, "not ticked at first");
        assert.equal((await sheet.locator("label.lib-keepbox").textContent()).trim(), w("Keep a copy in magpie's backups"));
        assert.deepEqual(await lines(), [w("It is taken out of every agent it was given to, and its folder is moved to magpie's backups.")]);
        await sheet.locator("label.lib-keepbox").click();
        assert.equal(await box.isChecked(), false);
        assert.deepEqual(await lines(), [w("It is taken out of every agent it was given to, and its folder is deleted, with no copy kept.")]);
        assert.deepEqual(await sideways(page), [], "sideways with the tick");
        await cancel();
        assert.deepEqual(posts, [], "Cancel posted");

        // remembered: unticked again, and Remove says so
        await ask("notes");
        assert.equal(await box.isChecked(), false, "the choice wasn't remembered");
        await sheet.locator(".bar button.primary").click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "skills/remove", body: { name: "notes", noBackup: true } });
        await row("notes").waitFor({ state: "detached" });

        // Remove all with only a linked one and one brought in: no tick
        await removeAll();
        assert.equal(await box.count(), 0, "a tick with nothing to keep or delete");
        assert.equal((await lines()).at(-1), [
          w("{n} go back to the agents they were brought in from, as they were.", { n: 1 }),
          w("{n} linked from folders of your own are only unlinked: those folders stay where they are.", { n: 1 }),
        ].join(" "));
        await cancel();

        // linked from a folder of the user's: no tick, its folder stays
        await ask("lint");
        assert.equal(await box.count(), 0, "a linked skill has the tick");
        assert.deepEqual(await lines(), [w("It is taken out of every agent it was given to. The folder it was linked from stays where it is.")]);
        await cancel();

        // an agent's own: the same tick; ticked again, Remove keeps a copy
        await ask("old");
        assert.equal(await box.isChecked(), false);
        assert.deepEqual(await lines(), [w("It is taken out of {agents}, and its folder is deleted, with no copy kept.", { agents: "Claude Code" })]);
        await sheet.locator("label.lib-keepbox").click();
        assert.deepEqual(await lines(), [w("It is taken out of {agents}, and its folder is moved to magpie's backups.", { agents: "Claude Code" })]);
        await sheet.locator(".bar button.primary").click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "skills/remove-found", body: { name: "old" } });

        // one in ~/.agents/skills, as most are: the same tick
        const at = `${HOME}/.agents/skills/grilling`;
        await ask("grilling");
        assert.equal(await box.count(), 1, "a skill in ~/.agents/skills has no tick");
        assert.equal(await box.isChecked(), true);
        assert.deepEqual(await lines(), [
          w("It is taken out of {agents}, and its folder is moved to magpie's backups.", { agents: "Goose" }),
          w("Its entry in {path} goes to the backups too, so no agent reads it from there.", { path: at }),
        ]);
        await sheet.locator("label.lib-keepbox").click();
        assert.deepEqual(await lines(), [
          w("It is taken out of {agents}, and its folder is deleted, with no copy kept.", { agents: "Goose" }),
          w("Its entry in {path} is deleted too, so no agent reads it from there.", { path: at }),
        ]);
        assert.deepEqual(await sideways(page), [], "sideways with the tick");
        await sheet.locator(".bar button.primary").click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "skills/remove-found", body: { name: "grilling", noBackup: true } });

        // a link there to a folder of the user's: only the link goes, no tick
        await ask("web");
        assert.equal(await box.count(), 0, "a linked skill has the tick");
        assert.deepEqual(await lines(), [
          w("It is taken out of {agents}. The folder it was linked from stays where it is.", { agents: "Claude Code" }),
          w("Its link in {path} goes too, so no agent reads it from there.", { path: `${HOME}/.agents/skills/web` }),
        ]);
        await cancel();
        assert.deepEqual(posts, []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
