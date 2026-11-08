// Run with Node's test runner and Playwright on the module path; see README.md.
// #1852: the library's skills went all at once (Remove all) or one row at a
// time; an outdated group (分组) of them had to go one by one. Select's bar
// now has Remove n…: it asks first in the page (no window.confirm), naming
// how many, and posts the names picked to skills/remove-all, the same
// change Remove all makes, each skill taken out as its row's Remove takes
// it. Here: a group's box picks its skills, Cancel posts nothing and leaves
// every one, Remove takes those picked and leaves the others; when one of
// those picked can't be taken out it stays, still picked, its row saying
// why, and only those gone are said to be removed; one picked alone that
// the change refuses stays too. Chromium and WebKit, 1100px and 390px, in
// en, zh, zh-TW, ja and de; no click moves the page, nothing runs off it
// sideways. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const gh = (name) => ({ name, description: name + " does a thing", kind: "github", repo: "old/pack", source: `https://github.com/old/pack/tree/main/skills/${name}`, agents: ["claude"] });
const local = (name) => ({ name, description: name + " does a thing", kind: "local", agents: ["claude", "codex"] });
const lib = (skills) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex")],
  instructions: { agents: [] }, servers: [], skills, foundServers: [], projects: [], foundSkills: [], problems: [],
});
const STUCK = "the folder couldn't be moved: permission denied";

let I18N;
async function loadI18N() {
  I18N = strings().I18N; // i18n.js's and the plugins'
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

// remove-all as RemoveSkills answers: "stuck" can't be taken out; the
// others go; when none went, the change fails with the first reason
function server(lang, state, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib(state.skills));
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST" && !url.pathname.endsWith("/reveal")) {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      if (what === "skills/remove-all") {
        const failed = body.names.filter((n) => n === "stuck").map((n) => ({ what: "skill:" + n, error: STUCK }));
        if (failed.length === body.names.length) return json({ error: STUCK }, 400);
        state.skills = state.skills.filter((s) => !body.names.includes(s.name) || s.name === "stuck");
        return json({ ...lib(state.skills), result: { changed: ["claude", "codex"], unremoved: failed.length ? failed : undefined } });
      }
      return json(lib(state.skills));
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

