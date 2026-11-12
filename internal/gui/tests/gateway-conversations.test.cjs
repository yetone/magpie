const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

// assertRecordCard: the recording setting is one card in the list's column,
// below the toolbar and as wide as the list, with a switch, its name and what
// it keeps where, and a delete that names what it deletes (lee on Discord: a
// bare checkbox and a lone trash icon under the tabs read as stray).
async function assertRecordCard(page) {
  const got = await page.evaluate(() => {
    const box = (e) => e && e.getBoundingClientRect();
    const card = document.querySelector("#view-sessions .sm-body > .sm-record");
    if (!card) return null;
    const list = document.querySelector("#view-sessions .sm-tree") || document.querySelector("#view-sessions .sm-body");
    const head = document.querySelector("#view-sessions .sm-head");
    const sw = card.querySelector(".sm-record-switch"), name = card.querySelector(".sm-record-name"), sub = card.querySelector(".sm-record-sub");
    const clear = card.querySelector(".sm-record-clear");
    const c = box(card), l = box(list), h = box(head), s = box(sw);
    return {
      inside: [...card.parentElement.parentElement.children].indexOf(card.parentElement) > [...card.parentElement.parentElement.children].indexOf(head),
      left: c.left - l.left, right: c.right - l.right, belowHead: c.top - h.bottom,
      sw: s.width > 0 && s.left >= c.left && s.right <= c.right && s.top >= c.top && s.bottom <= c.bottom,
      label: sw.getAttribute("aria-labelledby") === name.id && !!name.textContent.trim(),
      nameClipped: name.scrollWidth > name.clientWidth + 1, sub: sub.textContent.trim().length,
      clear: clear && { text: clear.textContent.trim(), inCard: box(clear).left >= c.left && box(clear).right <= c.right + 0.5 && box(clear).bottom <= c.bottom + 0.5 },
      offPage: c.right > innerWidth + 0.5,
    };
  });
  assert(got, "the recording card is in the Sessions body");
  assert(got.inside && got.belowHead >= 0, "the card sits below the toolbar, in the body");
  assert(Math.abs(got.left) <= 1 && Math.abs(got.right) <= 1, `the card lines up with the list (${got.left}, ${got.right})`);
  assert(got.sw, "the switch is inside the card");
  assert(got.label, "the switch is named by the card's title");
  assert(!got.nameClipped, "the title isn't cut off");
  assert(got.sub > 10, "the card says what it keeps and where");
  if (got.clear) {
    assert(got.clear.text.length > 2, "the delete says what it deletes");
    assert(got.clear.inCard, "the delete is inside the card");
  }
  assert(!got.offPage, "the card fits the window");
}
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: gateway recording consent, content, clear and narrow layout`, async (t) => {
      const browser = await (engine === "webkit" ? webkit : chromium).launch();
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1000, height: 850 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      let recording = false, cleared = false, recorded = false;
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
          agent: "claude", terminal: false, trash: [], recording, recorded,
          sessions: [{ agent: "claude", id: "gateway-session", title: "Gateway conversation", start: new Date().toISOString(), last: new Date().toISOString(), input: 1200, output: 180, cache_read: 800, cache_write: 0, size: 0, models: [], read_only: true, transcript: true, gateway: true }],
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
          // what the gateway keeps once recording is on, gone on clear
          recorded = recording || (recorded && !body.clear);
          return json({ recording });
        }
        if (u.pathname.startsWith("/api/")) return json({});
        const file = path.join(assets, u.pathname === "/" ? "index.html" : u.pathname);
        try { return route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)] }); }
        catch { return route.fulfill({ status: 404, body: "" }); }
      });
      await page.goto("http://magpie.test/?view=providers");
      await page.locator('#nav button[data-view="sessions"]').click();
      const sw = page.locator(".sm-record .sm-record-switch[role=switch]");
      await sw.waitFor();
      assert.equal(await sw.getAttribute("aria-checked"), "false");
      assert.equal(await page.locator(".sm-record-clear").count(), 0, "nothing kept, nothing to delete");
      await assertRecordCard(page);
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
      await page.waitForFunction(() => document.querySelector(".sm-record-switch")?.getAttribute("aria-checked") === "true");
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
      await page.locator(".sess-sheet .bar .primary").click();
      await page.locator(".sess-sheet").waitFor({ state: "detached" });
      for (const width of [1000, 390]) {
        await page.setViewportSize({ width, height: 850 });
        await page.waitForTimeout(100);
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no page overflow");
        await assertRecordCard(page);
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-gateway-conversation-${width}.png`), fullPage: true });
        }
      }
      // A transcript read started before clearing must not restore old content.
      await page.evaluate(() => document.querySelector("#nav button[data-view=sessions]").click());
      await row.locator(".who").waitFor();
      await page.waitForTimeout(300);
      if (!(await page.locator(".sess-talk-btn").count())) await row.locator(".who").click();
      holdNextTranscript = true;
      const readStarted = page.waitForRequest((r) => new URL(r.url()).pathname === "/api/sessions/transcript");
      await page.locator(".sess-talk-btn").click();
      await readStarted;
      await assertRecordCard(page);
      assert.equal(await page.locator(".sm-record .sm-record-clear").count(), 1, "kept text can be deleted from the card");
      await page.locator(".sm-record-clear").evaluate((b) => b.click());
      await page.locator("dialog.action-confirm .primary").click();
      await page.waitForFunction(() => document.querySelector(".sm-record-switch")?.getAttribute("aria-checked") === "false");
      await page.waitForFunction(() => document.querySelector(".sess-talk .cx-none") && !document.querySelector(".sess-talk .cx-part"));
      const lateResponse = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/sessions/transcript");
      releaseTranscript();
      await lateResponse;
      await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      assert.equal(await page.locator(".sess-talk .cx-part").count(), 0, "late transcript cannot restore cleared content");
      assert.deepEqual(writes, [{ on: true }, { clear: true }]);
      if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-recording-cleared.png`), fullPage: true });
      assert.equal(await row.count(), 1, "usage session survives clear");
      assert.equal(await page.locator(".sm-record-clear").count(), 0, "cleared, nothing left to delete");
      assert.deepEqual(errors, []);
    });
  }
}

// lee on Discord: on this computer's Sessions page (not gateway mode), the
// recording setting shows only on an agent with gateway sessions, as a card
// over its list, never on one without them, nor in the Trash.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the recording card shows only where gateway sessions are`, async (t) => {
      const browser = await (engine === "webkit" ? webkit : chromium).launch();
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1400, height: 800 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.stack || e.message));
      const now = new Date().toISOString();
      const sess = (agent, id, extra) => ({ agent, id, title: id, cwd: "/work/app", start: now, last: now, input: 1000, output: 100, cache_read: 0, cache_write: 0, size: 10, models: [], ...extra });
      await page.route("**/*", async (route) => {
        const u = new URL(route.request().url());
        const json = (value) => route.fulfill({ json: value });
        if (u.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs=${JSON.stringify({ web: true, lang, theme: "light" })}` });
        if (u.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
        if (u.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true } });
        if (u.pathname === "/api/plugins") return json({ plugins: [] });
        if (u.pathname === "/api/groups") return json({ groups: [], models: [] });
        if (u.pathname === "/api/sessions/manage") {
          const agent = u.searchParams.get("agent") || "codex";
          return json({
            agents: [{ agent: "codex", name: "Codex", icon: "codex", count: 101, deletable: true }, { agent: "claude", name: "Claude Code", icon: "claudecode", count: 12, deletable: true }],
            agent, terminal: false, trash: [], recording: false, recorded: false,
            sessions: agent === "codex"
              ? [sess("codex", "native-1", { path: "/home/u/.codex/sessions/a.jsonl", resume: "codex resume native-1" }), sess("codex", "gw-1", { read_only: true, transcript: true, gateway: true })]
              : [sess("claude", "native-2", { path: "/home/u/.claude/projects/a.jsonl", resume: "claude --resume native-2" })],
          });
        }
        if (u.pathname.startsWith("/api/")) return json({});
        const file = path.join(assets, u.pathname === "/" ? "index.html" : u.pathname);
        try { return route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)] }); }
        catch { return route.fulfill({ status: 404, body: "" }); }
      });
      await page.goto("http://magpie.test/?view=providers");
      await page.locator('#nav button[data-view="sessions"]').click();
      await page.locator("#view-sessions .row.sm-folder").first().waitFor();
      assert.equal(await page.locator(".sm-recording, .sm-record-label").count(), 0, "no bare checkbox bar under the tabs");
      for (const width of [1400, 390]) {
        await page.setViewportSize({ width, height: 800 });
        await page.waitForTimeout(100);
        await assertRecordCard(page);
        assert.equal(await page.locator(".sm-record-clear").count(), 0, "nothing kept, nothing to delete");
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no page overflow");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-record-card-${width}.png`) });
        }
      }
      await page.setViewportSize({ width: 1400, height: 800 });
      await page.locator("#view-sessions .sm-agents .opt").nth(1).click();
      await page.waitForFunction(() => document.querySelector('#view-sessions .row.sm-sess[data-id="native-2"]'));
      assert.equal(await page.locator(".sm-record").count(), 0, "no card on an agent without gateway sessions");
      await page.locator("#view-sessions .sm-agents .opt").nth(0).click();
      await page.locator(".sm-record").waitFor();
      await page.locator("#view-sessions .sm-trash-btn").click();
      assert.equal(await page.locator(".sm-record").count(), 0, "no card in the Trash");
      assert.deepEqual(errors, []);
    });
  }
}
