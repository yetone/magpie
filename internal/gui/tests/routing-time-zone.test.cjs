// Run with Node's test runner and Playwright on the module path; see README.md.
// A rule's hours are kept in the zone they were set in (gaubeebangeel on
// X: magpie on a cloud server in UTC, its user in Beijing, the hours 8
// off). When magpie's clock (/api/groups' clock) is in another zone than
// the hours, the zone is told beside them: on the group's rules and in the
// editor, the zone's name for hours that have one, the server's offset for
// hours set before zones were kept. Hours changed in the editor take the
// browser's zone and are saved with it; hours left alone keep theirs. When
// the clocks agree nothing is added. At a narrow width, in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/glm", name: "glm", providerName: "A", icon: "generic", context: 200000, ready: true },
  { id: "b/other", name: "other", providerName: "B", icon: "generic", context: 200000, ready: true },
];
const WEEKDAYS = ["mon", "tue", "wed", "thu", "fri"];
const groups = (clock) => ({
  models,
  groups: [{ id: "main", name: "Main", members: ["a/glm", "b/other"], routing: "order", ready: true,
    memberInfo: [{ id: "a/glm", ready: true, context: 200000 }, { id: "b/other", ready: true, context: 200000 }],
    rules: [
      { use: "b/other", time: { from: "09:00", to: "18:00", days: WEEKDAYS, zone: "Asia/Shanghai" } },
      { use: "b/other", time: { from: "22:00", to: "08:00" } },
    ] }],
  pools: [],
  clock,
});
const UTC = { zone: "Etc/UTC", offset: 0 };
const BEIJING = { zone: "Asia/Shanghai", offset: 8 * 3600 };

// what each language says: the zone's name, the server's offset, and the
// first rule's hours on the group (no zone added when the clocks agree)
const words = {
  en: { zone: "Asia/Shanghai time", server: "server time, UTC+0", rule1: "1. 09:00–18:00 Mon–Fri (Asia/Shanghai time) →", rule2: "2. 22:00–08:00 (server time, UTC+0) →", plain1: "1. 09:00–18:00 Mon–Fri →", edit: "Edit", save: "Save" },
  zh: { zone: "Asia/Shanghai 时间", server: "服务器时间 UTC+0", rule1: "1. 周一–周五 09:00–18:00（Asia/Shanghai 时间） →", rule2: "2. 22:00–08:00（服务器时间 UTC+0） →", plain1: "1. 周一–周五 09:00–18:00 →", edit: "编辑", save: "保存" },
  "zh-TW": { zone: "Asia/Shanghai 時間", server: "伺服器時間 UTC+0" },
  ja: { zone: "Asia/Shanghai 時間", server: "サーバー時刻 UTC+0" },
  de: { zone: "Asia/Shanghai-Zeit", server: "Serverzeit, UTC+0" },
};

function serve(lang, clock, posts) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json(groups(clock));
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups(clock));
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(t, engine, lang, clock, width) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1200 }, timezoneId: "Asia/Shanghai" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, clock, posts));
  await page.goto("http://magpie.test/?view=groups");
  const group = page.locator(".rt-group").first();
  await group.waitFor();
  const titles = () => group.evaluate((g) => [...g.querySelectorAll("[title]")].map((e) => e.title).join("\n"));
  return { page, group, errors, posts, titles };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a rule's hours on a server in another zone`, async (t) => {
      const { page, group, errors, posts, titles } = await open(t, engine, lang, UTC, 420);

      // the group's rules: the zone each one's hours are in
      await page.waitForFunction(() => document.querySelector(".rt-group [title]"));
      const said = await titles();
      assert(said.includes(w.rule1), `the first rule's zone is told: ${said}`);
      assert(said.includes(w.rule2), `the second rule's hours are the server's: ${said}`);

      // the editor: beside each rule's hours, in its row
      await group.locator("button", { hasText: w.edit }).click();
      const rows = page.locator(".rt-gedit .rt-rule");
      await rows.nth(1).waitFor();
      const tag = (i) => rows.nth(i).locator(".rt-zone");
      assert.equal(await tag(0).textContent(), w.zone);
      assert.equal(await tag(1).textContent(), w.server);
      for (const i of [0, 1]) {
        assert.equal(await tag(i).isVisible(), true);
        const [z, row] = [await tag(i).boundingBox(), await rows.nth(i).boundingBox()];
        assert(z.x >= row.x - 1 && z.x + z.width <= row.x + row.width + 1, `the zone stays in its row at 420px: ${JSON.stringify([z, row])}`);
      }
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no page scrolls sideways");

      // the second rule's hours changed here: now in this browser's zone
      const from2 = rows.nth(1).locator(".rt-cond.tm input").first();
      await from2.fill("23:00");
      await from2.blur();
      assert.equal(await tag(1).textContent(), w.zone);

      const saveBtn = page.locator(".rt-gedit button.primary", { hasText: w.save });
      await saveBtn.evaluate((b) => b.click()); // past the fold at this width; the click is not what is tested
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const save = posts.find((p) => p.path === "/api/groups/save");
      assert(save, `saved: ${JSON.stringify(posts)}`);
      assert.deepEqual(save.body.rules.map((r) => r.time), [
        { from: "09:00", to: "18:00", days: WEEKDAYS, zone: "Asia/Shanghai" }, // left alone: its own
        { from: "23:00", to: "08:00", days: [], zone: "Asia/Shanghai" }, // changed: this browser's
      ]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: nothing added when magpie's clock is the hours' zone`, async (t) => {
      const { page, group, errors, titles } = await open(t, engine, lang, BEIJING, 420);
      await page.waitForFunction(() => document.querySelector(".rt-group [title]"));
      const said = await titles();
      assert(said.includes(w.plain1), `the hours alone: ${said}`);
      assert(!said.includes("Asia/Shanghai") && !said.includes("UTC"), `no zone told: ${said}`);
      await group.locator("button", { hasText: w.edit }).click();
      const rows = page.locator(".rt-gedit .rt-rule");
      await rows.nth(1).waitFor();
      for (const i of [0, 1]) assert.equal(await rows.nth(i).locator(".rt-zone").isVisible(), false);
      assert.deepEqual(errors, []);
    });
  }

  // the other languages: the words beside the hours
  for (const lang of ["zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the zone in its words`, async (t) => {
      const { page, errors, titles } = await open(t, engine, lang, UTC, 420);
      await page.waitForFunction(() => document.querySelector(".rt-group [title]"));
      const said = await titles();
      assert(said.includes(words[lang].zone), `${lang}: ${said}`);
      assert(said.includes(words[lang].server), `${lang}: ${said}`);
      assert.deepEqual(errors, []);
    });
  }
}
