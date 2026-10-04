// Run with Node's test runner and Playwright on the module path; see README.md.
// Checkboxes are magpie's own, not the system's (ARNO on Discord: 会话面板下的
// checkbox的样式和软件整体的设计风格有点割裂 — white native boxes in the dark
// Sessions page). On the Sessions page, in light and dark, an unticked box
// is drawn on the card's colour with a rounded border, a ticked one and a
// partly ticked one (the folder's, one of its sessions picked) are filled
// with the accent, and a box still ticks and unticks on a click. In English
// and Chinese, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = (id, cwd, title, min) => ({
  agent: "codex", id, cwd, title, start: at(min + 5), last: at(min), models: [],
  resume: `cd ${cwd} && codex resume ${id}`, path: `~/.codex/sessions/2026/10/01/rollout-${id}.jsonl`, size: 4096, messages: 6, files: 1, deletable: true,
});

function fixture() {
  const app = [sess("a-run", "/work/app", "still running", 0.2), sess("a-login", "/work/app", "fix the login form", 30)];
  for (let i = 0; i < 12; i++) app.push(sess(`a-${i}`, "/work/app", `older task ${i}`, 60 + i * 10));
  return {
    codex: [...app, sess("b-1", "/work/old-blog", "write the post", 600), sess("b-2", "/work/old-blog", "", 700), sess("n-1", "", "no folder chat", 800)],
    opencode: [{ ...sess("o-1", "/work/app", "an opencode chat", 5), agent: "opencode", deletable: false, resume: "opencode -s o-1" }],
    trash: [],
  };
}

function serve(lang, calls, theme) {
  const store = fixture();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      const agent = url.searchParams.get("agent") === "opencode" ? "opencode" : "codex";
      return json({
        agents: [
          { agent: "codex", count: store.codex.length, deletable: true, name: "Codex", icon: "codex" },
          { agent: "opencode", count: store.opencode.length, deletable: false, name: "OpenCode", icon: "opencode" },
        ],
        agent, sessions: store[agent], terminal: false, trash: store.trash, trashDir: "~/Library/Application Support/magpie/trash/sessions",
      });
    }
    if (url.pathname === "/api/sessions/delete") {
      const body = req.postDataJSON();
      calls.push({ path: "delete", body });
      const out = { deleted: [], refused: [] };
      for (const id of body.ids) {
        if (id === "a-run") { out.refused.push({ id, error: "it was written to in the last minute; it may still be running", active: true }); continue; }
        const s = store.codex.find((x) => x.id === id);
        store.codex = store.codex.filter((x) => x.id !== id);
        store.trash.push({ key: `codex/20261001-${id}`, agent: "codex", id, title: s.title, cwd: s.cwd, last: s.last, deleted: at(0), size: s.size,
          items: [{ from: s.path, name: "0-" + id + ".jsonl" }], name: "Codex", icon: "codex" });
        out.deleted.push(id);
      }
      return json(out);
    }
    if (url.pathname.startsWith("/api/")) {
      if (req.method() === "POST") calls.push({ path: url.pathname });
      return json({});
    }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const nav = { en: "Sessions", zh: "会话" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const theme of ["light", "dark"]) {
      test(`${engine} ${lang} ${theme}: the Sessions page's checkboxes are drawn as magpie's own`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce", colorScheme: theme })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${theme}-checkbox-look.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, [], theme));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#nav").getByRole("button", { name: nav[lang], exact: true }).click();
        const view = page.locator("#view-sessions");
        await view.locator('.row.sm-sess[data-id="a-login"]').waitFor();
        // read after a frame: the page's reduced-motion transitions still take one
        const look = (l) => l.evaluate(async (e) => {
          for (let i = 0; i < 2; i++) await new Promise((r) => requestAnimationFrame(r));
          const s = getComputedStyle(e), card = getComputedStyle(document.documentElement).getPropertyValue("--card").trim();
          const accent = getComputedStyle(document.documentElement).getPropertyValue("--accent").trim();
          const probe = document.createElement("i");
          probe.style.transition = "none";
          document.body.append(probe);
          probe.style.color = card; const cardRGB = getComputedStyle(probe).color;
          probe.style.color = accent; const accentRGB = getComputedStyle(probe).color;
          probe.remove();
          return { appearance: s.appearance || s.webkitAppearance, bg: s.backgroundColor, radius: parseFloat(s.borderTopLeftRadius), width: e.getBoundingClientRect().width, cardRGB, accentRGB };
        });
        const box = view.locator('.row.sm-sess[data-id="a-login"] input.sm-check');
        let l = await look(box);
        assert.equal(l.appearance, "none");
        assert.equal(l.bg, l.cardRGB, "an unticked box is on the card's colour");
        assert.ok(l.radius >= 3 && l.width === 14, JSON.stringify(l));
        await box.click();
        assert.equal(await box.isChecked(), true);
        l = await look(box);
        assert.equal(l.bg, l.accentRGB, "a ticked box is filled with the accent");
        const folder = view.locator(".row.sm-folder").filter({ has: page.locator(".fold .name", { hasText: /^app$/ }) }).locator("input.sm-folder-check");
        assert.equal(await folder.evaluate((e) => e.indeterminate), true);
        l = await look(folder);
        assert.equal(l.bg, l.accentRGB, "a partly ticked box is filled with the accent");
        await box.click();
        assert.equal(await box.isChecked(), false);
        assert.deepEqual(errors, []);
      });
    }
  }
}
