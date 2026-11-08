// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing: in the context window card's head, the icons line up with the
// letters beside them, not with the line they sit in (the owner: the icon and
// the text weren't level). The model's lab icon is centred on its id's
// lowercase letters, and the agent's icon between its name's capitals and its
// lowercase. Measured on the drawn pixels, at 4x: with only x or only H in the
// texts, each one's ink middle is where those letters' middle is.
// Chromium and WebKit, English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (ms) => new Date(ms).toISOString();
function req(agent, model) {
  const seat = { id: "acc@me", provider: "anthropic", name: "Acme", who: "me@example.com", kind: "account", model, known: true };
  const prompt = { window: 200000, tokens: 52000, counted: true, turns: 1, parts: [{ kind: "system", tokens: 52000, items: [{ name: "prompt", tokens: 52000 }] }] };
  return {
    id: 101, seq: 1010, time: iso(Date.now() - 5000), agent, session: "s-1", model, provider: "anthropic",
    order: [seat], tries: [{ id: seat.id, model, start: iso(Date.now() - 5000), done: true, status: 200, ms: 3000 }],
    done: true, status: 200, ms: 3000, tokens: 900, usage: [{ in: 52000, out: 900 }], prompt,
  };
}
function serve(lang, r) {
  const state = { agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return;
      return json({ mine: true, now: new Date().toISOString(), seq: r.seq, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [r] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

// the vertical middle of each part's ink in the head, in CSS px from its top
async function inkMiddles(page, sels) {
  const head = page.locator(".rt-ctx .ctx-head");
  const hb = await head.boundingBox();
  const boxes = [];
  for (const s of sels) boxes.push(await head.locator(s).first().boundingBox());
  const png = (await page.screenshot({ clip: hb })).toString("base64");
  return page.evaluate(async ({ png, boxes, hb }) => {
    const img = new Image();
    img.src = "data:image/png;base64," + png;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.width; c.height = img.height;
    const x = c.getContext("2d");
    x.drawImage(img, 0, 0);
    const k = img.width / hb.width, d = x.getImageData(0, 0, c.width, c.height).data;
    const bg = [d[0], d[1], d[2]];
    return boxes.map((b) => {
      const x0 = Math.round((b.x - hb.x) * k), x1 = Math.round((b.x + b.width - hb.x) * k);
      let top = -1, bot = -1;
      for (let y = 0; y < c.height; y++) {
        for (let xx = x0; xx < x1; xx++) {
          const i = (y * c.width + xx) * 4;
          if (Math.abs(d[i] - bg[0]) + Math.abs(d[i + 1] - bg[1]) + Math.abs(d[i + 2] - bg[2]) > 60) { if (top < 0) top = y; bot = y; break; }
        }
      }
      return (top + bot) / 2 / k;
    });
  }, { png, boxes, hb });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the route's icons are level with their text`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1300, height: 900 }, deviceScaleFactor: 4, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, req("claude", "auto/deepseek-v4.1-flash")));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-ctx .ctx-route .ctx-what .ic").waitFor();
      const sels = [".ctx-who .ic", ".ctx-who-n", ".ctx-what .ic", ".ctx-model"];
      const with_ = async (s) => {
        await page.evaluate((s) => {
          document.querySelector(".rt-ctx .ctx-who-n").textContent = s;
          document.querySelector(".rt-ctx .ctx-model").textContent = s;
        }, s);
        await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
        return inkMiddles(page, sels);
      };
      const [agentIc, nameX, labIc, modelX] = await with_("xxxx");
      const [, nameH, , modelH] = await with_("HHHH");
      const at = JSON.stringify({ agentIc, nameX, nameH, labIc, modelX, modelH });
      assert.ok(nameX > nameH && modelX > modelH, "lowercase sits lower than capitals: " + at);
      assert.ok(Math.abs(labIc - modelX) <= 0.5, "the lab icon is centred on the model id's lowercase: " + at);
      assert.ok(Math.abs(agentIc - (nameX + nameH) / 2) <= 0.5, "the agent's icon is between its name's capitals and lowercase: " + at);
      assert.deepEqual(errors, []);
    });
  }
}
