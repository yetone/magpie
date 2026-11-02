// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's editor is tall (a model a row), and the reader scrolls down to its
// Save. Saving closes the editor, and the page shrinks by the editor's height:
// the click-hold that keeps what was clicked under the pointer used to hold the
// old place with a blank .view-room as tall as the editor, so the reader was
// left looking at an empty page — nothing of the view in sight, while the DOM
// was intact. The room is only kept while some of the view stays in sight; the
// browser's own clamp after the shrink stands otherwise. In English and
// Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// enough models that the editor is taller than the window it is opened in
const models = Array.from({ length: 18 }, (_, i) => ({
  id: `p/m${i}`, name: `M${i}`, providerName: "P", icon: "generic",
}));
const group = {
  id: "big", name: "Big", routing: "", ready: true,
  members: models.map((m) => m.id),
  memberInfo: models.map((m) => ({ id: m.id, ready: true, name: m.name, provider: "p", model: m.id, icon: "generic" })),
};

const words = {
  en: { save: "Save", saved: "Big saved" },
  zh: { save: "保存", saved: "已保存 Big" },
};

function serve(lang) {
  const groups = () => ({
    models, pools: [], deciders: [], found: false, groups: [group],
  });
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/groups/save") return json(groups());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// What the reader sees: whether any group row is in the view's own box, and how
// much of the view's foot is the room kept for a click.
const seen = (page) => page.evaluate(() => {
  const v = document.querySelector("#view-routing");
  const vr = v.getBoundingClientRect();
  const rows = [...v.querySelectorAll(".rt-group")];
  const inSight = rows.filter((r) => { const b = r.getBoundingClientRect(); return b.bottom > vr.top && b.top < vr.bottom; }).length;
  const room = v.querySelector(":scope > .view-room");
  return {
    inSight,
    rows: rows.length,
    room: room ? Math.round(room.getBoundingClientRect().height) : 0,
    window: Math.round(vr.height),
    scrollTop: Math.round(v.scrollTop),
    max: Math.round(v.scrollHeight - v.clientHeight),
  };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: saving a tall group's editor leaves the page in sight`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-after-save.png`) });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 640 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-group[data-id=big]").waitFor();

      // the editor, from the row
      await page.locator(".rt-group[data-id=big]").click();
      const save = page.locator(".rt-gedit button", { hasText: new RegExp(`^${w.save}$`) });
      await save.waitFor();
      const view = page.locator("#view-routing");
      const box = await view.boundingBox();
      // the reader scrolls down to the editor's foot: the editor is taller
      // than the window (its Save stays in sight on the way, held at the
      // view's foot, but the case here is a Save from down the page)
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
      for (let i = 0; i < 200; i++) {
        const b = await page.locator(".rt-gedit").boundingBox();
        if (b && b.y + b.height <= box.y + box.height) break;
        if (await view.evaluate((v) => v.scrollTop + v.clientHeight >= v.scrollHeight - 1)) break;
        await page.mouse.wheel(0, 120);
        await page.waitForTimeout(20);
      }
      await page.waitForTimeout(300);
      const before = await seen(page);
      const saveBox = await save.boundingBox();
      assert(before.scrollTop > 0, "the reader must be down the page, not at its top");
      assert(saveBox && saveBox.y >= box.y && saveBox.y + saveBox.height <= box.y + box.height + 1,
        "the Save must be in sight, or the click itself scrolls to it");

      await save.click();
      await page.waitForTimeout(1200);
      const after = await seen(page);
      assert.equal(after.rows, 1, "the editor closed, the group's row drawn again");
      assert(after.scrollTop <= after.max + 1, `the view is past its content: ${after.scrollTop} > ${after.max}`);
      assert(after.room < after.window, `a blank room of ${after.room}px in a ${after.window}px window`);
      assert(after.inSight > 0, "not one group row in sight: the page is blank after the Save");
      assert.deepEqual(errors, [], "no page error");
    });
  }
}
