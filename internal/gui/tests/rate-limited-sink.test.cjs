// Run with Node's test runner and Playwright on the module path; see README.md.
// 01huadalang (WorkBuddy, several accounts): an account rate limited while
// it still has quota goes back to the front once its rest ends, so one
// account takes everything until it is limited again — what a vendor's
// risk control notices. "Rate limited" beside a provider's Routing (its
// editor, made with its Save; the Routing page's row of providers with
// several accounts, at once) and a group's: "Keeps its place", the
// default, or "Goes to the back", which sends it behind every one not rate
// limited since. Hidden for In turn, which goes round already. The Routing
// page says which one is at the back and since when. No native select, and
// a click never scrolls the page. English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const base = { icon: "generic", catalog: "", models: [{ id: "glm-5", name: "GLM-5", on: true }], agents: [], fallback: [], headers: {}, routing: "order", affinity: "", ready: true };
const sub = {
  ...base, id: "workbuddy-ai", name: "WorkBuddy AI", chat: "", responses: "", anthropic: "", keyList: [],
  account: {
    agent: "workbuddy-ai", agentName: "WorkBuddy AI", user: "one@example.test", plan: "Pro",
    logins: ["one", "two", "three"].map((n, i) => ({ user: `${n}@example.test`, plan: "Pro", active: i === 0, on: true })),
  },
};
const members = ["workbuddy-ai/glm-5", "rl/glm-5"];
const groups = () => ({
  models: [{ id: "workbuddy-ai/glm-5", name: "glm-5", providerName: "WorkBuddy AI", icon: "generic" }, { id: "rl/glm-5", name: "glm-5", providerName: "Relay", icon: "generic" }],
  pools: [{ provider: "workbuddy-ai", name: "WorkBuddy AI", icon: "generic", kind: "account", who: ["one@example.test", "two@example.test", "three@example.test"], routing: "order", affinity: "" }],
  groups: [{ id: "glm", name: "GLM", members, fast: [], off: [], routing: "order", sink: true, ready: true, memberInfo: members.map((id) => ({ id, ready: true })) }],
});
const sunkAt = now - 60e3;
const order = ["two", "three", "one"].map((n, i) => ({
  id: `workbuddy-ai@${n}@example.test`, provider: "workbuddy-ai", name: "WorkBuddy AI", who: `${n}@example.test`, kind: "account", model: "glm-5", routing: "order", shared: true, rank: i,
  ...(n === "one" ? { sunk: iso(sunkAt) } : {}),
}));
const route = {
  id: 7, seq: 7, time: iso(now - 2000), agent: "pi", model: "glm-5", provider: "workbuddy-ai", done: true, status: 200, ms: 900, tokens: 1500,
  order, tries: [{ id: order[0].id, model: "glm-5", start: iso(now - 2000), done: true, ms: 800, status: 200 }],
};

