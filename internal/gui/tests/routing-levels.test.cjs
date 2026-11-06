// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group may name the reasoning levels agents are offered for it
// (#295: a cheap member with low/high/max took medium and xhigh from the
// strong ones beside it). The group editor's Levels reads "its models'
// shared" with those levels in words; "Named" shows a toggle per level,
// starting from the shared ones, each toggled with nothing moved; saved,
// they go as levels, lowest first, and the group's family stays; a group
// with its own opens on them, and back to shared saves none. None picked
// is refused. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const all = ["none", "low", "medium", "high", "xhigh", "max"];
const models = [
  { id: "a/sol", name: "sol", providerName: "A", icon: "generic", efforts: all },
  { id: "b/flash", name: "flash", providerName: "B", icon: "generic", efforts: ["low", "high", "max"] },
];
const group = (id, name, levels) => ({ id, name, members: ["a/sol", "b/flash"], routing: "order", family: "relay", ready: true, levels,
  memberInfo: [{ id: "a/sol", ready: true }, { id: "b/flash", ready: true }], offers: levels?.length ? levels : ["low", "high", "max"], shared: ["low", "high", "max"] });
const groups = () => ({ models, groups: [group("mix", "Mix"), group("own", "Own", ["medium", "xhigh"])], pools: [] });

const words = {
  en: { edit: "Edit", save: "Save", levels: "Levels", shared: "Its models' shared", named: "Named",
    sharedHint: "Agents are offered the levels every model has: low, high, max.", namedHint: "Agents are offered these. A model without the level asked is sent the one it has nearest.",
    none: "Pick a level to offer, or leave them to its models" },
  zh: { edit: "编辑", save: "保存", levels: "推理档位", shared: "成员共有", named: "自定",
    sharedHint: "对 Agent 提供所有模型都有的档位：low, high, max。", namedHint: "向 Agent 提供这些档位。模型没有所选档位时按最接近的发送。",
    none: "请至少选一个档位，或改回成员共有" },
};

function serve(lang, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
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
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a group names the reasoning levels agents are offered`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      await page.locator(".rt-group").first().waitFor();
      const ed = page.locator(".rt-gedit");
      // the editor's Levels row: the field after its label
      const row = () => ed.locator("label", { hasText: new RegExp(`^${w.levels}$`) }).locator("xpath=following-sibling::div[1]");
      const chips = () => row().locator(".rt-levels .rt-cond");
      const lit = () => row().locator(".rt-levels .rt-cond.on").allTextContents();
      const save = async () => {
        // (the view wheeled down to it, as the reader would — the page
        // keeps still for any other scroll — and clicked where it shows)
        const b = ed.locator("button.primary", { hasText: w.save });
        await page.mouse.move(550, 600);
        for (let i = 0; i < 10 && (await b.boundingBox()).y > 1400 - 160; i++) {
          await page.mouse.wheel(0, 300);
          await page.waitForTimeout(150);
        }
        const bb = await b.boundingBox();
        await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
      };

      // shared: the levels in words, no toggles
      await page.locator(".rt-group", { hasText: "Mix" }).locator("button", { hasText: w.edit }).click();
      await row().waitFor();
      assert.equal(await row().locator(".segs .opt.on").textContent(), w.shared);
      assert.equal(await row().locator(".hint").textContent(), w.sharedHint);
      assert.equal(await row().locator(".rt-levels").isHidden(), true);

      // named: one toggle per level, the shared ones on (the row, under the
      // window's foot as the editor grew, wheeled to as the reader would:
      // WebKit's own scroll left a sliver of it over the footer and clicked
      // the footer)
      await page.mouse.move(550, 600);
      for (let i = 0; i < 10 && (await row().boundingBox()).y > 1400 - 260; i++) {
        await page.mouse.wheel(0, 300);
        await page.waitForTimeout(150);
      }
      await row().locator(".segs .opt", { hasText: w.named }).click();
      assert.equal(await row().locator(".hint").textContent(), w.namedHint);
      assert.deepEqual(await chips().allTextContents(), ["none", "minimal", "low", "medium", "high", "xhigh", "max"]);
      assert.deepEqual(await lit(), ["low", "high", "max"]);
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + Math.round(document.querySelector(".rt-gedit").getBoundingClientRect().top));
      await row().evaluate((x) => x.scrollIntoView({ block: "center" })); // by the test, as the reader would
      await page.waitForTimeout(200);
      const before = await at();
      for (const v of ["xhigh", "medium", "none"]) await chips().filter({ hasText: new RegExp(`^${v}$`) }).click();
      await chips().filter({ hasText: /^none$/ }).click(); // off again
      assert.equal(await at(), before, "the clicks moved nothing");
      assert.deepEqual(await lit(), ["low", "medium", "high", "xhigh", "max"]);
      await save();
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      let sent = posts.find((p) => p.path === "/api/groups/save");
      assert.deepEqual(sent.body.levels, ["low", "medium", "high", "xhigh", "max"]);
      assert.equal(sent.body.family, "relay", "the family stays");

      // a group with its own opens on them; none picked is refused; back
      // to shared saves none
      posts.length = 0;
      await page.locator(".rt-group", { hasText: "Own" }).locator("button", { hasText: w.edit }).click();
      await row().waitFor();
      assert.equal(await row().locator(".segs .opt.on").textContent(), w.named);
      assert.deepEqual(await lit(), ["medium", "xhigh"]);
      await chips().filter({ hasText: /^medium$/ }).click();
      await chips().filter({ hasText: /^xhigh$/ }).click();
      await save();
      await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.none);
      assert.equal(posts.length, 0, "nothing saved with no level");
      await row().locator(".segs .opt", { hasText: w.shared }).click();
      assert.equal(await row().locator(".rt-levels").isHidden(), true);
      await save();
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      sent = posts.find((p) => p.path === "/api/groups/save");
      assert.deepEqual(sent.body.levels, []);
      assert.deepEqual(errors, []);
    });
  }
}
