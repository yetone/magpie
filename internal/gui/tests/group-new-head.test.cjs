// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group of any models is made with New group, which sat only at
// the routing groups' head, below the requests: out of sight on a first
// look, so a user used the found groups for days without knowing their
// own could be made (mintonight, #944). The Routing page's head has a New
// group too, by Hide accounts, with a plus and a tooltip saying what a
// group is; its click opens an empty group's editor and brings it into
// view, its name field focused. The groups' own New group has the plus as
// well. No left-border accent. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "b/m", name: "m", providerName: "B", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const found = { id: "auto-m", name: "Model M", members: ["a/m", "b/m"], auto: true, ready: true, memberInfo: info(["a/m", "b/m"]) };

const words = {
  en: { btn: "New group", tip: "Make a routing group of any models you like — agents pick it as one model" },
  zh: { btn: "新建组", tip: "用任意模型新建一个路由组，Agent 会把它当作一个模型来选" },
};

// requests enough to put the groups below the window's fold
const now = Date.now();
const routes = Array.from({ length: 30 }, (_, i) => ({
  id: "r" + i, seq: i + 1, at: new Date(now - i * 60000).toISOString(), agent: "claude", asked: "group/auto-m",
  model: "a/m", provider: "a", status: 200, ms: 900, ttft: 300, in: 100, out: 20, done: true,
}));

function serve(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 30, totals: { requests: 30, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models, pools: [], deciders: [], found: true, groups: [found] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: New group in the Routing page's head`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 560 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-new-head.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-group", { hasText: "Model M" }).waitFor({ state: "attached" });

      // the groups' own New group: with a plus
      const inList = page.locator(".rt-gsec > .row-head button", { hasText: w.btn });
      assert.equal(await inList.count(), 1);
      assert.equal(await inList.locator("svg").count(), 1, "the groups' New group has a plus");

      // the page's head: a New group in sight on arrival, the groups not
      const head = page.locator("#view-routing > .row-head #rtNewGroup");
      await head.waitFor();
      assert.equal((await head.textContent()).trim(), w.btn);
      assert.equal(await head.getAttribute("title"), w.tip);
      assert.equal(await head.locator("svg").count(), 1);
      const vh = await page.evaluate(() => innerHeight);
      const hb = await head.boundingBox();
      assert(hb && hb.y >= 0 && hb.y + hb.height <= vh, "the head's New group is in sight");
      const gTop = await page.locator(".rt-gsec").evaluate((e) => e.getBoundingClientRect().top);
      assert(gTop > vh, `the groups are below the fold here (${gTop} > ${vh})`);
      const border = await head.evaluate((e) => { const c = getComputedStyle(e); return c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor; });
      assert.equal(border, false, "no left-border accent");

      await head.click();
      const ed = page.locator(".rt-gsec .rt-gedit");
      await ed.waitFor();
      // an empty new group's editor, in view, its name field focused
      await page.waitForFunction(() => {
        const e = document.querySelector(".rt-gsec .rt-gedit");
        const r = e?.getBoundingClientRect();
        return r && r.top < innerHeight && r.bottom > 0;
      });
      assert.equal(await ed.locator("input").first().inputValue(), "");
      assert.equal(await ed.evaluate((e) => e.contains(document.activeElement) && document.activeElement.tagName), "INPUT");
      // Both New group entrances replace the same draft only after Discard.
      const name = ed.locator("input").first();
      const ask = page.locator("dialog.action-confirm[open]");
      for (const entrance of [inList, head]) {
        await name.fill("Unsaved group");
        await page.mouse.move(500, 300);
        for (let i = 0; i < 20; i++) {
          const b = await entrance.boundingBox();
          if (b.y >= 60 && b.y + b.height < vh - 60) break;
          await page.mouse.wheel(0, b.y < 60 ? -400 : 400);
          await page.waitForTimeout(100);
        }
        await entrance.click();
        await ask.waitFor();
        await ask.locator("button").first().click();
        assert.equal(await name.inputValue(), "Unsaved group", "Cancel keeps the existing group draft");
        await entrance.click();
        await ask.locator("button").last().click();
        await page.waitForFunction(() => document.querySelector(".rt-gedit input")?.value === "");
        assert.equal(await ask.count(), 0, "Discard opens a clean new group");
      }
      // The model picker's entry must protect the same draft as both buttons.
      await name.fill("Unsaved group");
      const fromModel = () => page.evaluate(() => { window.newGroupWith("a/m", "Model A"); });
      await fromModel();
      await ask.waitFor();
      await ask.locator("button").first().click();
      assert.equal(await name.inputValue(), "Unsaved group", "Cancel keeps the draft when making a group from a model");
      await fromModel();
      await ask.locator("button").last().click();
      await page.waitForFunction(() => document.querySelector(".rt-gedit input")?.value === "Model A");
      assert.deepEqual(await ed.locator(".fbrow .n").allTextContents(), ["mA"], "Discard opens the group with its chosen model");
      assert.deepEqual(errors, []);
    });
  }
}
