// The home page's Latest/Stable control in real browsers: each button's
// words sit in the middle of it, measured on the ink, not on a line box.
// Chromium and WebKit, every home page language, wide and phone widths,
// light and dark. Not a *.test.mjs, so CI's `node --test site/*.test.mjs`
// (no browsers there) leaves it out; run it by hand where Playwright is on
// NODE_PATH:
//   node site/chan.browser.mjs [screenshot-dir]
import { createServer } from "node:http";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { LANGS } from "./i18n.js";

const { chromium, webkit } = createRequire(import.meta.url)("playwright");
const pub = new URL("./public/", import.meta.url);
const shots = process.argv[2];
if (shots) await mkdir(shots, { recursive: true });

const types = { html: "text/html; charset=utf-8", js: "text/javascript", css: "text/css", svg: "image/svg+xml", png: "image/png" };
const server = createServer(async (req, res) => {
  let p = decodeURIComponent(new URL(req.url, "http://x").pathname).replace(/^\/+/, "") || "index.html";
  if (!/\.\w+$/.test(p)) p += ".html";
  try {
    const body = await readFile(new URL(p, pub));
    res.writeHead(200, { "content-type": types[p.split(".").pop()] || "application/octet-stream" });
    res.end(body);
  } catch {
    res.writeHead(404);
    res.end();
  }
});
await new Promise((ok) => server.listen(0, "127.0.0.1", ok));
const base = `http://127.0.0.1:${server.address().port}`;

const langs = { en: { html: "en", dict: { "chan.latest": "Latest", "chan.stable": "Stable" } }, ...LANGS };
// the most the ink's gap above may differ from the gap below, in CSS px
const SLACK = 1;
let fails = 0, runs = 0;

for (const [ename, engine] of [["chromium", chromium], ["webkit", webkit]]) {
  const browser = await engine.launch();
  for (const scheme of ["light", "dark"]) {
    for (const width of [1280, 390]) {
      for (const [lang, L] of Object.entries(langs)) {
        const name = `${ename} ${scheme} ${width} ${lang}`;
        runs++;
        const ctx = await browser.newContext({ viewport: { width, height: 800 }, colorScheme: scheme, deviceScaleFactor: 4 });
        const page = await ctx.newPage();
        try {
          await page.route(/\/api\/|releases|\.json/, (r) => r.abort());
          await page.goto(base + "/", { waitUntil: "load" });
          await page.evaluate(({ html, latest, stable }) => {
            document.documentElement.lang = html;
            const chan = document.querySelector(".chan");
            chan.querySelector('[data-ch="latest"] b').textContent = latest;
            chan.querySelector('[data-ch="stable"] b').textContent = stable;
            chan.querySelectorAll(".v").forEach((v) => (v.textContent = "0.1.4637"));
            chan.hidden = false;
            chan.scrollIntoView({ block: "center" });
          }, { html: L.html, latest: L.dict["chan.latest"], stable: L.dict["chan.stable"] });
          await page.evaluate(() => document.fonts.ready);
          for (const ch of ["latest", "stable"]) {
            const btn = page.locator(`.chan [data-ch="${ch}"]`);
            const png = await btn.screenshot({ animations: "disabled" });
            if (shots) await writeFile(`${shots}/${name.replaceAll(" ", "-")}-${ch}.png`, png);
            // the rows whose pixels differ from the button's own background
            const ink = await page.evaluate(async (b64) => {
              const img = new Image();
              img.src = "data:image/png;base64," + b64;
              await img.decode();
              const c = document.createElement("canvas");
              c.width = img.width; c.height = img.height;
              const g = c.getContext("2d");
              g.drawImage(img, 0, 0);
              const d = g.getImageData(0, 0, c.width, c.height).data;
              const px = (x, y) => { const i = (y * c.width + x) * 4; return [d[i], d[i + 1], d[i + 2]]; };
              // the background at the button's middle-left, inside its rounded corner
              const bg = px(Math.round(c.width * 0.02) + 8, c.height >> 1);
              const x0 = Math.round(c.width * 0.08), x1 = Math.round(c.width * 0.92);
              let top = -1, bottom = -1;
              for (let y = 0; y < c.height; y++) {
                for (let x = x0; x < x1; x++) {
                  const p = px(x, y);
                  if (Math.abs(p[0] - bg[0]) + Math.abs(p[1] - bg[1]) + Math.abs(p[2] - bg[2]) > 90) {
                    if (top < 0) top = y;
                    bottom = y;
                    break;
                  }
                }
              }
              return { top, bottom, h: c.height };
            }, png.toString("base64"));
            if (ink.top < 0) throw new Error(`${ch}: no ink found`);
            const above = ink.top / 4, below = (ink.h - 1 - ink.bottom) / 4;
            const off = above - below;
            if (Math.abs(off) > SLACK) throw new Error(`${ch}: ink ${above}px from the top, ${below}px from the bottom (off by ${off}px)`);
            console.log(`ok   ${name} ${ch}: above ${above} below ${below}`);
          }
        } catch (e) {
          fails++;
          console.log(`FAIL ${name}: ${e.message}`);
        }
        await ctx.close();
      }
    }
  }
  await browser.close();
}
server.close();
console.log(`${runs - fails}/${runs} ok`);
process.exit(fails ? 1 : 0);