// a click where the thing is, as the reader's, and what was clicked stays
// where it was on the screen while the click redraws around it
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
// the reader scrolls the Library (not a click) till what they'll press is
// out from under the bar that stays in view at its foot: at 390px a row's
// chips wrap, and the last rows start under it
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
    const vh = page.viewportSize().height, up = b.y + b.height / 2 < vh / 2;
    await page.mouse.move(b.x + b.width / 2, Math.min(Math.max(up ? b.y + b.height + 20 : b.y - 40, 10), vh - 10));
    await page.mouse.wheel(0, up ? -120 : 120);
    await page.waitForTimeout(80);
  }
  assert.fail(`${loc} is never out from under the bar`);
};
const settled = (page) => page.waitForFunction(() => !document.querySelector("#modal").getAnimations({ subtree: true }).length);
// nothing wider than the window: the page, the Library, its rows, bar and sheet
const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  const v = document.querySelector("#view-library");
  if (v.scrollWidth > v.clientWidth + 1) over.push("library " + v.scrollWidth + ">" + v.clientWidth);
  for (const e of document.querySelectorAll("#view-library .lib-skill, #view-library .lib-grouphead, #view-library .lib-pickbar, #view-library .lib-pickbar > *, #modal .lib-editor, #modal .lib-editor .bar > *")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": some skills, a group's among them, out of the library at once", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    await loadI18N();
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const width of [1100, 390]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const state = { skills: [gh("pack-a"), gh("pack-b"), gh("pack-c"), local("keep-me"), local("notes"), local("stuck")] };
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
        const row = (name) => v.locator(".lib-skill").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name) }) });
        const names = () => state.skills.map((s) => s.name);
        const shown = async () => (await v.locator(".lib-skill .who > .name").evaluateAll((ns) => ns.map((n) => n.firstChild.textContent))).sort();
        const group = v.locator(".lib-group").filter({ has: page.locator(".lib-grouphead .name", { hasText: /^old\/pack$/ }) });
        await group.waitFor();
        await row("stuck").waitFor();
        const bar = v.locator(".lib-pickbar"), rm = bar.locator(".lib-pickrm");
        const sheet = page.locator("#modal .lib-editor");

        // Select; the group's box picks its three skills. Done is as wide
        // as Select, so a heading on two lines doesn't carry it to the
        // line above, from under the pointer
        const selectAt = v.locator(".lib-skillshead .lib-select");
        const wide = (await selectAt.boundingBox()).width;
        await press(page, selectAt);
        assert(Math.abs((await selectAt.boundingBox()).width - wide) < 0.5, `Select ${wide}px wide, Done ${(await selectAt.boundingBox()).width}px`);
        await bar.locator(".lib-pickn.none").waitFor();
        assert.equal(await rm.count(), 0, "Remove with nothing picked");
        await press(page, group.locator(".lib-grouphead .lib-pickbox"));
        await bar.locator(".lib-pickn").filter({ hasText: w("{n} selected", { n: 3 }) }).waitFor();
        for (const n of ["pack-a", "pack-b", "pack-c"]) assert.equal(await row(n).locator(".lib-pickbox").isChecked(), true, n);
        assert.equal(await row("keep-me").locator(".lib-pickbox").isChecked(), false);
        assert.equal((await rm.textContent()).trim(), w("Remove…"));
        assert.equal(await rm.getAttribute("title"), w("Take the skills picked out of the library"));
        assert.deepEqual(await sideways(page), [], "sideways with the bar");

        // asked first, naming how many; Cancel posts nothing, leaves all six
        await press(page, rm);
        await sheet.waitFor();
        await settled(page);
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w("Remove {n} skills?", { n: 3 }));
        await sheet.locator(".lib-confirm").filter({ hasText: w("They are taken out of the library and out of {agents}.", { agents: "Claude Code" }) }).waitFor();
        await sheet.locator(".lib-confirm").filter({ hasText: w("{n} folders are moved to magpie's backups (Backups, at the top of the Library), not deleted.", { n: 3 }) }).waitFor();
        assert.equal((await sheet.locator(".bar button.primary").textContent()).trim(), w("Remove {n}", { n: 3 }));
        assert.deepEqual(await sideways(page), [], "sideways with the sheet");
        await sheet.locator(".bar button", { hasText: w("Cancel") }).click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts, [], "Cancel posted");
        assert.equal(names().length, 6);
        assert.equal((await shown()).length, 6);
        await bar.locator(".lib-pickn").filter({ hasText: w("{n} selected", { n: 3 }) }).waitFor();

        // Remove: the three go, the others stay, nothing is picked
        await press(page, rm);
        await sheet.waitFor();
        await settled(page);
        await sheet.locator(".bar button.primary").click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "skills/remove-all", body: { names: ["pack-a", "pack-b", "pack-c"] } });
        await page.locator("#status").filter({ hasText: w("{n} skills removed", { n: 3 }) }).waitFor();
        assert.deepEqual(await shown(), ["keep-me", "notes", "stuck"]);
        assert.equal(await group.count(), 0, "the group is still drawn");
        await bar.locator(".lib-pickn.none").waitFor();

        // two picked, one can't be taken out: it stays, picked, saying why;
        // only the one gone is said to be removed
        await press(page, row("notes").locator(".lib-pickbox"));
        await press(page, row("stuck").locator(".lib-pickbox"));
        await press(page, rm);
        await sheet.waitFor();
        await settled(page);
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w("Remove {n} skills?", { n: 2 }));
        await sheet.locator(".bar button.primary").click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "skills/remove-all", body: { names: ["notes", "stuck"] } });
        await page.locator("#status").filter({ hasText: w("{name} removed", { name: "notes" }) + " · " + w("{name} wasn't removed: {error}", { name: "stuck", error: STUCK }) }).waitFor();
        assert.deepEqual(await shown(), ["keep-me", "stuck"]);
        assert.equal(await row("stuck").locator(".lib-pickbox").isChecked(), true, "the one left was unpicked");
        assert.equal(await row("keep-me").locator(".lib-pickbox").isChecked(), false);
        assert.equal((await row("stuck").locator(".lib-pickerr").textContent()).trim(), w("Not removed: {error}", { error: STUCK }));
        await bar.locator(".lib-pickn").filter({ hasText: w("{n} selected", { n: 1 }) }).waitFor();
        assert.equal((await rm.textContent()).trim(), w("Remove…"));
        assert.deepEqual(await sideways(page), [], "sideways with a row's reason");

        // that one alone: the change refuses, the sheet stays with why, and
        // Cancel leaves it there, picked
        await press(page, rm);
        await sheet.waitFor();
        await settled(page);
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w("Remove {name}?", { name: "stuck" }));
        assert.equal((await sheet.locator(".bar button.primary").textContent()).trim(), w("Remove"));
        await sheet.locator(".bar button.primary").click();
        await page.locator("#status").filter({ hasText: STUCK }).waitFor();
        assert.deepEqual(posts.shift(), { what: "skills/remove-all", body: { names: ["stuck"] } });
        await sheet.locator(".bar button.primary:not([disabled])").waitFor();
        await sheet.locator(".bar button", { hasText: w("Cancel") }).click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(await shown(), ["keep-me", "stuck"]);
        assert.equal(await row("stuck").locator(".lib-pickbox").isChecked(), true);
        await row("stuck").locator(".lib-pickerr").waitFor();

        // Done: no boxes, no reasons
        await press(page, bar.locator(".lib-pickdone"));
        assert.equal(await v.locator(".lib-pickbox").count(), 0);
        assert.equal(await v.locator(".lib-pickerr").count(), 0);
        assert.deepEqual(posts, []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
