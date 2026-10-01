// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider set to "Only through routing groups" keeps its models out of
// the agents' pickers; one in no group was simply gone (悠悠哥 on Discord:
// hy4 vanished, it looked like a bug). Now the picker's filter, finding
// such a model, says why it isn't offered: in a group, that group is, and
// "Pick Mine" picks it; in none, nothing can use it, and "Make a routing
// group of it" opens the Routing page on a new group of it, saved as one.
// The provider's editor names its models in no group under the tick, each
// a click from a group of its own; the tray panel opens the window on the
// new group. Nothing scrolls the page; no left-border accent. In English
// and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [
  { value: "magpie/group/mine", label: "Mine", note: "routing group · via magpie", group: "Routing groups", ref: "group/mine", icon: "generic" },
  { value: "magpie/other/m1", label: "m1", note: "Other · via magpie", group: "Other", ref: "other/m1", icon: "generic" },
];
const unlisted = [
  { id: "hunyuan/hy4", name: "hy4", provider: "Hunyuan", icon: "generic", groups: [] },
  { id: "hunyuan/hy3", name: "hy3", provider: "Hunyuan", icon: "generic", groups: ["group/mine"] },
];
const hunyuan = {
  id: "hunyuan", name: "Hunyuan", icon: "generic", chat: "https://hy.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: ["hy3", "hy4", "hy-lite"].map((id) => ({ id, name: id, on: id !== "hy-lite" })), chosen: ["hy3", "hy4"],
  groups: { hy3: ["group/mine"] }, unlisted: true, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};

const words = {
  en: {
    none: "Hunyuan is used only through routing groups, and hy4 is in none, so no agent can use it. Make a group of it, or untick “Only through routing groups” in Hunyuan's models.",
    via: "Hunyuan is used only through routing groups: agents reach hy3 by picking Mine.",
    make: "Make a routing group of it", pick: "Pick Mine", save: "Add",
    lost: "In no routing group, so no agent can use them now: hy4.", makeOf: "Make a routing group of hy4",
  },
  zh: {
    none: "Hunyuan 设为只通过路由分组使用，而 hy4 不在任何分组里，所以没有 Agent 能用到它。可以用它建一个分组，或在 Hunyuan 的模型里取消勾选「只通过路由分组使用」。",
    via: "Hunyuan 设为只通过路由分组使用：选 Mine 即可用到 hy3。",
    make: "用它建路由分组", pick: "选 Mine", save: "添加",
    lost: "不在任何路由分组里，所以现在没有 Agent 能用到：hy4。", makeOf: "用 hy4 建路由分组",
  },
};

