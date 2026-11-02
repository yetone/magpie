// Run with Node's test runner and Playwright on the module path; see README.md.
// Aisiverto on X: Codex was wired to magpie, magpie was quit, and Codex
// failed with "502 Bad Gateway" five times with nothing to say why. A Quit
// the user chooses while agents go through magpie now asks first, in the
// app's own sheet (quitask.js; AskQuit in quitask_app.go opens the window
// on /?ask=quit or calls window.askQuit): it names each agent cut off,
// says that closing the window keeps magpie running (the menu bar on a
// Mac, the system tray elsewhere), and offers Quit, Disconnect and quit,
// and Cancel. Cancel posts nothing; Quit posts window/quit?now=1, which
// quits without asking again; Disconnect and quit disconnects each agent
// in turn and then quits, and a disconnect that fails keeps magpie open.
// With nothing connected any more it quits at once. Don't ask again
// (zhujiu on Discord), above the buttons and unticked, goes with the Quit
// chosen as noAsk=1 (the server saves settings.QuitNoAsk), and its tooltip
// names Settings › General › Ask before quitting. In every language and
// both engines; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const two = [
  { id: "codex", name: "Codex", icon: "generic" },
  { id: "claude", name: "Claude Code", icon: "generic" },
];
const keys = [
  "Quit magpie?",
  "{agent} goes through magpie, so it stops working while magpie is closed.",
  "These agents go through magpie, so they stop working while magpie is closed.",
  "Closing the window keeps magpie running in the menu bar.",
  "Closing the window keeps magpie running in the system tray.",
  "Disconnect and quit",
  "Puts back each agent's own settings first, so they work without magpie",
  "Quit",
  "Cancel",
  "Don't ask again",
  "Settings › General › Ask before quitting turns it back on",
];

// until waits for f to hold (the posts the page makes), at most 5s
const until = async (f) => { for (let i = 0; i < 250 && !f(); i++) await new Promise((r) => setTimeout(r, 20)); };

