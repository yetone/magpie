// Run with Node's test runner and Playwright on the module path; see README.md.
// Editing a routing group (Jiabubu on Discord, #feedback): the group being
// edited looked like the groups around it, and Save was a small text button
// at the editor's foot, a long scroll down. Now the editor reads as the
// selected row (a tint and an accent ring, no stripe down one side), and
// its Save and Cancel stay in the window while the editor's rows scroll by,
// at a push button's size. In both engines, desktop and web, at 1100, 770
// and 420, in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ids = ["an/opus", "an/sonnet", "oa/gpt-5", "oa/gpt-5-mini", "gg/gemini-3-pro", "gg/gemini-3-flash", "ds/deepseek-v4", "kk/kimi-k3", "zh/glm-5", "qw/qwen-4"];
const models = ids.map((id) => ({ id, name: id.split("/")[1], providerName: id.split("/")[0].toUpperCase(), icon: "generic" }));
const grp = (id, name, members) => ({ id, name, members, match: [], fast: [], off: [], routing: "order", ready: true, memberInfo: members.map((m) => ({ id: m, ready: true })) });
const groups = () => ({ models, pools: [], groups: [grp("fast", "Fast", ids.slice(0, 3)), grp("cheap", "Cheap", ids.slice(3, 6)), grp("coding", "Coding", ids), grp("free", "Free", ids.slice(6))] });

function serve(lang, theme, web) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the group being edited reads as selected, its Save in reach`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const theme of ["light", "dark"]) {
      for (const web of [false, true]) {
        for (const width of [1100, 770, 420]) {
          for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
            // the other theme in one language a width is enough
            if (theme === "dark" && lang !== "zh") continue;
            const at = `${theme} ${web ? "web" : "desktop"} ${width} ${lang}`;
            const context = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce", colorScheme: theme });
            const page = await context.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [];
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", serve(lang, theme, web));
            await page.goto("http://magpie.test/?view=routing");
            const card = page.locator(".rt-group", { hasText: "Coding" });
            await card.waitFor();
            await card.locator("button.text").first().click();
            const ed = page.locator(".rt-gedit");
            await ed.waitFor();
            // the editor's head at the top of the window, its foot far below
            // as the reader scrolls (the app puts back a scroll that isn't theirs)
            await page.mouse.move(width / 2, 400);
            await page.mouse.wheel(0, Math.round((await ed.boundingBox()).y) - 90);
            await page.waitForFunction(() => document.querySelector(".rt-gedit").getBoundingClientRect().top < 200);
            const got = await page.evaluate(() => {
              const ed = document.querySelector(".rt-gedit");
              const row = document.querySelector(".rt-groups > .rt-group");
              const cs = getComputedStyle(ed), rs = getComputedStyle(row);
              const save = ed.querySelector(":scope > .bar > .primary"), cancel = [...ed.querySelectorAll(":scope > .bar > .text")].find((b) => !b.classList.contains("primary") && !b.classList.contains("danger"));
              const r = save.getBoundingClientRect(), cx = r.left + r.width / 2, cy = r.top + r.height / 2;
              const hit = document.elementFromPoint(cx, cy);
              return {
                tall: ed.getBoundingClientRect().bottom > innerHeight + 50 && ed.getBoundingClientRect().top < 200,
                bg: cs.backgroundColor, rowBg: rs.backgroundColor, ring: cs.boxShadow,
                sides: [cs.borderLeftWidth, cs.borderRightWidth, cs.borderLeftColor, cs.borderRightColor],
                save: { top: r.top, bottom: r.bottom, h: r.height, font: parseFloat(getComputedStyle(save).fontSize), onTop: save === hit || save.contains(hit) },
                cancel: { h: cancel.getBoundingClientRect().height, font: parseFloat(getComputedStyle(cancel).fontSize) },
              };
            });
            assert.ok(got.tall, `${at}: the editor starts near the top and runs past the window (the case under test)`);
            assert.notEqual(got.bg, got.rowBg, `${at}: the editor is tinted apart from the other groups`);
            assert.match(got.ring, /inset/, `${at}: the editor has a ring all round`);
            assert.equal(got.sides[0], got.sides[1], `${at}: no stripe down one side`);
            assert.equal(got.sides[2], got.sides[3], `${at}: no coloured stripe down one side`);
            assert.ok(got.save.top >= 0 && got.save.bottom <= 700, `${at}: Save is in the window (${Math.round(got.save.top)}–${Math.round(got.save.bottom)})`);
            assert.ok(got.save.onTop, `${at}: nothing covers Save`);
            assert.ok(got.save.h >= 28 && got.save.font >= 13, `${at}: Save is a push button's size (${got.save.h}px, ${got.save.font}px)`);
            assert.ok(got.cancel.h >= 28 && got.cancel.font >= 13, `${at}: Cancel matches it (${got.cancel.h}px, ${got.cancel.font}px)`);
            // scrolled to the foot, the bar is where it always was: after
            // the editor's last row
            {
              const b = await ed.boundingBox();
              await page.mouse.wheel(0, Math.round(b.y + b.height - 700 + 40));
            }
            await page.waitForFunction(() => document.querySelector(".rt-gedit").getBoundingClientRect().bottom <= innerHeight);
            const foot = await page.evaluate(() => {
              const ed = document.querySelector(".rt-gedit"), bar = ed.querySelector(":scope > .bar");
              const last = bar.previousElementSibling;
              return { gap: bar.getBoundingClientRect().top - last.getBoundingClientRect().bottom, edBottom: ed.getBoundingClientRect().bottom - bar.getBoundingClientRect().bottom };
            });
            assert.ok(foot.gap >= 0, `${at}: at the foot the bar follows the last row (${foot.gap})`);
            assert.ok(foot.edBottom >= 0, `${at}: and sits inside the editor`);
            assert.deepEqual(errors, [], at);
            await context.close();
          }
        }
      }
    }
  });
}
