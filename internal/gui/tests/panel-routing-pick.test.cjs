// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel's Routing tab switches a Manual routing group's model
// (#2489, Jerell.OvO on Discord): each Manual group of two or more members
// that is on is a row under today's totals, its picked member a button. A
// click opens the members in the one model picker, the Agents page's (no
// native select, no menu of the panel's own, no model typed in), and a pick
// saves through /api/groups/pick, the group's id and the member alone, and
// the row says the new member. A group that routes otherwise, one switched
// off, and one of a single member get no row. At the panel's 440px, in both
// engines and every language, nothing runs out of the panel. No backend:
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "relay/gpt-6-sol", name: "GPT-6 Sol", providerName: "Relay", icon: "openai" },
  { id: "claude/claude-opus-5", name: "Claude Opus 5", providerName: "Claude", icon: "claude" },
  { id: "kimi/kimi-k3-with-a-rather-long-model-name", name: "Kimi K3 with a rather long model name", providerName: "Kimi Code", icon: "kimi" },
];
const info = (id, ready = true) => ({ id, ready, provider: id.split("/")[0], name: models.find((m) => m.id === id).providerName });
function groupsOf(pick) {
  const members = models.map((m) => m.id);
  return {
    models,
    groups: [
      { id: "daily", name: "Daily driver with a long group name", members, routing: "manual", pick, picked: pick, ready: true,
        memberInfo: [info(members[0]), info(members[1]), info(members[2], false)] },
      { id: "ordered", name: "Ordered", members, routing: "order", ready: true, memberInfo: members.map((id) => info(id)) },
      { id: "off", name: "Off", members, routing: "manual", disabled: true, ready: true, memberInfo: members.map((id) => info(id)) },
      { id: "one", name: "One", members: [members[0]], routing: "manual", ready: true, memberInfo: [info(members[0])] },
    ],
  };
}

function serve(lang, picks) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  let pick = "relay/gpt-6-sol";
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage") return json({ calls: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, series: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups/pick") {
      const body = req.postDataJSON();
      picks.push(body);
      pick = body.pick;
      return json(groupsOf(pick));
    }
    if (url.pathname.startsWith("/api/groups/")) { picks.push({ wrong: url.pathname }); return json(groupsOf(pick)); }
    if (url.pathname === "/api/groups") return json(groupsOf(pick));
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    // an icon the fixture's models name that the assets don't have
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404 }));
  };
}

const want = {
  en: { head: "Model every request goes to", note: "no provider serves it now" },
  zh: { head: "所有请求发往的模型", note: "当前没有提供商可用" },
  "zh-TW": { head: "所有請求發往的模型", note: "目前沒有供應商可用" },
  ja: { head: "すべてのリクエストの送信先モデル", note: "現在使えるプロバイダーなし" },
  de: { head: "Modell, an das jede Anfrage geht", note: "derzeit von keinem Anbieter bedient" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    test(`${engine} ${lang}: the tray panel switches a Manual group's model`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      let page;
      t.after(async () => {
        if (process.env.ARTIFACT_DIR && page) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-panel-routing-pick.png`) });
        }
        await browser.close();
      });
      const errors = [], picks = [];
      page = await (await browser.newContext({ viewport: { width: 440, height: 520 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, picks));
      await page.goto("http://magpie.test/?mode=panel");
      await page.locator('[data-ptab="routing"]').click();
      const row = page.locator(".pr-pick");
      await row.first().waitFor();

      // one row: the Manual group of three that is on, no other
      assert.deepEqual(await row.evaluateAll((rs) => rs.map((r) => r.dataset.id)), ["daily"]);
      const btn = row.locator("button.sess-pick");
      assert.equal((await btn.textContent()).trim(), "GPT-6 Sol");
      assert.equal(await page.locator("#panelRouting select").count(), 0, "no native select");
      // nothing runs out of the panel at 440px, and no stripe on the left
      const fit = await row.evaluate((r) => {
        const box = document.querySelector("#panelRouting").getBoundingClientRect(), b = r.getBoundingClientRect(), p = r.querySelector(".sess-pick").getBoundingClientRect();
        return { inside: b.left >= box.left - 0.5 && b.right <= box.right + 0.5 && p.right <= b.right + 0.5 && p.left >= b.left, over: r.scrollWidth > r.clientWidth + 1,
          stripe: [r, r.parentElement].map((e) => getComputedStyle(e).borderLeftWidth) };
      });
      assert(fit.inside && !fit.over, JSON.stringify(fit));
      assert.deepEqual(fit.stripe, ["0px", "0px"]);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");

      // the one model picker, the Agents page's (its rail, its search),
      // its members ticked at the picked one, none typed in
      assert((await btn.getAttribute("aria-label")).startsWith(want[lang].head), await btn.getAttribute("aria-label"));
      assert.equal(await btn.locator(".ic").count(), 1, "the member's logo on its button");
      await btn.click();
      const pop = page.locator("#pop.model-picker:not([hidden])");
      await pop.waitFor();
      assert(await pop.locator("#pickerRail:not([hidden]) .rail-item").count() >= 3, "the picker's rail");
      assert(await pop.locator("#q").isVisible(), "the picker's search");
      assert.equal(await page.locator(".proto-menu").count(), 0, "a menu of the page's own");
      const rows = pop.locator("#list li[data-i]");
      const items = await rows.evaluateAll((ls) => ls.map((l) => ({ name: l.querySelector(".v").textContent, note: l.querySelector(".n")?.textContent || "", on: l.classList.contains("cur") })));
      assert.deepEqual(items.map((i) => i.name).sort(), models.map((m) => m.name).sort());
      assert.deepEqual(items.filter((i) => i.on).map((i) => i.name), ["GPT-6 Sol"]);
      assert(items.find((i) => i.name === models[2].name).note.includes(want[lang].note), JSON.stringify(items));
      await pop.locator("#q").fill("no-such-model");
      assert.equal(await rows.count(), 0, "a model typed in is offered");
      await pop.locator("#q").fill("");
      const mb = await pop.boundingBox();
      assert(mb.x >= 0 && mb.x + mb.width <= 440 + 0.5, JSON.stringify(mb));

      // a pick saves the group's id and the member, nothing else of it
      await rows.filter({ hasText: "Claude Opus 5" }).click();
      await page.waitForFunction(() => document.querySelector(".pr-pick .sess-pick")?.textContent.trim() === "Claude Opus 5");
      assert.deepEqual(picks, [{ id: "daily", pick: "claude/claude-opus-5" }]);
      assert.equal(await pop.count(), 0, "the picker closed");

      // the picked one again changes nothing
      await page.locator(".pr-pick .sess-pick").click();
      await pop.waitFor();
      await rows.filter({ hasText: "Claude Opus 5" }).click();
      await pop.waitFor({ state: "hidden" });
      assert.equal(picks.length, 1);
      assert.deepEqual(errors, []);
    });
  }
}