function server(lang, posts, o) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/quit") return json({ agents: o.agents });
    if (req.method() === "POST" && url.pathname.startsWith("/api/agents/disconnect/")) {
      posts.push(url.pathname);
      if (o.fail === url.pathname.split("/").pop()) return json({ error: "config is locked" }, 500);
      return json(state);
    }
    if (req.method() === "POST" && url.pathname === "/api/window/quit") { posts.push(url.pathname + url.search); return json({}); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const open = async (t, o, platform = "Win32", url = "/?ask=quit", width = 980) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [], dialogs = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { dialogs.push(d.message()); d.dismiss(); });
      await page.addInitScript((p) => Object.defineProperty(navigator, "platform", { get: () => p }), platform);
      await page.route("**/*", server(lang, posts, o));
      await page.goto("http://magpie.test" + url);
      const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
      t.after(() => { assert.deepEqual(errors, []); assert.deepEqual(dialogs, [], "no native dialog"); });
      return { page, posts, tr };
    };

    test(`${engine} ${lang}: a Quit with agents connected names them and asks`, async (t) => {
      const { page, posts, tr } = await open(t, { agents: two });
      if (lang !== "en") {
        const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang][k]), [lang, keys]);
        assert.deepEqual(missing, [], "every string has its " + lang);
      }
      const ask = page.locator(".editor.quit-ask");
      await ask.waitFor();
      assert.equal(new URL(page.url()).searchParams.get("ask"), null, "the ask is spent, a reload doesn't ask again");
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), await tr("Quit magpie?"));
      assert.deepEqual(await ask.locator(".quit-agents li").allTextContents(), ["Codex", "Claude Code"]);
      const text = await ask.textContent();
      assert.ok(text.includes(await tr("These agents go through magpie, so they stop working while magpie is closed.")));
      assert.ok(text.includes(await tr("Closing the window keeps magpie running in the system tray.")), "off the Mac, the tray");
      const quit = ask.locator(".bar button.primary");
      assert.equal((await quit.textContent()).trim(), await tr("Quit"));
      assert.ok(await quit.evaluate((b) => b === document.activeElement), "Quit has the focus");
      assert.ok(await ask.locator(".bar button", { hasText: await tr("Disconnect and quit") }).getAttribute("title"), "it says what it does");

      // Cancel posts nothing
      await ask.locator(".bar button", { hasText: await tr("Cancel") }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(posts, []);

      // asked again (the window was up), Escape is a Cancel too
      await page.evaluate(() => window.askQuit());
      await ask.waitFor();
      await page.keyboard.press("Escape");
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(posts, []);

      // Quit quits, without asking again
      await page.evaluate(() => window.askQuit());
      await ask.waitFor();
      await quit.click();
      await until(() => posts.length);
      assert.deepEqual(posts, ["/api/window/quit?now=1"]);
    });

    test(`${engine} ${lang}: Disconnect and quit puts each agent back, then quits`, async (t) => {
      const { page, posts, tr } = await open(t, { agents: two }, "MacIntel", "/", 420);
      await page.locator("#status").waitFor({ state: "attached" });
      await page.evaluate(() => window.askQuit());
      const ask = page.locator(".editor.quit-ask");
      await ask.waitFor();
      assert.ok((await ask.textContent()).includes(await tr("Closing the window keeps magpie running in the menu bar.")), "on a Mac, the menu bar");
      // narrow: the bar's buttons stay inside the sheet
      const [box, bar] = await Promise.all([ask.boundingBox(), ask.locator(".bar").boundingBox()]);
      assert.ok(bar.x >= box.x - 1 && bar.x + bar.width <= box.x + box.width + 1, "the bar fits a narrow window");
      await ask.locator(".bar button", { hasText: await tr("Disconnect and quit") }).click();
      await until(() => posts.length >= 3);
      assert.deepEqual(posts, ["/api/agents/disconnect/codex", "/api/agents/disconnect/claude", "/api/window/quit?now=1"]);
    });

    test(`${engine} ${lang}: Don't ask again goes with the Quit, narrow too`, async (t) => {
      const { page, posts, tr } = await open(t, { agents: two }, "Win32", "/?ask=quit", 420);
      const ask = page.locator(".editor.quit-ask");
      await ask.waitFor();
      const never = ask.locator("label.quit-never");
      assert.equal((await never.textContent()).trim(), await tr("Don't ask again"));
      assert.equal(await never.getAttribute("title"), await tr("Settings › General › Ask before quitting turns it back on"));
      const box = never.locator("input[type=checkbox]");
      assert.equal(await box.isChecked(), false, "unticked at first");
      const [sheet, line, bar] = await Promise.all([ask.boundingBox(), never.boundingBox(), ask.locator(".bar").boundingBox()]);
      assert.ok(line.x >= sheet.x - 1 && line.x + line.width <= sheet.x + sheet.width + 1, "it fits a narrow window");
      assert.ok(line.y + line.height <= bar.y + 1, "above the buttons");
      // ticked then Cancel saves nothing
      await never.click();
      assert.equal(await box.isChecked(), true);
      await ask.locator(".bar button", { hasText: await tr("Cancel") }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(posts, []);
      // asked again, it is unticked; ticked, the Quit carries it
      await page.evaluate(() => window.askQuit());
      await ask.waitFor();
      assert.equal(await box.isChecked(), false, "a new question starts unticked");
      await never.click();
      await ask.locator(".bar button.primary").click();
      await until(() => posts.length);
      assert.deepEqual(posts, ["/api/window/quit?now=1&noAsk=1"]);
    });

    test(`${engine} ${lang}: Don't ask again goes with Disconnect and quit too`, async (t) => {
      const { page, posts, tr } = await open(t, { agents: two.slice(0, 1) });
      const ask = page.locator(".editor.quit-ask");
      await ask.waitFor();
      await ask.locator("label.quit-never").click();
      await ask.locator(".bar button", { hasText: await tr("Disconnect and quit") }).click();
      await until(() => posts.length >= 2);
      assert.deepEqual(posts, ["/api/agents/disconnect/codex", "/api/window/quit?now=1&noAsk=1"]);
    });

    test(`${engine} ${lang}: a disconnect that fails keeps magpie open`, async (t) => {
      const { page, posts, tr } = await open(t, { agents: two, fail: "codex" });
      const ask = page.locator(".editor.quit-ask");
      await ask.waitFor();
      const off = ask.locator(".bar button", { hasText: await tr("Disconnect and quit") });
      await off.click();
      await page.locator("#status.err").waitFor();
      assert.match(await page.locator("#status").textContent(), /^Codex: /);
      assert.deepEqual(posts, ["/api/agents/disconnect/codex"], "nothing after the failed one, no quit");
      assert.equal(await ask.count(), 1, "the sheet stays");
      assert.ok(await off.isEnabled(), "and can be tried again");
    });

    test(`${engine} ${lang}: one agent is named in the sentence`, async (t) => {
      const { page, tr } = await open(t, { agents: two.slice(0, 1) }, "Linux x86_64");
      const ask = page.locator(".editor.quit-ask");
      await ask.waitFor();
      const text = await ask.textContent();
      assert.ok(text.includes(await tr("{agent} goes through magpie, so it stops working while magpie is closed.", { agent: "Codex" })));
      assert.ok(text.includes(await tr("Closing the window keeps magpie running in the system tray.")));
    });

    test(`${engine} ${lang}: nothing connected any more quits at once`, async (t) => {
      const { page, posts } = await open(t, { agents: [] });
      await until(() => posts.length);
      assert.deepEqual(posts, ["/api/window/quit?now=1"]);
      assert.equal(await page.locator(".editor.quit-ask").count(), 0);
    });
  }
}
