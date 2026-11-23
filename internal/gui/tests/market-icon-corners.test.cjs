// Run with Node's test runner and Playwright on the module path; see README.md.
// An opaque logo (a GitHub avatar, as Linear's, Context7's and GitHub's
// servers have) shows no white at its four corners: the tile's white sat
// under the picture and bled through the rounded clip's soft edge. A logo
// with see-through parts still sits on white, and an initial on its grey.
// Dark, where the fringe showed; every language, a narrow window, both
// engines, at 2x. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/tester";
const items = [
  { id: "linear/mcp", name: "Linear", source: "linear", installs: 10, featured: true, icon: "https://github.com/linear.png?size=96", description: "Issues" },
  { id: "acme/mcp", name: "Acme", source: "acme", installs: 10, icon: "https://acme.example/logo.svg", description: "Acme" },
  { id: "none/mcp", name: "None", source: "none", installs: 10, icon: "https://none.example/gone.png", description: "None" },
];

// one black pixel, opaque: object-fit makes it the whole tile
const BLACK = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGNgYGAAAAAEAAH2FzhVAAAAAElFTkSuQmCC", "base64");
const SVG = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle cx="5" cy="5" r="3" fill="#111"/></svg>';

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "dark" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") {
      return json({
        dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
        agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` }],
        instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
      });
    }
    if (url.pathname === "/api/library/market/servers") return json({ items: [] });
    if (url.pathname === "/api/library/market/skills") return json({ items: structuredClone(items) });
    if (url.pathname === "/api/library/icon") {
      const u = url.searchParams.get("u");
      if (u.includes("linear")) return route.fulfill({ body: BLACK, contentType: "image/png" });
      if (u.endsWith(".svg")) return route.fulfill({ body: SVG, contentType: "image/svg+xml" });
      return route.fulfill({ status: 404, body: "" });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

// The tile's pixels as the screen shows them: [r, g, b] by [y][x]
async function pixels(page, tile) {
  // the router row over the market puts it under the fold: wheel to it
  await reach(page, tile);
  const shot = (await tile.screenshot()).toString("base64");
  return page.evaluate(async (b64) => {
    const img = new Image();
    img.src = "data:image/png;base64," + b64;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.width; c.height = img.height;
    const x = c.getContext("2d");
    x.drawImage(img, 0, 0);
    const d = x.getImageData(0, 0, c.width, c.height).data;
    const rows = [];
    for (let y = 0; y < c.height; y++) {
      const row = [];
      for (let i = 0; i < c.width; i++) { const o = (y * c.width + i) * 4; row.push([d[o], d[o + 1], d[o + 2]]); }
      rows.push(row);
    }
    return rows;
  }, shot);
}

const light = ([r, g, b]) => Math.min(r, g, b);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: an opaque logo has no white corners`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 520, height: 760 }, deviceScaleFactor: 2, reducedMotion: "reduce", colorScheme: "dark" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.addInitScript(() => { localStorage.setItem("magpie.libTab", "skills"); });
      t.after(() => browser.close());
      await page.goto("http://magpie.test/?view=library");
      const view = page.locator("#view-library");
      const icon = (id) => view.locator(`.mk[data-market="skills"] .mk-card[data-id="${id}"] .mk-icon`);
      for (const id of ["linear/mcp", "acme/mcp"]) {
        await icon(id).locator("img").evaluate((i) => new Promise((ok) => (i.complete && i.naturalWidth ? ok() : i.addEventListener("load", ok))));
      }
      await icon("none/mcp").and(page.locator(".mono")).waitFor();

      // every corner of the black tile: nothing brighter than the card
      // behind it, give or take the 8% ring
      const px = await pixels(page, icon("linear/mcp"));
      // a tile at a fractional y is shot a pixel row taller than wide
      const h = px.length, w = px[0].length, r = 18; // 9px radius at 2x
      const card = light(px[0][0]);
      for (const [cy, cx] of [[0, 0], [0, w - r], [h - r, 0], [h - r, w - r]]) {
        let most = 0;
        for (let y = cy; y < cy + r; y++) for (let x = cx; x < cx + r; x++) most = Math.max(most, light(px[y][x]));
        assert(most <= card + 30, `corner at ${cx},${cy}: ${most} against the card's ${card}`);
      }

      // a logo with see-through parts is still on white, near its edge too
      const svg = await pixels(page, icon("acme/mcp"));
      const my = svg.length >> 1, mx = svg[0].length >> 1;
      for (const [y, x] of [[12, 12], [6, mx], [my, 6], [svg.length - 7, mx], [my, svg[0].length - 7]]) assert(light(svg[y][x]) >= 240, `svg tile at ${x},${y}: ${svg[y][x]}`);

      // an initial keeps its grey, with no white under it
      const mono = await pixels(page, icon("none/mcp"));
      assert(light(mono[6][6]) < 200, `initial tile: ${mono[6][6]}`);

      assert.deepEqual(errors, []);
    });
  }
}
