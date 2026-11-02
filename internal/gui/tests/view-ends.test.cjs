// Run with Node's test runner and Playwright on the module path; see README.md.
// Scroll to top and Scroll to bottom (emo172, #1090): a quiet capsule at the
// open page's lower right (scrollends.js), shown once the page is scrolled a
// screen down and not before. Each press is the reader's own scroll: the page
// goes to its top or its foot and stays there, not put back by the "where the
// reader is" guard. Reached by the keyboard too (Tab, then Enter), its label
// and tooltip in every language, in both themes, at a narrow window and a wide
// one, with the motion on and off. It never makes the page taller (app.js
// measures it to keep the reader's place), so over the page's corner it
// steps aside at the foot once the pointer and focus leave it. Chromium and
// WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
// rows enough for a page several screens tall
const AGENTS = Array.from({ length: 40 }, (_, i) => ({
  id: "a" + i, name: "Agent " + i, path: "/t/a" + i + ".json", icon: "generic", wired: true,
  fields: [{ key: "model", label: "model", value: "m", options: [{ value: "m", label: "M", ref: "a/m" }] }],
}));
const WORDS = {
  en: ["Scroll to top", "Scroll to bottom"],
  zh: ["回到顶部", "回到底部"],
  "zh-TW": ["回到頂部", "回到底部"],
  ja: ["一番上へ", "一番下へ"],
  de: ["Nach ganz oben", "Nach ganz unten"],
};
const SHAPES = [
  { viewport: { width: 440, height: 620 }, theme: "light", motion: "reduce" },
  { viewport: { width: 1180, height: 640 }, theme: "dark", motion: "no-preference" },
  // wide enough that the page keeps a margin at its right: the capsule sits in it
  { viewport: { width: 1640, height: 700 }, theme: "light", motion: "no-preference", margin: true },
];

function server(lang, theme) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: AGENTS, profiles: [], settings: { lang, theme }, fx: { rate: 7.2, at: now.toISOString() } });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the page at rest: the same scrollTop over a few reads
async function rested(view, p, ms = 4000) {
  let last = NaN, same = 0;
  const until = Date.now() + ms;
  while (Date.now() < until) {
    const t = await view.evaluate((v) => v.scrollTop);
    if (t === last && ++same >= 3) return t;
    if (t !== last) same = 0;
    last = t;
    await p.waitForTimeout(80);
  }
  return last;
}

