// Run with Node's test runner and Playwright on the module path; see README.md.
// A profile chip's ↻ and × act on a second click, and its details close as
// they should (#478 emo172: ↻ overwrote and × deleted a profile on the first
// click; the details closed only by clicking the chip again, and Escape on
// them in the tray panel hid the whole panel; closed by hand they came back
// with the list, closed by Apply they didn't). A first click on ↻ or × asks
// ("Overwrite?", "Delete?") and changes nothing; a second does it; left
// alone, or on Escape, it goes back. The details have a ×, which closes them,
// the chip staying where it is; Escape closes the details, then the panel's
// list, and only then hides the panel; the list opens again on the chips
// alone. In the window and in the tray panel, in English and Chinese. No
// backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = ["model-a", "model-b"].map((m) => ({ value: m, label: m }));
const agent = (id, name) => ({ id, name, path: "/test/" + id, fields: [{ key: "model", label: "model", value: "model-a", options: models }] });
const profile = (name) => ({ name, summary: "", agents: [{ id: "codex", name: "Codex", fields: [{ key: "model", label: "model", value: "gpt-5" }] }] });

function server(lang, calls) {
  let profiles = ["home", "work"].map(profile);
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const state = () => ({ agents: [agent("codex", "Codex")], profiles, settings: { lang, theme: "light" } });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: state() });
    if (url.pathname === "/api/window/hide") { calls.push("hide"); return route.fulfill({ json: {} }); }
    if (url.pathname.startsWith("/api/profile/")) {
      const action = url.pathname.slice("/api/profile/".length), { name } = req.postDataJSON();
      calls.push(action + " " + name);
      if (action === "delete") profiles = profiles.filter((p) => p.name !== name);
      return route.fulfill({ json: { ...state(), changed: 1 } });
    }
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
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

const words = {
  en: { overwrite: "Overwrite?", del: "Delete?", close: "Close" },
  zh: { overwrite: "覆盖？", del: "删除？", close: "关闭" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a profile's ↻ and × ask first, and its details close", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-profile-confirm-${i}.png`) });
      }
      await browser.close();
    });

    for (const lang of ["en", "zh"]) for (const panel of [false, true]) {
      await t.test(`${lang}, ${panel ? "panel" : "window"}`, async () => {
        const w = words[lang], calls = [], errors = [];
        const page = await (await browser.newContext({ viewport: panel ? { width: 440, height: 420 } : { width: 900, height: 560 } })).newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, calls));
        await page.goto("http://magpie.test/" + (panel ? "?mode=panel" : ""));
        const chip = (name) => page.locator("#profiles .chip", { hasText: new RegExp("^" + name) });
        await chip("work").waitFor({ state: "attached" });
        const open = page.locator(".profiles.open");
        const openList = async () => {
          await page.locator("#profBtn").click();
          await open.waitFor();
          await page.waitForTimeout(450);
        };
        if (panel) await openList();
        const work = chip("work"), detail = page.locator(".prof-detail");
        const update = work.locator(".x").nth(0), del = work.locator(".x").nth(1);

        // ↻: a first click asks, a second does it
        await work.hover();
        await update.click();
        await page.waitForTimeout(150);
        assert.equal((await update.textContent()).trim(), w.overwrite);
        assert(await update.isVisible(), "the question shows without hovering the chip");
        assert.deepEqual(calls, [], "a first click on ↻ overwrites nothing");
        assert.equal(await detail.count(), 0, "nor opens the details");
        await update.click();
        await page.waitForFunction(() => !document.querySelector("#profiles .chip.pending"));
        assert.deepEqual(calls, ["save work"]);
        assert.equal((await chip("work").locator(".x").nth(0).textContent()).trim(), "↻");

        // ×: asking, it goes back on Escape, and when left alone; the panel
        // stays where it is
        calls.length = 0;
        await work.hover();
        await del.click();
        assert.equal((await del.textContent()).trim(), w.del);
        await page.keyboard.press("Escape");
        assert.equal((await del.textContent()).trim(), "×", "Escape takes the question back");
        if (panel) assert(await open.isVisible(), "and leaves the list open");
        await del.click();
        await page.waitForTimeout(4000);
        assert.equal((await del.textContent()).trim(), "×", "left alone, the question goes");
        await del.click();
        assert.deepEqual(calls, [], "a first click on × deletes nothing");
        await del.click();
        await page.waitForFunction(() => ![...document.querySelectorAll("#profiles .chip")].some((c) => /^work/.test(c.textContent)));
        assert.deepEqual(calls, ["delete work"]);
        calls.length = 0;

        // the details: their × closes them, the chip staying where it is
        const home = chip("home"), homeName = home.locator("span").first(); // clear of ↻ and ×
        const top = () => home.evaluate((c) => c.getBoundingClientRect().top);
        const before = await top();
        await homeName.click();
        await detail.waitFor();
        const close = detail.locator(".pd-close");
        assert.equal(await close.getAttribute("title"), w.close);
        await close.click();
        await page.waitForTimeout(200);
        assert.equal(await detail.count(), 0, "the details' × closes them");
        assert.equal(await home.getAttribute("aria-expanded"), "false");
        assert(Math.abs((await top()) - before) < 1, `closing them leaves the chip where it is: ${before} → ${await top()}`);

        // Escape closes the details, and only them
        await homeName.click();
        await detail.waitFor();
        await page.keyboard.press("Escape");
        await page.waitForTimeout(200);
        assert.equal(await detail.count(), 0, "Escape closes the details");
        if (panel) {
          assert(await open.isVisible(), "the panel's list stays open");
          assert.deepEqual(calls, [], "Escape on the details must not hide the panel");
          // then the list; only then the panel
          await page.keyboard.press("Escape");
          assert.equal(await open.count(), 0);
          await page.waitForTimeout(200);
          assert.deepEqual(calls, [], "Escape on the list must not hide the panel");
          await page.keyboard.press("Escape");
          await page.waitForTimeout(200);
          assert.deepEqual(calls, ["hide"]);
          calls.length = 0;

          // the list closed with the details open opens on the chips, as
          // after Apply
          await openList();
          await homeName.click();
          await detail.waitFor();
          await page.mouse.click(420, 150);
          assert.equal(await open.count(), 0);
          await openList();
          assert.equal(await detail.count(), 0, "closed with the list, the details stay closed");
          assert.equal(await home.getAttribute("aria-expanded"), "false");
        } else assert.deepEqual(calls, []);

        // Apply closes the details, for good, and × works after it (#489:
        // the details stayed, or came back with the list, and their × did
        // nothing)
        calls.length = 0; // in the panel, the list is open from above
        await homeName.click();
        await detail.waitFor();
        await detail.locator(".pd-apply").click();
        await page.waitForFunction(() => document.querySelector("#status").classList.contains("ok"));
        assert.deepEqual(calls, ["use home"]);
        await page.waitForTimeout(200);
        assert.equal(await detail.count(), 0, "Apply closes the details");
        assert.equal(await home.getAttribute("aria-expanded"), "false");
        if (panel) {
          assert.equal(await open.count(), 0, "Apply closes the panel's list");
          await openList();
          assert.equal(await detail.count(), 0, "after Apply the list opens on the chips");
        }
        await homeName.click();
        await detail.waitFor();
        await detail.locator(".pd-close").click();
        await page.waitForTimeout(200);
        assert.equal(await detail.count(), 0, "after Apply the details' × still closes them");
        assert.equal(await home.getAttribute("aria-expanded"), "false");
        assert.deepEqual(errors, []);
      });
    }
  });
}
