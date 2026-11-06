// Run with Node's test runner and Playwright on the module path; see README.md.
// PAMI on Discord: a routing group could not be switched off as a whole,
// and a group was made one model at a time, not seeing which groups a
// model was in already.
// - A group of the user's has a switch on its card: off, it posts
//   groups/switch with on false, the card's logos go grey and it
//   says "switched off", and the keyboard stays on the switch; on again
//   posts on true. A group magpie found has no switch (it is removed).
// - justacati on X: a group switched off is listed after the ones on, and
//   back in its place when it is on again; Move down on the last one on
//   is not offered, as it would move it nowhere one can see.
// - The editor's Add a model picker stays open: each model clicked is added
//   and ticked, clicked again it is taken out; each says the other groups
//   it is in. Saved, the members are the ones picked, in the order picked.
// No click moves the page. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/one", name: "one", providerName: "A", icon: "generic" },
  { id: "a/two", name: "two", providerName: "A", icon: "generic" },
  { id: "b/three", name: "three", providerName: "B", icon: "generic" },
];

const words = {
  en: { edit: "Edit", save: "Save", add: "Add another model", off: "switched off", in: "in Other", offSaid: "G switched off", onSaid: "G switched on" },
  zh: { edit: "编辑", save: "保存", add: "再添加一个模型", off: "已关闭", in: "已在 Other", offSaid: "已关闭 G", onSaid: "已打开 G" },
};

function serve(lang, posts) {
  let disabled = false;
  const groups = () => ({
    models, pools: [],
    groups: [
      { id: "g", name: "G", members: ["a/one"], off: [], routing: "order", ready: true, disabled, memberInfo: [{ id: "a/one", ready: true }] },
      { id: "other", name: "Other", members: ["b/three:high"], off: [], routing: "order", ready: true, memberInfo: [{ id: "b/three:high", ready: true }] },
      { id: "auto-one", name: "Found one", members: ["a/one"], auto: true, ready: true, memberInfo: [{ id: "a/one", ready: true }] },
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
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push({ path: url.pathname, body });
      if (url.pathname === "/api/groups/switch") disabled = !body.on;
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
    test(`${engine} ${lang}: a group switched off, and models added several at a time`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      const card = (id) => page.locator(`.rt-group[data-id="${id}"]`);
      await card("g").waitFor();
      await page.locator(".rt-gsec").evaluate((x) => x.scrollIntoView({ block: "center" })); // as the reader would
      await page.waitForTimeout(200);
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";"));
      const before = await at();

      // the switch: on a group of the user's, none on a found one
      assert.equal(await card("auto-one").locator(".rt-gon").count(), 0, "a found group has a switch");
      const sw = () => card("g").locator("button.rt-gon");
      assert.equal(await sw().getAttribute("aria-checked"), "true");
      await sw().focus();
      await page.keyboard.press("Space");
      await page.waitForFunction(() => document.querySelector('.rt-group[data-id="g"]')?.classList.contains("disabled"));
      assert.deepEqual(posts.at(-1), { path: "/api/groups/switch", body: { id: "g", on: false } });
      assert.equal(await sw().getAttribute("aria-checked"), "false");
      assert((await card("g").locator(".tags").textContent()).includes(w.off), "the card says it is off");
      await page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), w.offSaid);
      assert.equal(await page.evaluate(() => document.activeElement?.classList.contains("rt-gon")), true, "the keyboard left the switch");
      const order = () => page.locator(".rt-group[data-id]").evaluateAll((rs) => rs.map((r) => r.dataset.id));
      assert.deepEqual(await order(), ["other", "auto-one", "g"], "the group switched off is not listed after the ones on");
      // the last one on can't be moved down into the ones off
      await card("auto-one").locator(".rt-ghandle").click();
      const down = page.locator(".menu-item, [role=menuitem]").filter({ hasText: lang === "en" ? "Move down" : "下移" });
      await down.first().waitFor();
      assert.equal(await down.first().evaluate((b) => b.disabled || b.getAttribute("aria-disabled") === "true" || b.classList.contains("off")), true, "Move down offered on the last group on");
      await page.keyboard.press("Escape");
      await sw().focus();
      // off, its logos are grey and its words stay readable, not dimmed
      // (jiabubu on Discord; group-disabled-contrast measures them)
      const dim = await card("g").evaluate((r) => [...r.children].map((c) => [c.classList.contains("rt-ghandle"), getComputedStyle(c).opacity, getComputedStyle(c).filter]));
      for (const [isLogo, o, f] of dim) assert.deepEqual([o, f], isLogo ? ["0.6", "grayscale(1)"] : ["1", "none"]);
      assert.equal(await page.locator(".rt-gedit").count(), 0, "the switch opened the editor");
      await sw().click();
      await page.waitForFunction(() => !document.querySelector('.rt-group[data-id="g"]')?.classList.contains("disabled"));
      assert.deepEqual(posts.at(-1), { path: "/api/groups/switch", body: { id: "g", on: true } });
      assert.deepEqual(await order(), ["g", "other", "auto-one"], "switched on again, the group is not back in its place");
      assert.equal(await at(), before, "the switch moved the page");

      // Add a model: several in one go
      await card("g").locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      await ed.locator("button.rt-gadd", { hasText: w.add }).click();
      const pop = page.locator("#pop");
      await pop.locator("li", { hasText: "two" }).first().waitFor();
      const opt = (name) => pop.locator("li", { has: page.locator(".v", { hasText: new RegExp(`^${name}$`) }) }).first();
      assert.equal(await opt("one").evaluate((li) => li.classList.contains("cur")), true, "the member in it is not ticked");
      assert((await opt("three").textContent()).includes(w.in), "the picker doesn't say which group three is in");
      await opt("two").click();
      await opt("three").click();
      assert.equal(await pop.isHidden(), false, "the picker closed after one");
      assert.equal(await opt("two").evaluate((li) => li.classList.contains("cur")), true, "two is not ticked");
      const names = () => ed.locator(".fbl").first().locator(".fbrow .n > span:first-child").allTextContents();
      assert.deepEqual(await names(), ["one", "two", "three"]);
      // clicked again, taken out
      await opt("two").click();
      assert.deepEqual(await names(), ["one", "three"]);
      assert.equal(await opt("two").evaluate((li) => li.classList.contains("cur")), false);
      await page.keyboard.press("Escape");
      await pop.waitFor({ state: "hidden" });

      const b = ed.locator("button.primary", { hasText: w.save });
      await page.mouse.move(550, 600);
      for (let i = 0; i < 10 && (await b.boundingBox()).y > 1400 - 160; i++) {
        await page.mouse.wheel(0, 300);
        await page.waitForTimeout(150);
      }
      const bb = await b.boundingBox();
      await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const sent = posts.find((p) => p.path === "/api/groups/save");
      assert.deepEqual(sent.body.members, ["a/one", "b/three"]);

      const missing = await page.evaluate(() => [
        "in {groups}", "switched off", "{name} switched on", "{name} switched off",
        "Off: agents aren't offered {name} and a request to it is turned away. Click to switch it on",
        "On: agents may pick {name}. Click to switch it off and keep it as it is",
      ].filter((k) => !I18N.zh[k] || !I18N.ja[k] || !I18N.de[k]));
      assert.deepEqual(missing, [], "every string has its Chinese, Japanese and German");
      assert.deepEqual(errors, []);
    });
  }
}