function serve(lang, posts) {
  const state = { agents: [{ id: "pi", name: "Pi", path: "/test/pi", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const providers = { providers: [sub], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 7, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [route] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/providers") return json(providers);
    if (r.request().method() === "POST" && (url.pathname.startsWith("/api/provider") || url.pathname.startsWith("/api/groups/"))) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(url.pathname.startsWith("/api/groups/") ? groups() : providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { label: "Rate limited", keep: "Keeps its place", back: "Goes to the back", turn: "In turn", order: "In order", save: "Save", edit: "Edit", unsaved: "unsaved", tag: "Rate limited to the back", row: "at the back" },
  zh: { label: "被限流时", keep: "保持原位", back: "排到最后", turn: "轮流", order: "按顺序", save: "保存", edit: "编辑", unsaved: "未保存", tag: "限流后排到最后", row: "排在最后" },
};
const KEYS = [
  "Rate limited", "Keeps its place", "Goes to the back", "Rate limited to the back", "rate limited at {time} · at the back",
  "One rate limited while it still has quota rests as long as the vendor asks, then takes its place in the order again.",
  "One rate limited (429) while it still has quota goes to the back of the order, behind every one not rate limited since, and comes round again once those ahead of it are rate limited in turn — so the load goes round rather than back to the first each time. Out of quota, it rests as usual. Kept until magpie restarts.",
  "{who} was rate limited at {time} while it had quota left, so it went to the back: it comes round again once those ahead of it are rate limited in turn.",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang], zh = lang === "zh";
    const launch = () => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" });
    const said = (page, s) => page.evaluate(([k, zh]) => zh ? I18N.zh[k] : k, [s, zh]);

    test(`${engine} ${lang}: a provider's rate limited accounts go to the back, made with its Save`, async (t) => {
      const browser = await launch();
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 720 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: sub.name }).first().click();
      await page.locator(".editor .accts").first().waitFor();
      assert.deepEqual(await page.evaluate((keys) => keys.filter((k) => !I18N.zh[k]), KEYS), [], "every string has its Chinese");
      const label = page.locator(".editor label", { hasText: new RegExp(`^${w.label}$`) });
      const field = label.locator("xpath=following-sibling::div[1]");
      const opt = (name) => field.locator(".segs .opt", { hasText: new RegExp(`^${name}$`) });
      const routing = (name) => page.locator(".editor .segs .opt", { hasText: new RegExp(`^${name}$`) }).first();
      assert.equal(await page.locator(".editor select").count(), 0, "no native select");
      assert.match(await opt(w.keep).getAttribute("class"), /\bon\b/, "keeps its place by default");
      assert.equal(await field.locator(".hint").last().textContent(), await said(page, "One rate limited while it still has quota rests as long as the vendor asks, then takes its place in the order again."));
      const y = await page.evaluate(() => document.scrollingElement.scrollTop);
      await opt(w.back).click();
      assert.match(await opt(w.back).getAttribute("class"), /\bon\b/);
      assert.match(await field.locator(".hint").last().textContent(), zh ? /排到最后/ : /goes to the back of the order/);
      assert.equal(await field.getByText(w.unsaved, { exact: true }).isVisible(), true);
      // in turn goes round already: nothing to pick
      await routing(w.turn).click();
      assert.equal(await label.isVisible(), false);
      await routing(w.order).click();
      assert.equal(await label.isVisible(), true);
      assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y, "a click never scrolls the page");
      assert.deepEqual(posts, [], "nothing is sent before the Save");
      await page.getByRole("button", { name: w.save, exact: true }).click();
      await page.waitForFunction(() => !document.querySelector(".editor"));
      assert.deepEqual(posts.map((x) => x.path), ["/api/provider/save"]);
      assert.equal(posts[0].body.sink, true);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the Routing page sets it and says who is at the back`, async (t) => {
      const browser = await launch();
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");

      // the one at the back says so, and since when
      const stage = page.locator("li", { has: page.locator(".who", { hasText: "one@example.test" }) }).first();
      await stage.waitFor();
      await page.waitForFunction((s) => document.body.innerText.includes(s), w.row);
      assert.match(await stage.textContent(), new RegExp(w.row));
      await page.waitForFunction((s) => document.body.innerText.includes(s), zh ? "已排到最后" : "so it went to the back");

      // the providers with several accounts: set at once
      const pool = page.locator(".rt-pool", { hasText: "WorkBuddy AI" });
      await pool.waitFor();
      // the pool sits under the window's foot: wheeled to, as the reader
      // would (WebKit's own scroll into view is put back by the page, which
      // undoes a scroll the reader didn't make, and the click timed out
      // with the option outside the viewport, #1376)
      const back = pool.locator(".sink-pick .opt", { hasText: new RegExp(`^${w.back}$`) });
      await page.mouse.move(550, 600);
      for (let i = 0; i < 10 && (await back.boundingBox()).y > 1400 - 260; i++) {
        await page.mouse.wheel(0, 300);
        await page.waitForTimeout(150);
      }
      const y = await page.evaluate(() => document.scrollingElement.scrollTop);
      await back.click();
      await page.waitForTimeout(200);
      const sent = posts.find((p) => p.path === "/api/provider/sink");
      assert.deepEqual(sent?.body, { id: "workbuddy-ai", sink: true });

      // the group's card says it; its editor has it, kept by the Save
      const card = page.locator(".rt-group", { hasText: "GLM" });
      await card.locator(".tag", { hasText: w.tag }).waitFor();
      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const label = ed.locator("label", { hasText: new RegExp(`^${w.label}$`) });
      const field = label.locator("xpath=following-sibling::div[1]");
      await label.waitFor();
      assert.match(await field.locator(".opt", { hasText: new RegExp(`^${w.back}$`) }).getAttribute("class"), /\bon\b/);
      // the editor runs under the window's foot: wheeled to, as the reader
      // would (WebKit's own scroll left a sliver of In turn over the footer
      // and clicked the footer)
      const turn = ed.locator(".segs .opt", { hasText: new RegExp(`^${w.turn}$`) }).first();
      await page.mouse.move(550, 600);
      for (let i = 0; i < 10 && (await turn.boundingBox()).y > 1400 - 260; i++) {
        await page.mouse.wheel(0, 300);
        await page.waitForTimeout(150);
      }
      await ed.locator(".segs .opt", { hasText: new RegExp(`^${w.turn}$`) }).first().click();
      assert.equal(await label.isVisible(), false, "in turn: nothing to pick");
      await ed.locator(".segs .opt", { hasText: new RegExp(`^${w.order}$`) }).first().click();
      assert.equal(await label.isVisible(), true);
      assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y, "a click never scrolls the page");
      assert.equal(await page.locator("select").count(), 0, "no native select");
      const b = ed.locator("button.primary", { hasText: w.save });
      await page.mouse.move(550, 600); // the reader scrolls to it
      for (let i = 0; i < 10 && (await b.boundingBox()).y > 1400 - 160; i++) {
        await page.mouse.wheel(0, 300);
        await page.waitForTimeout(150);
      }
      const bb = await b.boundingBox();
      await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const saved = posts.find((p) => p.path === "/api/groups/save");
      assert.equal(saved.body.sink, true);
      assert.deepEqual(errors, []);
    });
  }
}
