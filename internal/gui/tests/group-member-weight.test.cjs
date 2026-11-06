// Run with Node's test runner and Playwright on the module path; see README.md.
// A group may route By weight (#1910, DD on Discord): each model takes its
// share of the requests by the weight set beside it. In the group editor
// the routing's segments offer By weight; picked, every member shows its
// weight and its share of the requests among those on ("Weight 3 · 60%"),
// one switched off its weight alone. A click on it edits it in place:
// Enter keeps it, Escape puts it back, and one out of 1 to 1000 is refused
// with a warning. Nothing on the page moves. Saved, the group goes with
// routing "weight" and the weights above 1 of the members it still has. At
// Sort by weight puts the heaviest first once, and is saved so. At
// 420px the segments and the rows don't spill. English, Chinese and German,
// Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "OpenAI", icon: "generic", efforts: ["low", "medium", "high", "xhigh"] },
  { id: "rl/glm-5", name: "glm-5", providerName: "Relay", icon: "generic" },
  { id: "rl/o4-mini", name: "o4-mini", providerName: "Relay", icon: "generic" },
  { id: "rl/kimi-k2.6", name: "kimi-k2.6", providerName: "Relay", icon: "generic" },
];
const members = ["oa/gpt-6.1-sol:high", "rl/glm-5", "rl/o4-mini", "rl/kimi-k2.6"];
const groups = () => ({
  models, pools: [],
  groups: [{ id: "sol", name: "Sol", members, routing: "order", ready: true, off: ["rl/kimi-k2.6"],
    weights: { "oa/gpt-6.1-sol:high": 3, "rl/kimi-k2.6": 2 },
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

function serve(lang, posts) {
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
  for (const [lang, width] of [["en", 1100], ["zh", 1100], ["en", 420], ["de", 420]]) {
    test(`${engine} ${lang} ${width}: a group routes by its members' weights`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const height = 1400;
      const page = await (await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-group-member-weight.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const tr = (k, v = {}) => page.evaluate(([lang, k, v]) => (lang === "en" ? k : I18N[lang][k] || k).replace(/\{(\w+)\}/g, (_, n) => v[n]), [lang, k, v]);
      const card = page.locator(".rt-group", { hasText: "Sol" });
      await card.waitFor();
      await card.locator("button", { hasText: await tr("Edit") }).click();
      const ed = page.locator(".rt-gedit");
      const row = (name) => ed.locator(".fbrow", { has: page.locator(".n", { hasText: name }) });
      const weight = (name) => row(name).locator("button.rt-weight");
      await row("glm-5").waitFor();
      assert.equal(await ed.locator("button.rt-weight").count(), 0, "In order shows no weights");
      const sort = ed.locator("button.rt-wsort");
      assert.equal(await sort.isVisible(), false, "In order offers no sort by weight");

      const byWeight = ed.locator(".segs .opt", { hasText: await tr("By weight") });
      // what is clicked is wheeled into view by the test, as the reader
      // would, and the click itself moves nothing: its row stays under
      // the pointer (rows above it that grow are held by the view)
      const still = async (what, act, why) => {
        await reach(page, what);
        const row = what.locator("xpath=ancestor::*[contains(@class,'fbrow') or contains(@class,'segs')][1]");
        const top = async () => Math.round((await row.boundingBox()).y);
        const before = await top();
        await act();
        await page.waitForTimeout(200);
        assert.equal(await top(), before, why);
      };
      await still(byWeight, () => byWeight.click(), "picking By weight moved nothing");
      assert.equal(await byWeight.getAttribute("aria-pressed"), "true");
      assert(await ed.locator(".hint", { hasText: (await tr("By weight: each model takes its share of the requests by the weight set beside it — one weighing 3 three for every one a model weighing 1 takes, spread out rather than in bursts. One resting after a failure leaves the round and the others share its requests; the next model is the fallback when one fails.")).slice(0, 12) }).count(), "the hint says what By weight does");
      // 3 + 1 + 1 among those on; kimi is off and has its weight alone
      assert.equal(await weight("gpt-6.1-sol").textContent(), await tr("Weight {n} · {share}%", { n: 3, share: 60 }));
      assert.equal(await weight("glm-5").textContent(), await tr("Weight {n} · {share}%", { n: 1, share: 20 }));
      assert.equal(await weight("o4-mini").textContent(), await tr("Weight {n} · {share}%", { n: 1, share: 20 }));
      assert.equal(await weight("kimi-k2.6").textContent(), await tr("Weight {n}", { n: 2 }));

      // edited in place: Enter keeps it
      const input = row("glm-5").locator("input.rt-weight-in");
      await still(weight("glm-5"), async () => {
        await weight("glm-5").click();
        assert.equal(await input.getAttribute("aria-label"), await tr("Weight of {name}", { name: "glm-5" }));
        await input.fill("6");
        await input.press("Enter");
      }, "editing a weight moved nothing");
      assert.equal(await weight("glm-5").textContent(), await tr("Weight {n} · {share}%", { n: 6, share: 60 }));
      assert.equal(await weight("gpt-6.1-sol").textContent(), await tr("Weight {n} · {share}%", { n: 3, share: 30 }));
      // Escape puts it back; out of 1 to 1000 is refused, with a warning
      await weight("o4-mini").click();
      await row("o4-mini").locator("input.rt-weight-in").fill("9");
      await row("o4-mini").locator("input.rt-weight-in").press("Escape");
      assert.equal(await weight("o4-mini").textContent(), await tr("Weight {n} · {share}%", { n: 1, share: 10 }));
      await weight("o4-mini").click();
      await row("o4-mini").locator("input.rt-weight-in").fill("1001");
      await row("o4-mini").locator("input.rt-weight-in").press("Enter");
      assert.equal(await weight("o4-mini").textContent(), await tr("Weight {n} · {share}%", { n: 1, share: 10 }));
      assert(await page.locator("body", { hasText: await tr("A weight is 1 to 1000") }).count(), "the refusal is said");

      // Sort by weight (Norcleeh on Discord) puts the heaviest first once,
      // equal weights in their order, one switched off too; then it goes,
      // since nothing is left to move
      const order = async () => {
        const names = await ed.locator(".fbl").first().locator(":scope > .fbrow .n > span:first-child").allTextContents();
        return names.map((x) => ["gpt-6.1-sol", "glm-5", "o4-mini", "kimi-k2.6"].find((n) => x.includes(n)));
      };
      assert.deepEqual(await order(), ["gpt-6.1-sol", "glm-5", "o4-mini", "kimi-k2.6"]);
      assert.equal(await sort.textContent(), await tr("Sort by weight"));
      await reach(page, sort);
      assert(await page.evaluate(() => document.querySelector(".rt-wsort").getBoundingClientRect().right <= document.querySelector(".rt-gedit").getBoundingClientRect().right + 1), "Sort by weight fits at " + width);
      await sort.click();
      assert.deepEqual(await order(), ["glm-5", "gpt-6.1-sol", "kimi-k2.6", "o4-mini"], "heaviest first");
      assert.equal(await sort.isVisible(), false, "sorted, it has nothing to do");
      assert.equal(await weight("glm-5").textContent(), await tr("Weight {n} · {share}%", { n: 6, share: 60 }), "each keeps its weight");

      // nothing spills, and no left-border accent
      const spill = await page.evaluate(() => {
        const ed = document.querySelector(".rt-gedit").getBoundingClientRect();
        const out = [...document.querySelectorAll(".rt-gedit .segs, .rt-gedit .rt-weight")].filter((e) => e.getBoundingClientRect().right > ed.right + 1).map((e) => e.className + ":" + e.textContent);
        if (document.scrollingElement.scrollWidth > innerWidth) out.push("page " + document.scrollingElement.scrollWidth);
        return out;
      });
      assert.deepEqual(spill, [], "nothing spills at " + width);
      const border = await page.evaluate(() => [...document.querySelectorAll(".rt-weight")].map((e) => getComputedStyle(e))
        .filter((c) => c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor));
      assert.equal(border.length, 0, "no left-border accent");

      const b = ed.locator("button.primary", { hasText: await tr("Save") });
      await page.mouse.move(width / 2, 600);
      for (let i = 0; i < 20 && (await b.boundingBox()).y > height - 160; i++) {
        await page.mouse.wheel(0, 300);
        await page.waitForTimeout(150);
      }
      const bb = await b.boundingBox();
      await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const sent = posts.find((p) => p.path === "/api/groups/save");
      assert.equal(sent.body.routing, "weight");
      assert.deepEqual(sent.body.weights, { "oa/gpt-6.1-sol:high": 3, "rl/glm-5": 6, "rl/kimi-k2.6": 2 });
      assert.deepEqual(sent.body.members, ["rl/glm-5", "oa/gpt-6.1-sol:high", "rl/kimi-k2.6", "rl/o4-mini"], "the sorted order is saved");

      const missing = await page.evaluate(() => [
        "By weight", "Sort by weight", "Heaviest first. When a model fails, the next one in this order takes over", "Weight {n}", "Weight {n} · {share}%", "Weight of {name}", "A weight is 1 to 1000",
        "Its share of the requests: a model weighing 3 takes three for every one a model weighing 1 takes",
        "By weight: each model takes its share of the requests by the weight set beside it — one weighing 3 three for every one a model weighing 1 takes, spread out rather than in bursts. One resting after a failure leaves the round and the others share its requests; the next model is the fallback when one fails.",
      ].filter((k) => !I18N.zh[k] || !I18N["zh-TW"][k] || !I18N.ja[k] || !I18N.de[k]));
      assert.deepEqual(missing, [], "every string has its translations");
      assert.deepEqual(errors, []);
    });
  }
}
