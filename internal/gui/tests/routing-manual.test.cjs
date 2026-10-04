// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group may be routed by hand (#317: pick the channel, as CC
// Switch does, rather than only by rules). The group editor offers Manual
// beside the other routings, with its hint, and saving keeps the pick; a
// manual group's card lists its models, the one every request goes to
// marked, and clicking another saves the group with that pick — the card's
// editor stays shut and nothing on the page moves. The rules tag says they
// wait. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/sol", name: "sol", providerName: "A", icon: "generic" },
  { id: "b/flash", name: "flash", providerName: "B", icon: "generic" },
  { id: "c/luna", name: "luna", providerName: "C", icon: "generic" },
];
const info = [{ id: "a/sol", ready: true }, { id: "b/flash", ready: true }, { id: "c/luna", ready: true }];
const base = { members: ["a/sol", "b/flash", "c/luna"], ready: true, memberInfo: info, offers: [], shared: [] };
const groups = (pick) => ({ models, pools: [], groups: [
  // filler above, so that the reader scrolls down to the group
  ...Array.from({ length: 14 }, (_, i) => ({ ...base, id: "g" + i, name: "Filler " + i, routing: "" })),
  { ...base, id: "hand", name: "Hand", routing: "manual", pick, picked: pick, affinity: "session", family: "relay",
    rules: [{ use: "c/luna", tokens: 100000 }] },
  { ...base, id: "auto", name: "Auto", routing: "order" },
] });

const words = {
  en: { edit: "Edit", save: "Save", routing: "Routing", manual: "Manual", smart: "Smart",
    hint: "Manual: every request goes to the model you pick on the group's card, over its own accounts or keys; the others, and the rules, wait until you pick another — none takes over when it fails.",
    rulesWait: "The rules wait while you pick the model by hand.", group: "Model every request goes to",
    saved: "Hand: every request to luna" },
  zh: { edit: "编辑", save: "保存", routing: "路由", manual: "手动", smart: "智能",
    hint: "手动：所有请求都发给你在路由组卡片上点选的模型（在它自己的账号或 Key 之间路由）；其他模型和规则暂不生效，直到你改选别的模型——它失败时也不会切到其他模型。",
    rulesWait: "手动选择模型时，规则暂不生效。", group: "所有请求发往的模型",
    saved: "Hand：所有请求改发给 luna" },
};

function serve(lang, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  let pick = "b/flash";
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
    if (url.pathname === "/api/groups") return json(groups(pick));
    if (url.pathname.startsWith("/api/groups/")) {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push({ path: url.pathname, body });
      if (body.id === "hand" && body.pick) pick = body.pick;
      return json(groups(pick));
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
    test(`${engine} ${lang}: a manual group's model is picked on its card`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 700 } })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      const card = page.locator(".rt-group", { hasText: "Hand" });
      await card.waitFor();

      // the card: every model, the picked one marked; the rules wait
      const picks = card.locator(`.rt-picks[role="radiogroup"][aria-label="${w.group}"] .rt-pick`);
      assert.deepEqual(await picks.locator(".n").allTextContents(), ["sol", "flash", "luna"]);
      assert.deepEqual(await card.locator(".rt-pick.on .n").allTextContents(), ["flash"]);
      assert.equal(await card.locator('.rt-pick[aria-checked="true"]').getAttribute("data-member"), "b/flash");
      assert.equal(await card.locator(".tag").first().textContent(), w.manual);
      assert.match(await card.locator(".tag.idle").getAttribute("title"), new RegExp("^" + w.rulesWait));
      // a group routed otherwise lists its models as before
      assert.equal(await page.locator(".rt-group", { hasText: "Auto" }).locator(".rt-picks").count(), 0);

      // the reader scrolls the card to the middle, then picks luna: saved
      // with the group's other fields as they were, nothing moves, the
      // editor stays shut
      const view = page.locator("#view-routing");
      const dy = await card.evaluate((g) => { const v = document.querySelector("#view-routing");
        return Math.round(g.getBoundingClientRect().top - v.getBoundingClientRect().top - v.clientHeight / 2); });
      await page.mouse.move(550, 400);
      await page.mouse.wheel(0, dy);
      await page.waitForTimeout(400);
      const at = () => view.evaluate((v) => v.scrollTop + "|" + Math.round(document.querySelector(".rt-picks").getBoundingClientRect().top));
      const before = await at();
      assert.ok(parseInt(before) > 200, `the view must be scrolled down to the group (${before})`);
      await picks.filter({ hasText: "luna" }).click();
      await page.waitForFunction(() => document.querySelector(".rt-pick.on .n")?.textContent === "luna");
      assert.equal(await at(), before, "the pick moved nothing");
      assert.equal(await page.locator(".rt-gedit").count(), 0, "the editor stays shut");
      assert.equal(posts.length, 1);
      const sent = posts[0];
      assert.equal(sent.path, "/api/groups/save");
      assert.deepEqual({ id: sent.body.id, routing: sent.body.routing, pick: sent.body.pick, members: sent.body.members, affinity: sent.body.affinity, family: sent.body.family, rules: sent.body.rules },
        { id: "hand", routing: "manual", pick: "c/luna", members: ["a/sol", "b/flash", "c/luna"], affinity: "session", family: "relay", rules: [{ use: "c/luna", tokens: 100000 }] });
      await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.saved);
      // the one picked already does nothing
      await picks.filter({ hasText: "luna" }).click();
      await page.waitForTimeout(200);
      assert.equal(posts.length, 1);

      // the editor: Manual among the routings, its hint, the pick kept on save
      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const row = ed.locator("label", { hasText: new RegExp(`^${w.routing}$`) }).locator("xpath=following-sibling::div[1]");
      assert.equal(await row.locator(".segs .opt.on").textContent(), w.manual);
      // the reader wheels the editor's routing into the middle (code that
      // scrolls is put back: only the reader moves the view)
      const wheelTo = async (x) => {
        const d = await x.evaluate((e) => { const v = document.querySelector("#view-routing");
          return Math.round(e.getBoundingClientRect().top - v.getBoundingClientRect().top - v.clientHeight / 2); });
        await page.mouse.move(550, 400);
        await page.mouse.wheel(0, d);
        await page.waitForTimeout(400);
      };
      await wheelTo(row);
      // the routing's own hint is the first; a group-in-group note follows it
      assert.equal(await row.locator(".hint").first().textContent(), w.hint);
      await row.locator(".segs .opt", { hasText: w.smart }).click();
      assert.notEqual(await row.locator(".hint").first().textContent(), w.hint);
      await row.locator(".segs .opt", { hasText: w.manual }).click();
      assert.equal(await row.locator(".hint").first().textContent(), w.hint);
      assert.ok((await ed.innerText()).includes(w.rulesWait));
      const b = ed.locator("button.primary", { hasText: w.save });
      await wheelTo(b);
      await b.click();
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const saved = posts.at(-1);
      assert.equal(saved.body.routing, "manual");
      assert.equal(saved.body.pick, "c/luna");
      assert.deepEqual(errors, []);
    });
  }
}
