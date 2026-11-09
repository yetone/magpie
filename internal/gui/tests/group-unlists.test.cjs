// Run with Node's test runner and Playwright on the module path; see README.md.
// #1783 (cybershape): a group can keep its models out of the lists agents
// and the gateway's /v1/models show, so a client lists the group alone.
// The group editor's "Model lists": "Its models too", the default, or "The
// group alone", sent with the Save as unlists; the group's card says so.
// No native select, a click never scrolls the page, and every language has
// its words. Wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
// the rows a group's editor folds under Advanced, opened
const openAdvanced = async (page) => {
  const d = page.locator(".rt-gedit .rt-gadv");
  await d.waitFor({ state: "attached" });
  if (!(await d.evaluate((e) => e.open))) await d.locator(":scope > summary").click();
};

const assets = path.resolve(__dirname, "../assets");
const members = ["a/m1", "a/m2"];
const groups = (unlists) => ({
  models: members.map((id) => ({ id, name: id.slice(2), providerName: "A", icon: "generic" })),
  pools: [],
  groups: [{ id: "g", name: "Glim", members, fast: [], off: [], routing: "", ready: true, ...(unlists ? { unlists: true } : {}), memberInfo: members.map((id) => ({ id, ready: true })) }],
});
const prov = { icon: "generic", catalog: "", models: [{ id: "m1", name: "m1", on: true }, { id: "m2", name: "m2", on: true }], agents: [], fallback: [], headers: {}, routing: "order", affinity: "", ready: true, id: "a", name: "A", chat: "http://127.0.0.1:1/v1", responses: "", anthropic: "", keyList: [] };

function serve(lang, posts) {
  let unlists = false;
  const state = { agents: [{ id: "pi", name: "Pi", path: "/test/pi", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  const providers = { providers: [prov], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups(unlists));
    if (url.pathname === "/api/providers") return json(providers);
    if (r.request().method() === "POST" && url.pathname.startsWith("/api/groups/")) {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push({ path: url.pathname, body });
      if (url.pathname === "/api/groups/save") unlists = !!body.unlists;
      return json(groups(unlists));
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const H0 = "Agents' model lists and the gateway's /v1/models show the group and each of its models.";
const H1 = "Agents' model lists and the gateway's /v1/models show the group, not the models it names or its patterns match: they are used through it. Codex keeps its ChatGPT sign-in's own models. A request naming one is still answered.";
const KEYS = ["Model lists", "Its models too", "The group alone", "Its models unlisted", H0, H1, "Edit", "Save"];
const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      const launch = () => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" });
      test(`${engine} ${lang} ${width}px: a group lists itself alone, made with its Save`, async (t) => {
        const browser = await launch();
        t.after(() => browser.close());
        const height = 1000;
        const page = await (await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=groups");
        const card = page.locator(".rt-group", { hasText: "Glim" });
        await card.waitFor();
        const w = await page.evaluate(([keys, lang]) => Object.fromEntries(keys.map((k) => [k, lang === "en" ? k : I18N[lang][k]])), [KEYS, lang]);
        assert.deepEqual(Object.entries(w).filter(([, v]) => !v).map(([k]) => k), [], "every string has its words");
        assert.equal(await card.locator(".tag.unlists").count(), 0, "listed by default: no tag");

        const ed = page.locator(".rt-gedit");
        const label = ed.locator("label", { hasText: new RegExp(`^${esc(w["Model lists"])}$`) });
        const field = label.locator("xpath=following-sibling::div[1]");
        const opt = (k) => field.locator(".list-pick .opt", { hasText: new RegExp(`^${esc(w[k])}$`) });
        const save = ed.locator("button.primary", { hasText: w.Save });
        const saveIt = async () => {
          await page.mouse.move(width / 2, 500); // the reader scrolls to it
          for (let i = 0; i < 20 && (await save.boundingBox()).y > height - 160; i++) {
            await page.mouse.wheel(0, 300);
            await page.waitForTimeout(150);
          }
          const bb = await save.boundingBox();
          await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
          await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        };
        const toOpt = async (k) => {
          await page.mouse.move(width / 2, 500);
          for (let i = 0; i < 20 && (await opt(k).boundingBox()).y > height - 160; i++) {
            await page.mouse.wheel(0, 300);
            await page.waitForTimeout(150);
          }
        };

        await card.locator("button", { hasText: w.Edit }).click();
        await openAdvanced(page);
        await label.waitFor();
        assert.match(await opt("Its models too").getAttribute("class"), /\bon\b/, "its models too by default");
        assert.equal(await field.locator(".hint").last().textContent(), w[H0]);
        await toOpt("The group alone");
        const box = await opt("The group alone").boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= width, `the option fits the width: ${JSON.stringify(box)}`);
        const y = await page.evaluate(() => document.scrollingElement.scrollTop);
        await opt("The group alone").click();
        assert.match(await opt("The group alone").getAttribute("class"), /\bon\b/);
        assert.equal(await field.locator(".hint").last().textContent(), w[H1]);
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y, "a click never scrolls the page");
        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.deepEqual(posts, [], "nothing is sent before the Save");
        await saveIt();
        assert.equal(posts.find((p) => p.path === "/api/groups/save")?.body.unlists, true);
        const tag = card.locator(".tag.unlists");
        await tag.waitFor();
        assert.equal((await tag.textContent()).trim(), w["Its models unlisted"]);
        assert.equal(await tag.getAttribute("title"), w[H1]);

        // and back: its models listed again
        await card.locator("button", { hasText: w.Edit }).click();
        await label.waitFor();
        assert.match(await opt("The group alone").getAttribute("class"), /\bon\b/, "the editor opens with what was saved");
        await toOpt("Its models too");
        await opt("Its models too").click();
        await saveIt();
        assert.equal(posts.filter((p) => p.path === "/api/groups/save").at(-1).body.unlists, false);
        await page.waitForFunction(() => !document.querySelector(".rt-group .tag.unlists"));
        assert.deepEqual(errors, []);
      });
    }
  }
}
