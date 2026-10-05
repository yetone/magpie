// Run with Node's test runner and Playwright on the module path; see README.md.
// Weighted keys (#841): a key provider's Routing has By weight, and with it
// picked each key that's on shows its weight and its share of the
// requests beside it; a click edits the weight in place and posts
// keys/weight. Hidden under the other routings, and offered to key
// providers only. The page doesn't scroll. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const relay = () => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "big", name: "", on: true }], agents: [], fallback: [], headers: {}, routing: "",
  key: { set: true, masked: "sk-…one" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: [
    { id: "aaaaaaaaaa", name: "team-a", masked: "sk-…one", on: true, active: true, weight: 3 },
    { id: "bbbbbbbbbb", name: "team-b", masked: "sk-…two", on: true },
    { id: "cccccccccc", name: "", masked: "sk-…off", on: false },
  ],
});

function serve(lang, posts) {
  const p = relay();
  const providers = { providers: [p], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/keys/weight" && route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      posts.push(body);
      p.keyList = p.keyList.map((k) => k.id === body.ref ? { ...k, weight: body.weight } : k);
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { by: "By weight", a: "Weight 3 · 75%", b: "Weight 1 · 25%", b2: "Weight 2 · 40%", a2: "Weight 3 · 60%", note: "team-b weighs 2" },
  zh: { by: "按权重", a: "权重 3 · 75%", b: "权重 1 · 25%", b2: "权重 2 · 40%", a2: "权重 3 · 60%", note: "team-b 的权重已设为 2" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: By weight shows each key's weight and share, and a click sets it`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-key-weight.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Relay" }).first().click();
      await page.locator(".editor .accts .acc.add").waitFor();
      const scrolled = () => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
      const chip = (id) => page.locator(`.acc[data-account-id="${id}"] .key-weight`);

      // smart: no weights shown
      assert.equal(await chip("aaaaaaaaaa").isVisible(), false);
      const by = page.locator(".editor").getByRole("button", { name: w.by, exact: true });
      await by.scrollIntoViewIfNeeded();
      const sc = await scrolled();
      await by.click();
      assert.equal(await scrolled(), sc, "a click scrolled");
      assert.equal((await chip("aaaaaaaaaa").textContent()).trim(), w.a);
      assert.equal((await chip("bbbbbbbbbb").textContent()).trim(), w.b);
      assert.equal(await chip("aaaaaaaaaa").isVisible(), true);
      assert.equal(await chip("cccccccccc").count(), 0, "a key that's off has no share");

      // a click edits it in place, Enter saves it
      await chip("bbbbbbbbbb").click();
      const box = page.locator('.acc[data-account-id="bbbbbbbbbb"] .key-weight-in');
      await box.waitFor();
      assert.equal(await box.getAttribute("aria-label"), lang === "zh" ? "密钥权重" : "Key weight");
      assert.equal(await scrolled(), sc, "a click scrolled");
      await box.fill("2");
      await box.press("Enter");
      for (let i = 0; i < 60 && !posts.length; i++) await new Promise((r) => setTimeout(r, 50));
      assert.deepEqual(posts.at(-1), { id: "relay", ref: "bbbbbbbbbb", weight: 2 });
      await page.locator(`.acc[data-account-id="bbbbbbbbbb"] .key-weight`, { hasText: w.b2 }).waitFor();
      assert.equal((await chip("aaaaaaaaaa").textContent()).trim(), w.a2);
      // still shown: the routing picked stays in the editor's draft
      assert.equal(await chip("bbbbbbbbbb").isVisible(), true);
      assert.equal((await page.locator("#status").textContent()).trim(), w.note);

      if (process.env.ARTIFACT_DIR) await page.locator(".editor .accts").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-key-weight-rows.png`) });
      // back to Smart hides them
      await page.locator(".editor").getByRole("button", { name: lang === "en" ? "Smart" : "智能", exact: true }).first().click();
      assert.equal(await chip("aaaaaaaaaa").isVisible(), false);
      assert.deepEqual(errors, []);
    });
  }
}