const shown = (p) => p.evaluate(() => {
  const b = document.querySelector(".view-ends");
  return !!b && getComputedStyle(b).visibility === "visible" && b.classList.contains("on");
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a long page has Scroll to top and Scroll to bottom", { timeout: 300000 }, async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit : chromium).launch();
    t.after(() => browser.close());
    for (const lang of Object.keys(WORDS)) {
      for (const shape of SHAPES) {
        await t.test(`${lang} ${shape.viewport.width} ${shape.theme}`, async () => {
          const errors = [];
          const ctx = await browser.newContext({ viewport: shape.viewport, reducedMotion: shape.motion, colorScheme: shape.theme });
          const p = await ctx.newPage();
          p.setDefaultTimeout(8000);
          p.setDefaultNavigationTimeout(60000);
          p.on("pageerror", (e) => {
            if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message);
          });
          await p.route("**/*", server(lang, shape.theme));
          const view = p.locator("#view-agents");
          await p.goto("http://magpie.test/?view=agents");
          await p.locator("#view-agents .ag-lead").first().waitFor();
          await p.waitForFunction(() => { const v = document.getElementById("view-agents"); return v.scrollHeight > v.clientHeight * 3; }).catch(() => {});
          const h = await view.evaluate((v) => ({ over: v.scrollHeight - v.clientHeight, h: v.clientHeight, tall: v.scrollHeight }));
          assert(h.over > 2 * h.h, `the page must be several screens tall: ${JSON.stringify(h)}`);

          // at the top, nothing shows
          assert.equal(await shown(p), false, "no capsule at the page's top");

          // the reader's wheel, past a screen
          const r = await view.boundingBox();
          await p.mouse.move(r.x + r.width / 2, r.y + r.height / 2);
          for (let i = 0; i < 12 && (await view.evaluate((v) => v.scrollTop <= v.clientHeight * 1.2)); i++) {
            await p.mouse.wheel(0, 300);
            await p.waitForTimeout(120);
          }
          await rested(view, p);
          await p.waitForFunction(() => document.querySelector(".view-ends.on"));
          assert.equal(await shown(p), true, "the capsule shows a screen down");

          const up = p.locator(".view-ends .ve-up"), down = p.locator(".view-ends .ve-down");
          // its words, as its name and its tooltip
          for (const [b, w] of [[up, WORDS[lang][0]], [down, WORDS[lang][1]]]) {
            assert.equal(await b.getAttribute("aria-label"), w);
            assert.equal(await b.getAttribute("title"), w);
          }
          await p.getByRole("button", { name: WORDS[lang][1], exact: true }).waitFor();

          // inside the window and over the page, quiet: no coloured left
          // border, its colours the theme's
          const look = await p.evaluate(() => {
            const b = document.querySelector(".view-ends"), r = b.getBoundingClientRect(), v = document.getElementById("view-agents").getBoundingClientRect();
            const cs = getComputedStyle(b);
            const rgb = cs.backgroundColor.match(/[\d.]+/g).map(Number);
            return {
              inside: r.left >= 0 && r.right <= innerWidth && r.top >= 0 && r.bottom <= innerHeight && r.right <= v.right && r.bottom <= v.bottom,
              wide: document.documentElement.scrollWidth > innerWidth || document.body.scrollWidth > innerWidth,
              left: parseFloat(cs.borderLeftWidth),
              light: (rgb[0] + rgb[1] + rgb[2]) / 3,
              image: cs.backgroundImage,
            };
          });
          assert.ok(look.inside, "the capsule sits inside the window, over the page");
          assert.equal(look.wide, false, "nothing scrolls sideways");
          assert.equal(look.left, 0);
          assert.equal(look.image, "none", "no gradient");
          if (shape.theme === "dark") assert.ok(look.light < 100, `dark in the dark theme: ${look.light}`);
          else assert.ok(look.light > 200, `light in the light theme: ${look.light}`);

          if (process.env.ARTIFACT_DIR) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${shape.viewport.width}-view-ends.png`) });

          // Scroll to bottom: to the page's foot, and it stays there
          await down.click();
          const max = await view.evaluate((v) => v.scrollHeight - v.clientHeight);
          await p.waitForFunction((m) => Math.abs(document.getElementById("view-agents").scrollTop - m) <= 1, max);
          await p.waitForTimeout(700);
          assert.ok(Math.abs((await view.evaluate((v) => v.scrollTop)) - max) <= 1, "the page stays at its foot");
          assert.equal(await down.getAttribute("aria-disabled"), "true", "at the foot, Scroll to bottom has nowhere to go");
          // the page's height is its own: nothing is added at its foot
          assert.equal(await view.evaluate((v) => v.scrollHeight), h.tall, "the page grew under the capsule");
          // at the foot nothing that can be pressed is under the capsule once
          // the pointer has left it: in a wide window's margin it is beside
          // every row; over the page's corner it steps aside, as the last row
          // can't be scrolled out from under it
          await p.mouse.move(r.x + 40, r.y + 40);
          await down.evaluate((b) => b.blur());
          // in sight in the margin; stepped aside, and gone, over the corner
          await p.waitForFunction((m) => {
            const b = document.querySelector(".view-ends");
            return m ? b.classList.contains("on") : getComputedStyle(b).visibility === "hidden";
          }, !!shape.margin);
          const clear = await p.evaluate(() => {
            const box = document.querySelector(".view-ends"), b = box.getBoundingClientRect();
            const under = new Set();
            if (getComputedStyle(box).visibility === "visible") for (let x = b.left + 2; x < b.right; x += 6) for (let y = b.top + 2; y < b.bottom; y += 6) {
              for (const e of document.elementsFromPoint(x, y)) {
                if (box.contains(e)) continue;
                const c = e.closest("button, a, input, label, [role], [tabindex]:not(.view)");
                if (c) under.add(c.outerHTML.slice(0, 80));
              }
            }
            const rows = [...document.querySelectorAll("#view-agents #agents > .row")];
            return { rows: rows.length, under: [...under], beside: rows.every((r) => r.getBoundingClientRect().right <= b.left) };
          });
          assert.ok(clear.rows > 30, `the page's rows: ${clear.rows}`);
          if (shape.margin) {
            assert.equal(await shown(p), true, "in the margin it stays at the foot");
            assert.ok(clear.beside, "in the page's margin, beside every row");
          } else assert.equal(await shown(p), false, "over the corner it steps aside at the foot");
          assert.deepEqual(clear.under, [], "at the foot nothing to press is under the capsule");
          assert.ok(Math.abs((await view.evaluate((v) => v.scrollTop)) - max) <= 1, "the page stays at its foot");

          // the keyboard: a button, so Tab reaches it; Enter takes the page to
          // its top, and it stays there. A little up from the foot it is back.
          await p.mouse.move(r.x + r.width / 2, r.y + r.height / 2);
          await p.mouse.wheel(0, -300);
          await p.waitForFunction(() => document.querySelector(".view-ends.on"));
          await p.mouse.move(r.x + 40, r.y + 40);
          await up.focus();
          await p.keyboard.press("Enter");
          await p.waitForFunction(() => document.getElementById("view-agents").scrollTop === 0);
          await p.waitForTimeout(700);
          assert.equal(await view.evaluate((v) => v.scrollTop), 0, "the page stays at its top");
          // focus keeps it in sight; once focus leaves, at the top it goes
          assert.equal(await shown(p), true, "a focused capsule stays");
          await up.evaluate((b) => b.blur());
          await p.waitForFunction(() => !document.querySelector(".view-ends.on"));

          // code moving the page is still put back: only the reader's scrolls
          // (and the capsule's presses) stand
          await view.evaluate((v) => { v.scrollTop = v.clientHeight * 2; });
          await p.waitForTimeout(300);
          assert.equal(await view.evaluate((v) => v.scrollTop), 0, "a scroll no one asked for is put back");
          assert.equal(await shown(p), false);

          assert.deepEqual(errors, []);
          await ctx.close();
        });
      }
    }
  });
}
