// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's rule may hold only at some hours of the day (a Discord
// user: send a provider's peak-price hours — DeepSeek's, GLM's — to
// another). A group's rule for them reads as its hours and days; the trace
// tells the rule that matched by them; in the editor the hours are two
// times typed into one pill, past midnight noted, the days picked like the
// agents, a time that isn't one marked, nothing moved by a click, and they
// are saved as time:{from,to,days}, with the browser's zone when typed.
// A time typed as a phone's number pad or a Chinese keyboard gives it
// ("9：00", "18") is that time, and the hours take a new rule's empty
// intent box's place (MinglunZhu on X: the time was awkward to set). A
// conversation a rule's hours sent somewhere goes where the group routes
// it once they are over, and the trace says so. In English and Chinese.
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
const groups = () => ({
  models,
  groups: [{ id: "main", name: "Main", members: ["a/glm", "b/other"], routing: "order", ready: true,
    memberInfo: [{ id: "a/glm", ready: true, context: 200000 }, { id: "b/other", ready: true, context: 200000 }],
    rules: [{ use: "b/other", time: { from: "22:00", to: "08:00", days: WEEKDAYS } }] }],
  pools: [],
});
const now = Date.now();
const route = {
  id: 7, seq: 7, time: new Date(now - 2000).toISOString(), agent: "claude", model: "main", provider: "b", done: true, status: 200, ms: 900, tokens: 9000,
  group: { id: "main", name: "Main", members: ["a/glm", "b/other"] },
  order: [{ id: "b", provider: "b", name: "B", kind: "key", model: "other", known: true }, { id: "a", provider: "a", name: "A", kind: "key", model: "glm", known: true }],
  tries: [{ id: "b", model: "other", start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200 }],
  rule: { n: 1, use: "b/other", when: ["time 22:00–08:00 Mon–Fri"], turn: 2, tokens: 9000 },
};
// the morning after: no rule matches, and the night's member gives it back
const morning = {
  ...route, id: 8, seq: 8, provider: "a",
  order: [route.order[1], route.order[0]],
  tries: [{ id: "a", model: "glm", start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200 }],
  affinity: { why: "hours", last: "b", turn: 3 },
  rule: { turn: 3, tokens: 9000, ended: 1 },
};

const words = {
  en: { cond: "22:00–08:00 Mon–Fri", story: "rule 1 matches — 22:00–08:00 Mon–Fri —", night: "runs past midnight, into the next day", every: "every day", weekdays: "weekdays", sat: "Sat", monSat: "Mon–Sat", edit: "Edit", add: "Add a rule", save: "Save", hours: "Hours and days", over: "by rule 1, but the rule's hours are over, so turn 3 goes where the group routes it." },
  zh: { cond: "周一–周五 22:00–08:00", story: "规则 1", night: "跨过午夜，到第二天", every: "每天", weekdays: "工作日", sat: "周六", monSat: "周一–周六", edit: "编辑", add: "添加规则", save: "保存", hours: "时段", over: "依据的是规则 1，但该规则的时段已过，所以第 3 轮" },
};

