// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel's profiles open from Profiles at the Agents tab's foot, on
// its left, over the list and upward from it (the user: tray window 中方案不
// 应该有一个单独的 tab，在 Agents tab 左下角有个方案即可): no Profiles tab; the
// button shows how many there are, only on the Agents tab; a click outside,
// Escape, another tab or a profile applied closes them.
// Saving the current setup as a profile there:
// "＋ Save current", with the list scrolled to its end, opens a name field
// beside it without moving the list (the field at the list's head drew it
// up under the tabs, the field's top cut off) and with one ring, not the
// page's focus ring over its own; field and button stay in sight, and the
// button then reads Save; a click on it saves as Enter does (it did nothing:
// the field lost focus to it and went). Escape closes the field and gives the
// button back. With magpie slow to answer (reading every agent again took it
// seconds) and no profiles yet, the chip saved is there at once, dimmed till
// the answer, under the tabs' line and not scrolled up behind them (the
// click held the button, which the chip came in above); ×, clicked twice,
// takes a chip away at once; a save refused puts the list back and says why. No backend: the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = ["model-a", "model-b"].map((m) => ({ value: m, label: m }));
const agent = (id) => ({
  id, name: id, path: "/test/" + id, fields: [
    { key: "model", label: "model", value: "model-a", options: models },
  ],
});
const profile = (name) => ({ name, summary: "" });

