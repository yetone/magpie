// Run with Node's test runner and Playwright on the module path; see README.md.
// An "api" sign-in method whose own question asks the key (keyAsked: it has
// prompts and an authorize that turns the answers into the key, as Devin's
// session token and inkstone's API key ways do) is signed in with the
// answer alone: no second key field after the question, whose entry went
// nowhere. A rejected answer is asked again on Try again. An "api" way
// with a question but no keyAsked still asks its key after it. Every
// language, both engines, 1100 and 420 wide; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const plugin = { id: "fakeco", pid: "fakeco", name: "FakeCo", icon: "generic", spec: "opencode-fakeco-auth", signedIn: false, models: 3,
  methods: [{ type: "api", label: "FakeCo session token", keyAsked: true }, { type: "api", label: "FakeCo site key" }] };

function server(lang, asked, state) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const body = () => route.request().postDataJSON();
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [plugin] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugin-signin/prompt") {
      const b = body();
      asked.push(["prompt", b]);
      const inputs = { ...(b.inputs || {}) };
      if (b.key) inputs[b.key] = b.value;
      if (b.method === 0 && !inputs.token) return json({ prompt: { type: "password", key: "token", message: "Your FakeCo session token", placeholder: "fs-..." }, inputs });
      if (b.method === 1 && !inputs.site) return json({ prompt: { type: "text", key: "site", message: "Your FakeCo site" }, inputs });
      return json({ prompt: null, inputs });
    }
    if (url.pathname === "/api/plugin-signin") {
      const b = body();
      asked.push(["signin", b]);
      if (b.inputs?.token === "fs-bad") return route.fulfill({ status: 400, json: { error: "that token is revoked" } });
      state.signed = true;
      return json({ agent: "fakeco", state: "done", user: "fakeco", using: true });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function open(page, way) {
  await page.goto("http://magpie.test/?view=providers");
  await page.locator("#addProvider").click();
  const sheet = page.locator("#addSheet");
  const tile = sheet.locator('.tile[data-pick="FakeCo"]');
  await reach(page, tile);
  await tile.click();
  const box = sheet.locator(".signing");
  const b = box.locator("button", { hasText: way });
  await reach(page, b);
  await b.click();
  return box;
}

// until waits for what the fake API was sent, not for the page: the
// field goes as soon as it is answered, before the sign-in is posted
async function until(ok, what) {
  for (const end = Date.now() + 5000; !ok(); ) {
    if (Date.now() > end) throw new Error("waited for " + what);
    await new Promise((r) => setTimeout(r, 20));
  }
}

async function answer(page, box, label, value) {
  const f = box.getByLabel(label, { exact: true });
  await f.waitFor();
  await reach(page, f);
  await f.fill(value);
  await f.press("Enter");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an api way whose question asks the key asks no second key", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} ${width}`, async () => {
          const ctx = await browser.newContext({ viewport: { width, height: 800 } });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [], asked = [], state = {};
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, asked, state));
          let box = await open(page, "FakeCo session token");

          // a rejected token: the sign-in is sent with the answer alone,
          // and Try again asks the question again
          await answer(page, box, "Your FakeCo session token", "fs-bad");
          await box.locator(".s", { hasText: "that token is revoked" }).waitFor();
          assert.equal(await box.getByLabel("FakeCo session token", { exact: true }).count(), 0, "a key field after the question");
          const again = box.locator("button.primary");
          await reach(page, again);
          await again.click();
          await answer(page, box, "Your FakeCo session token", "fs-good");
          await until(() => state.signed, "the second sign-in");
          const signins = asked.filter(([k]) => k === "signin").map(([, b]) => b);
          assert.deepEqual(signins, [
            { provider: "fakeco", method: 0, inputs: { token: "fs-bad" }, key: "" },
            { provider: "fakeco", method: 0, inputs: { token: "fs-good" }, key: "" },
          ]);
          assert.ok(state.signed);

          // a way with a question and no keyAsked still asks its key
          await page.keyboard.press("Escape");
          await page.locator("#modal").waitFor({ state: "hidden" });
          box = await open(page, "FakeCo site key");
          await answer(page, box, "Your FakeCo site", "eu");
          const key = box.getByLabel("FakeCo site key", { exact: true });
          await key.waitFor();
          assert.equal(await key.getAttribute("type"), "password");
          assert.equal(asked.filter(([k]) => k === "signin").length, 2, "signed in before the key was typed");
          await key.fill("sk-site");
          await key.press("Enter");
          await until(() => asked.filter(([k]) => k === "signin").length === 3, "the site key's sign-in");
          assert.deepEqual(asked.filter(([k]) => k === "signin").at(-1)[1], { provider: "fakeco", method: 1, inputs: { site: "eu" }, key: "sk-site" });
          assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
          assert.deepEqual(errors, []);
          await ctx.close();
        });
      }
    }
  });
}
