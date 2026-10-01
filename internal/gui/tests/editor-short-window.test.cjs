// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's editor in a short window (悠悠哥 on Discord: a small window's
// WorkBuddy editor showed part of its models, and the wheel took it no
// further until the window was maximized). The dialog fits the window, its
// head and Save stay in sight, and the wheel held over the model chips runs
// them to their last one and then takes the editor's body on to its end,
// where it used to stop part way down. The page itself never moves. In
// Chromium and WebKit, English and Chinese, at three window sizes.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const IDS = Array.from({ length: 40 }, (_, i) => `workbuddy-model-${String(i + 1).padStart(2, "0")}`);
const wb = {
  id: "workbuddy", name: "WorkBuddy", icon: "generic", chat: "https://copilot.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: IDS.map((id) => ({ id, name: id, on: true })), agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};

function serve(lang) {
  const providers = { providers: [wb], presets: [], excluded: [], gateway: { running: true, window: true } };
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
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = { en: { save: "Save" }, zh: { save: "保存" } };
const inside = (a, b, what) => {
  assert.ok(a.top >= b.top - 1 && a.bottom <= b.bottom + 1 && a.left >= b.left - 1 && a.right <= b.right + 1,
    `${what}: ${JSON.stringify(a)} within ${JSON.stringify(b)}`);
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const [width, height] of [[800, 500], [600, 420], [420, 340]]) {
      test(`${engine} ${lang} ${width}x${height}: the wheel takes a provider's editor past its models to the end`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { has: page.locator(".name", { hasText: /^WorkBuddy$/ }) }).click();
        const ed = page.locator("#modal .editor");
        await ed.waitFor();
        await page.waitForTimeout(200);
        const rects = () => page.evaluate(() => {
          const r = (e) => { const b = e.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, left: b.left, right: b.right }; };
          const d = document.querySelector("#modal .dialog"), body = d.querySelector(".ebody"), chips = body.querySelector(".mchips");
          return {
            view: { top: 0, bottom: innerHeight, left: 0, right: innerWidth }, dialog: r(d), body: r(body), chips: r(chips),
            last: r(chips.lastElementChild), head: r(d.querySelector(".ehead")), bar: r(d.querySelector(".editor > .bar")),
            bodyAt: body.scrollTop, bodyEnd: body.scrollHeight - body.clientHeight,
            chipsAt: chips.scrollTop, chipsEnd: chips.scrollHeight - chips.clientHeight,
            lastId: chips.lastElementChild.textContent, page: document.scrollingElement.scrollTop,
          };
        });
        let at = await rects();
        inside(at.dialog, at.view, "the dialog fits the window");
        inside(at.head, at.view, "the editor's head is in sight");
        inside(at.bar, at.view, "its buttons are in sight");
        assert.ok(at.bodyEnd > 0, "the fields are more than the body shows");
        assert.ok(at.chipsEnd > 0, "the models are more than their list shows");

        // bring the models into the body's view, then wheel over them alone
        await page.evaluate(() => document.querySelector("#modal .ebody .mchips").scrollIntoView({ block: "start" }));
        at = await rects();
        await page.mouse.move((at.chips.left + at.chips.right) / 2, Math.max(at.chips.top, at.body.top) + 20);
        for (let i = 0; i < 30; i++) {
          await page.mouse.wheel(0, 120);
          await page.waitForTimeout(30);
          const now = await rects();
          if (now.chipsAt >= now.chipsEnd - 1 && now.bodyAt >= now.bodyEnd - 1) break;
        }
        await page.waitForTimeout(150);
        at = await rects();
        assert.ok(at.chipsAt >= at.chipsEnd - 1, `the models run to their end (${at.chipsAt} of ${at.chipsEnd})`);
        assert.equal(at.lastId.trim().startsWith(IDS[IDS.length - 1]), true, "the last chip is the last model");
        assert.ok(at.last.bottom <= at.chips.bottom + 1 && at.last.top >= at.chips.top - 1, "the last model shows in its list");
        assert.ok(at.bodyAt >= at.bodyEnd - 1, `the wheel takes the body on to its end (${at.bodyAt} of ${at.bodyEnd})`);
        assert.equal(at.page, 0, "the page itself stays put");
        inside(at.head, at.view, "the head is still in sight");
        inside(at.bar, at.view, "Save is still in sight");
        const save = ed.locator(".bar").getByRole("button", { name: words[lang].save, exact: true });
        const box = await save.boundingBox();
        assert.ok(box, "Save is there");
        const hit = await page.evaluate(([x, y]) => document.elementFromPoint(x, y)?.closest("button")?.textContent, [box.x + box.width / 2, box.y + box.height / 2]);
        assert.equal(hit, words[lang].save, "nothing covers Save");
        assert.deepEqual(errors, []);
      });
    }
  }
}
