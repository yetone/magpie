// Run with Node's test runner and Playwright on the module path; see README.md.
// The status pill says a long outcome whole (littleHong on Discord): Test
// models in OfficeAce's editor failed on every model, and the red pill was
// one line ending in "…", so the part saying what to do was lost. A line
// longer than its place floats over the window wrapped, up to ten lines,
// the rest scrolled to; it stays while the pointer is on it and as long as
// it takes to read; its text can be selected; a short one stays in the
// footer as it was. In every language, light and dark, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// the reporter's error, as the OfficeAce plugin words it
const said = "OfficeAce 账号已登录，但还没有接入华为云 MaaS（凭据里没有 modelAppKey）。请在 OfficeAce 客户端完成「接入华为云 MaaS 模型」后重试；额度与签到不受影响，已经可以正常使用。";
const models = ["glm-5.3", "glm-5.3-flash", "deepseek-v4", "deepseek-v4-flash", "kimi-k3", "qwen4-coder", "qwen4-max", "minimax-m3", "glm-5.2", "deepseek-r3"];
const provider = {
  id: "officeace", name: "OfficeAce", icon: "generic", chat: "https://officeace.example/v2", responses: "", anthropic: "", catalog: "",
  agents: [], fallback: [], headers: {}, keyList: [], proxy: "", key: { set: true, masked: "sk-…one" },
  models: models.map((id) => ({ id, name: "", on: true })),
};