function server(lang, saved, { count = 30, delay = 0, refuse = "" } = {}) {
  let profiles = Array.from({ length: count }, (_, i) => profile("profile-" + i));
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const state = () => ({ agents: [agent("codex"), agent("claude")], profiles, settings: { lang, theme: "light" } });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: state() });
    if (url.pathname === "/api/profile/save") {
      const { name } = req.postDataJSON();
      await new Promise((r) => setTimeout(r, delay));
      if (name === refuse) return route.fulfill({ status: 400, json: { error: "can't write profiles.json" } });
      saved.push(name);
      profiles = [...profiles, profile(name)];
      return route.fulfill({ json: state() });
    }
    if (url.pathname === "/api/profile/delete") {
      const { name } = req.postDataJSON();
      await new Promise((r) => setTimeout(r, delay));
      profiles = profiles.filter((p) => p.name !== name);
      return route.fulfill({ json: state() });
    }
    if (url.pathname === "/api/profile/use") return route.fulfill({ json: { ...state(), changed: 1 } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": saving a profile in the tray panel", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-panel-profiles-${i}.png`) });
      }
      await browser.close();
    });
    const open = async (lang, saved, opts) => {
      const page = await (await browser.newContext({ viewport: { width: 440, height: 420 } })).newPage();
      pages.push(page);
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, saved, opts));
      await page.goto("http://magpie.test/?mode=panel");
      await page.locator("#profiles .chip, #profiles .hint").first().waitFor({ state: "attached" });
      assert.equal(await page.locator('[data-ptab="profiles"]').count(), 0, "no Profiles tab");
      await page.locator("#profBtn").click();
      await page.locator(".profiles.open").waitFor();
      await page.waitForTimeout(450);
      return page;
    };
    const view = (page) => page.locator(".profiles").evaluate((v) => v.scrollTop);

    for (const [lang, add, save] of [["en", "＋ Save current", "Save"], ["zh", "＋ 保存当前", "保存"]]) {
      await t.test(lang, async () => {
        const saved = [];
        const page = await open(lang, saved);
        const button = page.locator("#save");
        assert.equal((await button.textContent()).trim(), add);

        // over the list, upward from the button at its foot's left, the list
        // left where it was
        const pos = await page.evaluate(() => {
          const p = document.querySelector(".profiles").getBoundingClientRect(), b = document.querySelector("#profBtn").getBoundingClientRect();
          const f = document.querySelector(".foot").getBoundingClientRect(), tabs = document.querySelector("#ptabs").getBoundingClientRect();
          return { above: p.bottom <= b.top, near: b.top - p.bottom < 12, left: Math.abs(p.left - b.left) < 16 && b.left - f.left < 20, under: p.top >= tabs.bottom, count: document.querySelector("#profBtn").textContent, agents: getComputedStyle(document.querySelector("#agents")).display };
        });
        assert(pos.above && pos.near && pos.left && pos.under, JSON.stringify(pos));
        assert.match(pos.count, new RegExp(`${lang === "zh" ? "方案" : "Profiles"}\\s*30`));
        assert.notEqual(pos.agents, "none", "the agents stay under them");
        // the reader scrolls to the button; the field opens beside it, whole,
        // with one ring, and nothing moves
        const box = await page.locator(".profiles").boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + 60);
        for (let i = 0; i < 20; i++) { await page.mouse.wheel(0, 40); await page.waitForTimeout(20); }
        await page.waitForTimeout(400);
        const before = await view(page);
        assert(before > 0, "the list must be long enough to scroll");
        await button.click();
        const field = page.locator(".profiles > .chip-input");
        await field.waitFor();
        await page.waitForTimeout(300);
        assert.equal(await view(page), before, "opening the field must not scroll the list");
        const cut = await page.evaluate(() => {
          const v = document.querySelector(".profiles").getBoundingClientRect(), f = document.querySelector(".profiles > .chip-input");
          const inView = (e) => { const r = e.getBoundingClientRect(); return r.top >= v.top + 1 && r.bottom <= v.bottom; };
          return { field: inView(f), button: inView(document.querySelector("#save")), outline: getComputedStyle(f).outlineStyle, focused: document.activeElement === f };
        });
        assert(cut.focused, "the field must have focus");
        assert(cut.field, "the field must be whole in the view");
        assert(cut.button, "the button must stay in the view");
        assert.equal(cut.outline, "none", "the field's border is its focus; no ring over it");
        assert.equal((await button.textContent()).trim(), save, "with the field open the button saves");

        // a click on it saves, and the button is ＋ Save current again
        await field.pressSequentially("from click");
        await button.click();
        await page.locator('#profiles .chip:has-text("from click")').waitFor();
        assert.deepEqual(saved, ["from click"]);
        assert.equal(await page.locator(".profiles > .chip-input").count(), 0);
        assert.equal((await button.textContent()).trim(), add);

        // an empty field isn't saved; the click keeps it open, focused
        await button.click();
        await field.waitFor();
        await button.click();
        await page.waitForTimeout(250);
        assert.equal(await field.count(), 1, "an empty name keeps the field open");
        assert(await field.evaluate((f) => document.activeElement === f));
        assert.deepEqual(saved, ["from click"]);

        // Enter still saves; Escape closes and gives the button back
        await field.pressSequentially("from enter");
        await field.press("Enter");
        await page.locator('#profiles .chip:has-text("from enter")').waitFor();
        assert.deepEqual(saved, ["from click", "from enter"]);
        await button.click();
        await field.waitFor();
        await field.press("Escape");
        assert.equal(await field.count(), 0);
        assert.equal((await button.textContent()).trim(), add);
        assert(await page.locator(".profiles.open").isVisible(), "Escape in the field closes the field alone");

        // Escape closes them, the button opens them again, a click outside
        // closes them, and so does another tab (where the button isn't)
        await page.keyboard.press("Escape");
        assert.equal(await page.locator(".profiles.open").count(), 0);
        await page.locator("#profBtn").click();
        await page.locator(".profiles.open").waitFor();
        await page.mouse.click(420, 150);
        assert.equal(await page.locator(".profiles.open").count(), 0);
        await page.locator("#profBtn").click();
        await page.locator('[data-ptab="routing"]').click();
        assert.equal(await page.locator(".profiles.open").count(), 0);
        assert(!(await page.locator("#profBtn").isVisible()), "Profiles is the Agents tab's");
        await page.locator('[data-ptab="agents"]').click();
        assert(await page.locator("#profBtn").isVisible());

        // a profile applied closes them, the agents in sight
        await page.locator("#profBtn").click();
        await page.locator('#profiles .chip:has-text("profile-1")').first().click();
        await page.locator(".prof-detail .pd-apply").click();
        await page.waitForFunction(() => !document.querySelector(".profiles.open"));
      });

      await t.test(lang + ": a slow magpie, from none", async () => {
        const saved = [];
        const page = await open(lang, saved, { count: 0, delay: 1500, refuse: "refused" });
        const button = page.locator("#save"), field = page.locator(".profiles > .chip-input");
        const chip = (name) => page.locator("#profiles .chip", { hasText: name });
        const where = () => page.evaluate(() => {
          const v = document.querySelector(".profiles"), tabs = document.querySelector("#ptabs").getBoundingClientRect();
          const c = document.querySelector("#profiles .chip");
          return { scroll: v.scrollTop, below: !c || c.getBoundingClientRect().top >= tabs.bottom, status: document.querySelector("#status").textContent };
        });

        await button.click();
        await field.pressSequentially("hi");
        await button.click();
        await page.waitForTimeout(200);
        assert.equal(await chip("hi").count(), 1, "the chip saved is there before magpie answers");
        assert(await chip("hi").evaluate((c) => c.classList.contains("pending")), "and dimmed till it does");
        assert.equal(await field.count(), 0, "the field closes at once");
        assert.equal((await button.textContent()).trim(), add);
        await page.locator("#profiles .chip:not(.pending)").waitFor();
        await page.waitForTimeout(300);
        let w = await where();
        assert.equal(w.scroll, 0, "saving must not scroll the list");
        assert(w.below, "the chip saved must not be under the tabs");
        assert.match(w.status, /hi/);
        assert.deepEqual(saved, ["hi"]);

        // × (clicked twice, #478) takes it away at once
        await chip("hi").hover();
        await chip("hi").locator(".x").nth(1).click();
        await chip("hi").locator(".x.arm").click();
        await page.waitForTimeout(200);
        assert.equal(await chip("hi").count(), 0, "the chip deleted goes before magpie answers");
        await page.locator("#profiles .hint").waitFor({ state: "attached" });
        await page.waitForFunction(() => /hi/.test(document.querySelector("#status").textContent) && !document.querySelector("#status").classList.contains("ok"));
        assert.equal(await chip("hi").count(), 0);

        // a save refused: the chip goes again and the reason is shown
        await button.click();
        await field.pressSequentially("refused");
        await field.press("Enter");
        await page.waitForTimeout(200);
        assert.equal(await chip("refused").count(), 1);
        await page.waitForFunction(() => document.querySelector("#status").classList.contains("err"));
        assert.equal(await chip("refused").count(), 0, "a save refused is taken off the list");
        assert.match((await where()).status, /profiles\.json/);
        assert.equal((await where()).scroll, 0);
      });
    }
    assert.deepEqual(errors, []);
  });
}
