// Run with Node's test runner and Playwright on the module path; see README.md.
// aublurer on X, Omarchy (Arch + Hyprland): in the Library's MCP list the
// wheel didn't scroll the list, and the scrollbar's drag didn't follow the
// pointer. A scroll is heard at the frame after it, and the view guard
// ("where the reader is", gui-shell.md) took a scroll as the reader's only
// within 250ms of their wheel or drag. Where a frame takes longer than that
// (WebKitGTK drawing the long list, a busy machine), every scroll the wheel
// made came after its window and was put back. The window now lasts its next
// few frames as well, and a key's scroll, which WebKit plays over frames, is
// followed as a fling is. Here each frame, the one right after a wheel or a
// lift too, is made to take 400ms: the wheel and the bar's drag (its scrolls
// heard after the lift in WebKit) still move the page as far as they ask, and
// a scroll by code, at rest, is still put back. Page Down whose frame takes
// 600ms (past its 400ms) still scrolls all the way. The Library's MCP list and the Agents page (the
// guard is every page's), every language, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

// MAGPIE_SLOW_ASSETS: an older assets folder, to see the test fail on it
const assets = process.env.MAGPIE_SLOW_ASSETS || path.resolve(__dirname, "../assets");
const HOME = "/home/au";
const AGENTS = Array.from({ length: 14 }, (_, i) => ({
  id: "a" + i, name: "Agent " + i, path: "/t/a" + i + ".json", icon: "generic", wired: true,
  fields: [{ key: "model", label: "model", value: "m", options: [{ value: "m", label: "M", ref: "a/m" }] }],
}));
const library = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` }],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
  servers: Array.from({ length: 30 }, (_, i) => ({ name: "server" + String(i).padStart(2, "0"), transport: "stdio", command: "some-mcp", agents: ["claude"] })),
});

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: AGENTS, profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(library());
    if (url.pathname === "/api/library/mcp/check") return json({ servers: {} });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const PAGES = [
  { view: "library", ready: "#view-library .lib-body:not(.lib-skel) .lib-row" },
  { view: "agents", ready: "#view-agents .ag-lead" },
];
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const FRAME = 400; // ms each frame takes while the page is slow

// each frame takes FRAME ms of the page's own thread, as a long page drawn on
// the CPU does, and so does the frame right after a wheel or a lift: the
// page's own listeners (the guard's, at capture) have heard it, and the
// scrolls it makes are heard only after FRAME ms, past the 250ms the old
// guard gave them. Without that block, whether the wheel's scroll lands
// inside a slow frame is chance.
// A key's window is 400ms, so the key's own frame takes KEY_FRAME (__slowKey).
// Only that one: with every frame slow, Chromium never starts Page Down's
// scroll on the Library at all, put back or not.
const KEY_FRAME = 600;
const SLOW = `{
  const blockFor = (ms) => () => { const t = performance.now(); while (performance.now() - t < ms); };
  const block = blockFor(${FRAME}), blockKey = blockFor(${KEY_FRAME});
  window.__slow = (on) => {
    window.__slowOn = on;
    for (const type of ["wheel", "pointerup"]) {
      if (on) addEventListener(type, block, { passive: true });
      else removeEventListener(type, block, { passive: true });
    }
    if (on) requestAnimationFrame(function f() {
      if (!window.__slowOn) return;
      block();
      requestAnimationFrame(f);
    });
  };
  window.__slowKey = (on) => {
    if (on) addEventListener("keydown", blockKey, { passive: true });
    else removeEventListener("keydown", blockKey, { passive: true });
  };
}`;

// the page at rest: its scrollTop the same over looks further apart than a
// slow frame
async function rested(p, view, ms = 12000) {
  let last = NaN, same = 0;
  const until = Date.now() + ms;
  while (Date.now() < until) {
    const t = await view.evaluate((v) => v.scrollTop);
    same = t === last ? same + 1 : 0;
    if (same >= 3) return t;
    last = t;
    await p.waitForTimeout(FRAME + 100);
  }
  return last;
}

async function settled(p, view) {
  let last = -1;
  for (let i = 0; i < 40; i++) {
    const h = await view.evaluate((v) => v.scrollHeight);
    if (h === last) break;
    last = h;
    await p.waitForTimeout(150);
  }
  return view.evaluate((v) => ({ over: v.scrollHeight - v.clientHeight, bar: v.offsetWidth - v.clientWidth, h: v.clientHeight }));
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the wheel and the bar's drag scroll a page whose frames are slow", { timeout: 600000 }, async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    // headless Chromium hides scrollbars unless asked not to
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium", ignoreDefaultArgs: ["--hide-scrollbars"] }));
    t.after(() => browser.close());
    for (const lang of LANGS) {
      for (const page of PAGES) {
        await t.test(`${lang} ${page.view}`, async () => {
          const ctx = await browser.newContext({ viewport: { width: 760, height: 560 }, reducedMotion: "reduce" });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
          await ctx.addInitScript(SLOW);
          const p = await ctx.newPage();
          p.setDefaultTimeout(20000);
          p.setDefaultNavigationTimeout(60000);
          const errors = [];
          p.on("pageerror", (e) => { if (!/^ResizeObserver loop/.test(e.message)) errors.push(e.message); });
          await p.route("http://magpie.test/**", server(lang));
          const view = p.locator("#view-" + page.view);
          const top = () => view.evaluate((v) => v.scrollTop);
          await p.goto(`http://magpie.test/?view=${page.view}`);
          await p.locator(page.ready).first().waitFor();
          const shape = await settled(p, view);
          assert(shape.over > 500, `${page.view} must be tall enough to scroll: ${JSON.stringify(shape)}`);
          assert(shape.bar > 0, `${page.view} must draw a scrollbar: ${JSON.stringify(shape)}`);
          const r = await view.boundingBox();
          await p.mouse.move(r.x + r.width / 2, r.y + r.height / 2);
          await rested(p, view, 3000);
          assert.equal(await top(), 0);

          // the wheel, every frame slow
          await p.evaluate(() => window.__slow(true));
          await p.mouse.wheel(0, 300);
          const wheeled = await rested(p, view);
          await p.evaluate(() => window.__slow(false));
          assert.ok(wheeled >= 250, `the wheel scrolled the page 300px down with its frames slow: at ${wheeled}`);

          // the bar's drag, every frame slow, lifted at once: WebKit sends
          // its scrolls only after the lift. Where the page is when the drag
          // ends is where it stays
          const s = await view.evaluate((v) => ({ top: v.scrollTop, h: v.clientHeight, sh: v.scrollHeight }));
          const thumb = (s.h * s.h) / s.sh;
          const x = r.x + r.width - shape.bar / 2, y = r.y + (s.top / shape.over) * (s.h - thumb) + thumb / 2;
          await p.mouse.move(x, y);
          await p.evaluate(() => window.__slow(true));
          await p.mouse.down();
          for (let i = 1; i <= 6; i++) await p.mouse.move(x, y + i * 12, { steps: 2 });
          await p.mouse.up();
          const dragged = await rested(p, view);
          await p.evaluate(() => window.__slow(false));
          const want = wheeled + 72 * (s.sh - s.h) / (s.h - thumb);
          assert.ok(dragged >= wheeled + (want - wheeled) * 0.7, `the bar's drag moved the page with its frames slow: from ${wheeled} to ${dragged}, ${Math.round(want)} asked`);

          // and the wheel back up, frames fast again
          await p.mouse.move(r.x + r.width / 2, r.y + r.height / 2);
          await p.mouse.wheel(0, -200);
          const up = await rested(p, view, 4000);
          assert.ok(up < dragged - 100, `the wheel scrolls up: from ${dragged} to ${up}`);

          // a scroll by code, at rest, is still put back
          await p.waitForTimeout(600);
          const code = up > 200 ? up - 150 : up + 150;
          await view.evaluate((v, y) => { v.scrollTop = y; }, code);
          await p.waitForTimeout(400);
          assert.equal(await top(), up, `a scroll code asked for is put back: at ${up}, asked for ${code}`);

          // a key whose frame is slow: Page Down from a button in the page
          // (the window a key opens, not a wheel's, is what hears it)
          await p.mouse.wheel(0, -2000);
          await rested(p, view, 4000);
          await p.waitForTimeout(600);
          const from = await top();
          assert.equal(from, 0, "the wheel goes back to the top");
          await view.locator("button:visible:not(.opt)").first().focus();
          await p.waitForTimeout(600);
          assert.equal(await top(), from, "focusing a button in sight doesn't scroll the page");
          await p.evaluate(() => window.__slowKey(true));
          await p.keyboard.press("PageDown");
          const keyed = await rested(p, view);
          await p.evaluate(() => window.__slowKey(false));
          assert.ok(keyed >= from + 200, `Page Down scrolled the page with its frame slow: from ${from} to ${keyed}`);
          assert.deepEqual(errors, []);
          await ctx.close();
        });
      }
    }
  });
}
