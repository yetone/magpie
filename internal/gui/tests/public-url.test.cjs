// Run with Node's test runner and Playwright on the module path; see README.md.
// MAGPIE_PUBLIC_URL is the address other machines are told to reach the
// gateway at: the connect page offers it beside this computer's loopback
// address, and its hint tells the truth about the states — loopback, open to
// the network (listening beyond loopback with nothing shared, which is the
// Docker image's default), and a network address that needs a gateway key.
// English and Chinese, Chromium and WebKit; no backend, the API is faked
// here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const network = "https://magpie.example.com";
const cases = [
  {
    name: "loopback",
    gateway: { url: "http://127.0.0.1:3999", lan: false, open: false, lanURLs: [] },
    note: "loopback",
  },
  {
    name: "open",
    gateway: { url: "http://127.0.0.1:3999", lan: false, open: true, lanURLs: [] },
    note: "open",
  },
  {
    name: "network address",
    gateway: { url: "http://127.0.0.1:3999", lan: true, open: false, lanURLs: [network] },
    note: "loopback",
    picks: "remote",
  },
];
const words = {
  en: {
    loopback: "Loopback only · the key can be anything",
    open: "Open to the network · anyone who reaches it can use any key",
    remote: "Local network · an enabled gateway key is required",
  },
  zh: {
    loopback: "仅限本机回环 · 密钥可以随意填",
    open: "已开放到网络 · 任何能连上的人用任意密钥都能访问",
    remote: "局域网接入需要已启用的网关密钥",
  },
};

function server(language, gateway) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${language}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: language, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the gateway's network address and its connection hint", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const language of ["en", "zh"]) {
      await t.test(language, async (t) => {
        const context = await browser.newContext({ viewport: { width: 1100, height: 1000 }, reducedMotion: "reduce" });
        t.after(() => context.close());
        const page = await context.newPage();
        page.setDefaultTimeout(6000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const gateway = { running: true, mine: true, window: true, models: 0, calls: [], groups: [] };
        await page.route("**/*", server(language, gateway));
        const w = words[language];
        const note = page.locator("#connectNote");
        for (const c of cases) {
          await t.test(c.name, async () => {
            Object.assign(gateway, c.gateway);
            await page.goto("http://magpie.test/?view=gateway");
            await note.waitFor();
            assert.equal(await note.textContent(), w[c.note]);
            if (!c.picks) return;
            await page.locator("#connectAddress").click();
            await page.locator(".proto-menu .pm-item", { hasText: network }).click();
            assert.equal(await note.textContent(), w[c.picks]);
            const base = await page.locator("#connect .val").first().locator("code").textContent();
            assert(base.startsWith(network), base);
          });
        }
        assert.deepEqual(errors, []);
      });
    }
  });
}