function serve(lang, posts, routes = [route]) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  let first = true;
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      const out = first ? routes : [];
      first = false;
      return json({ mine: true, now: new Date().toISOString(), seq: routes.at(-1).seq, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes: out });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
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
    test(`${engine} ${lang}: a rule for hours of the day`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, timezoneId: "Asia/Shanghai" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      const group = page.locator(".rt-group").first();
      await group.waitFor({ state: "attached" });

      // the trace: the rule that matched, told by its hours
      await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.cond);
      const said = await page.evaluate(() => document.documentElement.innerText);
      assert(said.includes(w.story), `the rule that matched is told: ${said.slice(0, 400)}`);
      await page.evaluate(() => window.show("groups"));
      await group.waitFor();

      // the group's rule, in words
      const title = await group.evaluate((g) => [...g.querySelectorAll("[title]")].map((e) => e.title).join("\n"));
      assert(title.includes(`1. ${w.cond} →`), `the rule reads as its hours: ${title}`);

      // the editor: the hours in their pill, the days on theirs, past midnight noted
      await group.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const rows = ed.locator(".rt-rule");
      await rows.first().waitFor();
      const tm1 = rows.first().locator(".rt-cond.tm");
      assert.equal(await tm1.getAttribute("class"), "rt-cond tm on");
      assert.deepEqual(await tm1.locator("input").evaluateAll((xs) => xs.map((x) => x.value)), ["22:00", "08:00"]);
      const days1 = rows.first().locator("button.rt-cond", { hasText: lang === "en" ? "Mon–Fri" : "周一–周五" });
      assert.equal(await days1.getAttribute("class"), "rt-cond on");
      assert.equal(await rows.first().locator(".rt-rwarn").textContent(), w.night);

      // a second rule: its hours typed, nothing moved by a click. The
      // reader wheels Add a rule into view first: the editor runs past the
      // fold since the hints under Routing (a click's scroll is put back)
      const wheelTo = async (x) => {
        const d = await x.evaluate((e) => { const v = document.querySelector("#view-groups");
          return Math.round(e.getBoundingClientRect().top - v.getBoundingClientRect().top - v.clientHeight / 2); });
        await page.mouse.move(550, 400);
        await page.mouse.wheel(0, d);
        await page.waitForTimeout(400);
      };
      const addBtn = ed.locator("button", { hasText: w.add });
      await wheelTo(addBtn);
      await addBtn.click();
      const row2 = rows.nth(1);
      // a new rule asks for an intent; the hours, added from the rule's +
      // menu, take the empty box's place
      assert.equal(await row2.locator(".rt-cond.tm").count(), 0, "no hours until they are added");
      assert.equal(await row2.locator('.rt-c[data-k="intent"]').count(), 1);
      await row2.locator(".rt-addc").click();
      await page.locator(".proto-menu .pm-item", { hasText: w.hours }).click();
      const tm2 = row2.locator(".rt-cond.tm");
      await tm2.waitFor();
      assert.equal(await row2.locator('.rt-c[data-k="intent"]').count(), 0, "the empty intent gave way to the hours");
      const dayBtn2 = row2.locator("button.rt-cond", { hasText: w.every });
      assert.equal(await dayBtn2.isVisible(), true, "the days beside the hours, every day until picked");
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + Math.round(document.querySelector(".rt-gedit").getBoundingClientRect().top));
      await tm2.scrollIntoViewIfNeeded(); // by the test, as the reader would
      await page.waitForTimeout(200);
      const before = await at();
      const label = tm2.locator("span").first();
      await label.click();
      assert.equal(await at(), before, "the click moved nothing");
      const [from2, to2] = [tm2.locator("input").nth(0), tm2.locator("input").nth(1)];
      assert.equal(await from2.evaluate((x) => x === document.activeElement), true, "a click on the pill types its first time");
      await page.keyboard.type("25:00");
      await to2.click();
      assert.match(await tm2.getAttribute("class"), /\bbad\b/, "a time that isn't one is marked once left");
      assert.equal(await at(), before, "the click moved nothing");
      // typed as a Chinese keyboard and a phone's number pad give them
      await from2.fill("9：00");
      await to2.fill("18");
      await to2.blur();
      assert.equal(await from2.inputValue(), "09:00");
      assert.equal(await to2.inputValue(), "18:00");
      assert.equal(await tm2.getAttribute("class"), "rt-cond tm on");
      assert.equal(await row2.locator(".rt-rwarn").textContent(), "");

      // the days: weekdays, and Saturday too
      await dayBtn2.waitFor();
      await dayBtn2.click();
      assert.equal(await at(), before, "the click moved nothing");
      await page.locator("#list li").filter({ hasText: w.weekdays }).first().click();
      const days2 = row2.locator('.rt-c[data-k="time"] > button.rt-cond').last();
      assert.equal(await days2.textContent(), lang === "en" ? "Mon–Fri" : "周一–周五");
      await days2.click();
      await page.locator("#list li").filter({ hasText: w.sat }).last().click(); // the day's own, after weekends
      assert.equal(await days2.textContent(), w.monSat);
      assert.equal(await days2.getAttribute("class"), "rt-cond on");

      // saved: time:{from,to,days}, neither rule refused as having no condition
      const saveBtn = ed.locator("button.primary", { hasText: w.save });
      await page.mouse.move(550, 600);
      await page.mouse.wheel(0, 600);
      await page.waitForTimeout(300);
      const sb = await saveBtn.boundingBox();
      await page.mouse.click(sb.x + sb.width / 2, sb.y + sb.height / 2);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const save = posts.find((p) => p.path === "/api/groups/save");
      assert(save, `saved: ${JSON.stringify(posts)}`);
      assert.deepEqual(save.body.rules.map((r) => [r.use, r.time]), [
        ["b/other", { from: "22:00", to: "08:00", days: WEEKDAYS }],
        ["b/other", { from: "09:00", to: "18:00", days: [...WEEKDAYS, "sat"], zone: "Asia/Shanghai" }], // typed here: in this browser's zone
      ]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the rule's hours over, the conversation goes back`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, timezoneId: "Asia/Shanghai" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, [], [morning]));
      await page.goto("http://magpie.test/?view=routing");
      // the caption under the stage tells it
      const said = () => document.body.innerText + "\n" + (document.querySelector(".rt-cap")?.textContent || "");
      await page.waitForFunction((s) => (document.body.innerText + "\n" + (document.querySelector(".rt-cap")?.textContent || "")).includes(s), w.over)
        .catch(async (e) => { throw new Error(`${e.message}: ${await page.evaluate(said)}`); });
      assert.deepEqual(errors, []);
    });
  }
}
