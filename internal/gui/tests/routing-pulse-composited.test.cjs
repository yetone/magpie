// Routing's live dots pulse on the compositor (unscientific9 on Discord:
// Routing stuck with many requests in flight). The pulse was a box-shadow
// keyframe, which repainted the page and recalculated its style every
// frame for as long as any request was under way: 64 in flight kept the
// renderer's main thread about a third busy. Each live dot's pulse is now
// a disk that grows and fades by transform and opacity alone. Checked with
// the real stylesheets in both engines: every endless animation of a live
// dot moves only those two, each live dot does pulse, a muted dot's disk
// lies behind it (its centre keeps its colour), and the dot doesn't move.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = path.resolve(__dirname, "../assets");

// each: its name, its markup, the dot, the pseudo-element drawing it, and
// whether the dot is muted (its pulse must go behind it)
const DOTS = [
  ["account", '<ul class="rt-accts"><li class="on now"><span class="dot"></span><b>acct</b><em>answering</em></li></ul>', ".dot", null, false],
  ["live dot", '<span class="rt-livedot on"></span>', ".rt-livedot", null, false],
  ["waiting try", '<div class="rt-req wait"><span class="to"><i></i>waiting</span></div>', ".to i", null, true],
  ["waiting probe", '<div class="rt-probe wait"><span class="to"><i></i>waiting</span></div>', ".to i", null, true],
  ["waiting preview", '<span class="ps-node ps-dst wait"><i></i><span class="ps-name">a</span></span>', ".ps-dst > i", null, true],
  ["waiting step", '<ul class="rt-steps"><li class="wait">asking</li></ul>', ".rt-steps li", "::before", true],
];
// what the compositor animates without a paint
const COMPOSITED = new Set(["transform", "opacity"]);

const page = (theme) => `<!doctype html><html data-theme="${theme}"><head><meta charset="utf-8">
<link rel="stylesheet" href="/app.css"><link rel="stylesheet" href="/plugins/routing/routing.css">
<style>body { margin: 0; padding: 24px; background: var(--card); } .one { padding: 14px 20px; } .rt-steps, .rt-accts { margin: 0; padding: 0; list-style: none; }</style>
</head><body>${DOTS.map(([, html]) => `<div class="one">${html}</div>`).join("")}</body></html>`;

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  test(engine + ": Routing's live dots pulse by transform and opacity only", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const theme of ["light", "dark"]) {
      await t.test(theme, async () => {
        const context = await browser.newContext({ viewport: { width: 600, height: 700 }, colorScheme: theme, reducedMotion: "no-preference" });
        t.after(() => context.close());
        const p = await context.newPage();
        await p.route("**/*", async (route) => {
          const url = new URL(route.request().url());
          if (url.pathname === "/") return route.fulfill({ contentType: "text/html", body: page(theme) });
          if (url.pathname.endsWith(".css")) return route.fulfill({ contentType: "text/css", body: await fs.readFile(path.join(assets, url.pathname)) });
          return route.fulfill({ status: 404, body: "" });
        });
        await p.goto("http://magpie.test/");

        for (let i = 0; i < DOTS.length; i++) {
          const [name, , sel, pseudo, muted] = DOTS[i];
          const one = p.locator(`.one >> nth=${i}`);
          const r = await one.evaluate((box) => box.getAnimations({ subtree: true })
            .filter((a) => a.effect.getComputedTiming().iterations === Infinity)
            .map((a) => ({ name: a.animationName, props: [...new Set(a.effect.getKeyframes().flatMap((k) => Object.keys(k)))]
              .filter((k) => !["offset", "computedOffset", "easing", "composite"].includes(k)) })));
          assert.ok(r.length > 0, `${name}: the live dot pulses`);
          for (const a of r) {
            const off = a.props.filter((k) => !COMPOSITED.has(k));
            assert.deepEqual(off, [], `${name}: ${a.name} animates ${off.join(", ")}, which repaints every frame`);
          }
          if (!muted) continue;
          // the disk at its start, the dot's own size and fully drawn: a
          // muted dot's centre is still the dot's colour, so it lies behind
          const centre = async () => {
            const b = await one.locator(sel).first().evaluate((e, pseudo) => {
              const r = e.getBoundingClientRect(), s = getComputedStyle(e, pseudo);
              const x = pseudo ? r.left + parseFloat(s.left) + parseFloat(s.width) / 2 : r.left + r.width / 2;
              const y = pseudo ? r.top + parseFloat(s.top) + parseFloat(s.height) / 2 : r.top + r.height / 2;
              return { x: Math.round(x), y: Math.round(y) };
            }, pseudo);
            const png = await p.screenshot({ clip: { x: b.x, y: b.y, width: 1, height: 1 } });
            return p.evaluate(async (b64) => {
              const img = new Image();
              img.src = "data:image/png;base64," + b64;
              await img.decode();
              const c = document.createElement("canvas");
              c.width = c.height = 1;
              const g = c.getContext("2d");
              g.drawImage(img, 0, 0);
              return [...g.getImageData(0, 0, 1, 1).data.slice(0, 3)];
            }, png.toString("base64"));
          };
          await one.evaluate((box) => { for (const a of box.getAnimations({ subtree: true })) { a.pause(); a.currentTime = 0; } });
          const pulsing = await centre();
          await one.evaluate((box) => { for (const a of box.getAnimations({ subtree: true })) a.cancel(); });
          const still = await centre();
          const gap = Math.max(...pulsing.map((v, k) => Math.abs(v - still[k])));
          assert.ok(gap <= 2, `${name}: the pulse tints the dot it rings (${still} → ${pulsing})`);
        }
        // the dots keep their places when they start to pulse
        const at = () => p.evaluate((sels) => sels.map((s, i) => { const r = document.querySelectorAll(".one")[i].querySelector(s).getBoundingClientRect(); return [r.left, r.top, r.width]; }), DOTS.map((d) => d[2]));
        const pulsing = await at();
        await p.evaluate(() => { for (const e of document.querySelectorAll(".now, .on, .wait")) e.classList.remove("now", "on", "wait"); });
        assert.deepEqual(await at(), pulsing, "a dot moved when it started to pulse");
      });
    }
  });
}
