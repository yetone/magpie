// Run with Node's test runner and Playwright on the module path; see README.md.
// visamzh on X: a routing group held gpt-6.2-sol at high and at xhigh, two
// members of one model (#189), but the Add a model picker took a model in
// the group at a fixed level as that member (#907): clicking it took the
// member out, so the model could not be added a second time.
// - The picker lists the member at its level as a ticked row of its own
//   and the model's own row under it, not ticked; a click there adds the
//   model again, whose reasoning is then fixed at another level, and a
//   save sends both members.
// - A group read with the same model at two levels keeps both, in the
//   editor and in what it saves.
// In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const efforts = ["low", "medium", "high", "xhigh"];
const models = [
  { id: "cx/sol", name: "sol", providerName: "Codex", icon: "generic", efforts },
  { id: "cx/terra", name: "terra", providerName: "Codex", icon: "generic", efforts },
];

const words = {
  en: { edit: "Edit", save: "Save", add: "Add another model", xhigh: "xhigh reasoning", high: "high reasoning" },
  zh: { edit: "编辑", save: "保存", add: "再添加一个模型", xhigh: "xhigh 推理强度", high: "high 推理强度" },
};

function serve(lang, posts) {
  const info = (ids) => ids.map((id) => ({ id, ready: true }));
  const groups = () => ({
    models, pools: [],
    groups: [
      { id: "g", name: "G", members: ["cx/sol:xhigh", "cx/terra:high"], off: [], routing: "order", ready: true, memberInfo: info(["cx/sol:xhigh", "cx/terra:high"]) },
      { id: "pair", name: "Pair", members: ["cx/sol:xhigh", "cx/sol:high"], off: [], routing: "order", ready: true, memberInfo: info(["cx/sol:xhigh", "cx/sol:high"]) },
    ],
  });
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
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
    test(`${engine} ${lang}: a model in a group at one reasoning is added again at another`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const card = (id) => page.locator(`.rt-group[data-id="${id}"]`);
      await card("g").waitFor();
      const pop = page.locator("#pop");
      const save = async (ed) => {
        const b = ed.locator("button.primary", { hasText: w.save });
        await page.mouse.move(550, 600);
        for (let i = 0; i < 10 && (await b.boundingBox()).y > 1400 - 160; i++) {
          await page.mouse.wheel(0, 300);
          await page.waitForTimeout(150);
        }
        const bb = await b.boundingBox();
        await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        return posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
      };

      // G holds sol at xhigh: the picker adds sol again
      await card("g").locator("button", { hasText: w.edit }).click();
      let ed = page.locator(".rt-gedit");
      const rows = () => ed.locator(".fbl").first().locator(".fbrow");
      assert.equal(await rows().count(), 2);
      await ed.locator("button.rt-gadd", { hasText: w.add }).click();
      const sols = pop.locator("li", { has: page.locator(".v", { hasText: /^sol$/ }) });
      await sols.first().waitFor();
      assert.equal(await sols.count(), 2, "sol at xhigh and sol itself are not two rows");
      const cur = (li) => li.evaluate((x) => x.classList.contains("cur"));
      assert.equal(await cur(sols.nth(0)), true, "sol at xhigh is not ticked");
      assert((await sols.nth(0).locator(".n").textContent()).includes(w.xhigh), "the held row doesn't say its level");
      assert.equal(await cur(sols.nth(1)), false, "sol itself is ticked while it is in at xhigh only");
      await sols.nth(1).click();
      assert.equal(await rows().count(), 3, "sol was not added again");
      assert.equal(await cur(sols.nth(0)), true, "adding sol again took sol at xhigh out");
      assert.equal(await cur(sols.nth(1)), true);
      await page.keyboard.press("Escape");
      await pop.waitFor({ state: "hidden" });

      // the new row fixed at high
      await rows().nth(2).locator("button.rt-fixed:not(.rt-fast)").click();
      await pop.locator("li", { has: page.locator(".v", { hasText: new RegExp(`^${w.high}$`) }) }).first().click();
      await pop.waitFor({ state: "hidden" });
      assert.equal((await rows().nth(2).locator("button.rt-fixed:not(.rt-fast)").textContent()).trim(), w.high);
      assert.equal((await rows().nth(0).locator("button.rt-fixed:not(.rt-fast)").textContent()).trim(), w.xhigh);
      let sent = await save(ed);
      assert.deepEqual(sent.members, ["cx/sol:xhigh", "cx/terra:high", "cx/sol:high"], "both of sol's members are not sent");

      // Pair, read with sol at two levels, keeps both
      await card("pair").locator("button", { hasText: w.edit }).click();
      ed = page.locator(".rt-gedit");
      assert.equal(await rows().count(), 2, "a member of the same model was dropped on read");
      sent = await save(ed);
      assert.deepEqual(sent.members, ["cx/sol:xhigh", "cx/sol:high"]);
      assert.deepEqual(errors, []);
    });
  }
}
