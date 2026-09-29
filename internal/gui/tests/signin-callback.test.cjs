// Run with Node's test runner and Playwright on the module path; see README.md.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium } = require("playwright");

test("DimAgent callback input retries errors and submits only once", async (t) => {
  const browser = await chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" });
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width: 420, height: 640 } });
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const assets = path.resolve(__dirname, "../assets");
  let requests = 0;
  let release, received;
  const pending = new Promise((r) => { release = r; });
  const submitted = new Promise((r) => { received = r; });
  t.after(release);
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"zh",theme:"dark",web:true};' });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "zh", theme: "dark" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: {} });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/signin/test-flow/callback") {
      requests++;
      if (requests === 1) return route.fulfill({ status: 400, json: { error: "wrong callback state" } });
      assert.equal(route.request().postDataJSON().url, "http://localhost:54321/auth/callback?code=good&state=right");
      received();
      await pending;
      return route.fulfill({ status: 204 });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  });
  await page.goto("http://magpie.test/");
  await page.waitForFunction(() => state.settings.lang === "zh");
  await page.evaluate(() => {
    signing = { id: "test-flow", agent: "dimagent", state: "waiting", url: "http://vendor.test/oauth", pasteCallback: true };
    const host = document.createElement("div");
    host.style.width = "100%";
    host.style.cssText += "position:fixed;inset:0 auto auto 0;z-index:1000;";
    host.append(renderSigning({ name: "DimAgent", agent: "dimagent" }));
    document.body.append(host);
  });
  const input = page.getByRole("textbox", { name: "回调 URL" });
  const submit = page.getByRole("button", { name: "完成登录" });
  assert(await submit.isDisabled());
  await input.fill("http://localhost:54321/auth/callback?code=bad&state=wrong");
  await input.press("Enter");
  await page.getByText("wrong callback state").waitFor();
  assert.equal(await input.inputValue(), "http://localhost:54321/auth/callback?code=bad&state=wrong");
  assert(await input.isEnabled());
  assert(await submit.isEnabled());
  await input.fill("http://localhost:54321/auth/callback?code=good&state=right");
  await submit.click();
  await submitted;
  assert(await input.isDisabled());
  assert(await submit.isDisabled());
  await page.evaluate(() => document.querySelector(".callback-form").requestSubmit());
  assert.equal(requests, 2, "a pending request was submitted twice");
  release();
  await page.waitForFunction(() => signing.callbackSubmitted);
  assert(await input.isDisabled());
  const box = await input.boundingBox();
  assert(box.x >= 0 && box.x + box.width <= 420, "callback input overflowed the narrow window");
  if (process.env.ARTIFACT_DIR) {
    await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
    await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, "signin-callback.png") });
  }
  assert.deepEqual(errors, []);
});
