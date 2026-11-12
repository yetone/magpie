// Run with Node's test runner and Playwright on the module path; see README.md.
// pluoki on X: Tailscale's serve held the gateway's port and the page said
// only "another program". With the gateway not running and the backend
// naming what holds the port (state.gatewayHeld), the Gateway card says so:
// Tailscale gets the way out (serve it from another port that forwards to
// magpie), another program is named, and with nothing read the card keeps
// its old line. The Start button's answer says the same. In every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture(lang, world) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (route.request().method() === "POST" && url.pathname.startsWith("/api/gateway/")) return json(world.answer);
    if (route.request().method() !== "GET" && url.pathname.startsWith("/api/")) return json({});
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, gatewayHeld: world.held });
    if (url.pathname === "/api/providers") {
      return json({ providers: [], gateway: { running: false, mine: false, window: true, url: "http://127.0.0.1:8848", calls: [], groups: [], models: 0 } });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: false, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const ts = { port: "8848", tailscale: true, alt: "8443" };
const words = {
  en: { tail: /Tailscale's serve or funnel has port 8848\. .*tailscale serve --https=8443 http:\/\/127\.0\.0\.1:8848.*another port in Settings/, named: /Port 8848 is in use by nginx \(pid 77\): quit it/, old: /start it with magpie serve/ },
  zh: { tail: /Tailscale 的 serve 或 funnel 占用了端口 8848。.*tailscale serve --https=8443 http:\/\/127\.0\.0\.1:8848.*其他端口/, named: /端口 8848 被 nginx（pid 77）占用：请退出它/, old: /magpie serve/ },
  "zh-TW": { tail: /Tailscale 的 serve 或 funnel 佔用了連接埠 8848。.*tailscale serve --https=8443 http:\/\/127\.0\.0\.1:8848.*其他連接埠/, named: /連接埠 8848 被 nginx（pid 77）佔用：請退出它/, old: /magpie serve/ },
  ja: { tail: /Tailscale の serve または funnel がポート 8848 を使っています。.*tailscale serve --https=8443 http:\/\/127\.0\.0\.1:8848.*別のポート/, named: /ポート 8848 は nginx（pid 77）が使用中です/, old: /magpie serve/ },
  de: { tail: /Tailscales serve oder funnel belegt Port 8848\. .*tailscale serve --https=8443 http:\/\/127\.0\.0\.1:8848.*anderen Port/, named: /Port 8848 wird von nginx \(PID 77\) verwendet/, old: /magpie serve/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: the gateway card names what holds its port`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch());
      t.after(() => browser.close());
      const errors = [];
      const open = async (world) => {
        const context = await browser.newContext({ viewport: { width: 420, height: 520 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, world));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gateway .state").waitFor();
        return { context, page };
      };
      const sub = (page) => page.locator("#gateway .sub");

      // Tailscale's serve, the daemon out of sight
      {
        const { context, page } = await open({ held: ts, answer: {} });
        assert.match(await sub(page).textContent(), w.tail);
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "the page scrolls sideways");
        await context.close();
      }
      // another program, named
      {
        const { context, page } = await open({ held: { port: "8848", name: "nginx", pid: 77 }, answer: {} });
        assert.match(await sub(page).textContent(), w.named);
        await context.close();
      }
      // the holder not read: the card's old line, nothing made up
      {
        const { context, page } = await open({ held: undefined, answer: {} });
        assert.match(await sub(page).textContent(), w.old);
        assert.equal(await page.locator("#gateway .sub.err").count(), 0);
        await context.close();
      }
      // Start's own answer, when the page had not been told
      {
        const world = { held: undefined, answer: { ok: false, reason: "taken", port: "8848", pid: 612, name: "tailscaled", tailscale: true, alt: "8443" } };
        const { context, page } = await open(world);
        await page.locator("#gateway .gw-fix").click();
        await page.locator("#gateway .sub.err").waitFor();
        assert.match(await page.locator("#gateway .sub.err").textContent(), w.tail);
        await context.close();
      }
      assert.deepEqual(errors, []);
    });
  }
}
