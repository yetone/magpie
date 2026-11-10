// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider on a plan and one paid as used are told apart (EV_John_Liu
// on X): a quiet grey badge on its Providers row (Plan, Pay as you go, or
// both for keys on each), none where magpie can't tell; the editor's
// Billing under Advanced sets it by the user's word, Auto handing it back;
// and a routing group's card sums up its members' billing, each member's
// on hover. In every language, in both engines, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "m", name: "M", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…1234" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false, unredacted: false,
};
const glm = { ...base, id: "zhipu", name: "Zhipu GLM", preset: "zhipu", chat: "https://open.bigmodel.cn/api/coding/paas/v4", billingKind: "plan" };
const relay = { ...base, id: "relay", name: "Some Relay", chat: "https://relay.example.com/v1", billingKind: "payg", billing: "payg" };
const volc = { ...base, id: "volc", name: "Volcengine", preset: "volcengine", chat: "https://ark.cn-beijing.volces.com/api/coding/v3", billingKind: "mixed" };
const own = { ...base, id: "own", name: "Own Server", chat: "http://192.168.1.5:8000/v1" };

const models = [
  { id: "zhipu/glm-5", name: "glm-5", providerName: "Zhipu GLM", icon: "generic" },
  { id: "relay/glm-5", name: "glm-5", providerName: "Some Relay", icon: "generic" },
  { id: "own/glm-5", name: "glm-5", providerName: "Own Server", icon: "generic" },
];
const member = (id, name, billing) => ({ id, ready: true, provider: id.split("/")[0], name, model: "glm-5", on: 1, ...(billing ? { billing } : {}) });
const groups = [
  { id: "both", name: "Both", members: ["zhipu/glm-5", "relay/glm-5"], ready: true, memberInfo: [member("zhipu/glm-5", "Zhipu GLM", "plan"), member("relay/glm-5", "Some Relay", "payg")] },
  { id: "plans", name: "Plans", members: ["zhipu/glm-5"], ready: true, memberInfo: [member("zhipu/glm-5", "Zhipu GLM", "plan")] },
  { id: "unsure", name: "Unsure", members: ["zhipu/glm-5", "own/glm-5"], ready: true, memberInfo: [member("zhipu/glm-5", "Zhipu GLM", "plan"), member("own/glm-5", "Own Server", "")] },
];

function serve(lang, posts) {
  const providers = { providers: [glm, relay, volc, own], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models, pools: [], deciders: [], found: true, groups });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    if (!body) return route.fulfill({ status: 404, body: "" });
    await route.fulfill({ body, contentType });
  };
}

const STRINGS = ["Billing", "Plan", "Pay as you go", "Plan + pay as you go", "Auto"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [900, 440]) {
      const open = async (t, view, name) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}-billing.png`), fullPage: true });
          }
          await browser.close();
        });
        page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto(`http://magpie.test/?view=${view}`);
        const w = await page.evaluate((ks) => Object.fromEntries(ks.map((k) => [k, t(k)])), STRINGS);
        return { page, errors, posts, w };
      };
      const badge = (page, name) => page.locator(".row.provider", { hasText: name }).locator(".name .badge.bill");
      const adv = (page) => page.locator(".editor details.advanced");
      const seg = (page) => page.locator(".editor .segs.billing");
      const save = async (page, posts) => {
        const n = posts.length;
        const label = await page.evaluate(() => t("Save"));
        await page.locator(".editor .bar").getByRole("button", { name: label, exact: true }).click();
        for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
        return posts.at(-1);
      };

      test(`${engine} ${lang} ${width}: each provider says how it bills, quietly`, async (t) => {
        const { page, errors, posts, w } = await open(t, "providers", "rows");
        await page.locator(".row.provider", { hasText: "Zhipu GLM" }).waitFor();
        assert.equal(await badge(page, "Zhipu GLM").textContent(), w["Plan"]);
        assert.equal(await badge(page, "Some Relay").textContent(), w["Pay as you go"]);
        assert.equal(await badge(page, "Volcengine").textContent(), w["Plan + pay as you go"]);
        assert.equal(await badge(page, "Own Server").count(), 0, "no badge where magpie can't tell");
        // grey, not an accent: the muted colour on the pill
        const look = await badge(page, "Zhipu GLM").evaluate((b) => {
          const s = getComputedStyle(b), probe = document.createElement("span");
          probe.style.color = "var(--muted)";
          document.body.append(probe);
          const muted = getComputedStyle(probe).color;
          probe.remove();
          return { color: s.color, muted, image: s.backgroundImage };
        });
        assert.equal(look.color, look.muted);
        assert.equal(look.image, "none");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
        if (lang !== "en") {
          const missing = await page.evaluate(([l, ks]) => ks.filter((k) => !I18N[l][k]), [lang, STRINGS]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.deepEqual(posts, []);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: Billing is set in the editor, and Auto hands it back`, async (t) => {
        const { page, errors, posts, w } = await open(t, "providers", "editor");
        await page.locator(".row.provider", { hasText: "Own Server" }).click();
        await page.locator(".editor .bar").waitFor();
        assert.equal(await adv(page).evaluate((d) => d.open), false, "nothing set opens Advanced");
        await adv(page).locator(":scope > summary").click();
        await seg(page).scrollIntoViewIfNeeded();
        assert.equal(await seg(page).locator(".opt.on").textContent(), w["Auto"]);
        await seg(page).getByRole("button", { name: w["Plan"], exact: true }).click();
        let saved = await save(page, posts);
        assert.equal(saved.action, "save");
        assert.equal(saved.body.id, "own");
        assert.equal(saved.body.billing, "plan");

        await page.locator(".row.provider", { hasText: "Some Relay" }).click();
        await page.locator(".editor .bar").waitFor();
        await page.waitForFunction(() => document.querySelector(".editor details.advanced")?.open);
        await seg(page).scrollIntoViewIfNeeded();
        assert.equal(await seg(page).locator(".opt.on").textContent(), w["Pay as you go"], "opens as saved");
        const r = await seg(page).boundingBox();
        assert.ok(r && r.x >= 0 && r.x + r.width <= width, `fits: ${JSON.stringify(r)}`);
        await seg(page).getByRole("button", { name: w["Auto"], exact: true }).click();
        saved = await save(page, posts);
        assert.equal(saved.body.id, "relay");
        assert.equal(saved.body.billing, "");
        assert.deepEqual(errors, []);
      });

      if (width === 900) test(`${engine} ${lang}: a routing group sums up its members' billing`, async (t) => {
        const { page, errors, w } = await open(t, "groups", "routing");
        const card = (name) => page.locator(".rt-groups .rt-group", { hasText: name });
        await card("Both").waitFor();
        const tag = (name) => card(name).locator(".tag.bill");
        assert.equal(await tag("Both").textContent(), w["Plan + pay as you go"]);
        const title = await tag("Both").getAttribute("title");
        assert.ok(title.includes(w["Plan"]) && title.includes(w["Pay as you go"]), title);
        assert.equal(await tag("Plans").textContent(), w["Plan"]);
        assert.equal(await tag("Unsure").count(), 0, "one member unknown: said nowhere");
        assert.deepEqual(errors, []);
      });
    }
  }
}
