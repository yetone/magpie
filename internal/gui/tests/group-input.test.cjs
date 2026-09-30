// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's input tags are separate from Automatic. Choosing Image
// and saving posts text and image, and the family's, context and reasoning
// levels already on the group go with them. The click leaves the page where it was. English
// and Chinese; Chromium and WebKit. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/opus", name: "opus", providerName: "A", icon: "generic", context: 1000000, ready: true },
];
const groups = () => ({
  models,
  groups: [{
    id: "kept", name: "Kept", members: ["a/opus"], routing: "order", ready: true,
    family: "claude", context: 200000, levels: ["low", "high"], effectiveInput: ["text"],
    memberInfo: [{ id: "a/opus", ready: true, context: 1000000, images: true }], rules: [],
  }],
  pools: [],
});

const words = {
  en: {
    summary: "Input: Text only", input: "Input types", auto: "Automatic", text: "Text (required)", image: "Image",
    hint: "Image declares image understanding", edit: "Edit", save: "Save",
  },
  zh: {
    summary: "输入：仅文本", input: "输入类型", auto: "自动", text: "文本（必选）", image: "图片",
    hint: "图片向 Agent 声明图片理解能力", edit: "编辑", save: "保存",
  },
};

function serve(lang, posts, held) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/groups/save") {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      if (held) await held;
      return json(groups());
    }
    if (url.pathname.startsWith("/api/groups/")) return json(groups());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const view = "#view-routing";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: group input is saved with family and context, and a click does not scroll`, async (t) => {
      assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 640 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      let releaseSave = () => {};
      const saveHeld = new Promise((resolve) => { releaseSave = resolve; });
      t.after(() => releaseSave());
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts, saveHeld));
      await page.goto("http://magpie.test/?view=routing");
      const group = page.locator(".rt-group").first();
      await group.waitFor();
      assert.equal(await group.locator(".rt-ginput-summary").textContent(), w.summary);

      await group.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      await ed.locator("label", { hasText: w.input }).waitFor();
      const image = ed.locator(".rt-ginputs button.mchip", { hasText: w.image });
      const text = ed.locator(".rt-ginputs button.mchip", { hasText: w.text });
      const auto = ed.locator(".rt-ginputs button.mchip", { hasText: w.auto });
      assert.equal(await auto.getAttribute("aria-pressed"), "true");
      assert.equal(await text.getAttribute("aria-pressed"), "false");

      // the reader wheels the tags into the view; the click itself must not
      const box = await page.locator(view).boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + 80);
      for (let i = 0; i < 50; i++) {
        const at = await image.evaluate((e, sel) => {
          const v = document.querySelector(sel);
          return e.getBoundingClientRect().top - v.getBoundingClientRect().top;
        }, view);
        if (at > 140 && at < 280) break;
        await page.mouse.wheel(0, at > 280 ? 60 : -60);
        await page.waitForTimeout(20);
      }
      const place = () => page.evaluate((sel) => {
        const v = document.querySelector(sel);
        const buttons = [...document.querySelectorAll(".rt-ginputs button.mchip")];
        const imageBtn = buttons[buttons.length - 1];
        return { scroll: v.scrollTop, top: Math.round(imageBtn.getBoundingClientRect().top) };
      }, view);
      const before = await place();
      const hit = await image.boundingBox();
      await page.mouse.click(hit.x + hit.width / 2, hit.y + hit.height / 2);
      assert.equal(await image.getAttribute("aria-pressed"), "true");
      assert.equal(await auto.getAttribute("aria-pressed"), "false");
      assert.match(await ed.locator(".rt-ginputs .hint").textContent(), new RegExp(w.hint.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
      assert.deepEqual(await place(), before, "choosing Image moved the page");

      const saveBtn = ed.locator("button.primary", { hasText: w.save });
      for (let i = 0; i < 50; i++) {
        const at = await saveBtn.evaluate((e, sel) => {
          const v = document.querySelector(sel);
          return e.getBoundingClientRect().top - v.getBoundingClientRect().top;
        }, view);
        if (at > 140 && at < 360) break;
        await page.mouse.wheel(0, at > 360 ? 60 : -60);
        await page.waitForTimeout(20);
      }
      const saveAt = await saveBtn.boundingBox();
      const placeView = () => page.locator(view).evaluate((v) => {
        const ed = document.querySelector(".rt-gedit");
        const group = document.querySelector(".rt-group");
        return {
          scroll: v.scrollTop,
          editor: !!ed,
          top: (ed || group).getBoundingClientRect().top,
        };
      });
      const beforeSave = await placeView();
      await page.evaluate(() => {
        const v = document.querySelector("#view-routing");
        const top = () => (document.querySelector(".rt-gedit") || document.querySelector(".rt-group")).getBoundingClientRect().top;
        const want = top(), scroll = v.scrollTop;
        window.__saveMoves = [];
        const look = () => {
          if (v.scrollTop !== scroll || Math.abs(top() - want) > 1.5)
            window.__saveMoves.push({ scroll: v.scrollTop, top: top() });
        };
        window.__saveObserver = new ResizeObserver(look);
        for (const child of [v, ...v.children]) window.__saveObserver.observe(child);
        v.addEventListener("scroll", look);
      });
      await page.mouse.click(saveAt.x + saveAt.width / 2, saveAt.y + 4);
      const whileSaving = await placeView();
      assert.equal(whileSaving.scroll, beforeSave.scroll, "Save scrolled the page");
      assert.equal(whileSaving.editor, true, "Save redrew before its reply");
      releaseSave();
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      await page.waitForTimeout(700);
      const afterSave = await placeView();
      assert.equal(afterSave.scroll, beforeSave.scroll, "Save left the page elsewhere");
      // WebKit scrolls in whole CSS pixels while layout keeps fractions.
      // Compare the raw coordinates: rounding each separately turns a
      // subpixel difference across .5 into a spurious one-pixel failure.
      assert(Math.abs(afterSave.top - beforeSave.top) <= 1.5,
        `Save moved the group: ${beforeSave.top} → ${afterSave.top}`);
      const moves = await page.evaluate(() => { window.__saveObserver.disconnect(); return window.__saveMoves; });
      assert.deepEqual(moves, [], "Save moved the page during layout or scrolling");
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-input.png`) });
      }
      const save = posts.find((p) => p.path === "/api/groups/save");
      assert(save, `saved: ${JSON.stringify(posts)}`);
      assert.equal(save.body.id, "kept");
      assert.equal(save.body.from, "kept");
      assert.equal(save.body.family, "claude");
      assert.equal(save.body.context, 200000);
      assert.deepEqual(save.body.levels, ["low", "high"]);
      assert.deepEqual(save.body.input, ["text", "image"]);
      assert.deepEqual(save.body.members, ["a/opus"]);
      assert.deepEqual(errors, []);
    });
  }
}
