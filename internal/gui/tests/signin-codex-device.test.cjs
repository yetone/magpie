// Run with Node's test runner and Playwright on the module path; see README.md.
// A ChatGPT sign-in by device code (curtinp118 on Discord: magpie on a VPS,
// its web UI in a browser at home; ChatGPT's browser sign-in comes back to
// localhost:1455, the home machine). Opened from another computer, the
// sign-in asks for a device code: the box shows the code and ChatGPT's
// device page, and the account comes in when the code is entered, with no
// address to copy back. "Sign in in the browser instead" goes back to the
// browser's way. Opened on this computer, the browser's way is first and a
// code is a link away. English and Chinese, Chromium and WebKit, narrow;
// no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const LINK = "https://auth.openai.com/oauth/authorize?client_id=app&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback&state=st1";
const DEVICE = "https://auth.openai.com/codex/device";

function server(lang, started) {
  let signing = null, polls = 0;
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/signin") {
      const body = route.request().postDataJSON();
      started.push(body);
      signing = body.site === "device"
        ? { id: "dv" + started.length, agent: "codex", state: "waiting", url: DEVICE, code: "WG0E-J29UL" }
        : { id: "cx" + started.length, agent: "codex", state: "waiting", url: LINK, pasteCallback: true };
      polls = 0;
      return json(signing);
    }
    if (signing && url.pathname === "/api/signin/" + signing.id) {
      // a device code entered after a few polls; the browser's way waits
      if (signing.code && ++polls > 3) signing = { ...signing, state: "done", user: "dc@example.com", using: true };
      return json(signing);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: {
    say: "magpie opened ChatGPT's device page. Enter this code there; the account shows up here as soon as you're done.",
    browser: "Sign in in the browser instead", code: "Sign in with a code instead", field: "Callback URL",
  },
  zh: {
    say: "已打开 ChatGPT 设备登录页，请在其中输入下方验证码；完成后账号会出现在这里。",
    browser: "改在浏览器中登录", code: "改用验证码登录", field: "回调 URL",
  },
};

async function open(t, engine, lang, host, name) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width: 420, height: 800 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], started = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, started));
  // the vendor's pages, opened in a new window, get an empty page
  await page.context().route((url) => url.hostname !== host, (route) => route.fulfill({ contentType: "text/html", body: "" }));
  await page.goto(`http://${host}/?view=providers`);
  await page.locator("#addProvider").click();
  const sheet = page.locator("#addSheet");
  await sheet.locator('.tile[data-pick="ChatGPT"]').click();
  return { page, box: sheet.locator(".signing"), errors, started };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = W[lang];
    test(`${engine} ${lang}: opened from another computer, ChatGPT signs in by a device code`, async (t) => {
      const { page, box, errors, started } = await open(t, engine, lang, "vps.example", "signin-codex-device");
      await box.getByText(w.say).waitFor();
      assert.deepEqual(started, [{ agent: "codex", site: "device" }]);
      assert(await box.getByText("WG0E-J29UL", { exact: true }).isVisible(), "the code is shown");
      assert(await box.getByText(DEVICE, { exact: true }).isVisible(), "the device page is shown");
      assert.equal(await box.getByRole("textbox", { name: w.field }).count(), 0, "no address to paste back");
      const back = box.getByRole("button", { name: w.browser });
      assert(await back.isVisible());
      // the code and its copy button inside the box at 420px
      const fit = await box.evaluate((b) => {
        const r = b.getBoundingClientRect(), c = b.querySelector(".devcode").getBoundingClientRect();
        return c.left >= r.left - 0.5 && c.right <= r.right + 0.5;
      });
      assert(fit, "the code fits its box");
      await page.waitForFunction(() => document.querySelector("#status").textContent.includes("dc@example.com") && !document.querySelector("#addSheet .devcode"));
      assert.equal(started.length, 1, "started once");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the browser's way, from the device code's box`, async (t) => {
      const { box, errors, started } = await open(t, engine, lang, "vps.example", "signin-codex-device-back");
      await box.getByText(w.say).waitFor();
      await box.getByRole("button", { name: w.browser }).click();
      await box.getByRole("textbox", { name: w.field }).waitFor();
      assert.deepEqual(started.map((s) => s.site), ["device", "browser"]);
      assert(await box.getByRole("button", { name: w.code }).isVisible(), "a code is a link away again");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: on this computer, the browser's way first and a code a link away`, async (t) => {
      const { box, errors, started } = await open(t, engine, lang, "localhost", "signin-codex-local");
      await box.getByRole("textbox", { name: w.field }).waitFor();
      assert.deepEqual(started, [{ agent: "codex" }]);
      await box.getByRole("button", { name: w.code }).click();
      await box.getByText(w.say).waitFor();
      assert.deepEqual(started.map((s) => s.site), [undefined, "device"]);
      assert.deepEqual(errors, []);
    });
  }
}