function serve(lang, theme) {
  const list = { providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(list);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/provider/test") {
      const body = route.request().postDataJSON();
      return json({ results: (body.test || []).map((model) => ({ ok: false, model, status: 400, error: said })) });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// how the pill is drawn: whether it is cut, and where it is
const look = (page) => page.locator("#status").evaluate((e) => {
  const cs = getComputedStyle(e), r = e.getBoundingClientRect();
  return {
    text: e.textContent, lift: e.classList.contains("lift"),
    cutAcross: e.scrollWidth > e.clientWidth + 1, cutDown: e.scrollHeight > e.clientHeight + 1,
    height: r.height, left: r.left, right: r.right, top: r.top, bottom: r.bottom, vw: innerWidth, vh: innerHeight,
    overflowY: cs.overflowY, select: cs.userSelect || cs.webkitUserSelect, borderLeft: parseFloat(cs.borderLeftWidth) || 0,
    lineHeight: parseFloat(cs.lineHeight),
  };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  const launch = () => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });
  const open = async (t, lang, theme, width, name, clock = false) => {
    const browser = await launch();
    const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${theme}-${width}-${name}-status-long.png`) });
      }
      await browser.close();
    });
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    if (clock) await page.clock.install();
    await page.route("**/*", serve(lang, theme));
    await page.goto("http://magpie.test/?view=providers");
    return { page, errors };
  };

  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const [theme, width] of [["light", 900], ["dark", 390]]) {
      test(`${engine} ${lang} ${theme} ${width}: Test models' failure is said whole over the editor`, async (t) => {
        const { page, errors } = await open(t, lang, theme, width, "editor");
        await page.locator(".row.provider", { hasText: "OfficeAce" }).first().click();
        await page.locator(".editor .mchips .mchip").first().waitFor();
        const name = await page.evaluate(() => t("Test models"));
        await page.locator(".editor .mfoot").getByRole("button", { name, exact: true }).click();
        await page.waitForFunction((s) => document.querySelector("#status").textContent.includes(s), said);
        const want = await page.evaluate(([s, m]) => t("{n} of {all} models didn't answer; {model}: {error}", { n: 10, all: 10, model: m, error: "400 · " + s }), [said, models[0]]);
        const got = await look(page);
        assert.equal(got.text, want);
        assert(got.lift, "the pill floats over the dialog");
        assert(!got.cutAcross && !got.cutDown, `the whole message shows, nothing cut: ${JSON.stringify(got)}`);
        assert(got.height > 2 * got.lineHeight, `it wraps to several lines: ${JSON.stringify(got)}`);
        assert(got.left >= 0 && got.right <= got.vw && got.top >= 0 && got.bottom <= got.vh, `it fits the window: ${JSON.stringify(got)}`);
        assert.equal(got.select, "text", "its text can be selected");
        assert.equal(got.borderLeft, 0, "no left-border accent");
        // the words it ends with can be read where they are drawn
        const end = await page.locator("#status").evaluate((e) => {
          const r = document.createRange();
          r.setStart(e.firstChild, e.firstChild.length - 1);
          r.setEnd(e.firstChild, e.firstChild.length);
          const b = r.getBoundingClientRect(), box = e.getBoundingClientRect();
          return b.bottom <= box.bottom + 0.5 && b.right <= box.right + 0.5 && b.width > 0;
        });
        assert(end, "its last character is inside the pill");
        assert.deepEqual(errors, []);
      });
    }
  }

  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a long line from a page floats wrapped, a short one stays in the footer`, async (t) => {
      const { page, errors } = await open(t, lang, "light", 900, "footer");
      await page.locator(".row.provider", { hasText: "OfficeAce" }).first().waitFor();
      await page.evaluate(() => status(t("Saved"), "ok"));
      let got = await look(page);
      assert(!got.lift && !got.cutAcross, `a short line stays in the footer: ${JSON.stringify(got)}`);
      for (const kind of ["err", "warn", "ok"]) {
        await page.evaluate(([s, k]) => status(s, k), [said, kind]);
        got = await look(page);
        assert(got.lift, `${kind}: a line longer than the footer floats`);
        assert(!got.cutAcross && !got.cutDown, `${kind}: shown whole: ${JSON.stringify(got)}`);
        assert(got.left >= 0 && got.right <= got.vw && got.bottom <= got.vh, `${kind}: fits the window: ${JSON.stringify(got)}`);
      }
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a very long line is held to ten lines and scrolled to its end`, async (t) => {
      const { page, errors } = await open(t, lang, "dark", 390, "scroll");
      await page.locator(".row.provider", { hasText: "OfficeAce" }).first().waitFor();
      await page.evaluate((s) => status(Array(12).fill(s).join(" "), "err"), said);
      const got = await look(page);
      assert(got.lift && !got.cutAcross, `wrapped: ${JSON.stringify(got)}`);
      assert(got.height <= 10 * got.lineHeight + 8, `held to ten lines: ${JSON.stringify(got)}`);
      assert(got.bottom <= got.vh && got.top >= 0, `fits the window: ${JSON.stringify(got)}`);
      assert.equal(got.overflowY, "auto");
      assert(got.cutDown, "the rest is under it, to scroll to");
      const s = page.locator("#status");
      await s.hover();
      await page.mouse.wheel(0, 4000);
      await page.waitForFunction(() => { const e = document.querySelector("#status"); return e.scrollTop + e.clientHeight >= e.scrollHeight - 1; });
      assert.equal(await page.evaluate(() => scrollY), 0, "the page under it didn't scroll");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the pill stays while the pointer is on it, and goes after`, async (t) => {
      const { page, errors } = await open(t, lang, "light", 900, "hover");
      await page.locator(".row.provider", { hasText: "OfficeAce" }).first().waitFor();
      // a short one goes at the time it was given
      await page.evaluate(() => status("Saved", "ok", 200));
      await page.waitForFunction(() => document.querySelector("#status").textContent === "", null, { timeout: 3000 });
      // a long one is given the time its words take
      await page.evaluate((s) => status(s, "err", 200), said);
      await page.waitForTimeout(600);
      assert.equal(await page.locator("#status").textContent(), said, "a long line stays long enough to read");
      await page.locator("#status").hover();
      // past its reading time, held by the pointer
      await page.waitForTimeout(said.length * 80 + 300);
      assert.equal(await page.locator("#status").textContent(), said, "held while the pointer is on it");
      await page.mouse.move(5, 5);
      await page.waitForFunction(() => document.querySelector("#status").textContent === "", null, { timeout: 5000 });
      assert.deepEqual(errors, []);
    });

    // water5433 on Discord: a long error floated over the window for a
    // minute and more (80ms a character). Its reading time stops at 15s,
    // a line that takes less keeps all of it, and a time the caller gives
    // (a damaged file's warning, 20s) is kept whole.
    for (const [theme, width] of [["light", 900], ["dark", 390]]) {
      test(`${engine} ${lang} ${theme} ${width}: a long line goes after its reading time, 15s at most`, async (t) => {
        const { page, errors } = await open(t, lang, theme, width, "timeout", true);
        await page.locator(".row.provider", { hasText: "OfficeAce" }).first().waitFor();
        const text = () => page.locator("#status").textContent();
        await page.mouse.move(5, 5);
        // a very long error: 92s at 80ms a character
        const long = Array(12).fill(said).join(" ");
        await page.evaluate((s) => status(s, "err"), long);
        await page.clock.fastForward(14500);
        assert.equal(await text(), long, "still there before 15s");
        await page.clock.fastForward(700);
        assert.equal(await text(), "", "gone at 15s, not after its 80ms a character");
        // Test models' failure, as littleHong read it: all of its reading time
        const one = await page.evaluate(([s, m]) => t("{n} of {all} models didn't answer; {model}: {error}", { n: 10, all: 10, model: m, error: "400 · " + s }), [said, models[0]]);
        const read = one.length * 80;
        assert.ok(read > 10000 && read < 15000, `the reporter's line takes ${read}ms to read`);
        await page.evaluate((s) => status(s, "err"), one);
        await page.clock.fastForward(read - 300);
        assert.equal(await text(), one, "kept for all of its reading time");
        await page.clock.fastForward(600);
        assert.equal(await text(), "", "gone after it");
        // a caller's own time is kept whole
        await page.evaluate((s) => status(s, "warn", 20000), long);
        await page.clock.fastForward(19500);
        assert.equal(await text(), long, "a caller's 20s is kept");
        await page.clock.fastForward(700);
        assert.equal(await text(), "");
        // the pointer still holds it past 15s, and it goes 2s after it leaves
        await page.evaluate((s) => status(s, "err"), long);
        await page.locator("#status").hover();
        await page.clock.fastForward(30000);
        assert.equal(await text(), long, "held while the pointer is on it");
        await page.mouse.move(5, 5);
        await page.clock.fastForward(2200);
        assert.equal(await text(), "");
        assert.deepEqual(errors, []);
      });
    }
  }
}
