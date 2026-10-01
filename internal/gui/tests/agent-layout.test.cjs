// An agent's name must stay readable when the main window is narrow or
// enlarged, without making the model and effort controls run past the row.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const state = {
  agents: [
    {
      id: "claude", name: "Claude Code", path: "/test/claude.json", icon: "claudecode-color",
      fields: [
        { key: "model", label: "model", value: "claude-sonnet-4.5", options: [{ value: "claude-sonnet-4.5", label: "Claude Sonnet 4.5", icon: "claude-color" }] },
        { key: "effort", label: "effort", value: "high", options: [{ value: "high", label: "High" }] },
      ],
    },
    {
      id: "codex", name: "Codex", path: "/test/codex.toml", icon: "codex-color",
      fields: [
        { key: "model", label: "model", value: "gpt-6.1-sol", options: [{ value: "gpt-6.1-sol", label: "GPT-6.1-Sol", icon: "openai" }] },
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

      const row = page.locator('.row.agent[data-id="claude"]');
      const who = row.locator(".who");
      const name = row.locator(".name");
      const fields = row.locator(".fields");
      await fields.waitFor();
      for (const width of [520, 560, 600]) {
        await page.setViewportSize({ width, height: 700 });
        const [rb, wb, nb, fb] = await Promise.all([row.boundingBox(), who.boundingBox(), name.boundingBox(), fields.boundingBox()]);
        assert(wb.width >= 150, `agent identity column is too narrow at ${width}px: ${JSON.stringify({ rb, wb, nb, fb })}`);
        assert(fb.y >= wb.y + wb.height - 1, `controls should wrap below the agent name at ${width}px: ${JSON.stringify({ wb, fb })}`);
        assert(Math.abs(fb.x - wb.x) < 1, `wrapped controls should align with the name at ${width}px: ${JSON.stringify({ wb, fb })}`);
        assert(fb.x + fb.width <= rb.x + rb.width - 8, `controls must stay inside the row at ${width}px: ${JSON.stringify({ rb, fb })}`);
        if (width === 520) narrowShot = await page.screenshot();
      }

      // The default 660px window and widths above the breakpoint keep
      // the controls beside the names and their right edges aligned.
      for (const width of [660, 601, 960]) {
        await page.setViewportSize({ width, height: 700 });
        const boxes = await page.locator(".row.agent").evaluateAll((els) => els.map((el) => {
          const whoBox = el.querySelector(".who").getBoundingClientRect();
          const fieldsBox = el.querySelector(".fields").getBoundingClientRect();
          return { who: { x: whoBox.x, y: whoBox.y, right: whoBox.right, bottom: whoBox.bottom }, fields: { x: fieldsBox.x, y: fieldsBox.y, right: fieldsBox.right } };
        }));
        assert(boxes.every(({ who, fields }) => fields.y < who.bottom && fields.x > who.right), `controls should stay beside names at ${width}px: ${JSON.stringify(boxes)}`);
        assert(Math.abs(boxes[0].fields.right - boxes[1].fields.right) < 1, `control columns should remain aligned at ${width}px: ${JSON.stringify(boxes)}`);
        if (width === 660) defaultShot = await page.screenshot();
      }
    });
  }
}
