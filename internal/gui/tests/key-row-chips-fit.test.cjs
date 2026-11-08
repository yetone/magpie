// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's row keeps every chip and button whole (young02036 on Discord, a
// Remote magpie's keys on Windows): hovered, a key with a protocol pick, its
// models chip, its limit and Switch off, Remove and Make first showed its
// models chip as one character, "全", the rest of 全部模型 cut off. Only the
// key's name and its masked key give way; what doesn't fit beside them then
// goes under the row's first line, Make first still at the right end. Every
// chip's text fits its box, no two controls overlap and nothing leaves the
// card, hovered or not, and the row is laid out the same under the pointer
// as off it (tenacious_eagle_96548 on Discord: the row grew under the
// pointer and shrank as it left). In every language, in the app's window
// from its narrowest and in a browser tab, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.ASSET_DIR || path.resolve(__dirname, "../assets"));
const remote = () => ({
  id: "remote", name: "Remote magpie", preset: "remote-magpie", icon: "generic",
  chat: "https://magpie.example.com/v1", responses: "https://magpie.example.com/v1", anthropic: "https://magpie.example.com", catalog: "",
  models: [{ id: "glm-5", name: "", on: true }, { id: "claude-opus-5", name: "", on: true }, { id: "gpt-6", name: "", on: true }],
  agents: [], fallback: [], headers: {}, routing: "",
  key: { set: true, masked: "sk-m…a1b2" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: [
    { id: "aaaaaaaaaa", name: "GLM", masked: "sk-m…a1b2", on: true, active: true },
    { id: "bbbbbbbbbb", name: "除GLM之外的全部模型", masked: "sk-m…c3d4", on: true },
    { id: "cccccccccc", name: "", masked: "sk-m…e5f6", on: true },
  ],
  accountModels: { aaaaaaaaaa: ["glm-5"] },
});

function serve(lang, web) {
  const providers = { providers: [remote()], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

// measure: each shown control of a key's row, in order, and the card's edges
function measure(id) {
  const row = document.querySelector(`.editor .accts.keys .acc[data-account-id="${id}"]`);
  const card = row.closest(".accts").getBoundingClientRect();
  const shown = [...row.querySelectorAll("button, .plan, .using, .key-nourl")].filter((e) => {
    const s = getComputedStyle(e), b = e.getBoundingClientRect();
    return s.display !== "none" && s.visibility !== "hidden" && +s.opacity > 0.5 && b.width > 0;
  });
  return {
    card: { left: card.left, right: card.right, top: card.top },
    els: shown.map((e) => {
      const b = e.getBoundingClientRect();
      return { cls: String(e.className), text: e.textContent.trim(), left: b.left, right: b.right, top: b.top, bottom: b.bottom, sw: e.scrollWidth, cw: e.clientWidth };
    }),
  };
}

const langs = ["zh", "en", "zh-TW", "ja", "de"];
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of langs) {
    // the app's window (its least is 560px) and a browser tab, which at
    // 760px and under lays a row out as on a phone
    for (const [web, width] of [[false, 560], [false, 760], [false, 1000], [false, 1280], [true, 420], [true, 1000]]) {
      const mode = web ? "tab" : "app";
      test(`${engine} ${lang} ${mode} ${width}px: a key's chips and buttons keep their whole text`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.locator(".editor .accts.keys").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${mode}-${width}-key-row-chips.png`) }).catch(() => {});
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, web));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Remote magpie" }).first().click();
        await page.locator('.editor .accts.keys .acc[data-account-id="bbbbbbbbbb"] .proto').waitFor({ state: "attached" });

        const check = async (id, hovered) => {
          const row = page.locator(`.editor .accts.keys .acc[data-account-id="${id}"]`);
          if (hovered) {
            await row.scrollIntoViewIfNeeded();
            await row.locator(".dot").hover();
          } else await page.mouse.move(0, 0);
          // the hover's fade is over: waited for, not timed (a busy WebKit
          // was still fading in after 250ms)
          await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== "running"));
          const m = await page.evaluate(measure, id);
          const at = `${id}${hovered ? " hovered" : ""}`;
          const all = m.els.map((e) => `${e.cls}「${e.text}」${Math.round(e.left)}–${Math.round(e.right)}@${Math.round(e.top)}`).join(" | ");
          // the chips the report is about are shown, hovered or not
          for (const c of ["proto", "amodels", "key-switch"]) assert.ok(m.els.some((e) => e.cls.split(" ").includes(c)), `${at}: no ${c} shown: ${all}`);
          for (const e of m.els) {
            const words = e.cls.split(" ");
            // only the name and the masked key may end in "…"
            if (!words.includes("n") && !words.includes("plan")) assert.ok(e.sw <= e.cw + 1, `${at}: ${e.cls}「${e.text}」is cut (${e.cw} of ${e.sw}px): ${all}`);
            assert.ok(e.left >= m.card.left - 0.5 && e.right <= m.card.right + 0.5, `${at}: ${e.cls}「${e.text}」leaves the card (${e.left}–${e.right} in ${m.card.left}–${m.card.right})`);
          }
          // the name is never squeezed to nothing: a short one is whole,
          // a long one keeps a few letters
          const name = m.els.find((e) => e.cls.split(" ").includes("n"));
          assert.ok(name && name.cw >= Math.min(name.sw - 1, 30), `${at}: the name is squeezed to ${name?.cw}px: ${all}`);
          // no two controls overlap
          for (let i = 0; i < m.els.length; i++) for (let j = i + 1; j < m.els.length; j++) {
            const a = m.els[i], b = m.els[j];
            const apart = a.right <= b.left + 0.5 || b.right <= a.left + 0.5 || a.bottom <= b.top + 0.5 || b.bottom <= a.top + 0.5;
            assert.ok(apart, `${at}: ${a.cls}「${a.text}」and ${b.cls}「${b.text}」overlap: ${all}`);
          }
          return m;
        };
        // the pointer moves nothing: every control where it was off the row
        const where = (m) => m.els.map((e) => `${e.cls.split(" ")[0]}@${Math.round(e.left - m.card.left)},${Math.round(e.top - m.card.top)},${Math.round(e.right - e.left)}`).join(" | ");
        for (const id of ["aaaaaaaaaa", "bbbbbbbbbb", "cccccccccc"]) {
          const off = await check(id, false);
          const on = await check(id, true);
          assert.equal(where(on), where(off), `${id}: the row is laid out otherwise under the pointer`);
        }
        // Make first ends its row on the right, wrapped or not
        const ends = await page.evaluate(() => {
          const row = document.querySelector('.editor .accts.keys .acc[data-account-id="bbbbbbbbbb"]');
          const first = [...row.querySelectorAll("button.text")].find((b) => !b.classList.contains("quiet"));
          const pad = parseFloat(getComputedStyle(row).paddingRight);
          return { first: first.getBoundingClientRect().right, row: row.getBoundingClientRect().right - pad };
        });
        assert.ok(Math.abs(ends.first - ends.row) <= 1, `Make first doesn't end its row (${ends.first} vs ${ends.row})`);
        const over = await page.evaluate(() => [...document.querySelectorAll(".editor .accts .acc")].filter((e) => e.scrollWidth > e.clientWidth + 1).map((e) => `${e.dataset.accountId}:${e.scrollWidth}/${e.clientWidth}`));
        assert.deepEqual(over, [], "a row overflows its card");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
}