function serve(lang, posts) {
  const state = () => ({ agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [{ key: "model", label: "model", value: "magpie/other/m1", options }] }],
    profiles: [], unlisted, settings: { lang, theme: "light" } });
  const groups = { groups: [], pools: [], deciders: [], models: [{ id: "hunyuan/hy4", name: "hy4", provider: "hunyuan", providerName: "Hunyuan", icon: "generic" }] };
  return async (r) => {
    const req = r.request(), url = new URL(req.url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (req.method() === "POST" && url.pathname.startsWith("/api/")) posts.push({ path: url.pathname + url.search, body: JSON.parse(req.postData() || "{}") });
    if (url.pathname === "/api/state" || url.pathname === "/api/set") return json(state());
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups" || url.pathname.startsWith("/api/groups/")) return json(groups);
    if (url.pathname === "/api/providers") return json({ providers: [hunyuan], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999", groups: [] } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = '.row.agent[data-id="claude"]';
const leftBorders = (page, sel) => page.evaluate((s) => [...document.querySelectorAll(s)].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1), sel);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a model kept for routing groups is said, not gone`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      const open = async (url, posts, viewport = { width: 1000, height: 700 }) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto(url);
        return page;
      };
      const filterFor = async (page, field, q) => {
        await field.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        await page.locator("#q").fill(q);
      };

      // the window's picker: hy3 is reached through Mine, picked at a click
      let posts = [];
      let page = await open("http://magpie.test/", posts);
      const field = page.locator(`${row} .field[data-key="model"]`);
      await field.waitFor();
      const view = page.locator("#view-agents");
      await filterFor(page, field, "hy3");
      const kept = page.locator("#list li.kept");
      await kept.waitFor();
      assert.equal(await kept.count(), 1);
      assert.equal(await kept.locator(".kept-words span").textContent(), w.via);
      assert.deepEqual(await leftBorders(page, "#pop, #pop *"), [], "no left-border accent");
      const top = await view.evaluate((v) => v.scrollTop);
      await kept.getByRole("button", { name: w.pick }).click();
      await page.waitForFunction(() => document.querySelector("#pop").hidden);
      for (let i = 0; i < 40 && !posts.some((p) => p.path === "/api/set"); i++) await page.waitForTimeout(50);
      assert.deepEqual(posts.find((p) => p.path === "/api/set")?.body, { agent: "claude", field: "model", value: "magpie/group/mine" });
      assert.equal(await view.evaluate((v) => v.scrollTop), top, "the pick moved the page");

      // hy4, in none: said, and a click opens a new group of it, saved as one
      await filterFor(page, field, "hy4");
      await kept.waitFor();
      assert.equal(await kept.locator(".kept-words b").textContent(), "hy4");
      assert.equal(await kept.locator(".kept-words span").textContent(), w.none);
      // the list's own choices are still the ones the keys move over
      assert.equal(await page.locator("#list li[data-i]").count(), 1, "only “use as typed” is a choice");
      await kept.getByRole("button", { name: w.make }).click();
      const ed = page.locator("#view-routing:not([hidden]) .rt-gedit");
      await ed.waitFor();
      assert.equal(await ed.locator("input").first().inputValue(), "hy4", "named after the model");
      await ed.getByRole("button", { name: w.save, exact: true }).click();
      for (let i = 0; i < 40 && !posts.some((p) => p.path === "/api/groups/save"); i++) await page.waitForTimeout(50);
      const saved = posts.find((p) => p.path === "/api/groups/save")?.body;
      assert.deepEqual(saved?.members, ["hunyuan/hy4"], "a group of hy4 saved");
      assert.equal(saved.id, "hy4");

      // the provider's editor names the model in no group under the tick
      page = await open("http://magpie.test/?view=providers", posts);
      await page.locator(".row.provider", { hasText: "Hunyuan" }).click();
      const lost = page.locator(".editor .model-hint.warn");
      await lost.waitFor();
      assert((await lost.textContent()).startsWith(w.lost), await lost.textContent());
      assert.equal(await lost.getByRole("button").count(), 1);
      // unticked, nothing is lost
      await page.locator(".editor label", { hasText: lang === "zh" ? "只通过路由分组使用" : "Only through routing groups" }).locator("input").uncheck();
      assert(await lost.isHidden(), "said only while kept for groups");
      await page.locator(".editor label", { hasText: lang === "zh" ? "只通过路由分组使用" : "Only through routing groups" }).locator("input").check();
      await lost.getByRole("button", { name: w.makeOf }).click();
      const ed2 = page.locator("#view-routing:not([hidden]) .rt-gedit");
      await ed2.waitFor();
      assert.equal(await ed2.locator("input").first().inputValue(), "hy4");

      // opened with ?newgroup=, as the tray panel asks the window
      page = await open("http://magpie.test/?view=routing&newgroup=hunyuan%2Fhy4", posts);
      await page.locator(".rt-gedit").waitFor();
      assert.equal(await page.locator(".rt-gedit input").first().inputValue(), "hy4");

      // the tray panel opens the window on it
      posts = [];
      page = await open("http://magpie.test/?mode=panel", posts, { width: 440, height: 620 });
      await page.locator(`${row} .ag-sum`).click();
      await page.waitForTimeout(500);
      await filterFor(page, page.locator(`${row} .ag-open .field[data-key="model"]`), "hy4");
      await page.locator("#list li.kept").getByRole("button", { name: w.make }).click();
      for (let i = 0; i < 40 && !posts.some((p) => p.path.startsWith("/api/window/main")); i++) await page.waitForTimeout(50);
      assert.equal(posts.find((p) => p.path.startsWith("/api/window/main"))?.path, "/api/window/main?view=routing&newgroup=hunyuan%2Fhy4");

      const missing = await page.evaluate((ks) => ks.filter((k) => !I18N.zh[k]), [
        "In no routing group, so no agent can use them now: {models}.", "Once saved, make a group of them in Routing.",
        "Make a routing group of {model}", "Make a routing group of it", "Pick {group}",
        "{provider} is used only through routing groups: agents reach {model} by picking {group}.",
        "{provider} is used only through routing groups, and {model} is in none, so no agent can use it. Make a group of it, or untick “Only through routing groups” in {provider}'s models.",
      ]);
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
