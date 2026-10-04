// An agent's name must stay readable when the main window is narrow or
// enlarged, without making its 「接入」 switch, the link that opens it and
// the model it starts on run past the row; the switches and the model
// pickers line up down the list.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const state = {
  agents: [
    {
      id: "claude", name: "Claude Code", path: "/test/claude.json", icon: "claudecode-color", wired: true,
      fields: [
        { key: "model", label: "model", value: "claude-sonnet-4.5", options: [{ value: "claude-sonnet-4.5", label: "Claude Sonnet 4.5", icon: "claude-color", ref: "claude/claude-sonnet-4.5" }] },
        { key: "effort", label: "effort", value: "high", options: [{ value: "high", label: "High" }] },
      ],
    },
    {
      id: "codex", name: "Codex", path: "/test/codex.toml", icon: "codex-color", wired: true,
      fields: [
        { key: "model", label: "model", value: "gpt-6.1-sol", options: [{ value: "gpt-6.1-sol", label: "GPT-6.1-Sol", icon: "openai", ref: "openai/gpt-6.1-sol" }] },
        { key: "effort", label: "effort", value: "high", options: [{ value: "high", label: "Highest" }] },
      ],
    },
  ],
  profiles: [], settings: { lang: "en", theme: "light", textSize: 100 },
};

async function serve(route, lang) {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") {
    return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false,textSize:100};` });
  }
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ ...state, settings: { ...state.settings, lang } });
  if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  await route.fulfill({ body: await fs.readFile(file), contentType });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: agent names and controls fit a narrow window`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 520, height: 700 } });
      let narrowShot, defaultShot;
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          if (narrowShot) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-agent-layout-narrow.png`), narrowShot);
          if (defaultShot) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-agent-layout-default.png`), defaultShot);
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-agent-layout.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      await page.route("**/*", (route) => serve(route, lang));
      await page.goto("http://magpie.test/");
      await page.waitForLoadState("networkidle");

      await page.locator('.row.agent[data-id="claude"] .ag-conn').waitFor();
      const boxes = () => page.locator(".row.agent").evaluateAll((els) => els.map((el) => {
        const box = (e) => { const r = e.getBoundingClientRect(); return { x: r.x, y: r.y, right: r.right, bottom: r.bottom, width: r.width }; };
        return { row: box(el), who: box(el.querySelector(".who")), controls: [...el.querySelectorAll(":scope > .ag-link, :scope > .ag-conn")].map(box), start: box(el.querySelector(":scope > .ag-start")) };
      }));
      for (const width of [520, 560, 600, 660, 601, 960]) {
        await page.setViewportSize({ width, height: 700 });
        const rows = await boxes();
        for (const { row, who, controls } of rows) {
          assert(who.width >= 150, `agent identity column is too narrow at ${width}px: ${JSON.stringify({ row, who })}`);
          assert.equal(controls.length, 2, "the link and the switch");
          for (const c of controls) {
            assert(c.right <= row.right - 8, `controls must stay inside the row at ${width}px: ${JSON.stringify({ row, c })}`);
            // beside the name, or on a line below it in a narrow window
            const ok = width > 600 ? c.x >= who.right && c.y < who.bottom : c.x >= who.right || c.y >= who.bottom - 1;
            assert(ok, `controls stay clear of the name at ${width}px: ${JSON.stringify({ who, c })}`);
          }
        }
        // the model it starts on, one click away: inside the row, the same
        // width down the list, beside the switch or on a line under the name
        for (const { row, who, start } of rows) {
          assert(start.width >= 120 && start.right <= row.right - 8, `the model picker fits at ${width}px: ${JSON.stringify({ row, start })}`);
          assert(width > 600 ? start.x >= who.right : start.y >= who.bottom - 1, `the model picker stays clear of the name at ${width}px: ${JSON.stringify({ who, start })}`);
        }
        assert(Math.abs(rows[0].start.right - rows[1].start.right) < 1 && Math.abs(rows[0].start.width - rows[1].start.width) < 1, `the model pickers line up at ${width}px`);
        const rights = rows.map((r) => r.controls[1].right);
        assert(Math.abs(rights[0] - rights[1]) < 1, `the switches line up at ${width}px: ${JSON.stringify(rights)}`);
        if (width === 520) narrowShot = await page.screenshot();
        if (width === 660) defaultShot = await page.screenshot();
      }
    });
  }
}
