// Run with Node's test runner and Playwright on the module path; see README.md.
// lc on Discord ("只可列出？不可删除？"): Claude Code sessions seen only through
// magpie's gateway, their own files on another computer, were listed under
// "No folder" at "0 B" with a note that magpie can't delete them. Now they
// sit under "Seen through the gateway", say why they can't be resumed,
// show the size of what magpie recorded of them (nothing when it recorded
// nothing), and each has a Delete that asks in magpie's own dialog, says
// what goes and what stays, and moves them to the Trash, where they are
// marked and the recording's expiry is said. A click leaves the page where
// it is; it fits at 390px. In every language, Chromium and WebKit; no
// backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const A = "0b6f2a4e-8d1c-4f7a-9c3e-2a5d7e9f1b3c", B = "7c1d9e2f-3a4b-4c5d-8e6f-9a0b1c2d3e4f";
const gw = (id, size, min) => ({
  agent: "claude", id, title: id, start: at(min + 30), last: at(min), input: 4000, output: 300, cache_read: 0, cache_write: 0,
  size, models: [{ model: "deepseek-flash", requests: 3 }], read_only: true, transcript: size > 0, gateway: true, deletable: true,
});
const DIR = "~/Library/Application Support/magpie/trash/sessions";

function serve(lang, calls) {
  const store = { sessions: [gw(A, 2048, 90), gw(B, 0, 120)], trash: [] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      return json({
        agents: [{ agent: "claude", count: store.sessions.length, deletable: true, name: "Claude Code", icon: "claudecode-color" }],
        agent: "claude", sessions: store.sessions, terminal: false, trash: store.trash, trashDir: DIR, recording: true, recorded: true,
      });
    }
    if (url.pathname === "/api/sessions/delete") {
      const body = route.request().postDataJSON();
      calls.push({ path: "delete", body });
      for (const id of body.ids) {
        const s = store.sessions.find((x) => x.id === id);
        store.sessions = store.sessions.filter((x) => x.id !== id);
        store.trash.push({ key: `gateway/20261010-${id.slice(0, 8)}`, agent: "claude", id, title: id, last: s.last, deleted: at(0), size: s.size, gateway: true,
          items: s.size ? [{ from: `/x/gateway-conversations/2026-10-10/${id.slice(0, 8)}`, name: "2026-10-10" }] : [], name: "Claude Code", icon: "claudecode-color" });
      }
      return json({ deleted: body.ids, refused: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const KEYS = {
  seen: "Seen through the gateway",
  note: "These {agent} sessions were seen only through magpie's gateway: {agent}'s own files of them aren't on this computer (it may run on another one, in a container or with another config folder), so they can't be resumed here. Delete takes one off this list and moves any conversation magpie recorded of it to its trash; Usage keeps what it spent.",
  some: "Sessions under \"Seen through the gateway\" have no {agent} files on this computer, so they can't be resumed here. Delete takes one off this list and moves any conversation magpie recorded of it to its trash; Usage keeps what it spent.",
  size: "{size} recorded",
  confirm: "A session seen only through the gateway leaves this list, and any conversation magpie recorded of it is moved to magpie's trash ({dir}): Trash puts it back. {agent}'s own files elsewhere are not touched, and Usage keeps what it spent. One with a request in the last minute is left alone.",
  expire: "Conversations magpie recorded through the gateway still expire after 7 days here, and Delete saved conversations… erases them too.",
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a session seen only through the gateway can be deleted, after magpie's own dialog`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
      const calls = [];
      await page.route("**/*", serve(lang, calls));
      await page.goto("http://magpie.test/?view=providers");
      const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
      if (lang !== "en") {
        const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang]?.[k]), [lang, Object.values(KEYS)]);
        assert.deepEqual(missing, [], `every new string has its ${lang}`);
      }
      await page.locator('#nav button[data-view="sessions"]').click();
      const view = page.locator("#view-sessions");
      const rowA = view.locator(`.sm-sess`).filter({ hasText: A });
      await rowA.waitFor();

      // why it can't be resumed and what Delete takes, not "read only"
      const notes = (await view.locator(".sm-note:not(.sm-speed-note)").allTextContents()).map((x) => x.trim());
      assert.deepEqual(notes, [await tr(KEYS.note, { agent: "Claude Code" })]);
      // grouped as seen through the gateway, with no path
      const folder = view.locator(".sm-folder").first();
      assert.equal((await folder.locator(".fold .name").textContent()).trim(), await tr(KEYS.seen));
      assert.equal(await folder.locator(".sm-path").count(), 0);
      // what magpie recorded is its size; nothing recorded says no size at all
      const subA = (await rowA.locator(".sub").first().textContent());
      assert(subA.includes(await tr(KEYS.size, { size: "2 KB" })) || subA.includes(await tr(KEYS.size, { size: "2.0 KB" })), `recorded size in ${subA}`);
      const subB = (await view.locator(".sm-sess").filter({ hasText: B }).locator(".sub").first().textContent());
      assert(!/\b0 B\b/.test(subB), `no 0 B in ${subB}`);
      assert.equal(await rowA.locator(".sess-resume").count(), 0, "can't be resumed here");

      // Delete asks; Cancel sends nothing, and the page stays put
      const top = (l) => l.evaluate((e) => e.getBoundingClientRect().top);
      const before = await top(rowA);
      const del = rowA.locator(".sm-del");
      await del.click();
      const ask = page.locator("#modal .sm-ask");
      await ask.waitFor();
      const said = (await ask.locator(".lib-confirm").allTextContents()).map((x) => x.trim());
      assert.deepEqual(said, [await tr(KEYS.confirm, { dir: DIR, agent: "Claude Code" })], "says what goes and what stays, not \"their files\"");
      assert.equal(await ask.locator(".sm-ask-path").count(), 0, "no folder path for the gateway group");
      const border = await page.evaluate(() => [...document.querySelectorAll("#view-sessions, #view-sessions *, #modal .sm-ask, #modal .sm-ask *")]
        .filter((e) => parseFloat(getComputedStyle(e).borderLeftWidth) > 1 && getComputedStyle(e).borderLeftColor !== getComputedStyle(e).borderRightColor).map((e) => e.className));
      assert.deepEqual(border, [], "no left-border accent");
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.waitForTimeout(400);
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-gateway-delete-ask.png`) });
      }
      await ask.locator(".bar button").first().click();
      await ask.waitFor({ state: "detached" });
      assert.equal(calls.length, 0, "Cancel deletes nothing");
      assert.equal(await top(rowA), before, "the click moved the page");

      // confirmed: posted, gone from the list, marked in the Trash
      await del.click();
      await ask.waitFor();
      await ask.locator(".bar button.primary").click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(calls, [{ path: "delete", body: { agent: "claude", ids: [A] } }]);
      await rowA.waitFor({ state: "detached" });
      await view.locator(".sm-trash-btn").click();
      const trow = view.locator(".sm-trash .row").filter({ hasText: A });
      await trow.waitFor();
      assert((await trow.locator(".sub").textContent()).includes(await tr(KEYS.seen)), "marked as seen through the gateway");
      assert.equal((await view.locator(".sm-gw-trash-note").textContent()).trim(), await tr(KEYS.expire));

      for (const width of [1000, 390]) {
        await page.setViewportSize({ width, height: 700 });
        await page.waitForTimeout(100);
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `no page overflow at ${width}`);
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-gateway-trash-${width}.png`), fullPage: true });
      }
      await view.locator(".sm-trash-btn").click();
      await view.locator(".sm-sess").filter({ hasText: B }).waitFor();
      for (const width of [390, 1000]) {
        await page.setViewportSize({ width, height: 700 });
        await page.waitForTimeout(100);
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `no page overflow at ${width}`);
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-gateway-list-${width}.png`), fullPage: true });
      }
      assert.deepEqual(errors, []);
    });
  }
}
