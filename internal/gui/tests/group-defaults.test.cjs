// Run with Node's test runner and Playwright on the module path; see README.md.
// What every routing group does when one is rate limited, slow to start or
// every member is busy is set once, in the Settings card under the groups
// (jiezhengj on Discord: each group had to be set on its own). Each row
// picks its value in the app's segments and posts groups/defaults with all
// three at once; a group's editor opens on Default for those it sets none
// of, its hint saying the value it follows, and the group's own pick still
// wins. In five languages, Chromium and WebKit, at 1100 and 420, with no
// native select and nothing past the window's side.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
// the rows a group's editor folds under Advanced, opened
const openAdvanced = async (page) => {
  const d = page.locator(".rt-gedit .rt-gadv");
  await d.waitFor({ state: "attached" });
  if (!(await d.evaluate((e) => e.open))) await d.locator(":scope > summary").click();
};

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "b/m", name: "m", providerName: "B", icon: "generic" },
];
const members = models.map((m) => m.id);

function serve(lang, posts, st) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const groups = () => ({
    models, pools: [], deciders: [], found: true, defaults: { ...st.defaults },
    groups: [{ id: "sol", name: "Sol", members, routing: "order", ready: true, memberInfo: members.map((id) => ({ id, ready: true })), ...st.group }],
  });
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
    if (url.pathname.startsWith("/api/groups/")) {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push({ path: url.pathname, body });
      if (url.pathname === "/api/groups/defaults") st.defaults = { sink: !!body.sink, firstToken: body.firstToken || 0, patience: body.patience || 0 };
      if (url.pathname === "/api/groups/save") st.group = { sink: !!body.sink, noSink: !!body.noSink, firstToken: body.firstToken || 0, patience: body.patience || 0 };
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
    test(`${engine} ${lang} ${width}: every group follows one default unless it sets its own`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width, height: 2000 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-group-defaults.png`), fullPage: true });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [], st = { defaults: { sink: false, firstToken: 0, patience: 0 }, group: {} };
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts, st));
      await page.goto("http://magpie.test/?view=groups");
      const card = page.locator(".rt-group", { hasText: "Sol" });
      await card.waitFor();
      const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
      // at 420 the page runs past the window's foot, and WebKit doesn't
      // scroll the page's own scroller to a click: wheel it into view
      const reach = async (loc) => {
        await loc.waitFor();
        await page.mouse.move(width / 2, 600);
        for (let i = 0; i < 30; i++) {
          const b = await loc.boundingBox();
          if (b && b.y >= 0 && b.y + b.height <= 2000) return loc;
          await page.mouse.wheel(0, b && b.y < 0 ? -400 : 400);
          await page.waitForTimeout(50);
        }
        return loc;
      };
      const on = async (row) => (await row.locator(".opt.on").textContent()).trim();
      // a pick is saved, and the row drawn again from what came back
      const pick = async (row, name) => {
        const was = await row.locator(".segs").elementHandle();
        await (await reach(row.locator(".opt", { hasText: new RegExp(`^${name}$`) }))).click();
        await page.waitForFunction((s) => !s.isConnected, was);
        assert.equal(await on(row), name);
        return posts.filter((p) => p.path === "/api/groups/defaults").at(-1)?.body;
      };

      // the Settings card under the list: each row says what it is and
      // starts at magpie's own
      const set = page.locator(".rt-gset");
      const sink = set.locator(".rt-gdef-sink"), first = set.locator(".rt-gdef-first"), pat = set.locator(".rt-gdef-patience");
      await reach(pat);
      assert.equal((await sink.locator("b").textContent()).trim(), await tr("Rate limited"));
      assert.equal((await first.locator("b").textContent()).trim(), await tr("Slow to start"));
      assert.equal((await pat.locator("b").textContent()).trim(), await tr("Every member busy"));
      assert.equal(await on(sink), await tr("Keeps its place"));
      assert.equal(await on(first), await tr("Wait"));
      assert.equal(await on(pat), await tr("1 min"));
      assert.equal(await page.locator("select").count(), 0, "no native select");

      // each pick posts all three, the others as they are
      assert.deepEqual(await pick(sink, await tr("Goes to the back")), { sink: true, firstToken: 0, patience: 0 });
      assert.deepEqual(await pick(first, await tr("1 min")), { sink: true, firstToken: 60, patience: 0 });
      assert.deepEqual(await pick(pat, await tr("Off")), { sink: true, firstToken: 60, patience: -1 });
      assert((await first.locator("small").textContent()).includes(await tr("{n} s", { n: 60 })), "the hint says what the value does");
      assert((await pat.locator("small").textContent()).includes(await tr("When every member has failed, the agent gets the last one's error at once.")));
      const fits = () => page.evaluate(() => {
        const box = document.querySelector(".rt-gset").getBoundingClientRect();
        return [...document.querySelectorAll(".rt-gdef .segs")].every((s) => {
          const b = s.getBoundingClientRect();
          return b.left >= box.left - 0.5 && b.right <= box.right + 0.5;
        }) && document.scrollingElement.scrollWidth <= document.scrollingElement.clientWidth;
      });
      assert.equal(await fits(), true, "the rows fit at " + width);

      // a group that sets none opens on Default, its hint saying what it follows
      await (await reach(card.locator("button", { hasText: await tr("Edit") }))).click();
      await openAdvanced(page);
      const ed = page.locator(".rt-gedit");
      const field = (label) => ed.locator("label", { hasText: new RegExp(`^${label}$`) }).locator("xpath=following-sibling::div[1]");
      const eSink = field(await tr("Rate limited")), eFirst = field(await tr("Slow to start")), ePat = field(await tr("Every member busy"));
      await eSink.waitFor();
      const dflt = await tr("Default");
      for (const row of [eSink, eFirst, ePat]) assert.equal(await on(row), dflt);
      const follows = (v) => tr("As set for every group under the list: {v}.", { v });
      assert((await eSink.locator(".hint").textContent()).startsWith(await follows(await tr("Goes to the back"))));
      assert((await eFirst.locator(".hint").textContent()).startsWith(await follows(await tr("1 min"))));
      assert((await ePat.locator(".hint").textContent()).startsWith(await follows(await tr("Off"))));
      assert((await ePat.locator(".hint").textContent()).includes(await tr("When every member has failed, the agent gets the last one's error at once.")));

      // the group's own picks win, and are sent as its own
      await (await reach(eSink.locator(".opt", { hasText: new RegExp(`^${await tr("Keeps its place")}$`) }))).click();
      await (await reach(eFirst.locator(".opt", { hasText: new RegExp(`^${await tr("Wait")}$`) }))).click();
      await (await reach(ePat.locator(".opt", { hasText: new RegExp(`^${await tr("1 min")}$`) }))).click();
      assert(!(await ePat.locator(".hint").textContent()).startsWith(await follows(await tr("Off"))), "its own value's hint, not the default's");
      await (await reach(ed.locator("button.primary", { hasText: await tr("Save") }))).click();
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const saved = posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
      assert.deepEqual([saved.sink, saved.noSink, saved.firstToken, saved.patience], [false, true, -1, 60]);

      // opened again, it shows its own; Default sends none of them
      await (await reach(card.locator("button", { hasText: await tr("Edit") }))).click();
      await openAdvanced(page);
      await eSink.waitFor();
      assert.equal(await on(eSink), await tr("Keeps its place"));
      assert.equal(await on(eFirst), await tr("Wait"));
      assert.equal(await on(ePat), await tr("1 min"));
      for (const row of [eSink, eFirst, ePat]) await (await reach(row.locator(".opt", { hasText: new RegExp(`^${dflt}$`) }))).click();
      await (await reach(ed.locator("button.primary", { hasText: await tr("Save") }))).click();
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const back = posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
      assert.deepEqual([back.sink, back.noSink, back.firstToken, back.patience], [false, false, 0, 0]);
      assert.equal(await fits(), true, "the rows fit at " + width + " after the editor");

      const missing = await page.evaluate(() => [
        "As set for every group under the list: {v}.", "Default", "Rate limited", "Slow to start", "Every member busy",
        "Keeps its place", "Goes to the back", "Wait", "Off", "30 s", "1 min", "2 min", "4 min",
      ].flatMap((k) => ["zh", "zh-TW", "ja", "de"].filter((l) => !I18N[l][k]).map((l) => l + ": " + k)));
      assert.deepEqual(missing, [], "every string has its zh, zh-TW, ja and de");
      assert.deepEqual(errors, []);
    });
  }
}
