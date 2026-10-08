const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: gateway recording consent, content, clear and narrow layout`, async (t) => {
      const browser = await (engine === "webkit" ? webkit : chromium).launch();
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1000, height: 850 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      let recording = false, cleared = false;
      let holdNextTranscript = false, releaseTranscript;
      const writes = [], errors = [];
      page.on("pageerror", (e) => errors.push(e.stack || e.message));
      await page.route("**/*", async (route) => {
        const u = new URL(route.request().url());
        const json = (value) => route.fulfill({ json: value });
        if (u.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs=${JSON.stringify({ web: true, gateway: true, lang, theme: "dark" })}` });
        if (u.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "dark", gatewayMode: "on" } });
        if (u.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true } });
        if (u.pathname === "/api/plugins") return json({ plugins: [] });
        if (u.pathname === "/api/groups") return json({ groups: [], models: [] });
        if (u.pathname === "/api/sessions/manage") return json({
          agents: [{ agent: "claude", name: "Claude Code", icon: "claudecode", count: 1, deletable: false }],
          agent: "claude", terminal: false, trash: [], recording,
          sessions: [{ agent: "claude", id: "gateway-session", title: "Gateway conversation", start: new Date().toISOString(), last: new Date().toISOString(), input: 1200, output: 180, cache_read: 800, cache_write: 0, size: 0, models: [], read_only: true, transcript: true }],
        });
        if (u.pathname === "/api/sessions/transcript") {
          const transcript = { source: "gateway", cut: !cleared, parts: cleared ? [] : [
          { role: "system", kind: "context", name: "System prompt", text: "Project rules" },
          { role: "user", kind: "text", text: "Find the project version" },
          { role: "assistant", kind: "tool_use", name: "bash", text: '{"command":"cat package.json"}' },
          { role: "tool", kind: "tool_result", text: '{"version":"1.2.3"}' },
          { role: "assistant", kind: "text", text: "The project version is 1.2.3." },
          ] };
          if (holdNextTranscript) {
            holdNextTranscript = false;
            await new Promise((resolve) => { releaseTranscript = resolve; });
          }
          return json(transcript);
        }
        if (u.pathname === "/api/sessions/recording") {
          const body = route.request().postDataJSON();
          writes.push(body);
          recording = !!body.on && !body.clear;
          cleared ||= !!body.clear;
          return json({ recording });
        }
        if (u.pathname.startsWith("/api/")) return json({});
        const file = path.join(assets, u.pathname === "/" ? "index.html" : u.pathname);
        try { return route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)] }); }
        catch { return route.fulfill({ status: 404, body: "" }); }
      });
      await page.goto("http://magpie.test/?view=providers");
      await page.locator('#nav button[data-view="sessions"]').click();
      const sw = page.locator(".sm-record-label input");
      await sw.waitFor();
      assert.equal(await sw.isChecked(), false);
      await sw.click();
      await page.locator("dialog.action-confirm").waitFor();
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-recording-consent.png`), fullPage: true });
      }
      assert.equal(writes.length, 0, "no consent yet");
      await page.keyboard.press("Escape");
      assert.equal(writes.length, 0, "cancel must not record");
      await sw.click();
      await page.locator("dialog.action-confirm .primary").click();
      await page.waitForFunction(() => document.querySelector(".sm-record-label input")?.checked);
      assert.deepEqual(writes, [{ on: true }]);
      const row = page.locator('.sm-sess[data-id="gateway-session"]');
      assert.equal(await row.locator(".sm-del, .sess-carry").count(), 0);
      await row.locator(".who").click();
      const top = await row.evaluate((e) => e.getBoundingClientRect().top);
      await page.locator(".sess-talk-btn").click();
      await page.locator(".sess-talk .cx-part").first().waitFor();
      assert.equal(await page.locator(".sess-talk .cx-part").count(), 5);
      assert.equal(await page.locator(".sess-talk details").getAttribute("open"), null);
      assert.equal(await row.evaluate((e) => e.getBoundingClientRect().top), top, "no click scroll");
      for (const width of [1000, 390]) {
        await page.setViewportSize({ width, height: 850 });
        await page.waitForTimeout(100);
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no page overflow");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-gateway-conversation-${width}.png`), fullPage: true });
        }
      }
      // A transcript read started before clearing must not restore old content.
      holdNextTranscript = true;
      const readStarted = page.waitForRequest((r) => new URL(r.url()).pathname === "/api/sessions/transcript");
      await page.evaluate(() => window.loadSessionsPage());
      await readStarted;
      await page.locator(".sm-record-clear").click();
      await page.locator("dialog.action-confirm .primary").click();
      await page.waitForFunction(() => !document.querySelector(".sm-record-label input")?.checked);
      await page.waitForFunction(() => document.querySelector(".sess-talk .cx-none") && !document.querySelector(".sess-talk .cx-part"));
      const lateResponse = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/sessions/transcript");
      releaseTranscript();
      await lateResponse;
      await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      assert.equal(await page.locator(".sess-talk .cx-part").count(), 0, "late transcript cannot restore cleared content");
      assert.deepEqual(writes, [{ on: true }, { clear: true }]);
      if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-recording-cleared.png`), fullPage: true });
      assert.equal(await row.count(), 1, "usage session survives clear");
      assert.deepEqual(errors, []);
    });
  }
}
