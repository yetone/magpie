// Run with Node's test runner and Playwright on the module path; see README.md.
// wongxy on Discord (资源库技能支持批量删除): the skills the agents have of
// their own ("In your agents") went one row's trash at a time. That list has
// a Select of its own now: each row becomes its box (its Bring in and trash
// give way), and the bar's Remove… asks first, naming how many and where
// they go, then posts the names to skills/remove-found-some, each taken out
// as its row's trash takes it. Cancel posts nothing; one that can't be taken
// out stays, still picked, its row saying why, and only those gone are said
// to be removed; one alone that the change refuses keeps the sheet open.
// The library's own Select and this one are never on together. Chromium and
// WebKit, 1100px and 420px, en, zh, zh-TW, ja and de; no click moves the
// page, nothing runs off it sideways. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const FOUND = [
  { name: "notes", description: "Mine", agents: ["claude"], copies: ["codex"], others: ["gemini"] },
  { name: "dev", description: "Work in progress", agents: ["claude"], link: `${HOME}/src/dev` },
  { name: "grilling", description: "Grill a plan", agents: ["goose"], shared: `${HOME}/.agents/skills/grilling` },
  { name: "stuck", description: "Held", agents: ["gemini"] },
];
const local = (name) => ({ name, description: name + " does a thing", kind: "local", agents: ["claude"] });
const lib = (found) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("gemini", "Gemini CLI"), agent("goose", "Goose")],
  instructions: { agents: [] }, servers: [], skills: [local("lib-a"), local("lib-b")], foundServers: [], projects: [], foundSkills: found, problems: [],
});
const STUCK = "the folder couldn't be moved: permission denied";

let I18N;
async function loadI18N() {
  I18N = strings().I18N; // i18n.js's and the plugins'
}
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

// remove-found-some as RemoveFoundSkills answers: "stuck" can't be taken
// out, the others go; when none went, the change fails with the reason
function server(lang, state, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib(state.found));
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST" && !url.pathname.endsWith("/reveal")) {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      if (what === "skills/remove-found-some") {
        const failed = body.names.filter((n) => n === "stuck").map((n) => ({ what: "skill:" + n, error: STUCK }));
        if (failed.length === body.names.length) return json({ error: STUCK }, 400);
        state.found = state.found.filter((f) => !body.names.includes(f.name) || f.name === "stuck");
        return json({ ...lib(state.found), result: { changed: ["claude", "codex", "goose"], unremoved: failed.length ? failed : undefined } });
      }
      return json(lib(state.found));
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

// the reader scrolls (not a click) till what they'll press is out from
// under the bar that stays in view at the list's foot
const reach = async (page, loc) => {
  for (let i = 0; i < 20; i++) {
    const b = await loc.boundingBox();
    if (b && await loc.evaluate((e, [x, y]) => e.contains(document.elementFromPoint(x, y)), [b.x + b.width / 2, b.y + b.height / 2])) {
      // the wheel's scroll has come to rest before the reader clicks
      await page.evaluate(() => new Promise((done) => {
        const at = () => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop).join(" ");
        let last = at(), still = 0;
        const tick = () => { const now = at(); still = now === last ? still + 1 : 0; last = now; still >= 5 ? done() : requestAnimationFrame(tick); };
        requestAnimationFrame(tick);
      }));
      return;
    }
    // the wheel over the list beside the thing, inside the window: up to
    // one under the tabs at the top, down to one below the fold or under
    // the bar or a note at the foot
    const vh = page.viewportSize().height, up = b.y + b.height / 2 < vh / 2;
    await page.mouse.move(b.x + b.width / 2, Math.min(Math.max(up ? b.y + b.height + 20 : b.y - 40, 10), vh - 10));
    await page.mouse.wheel(0, up ? -120 : 120);
    await page.waitForTimeout(80);
  }
  const b = await loc.boundingBox();
  const over = b && await page.evaluate(([x, y]) => { const e = document.elementFromPoint(x, y); return e ? e.outerHTML.slice(0, 160) : "nothing"; }, [b.x + b.width / 2, b.y + b.height / 2]);
  assert.fail(`${loc} is never out from under the bar: ${JSON.stringify(b)} ${over}`);
};
// a click where the thing is; nothing scrolls, and what was clicked stays put
const press = async (page, loc) => {
  await reach(page, loc);
  await page.waitForFunction(() => !document.querySelector("#view-library").getAnimations({ subtree: true }).some((a) => a.effect?.getComputedTiming().endTime !== Infinity));
  const b = await loc.boundingBox();
  const sel = String(loc);
  const scrolls = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => (e.id || e.className) + ":" + e.scrollTop).join(" "));
  const top = await scrolls();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
  await page.waitForTimeout(200);
  assert.equal(await scrolls(), top, `${sel}'s click scrolled the page`);
  if (!(await loc.count())) return;
  const a = await loc.boundingBox();
  if (a && a.width) assert(Math.abs(a.y - b.y) <= 1, `${sel} moved on the screen from ${Math.round(b.y)} to ${Math.round(a.y)}`);
};
const settled = (page) => page.waitForFunction(() => !document.querySelector("#modal").getAnimations({ subtree: true }).length);
const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-found, #view-library .lib-foundhead > *, #view-library .lib-foundbar, #view-library .lib-foundbar > *, #modal .lib-editor, #modal .lib-editor .bar > *")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": some skills the agents have of their own, out of them at once", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    await loadI18N();
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const width of [1100, 420]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const state = { found: FOUND.map((f) => ({ ...f })) };
        const posts = [], errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
        await page.route("**/*", server(lang, state, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const row = (name) => v.locator(".lib-found").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
        const shown = async () => (await v.locator(".lib-found .who > .name").allTextContents()).sort();
        await row("stuck").waitFor();
        const select = v.locator(".lib-foundhead .lib-foundselect");
        const bar = v.locator(".lib-foundbar"), rm = bar.locator(".lib-pickrm");
        const sheet = page.locator("#modal .lib-editor");
        const lines = () => sheet.locator(".lib-confirm").allTextContents();

        // Select: each row a box, its own buttons gone; the bar says to pick
        await press(page, select);
        await bar.locator(".lib-pickn.none").waitFor();
        assert.equal((await bar.locator(".lib-pickn").textContent()).trim(), w("Pick skills to take them out of your agents together"));
        assert.equal(await rm.count(), 0, "Remove with nothing picked");
        assert.equal(await v.locator(".lib-found button.lib-icon.danger, .lib-found button.action").count(), 0, "a row kept its own buttons");
        assert.equal(await v.locator(".lib-found .lib-pickbox").count(), 4);
        // one Select at a time: while either is on the other is dimmed,
        // saying why, so neither click takes the other's bar away
        const libSelect = v.locator(".lib-skillshead .lib-select");
        assert.equal(await libSelect.isDisabled(), true, "the library's Select while this one is on");
        assert.equal(await libSelect.getAttribute("title"), w("Done picking the skills in your agents first"));
        await press(page, bar.locator(".lib-pickdone"));
        assert.equal(await bar.count(), 0);
        assert.equal(await libSelect.isDisabled(), false);
        await press(page, libSelect);
        await v.locator(".lib-pickbar").waitFor();
        assert.equal(await select.isDisabled(), true, "the found Select while the library's is on");
        assert.equal(await select.getAttribute("title"), w("Done picking the library's skills first"));
        await press(page, libSelect); // the head's Done: the bar's is library-pick-remove's
        assert.equal(await v.locator(".lib-pickbar").count(), 0);
        await press(page, select);
        await bar.waitFor();
        assert.equal(await v.locator(".lib-skill .lib-pickbox").count(), 0);

        // three picked, a row's click its box; asked first, naming how many
        for (const n of ["notes", "dev", "grilling"]) await press(page, row(n));
        await bar.locator(".lib-pickn").filter({ hasText: w("{n} selected", { n: 3 }) }).waitFor();
        assert.equal(await row("stuck").locator(".lib-pickbox").isChecked(), false);
        assert.equal((await rm.textContent()).trim(), w("Remove…"));
        assert.equal(await rm.getAttribute("title"), w("Take the skills picked out of your agents"));
        assert.deepEqual(await sideways(page), [], "sideways with the bar");
        await press(page, rm);
        await sheet.waitFor();
        await settled(page);
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w("Remove {n} skills?", { n: 3 }));
        assert.deepEqual(await lines(), [
          w("They are taken out of {agents}.", { agents: "Claude Code, Codex, Goose" }),
          w("{n} folders are moved to magpie's backups (Backups, at the top of the Library), not deleted.", { n: 2 }) + " " + w("{n} linked from folders of your own are only unlinked: those folders stay where they are.", { n: 1 }),
          w("{n} have an entry in the shared skills folder, which goes to the backups too, so no agent reads them from there.", { n: 1 }),
          w("Another agent's own skill by one of these names stays."),
        ]);
        assert.equal((await sheet.locator(".bar button.primary").textContent()).trim(), w("Remove {n}", { n: 3 }));
        assert.deepEqual(await sideways(page), [], "sideways with the sheet");
        await sheet.locator(".bar button", { hasText: w("Cancel") }).click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts, [], "Cancel posted");
        assert.equal((await shown()).length, 4);

        // two of them and stuck: stuck can't go and stays, picked, saying
        // why; only those gone are said to be removed
        await press(page, bar.locator(".lib-pickclear"));
        await bar.locator(".lib-pickn.none").waitFor();
        for (const n of ["notes", "stuck"]) await press(page, row(n).locator(".lib-pickbox"));
        await press(page, rm);
        await sheet.waitFor();
        await settled(page);
        await sheet.locator(".bar button.primary").click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "skills/remove-found-some", body: { names: ["notes", "stuck"] } });
        await page.locator("#status").filter({ hasText: w("{name} removed", { name: "notes" }) + " · " + w("{name} wasn't removed: {error}", { name: "stuck", error: STUCK }) }).waitFor();
        assert.deepEqual(await shown(), ["dev", "grilling", "stuck"]);
        assert.equal(await row("stuck").locator(".lib-pickbox").isChecked(), true, "the one left was unpicked");
        assert.equal((await row("stuck").locator(".lib-pickerr").textContent()).trim(), w("Not removed: {error}", { error: STUCK }));
        await bar.locator(".lib-pickn").filter({ hasText: w("{n} selected", { n: 1 }) }).waitFor();
        assert.deepEqual(await sideways(page), [], "sideways with a row's reason");

        // that one alone: asked as its row's trash asks; the change refuses,
        // the sheet stays with why, and Cancel leaves it there, picked
        await press(page, rm);
        await sheet.waitFor();
        await settled(page);
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w("Remove {name}?", { name: "stuck" }));
        assert.deepEqual(await lines(), [w("It is taken out of {agents}, and its folder is moved to magpie's backups.", { agents: "Gemini CLI" })]);
        assert.equal((await sheet.locator(".bar button.primary").textContent()).trim(), w("Remove"));
        await sheet.locator(".bar button.primary").click();
        await page.locator("#status").filter({ hasText: STUCK }).waitFor();
        assert.deepEqual(posts.shift(), { what: "skills/remove-found-some", body: { names: ["stuck"] } });
        await sheet.locator(".bar button.primary:not([disabled])").waitFor();
        await sheet.locator(".bar button", { hasText: w("Cancel") }).click();
        await sheet.waitFor({ state: "detached" });
        assert.equal(await row("stuck").locator(".lib-pickbox").isChecked(), true);
        await row("stuck").locator(".lib-pickerr").waitFor();

        // All, then stuck unpicked: the other two go; one left ends Select
        await press(page, bar.locator(".lib-pickall .lib-pickbox"));
        await bar.locator(".lib-pickn").filter({ hasText: w("{n} selected", { n: 3 }) }).waitFor();
        await press(page, row("stuck").locator(".lib-pickbox"));
        await press(page, rm);
        await sheet.waitFor();
        await settled(page);
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w("Remove {n} skills?", { n: 2 }));
        await sheet.locator(".bar button.primary").click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "skills/remove-found-some", body: { names: ["dev", "grilling"] } });
        await page.locator("#status").filter({ hasText: w("{n} skills removed", { n: 2 }) }).waitFor();
        assert.deepEqual(await shown(), ["stuck"]);
        assert.equal(await bar.count(), 0, "Select stayed on with one left");
        assert.equal(await v.locator(".lib-found .lib-pickbox").count(), 0);
        await row("stuck").locator("button.lib-icon.danger").waitFor();
        assert.deepEqual(posts, []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
