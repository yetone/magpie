// Run with Node's test runner and Playwright on the module path; see README.md.
// 断开确认框的文件预览应占满正文，不能落入编辑器的标签列；Codex 的
// 保留说明另占一行。使用真实页面和隔离的 API 数据，不读取或改写用户配置。
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const changes = (id) => [
  {
    path: id === "omp" ? "~/.omp/agent/config.yml" : "~/.codex/config.toml",
    lines: [
      { op: "-", text: "default: magpie/group/auto-model" },
      { op: "-", text: "defaultThinkingLevel: xhigh" },
      ...Array.from({ length: 12 }, (_, i) => ({ op: "-", text: `  model-${i}: magpie/group/auto-model-${i}` })),
    ],
  },
  {
    path: id === "omp" ? "~/.omp/agent/models.yml" : "~/.codex/models.json",
    lines: [{ op: "~", text: "providers: {}", was: "providers: {magpie: {baseUrl: 'http://127.0.0.1:3425/v1', models: [{id: group/auto-model, name: Auto model}]}}" }],
  },
];

function serve(lang, textSize, posts) {
  const state = {
    agents: ["omp", "codex"].map((id) => ({
      id, name: id === "omp" ? "omp" : "Codex", icon: id === "omp" ? "omp" : "generic", wired: true,
      path: id === "omp" ? "~/.omp/agent/config.yml" : "~/.codex/config.toml",
      fields: [{ key: "model", label: "model", value: "magpie/relay/m1", options: [{ value: "magpie/relay/m1", label: "Model one", ref: "relay/m1" }] }],
    })),
    profiles: [], settings: { lang, theme: "light", textSize },
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    // the window saying it is in use (update/using) is not the page's doing
    if (req.method() === "POST" && url.pathname !== "/api/update/using") posts.push(url.pathname);
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", textSize, web: false })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") return json(state.settings);
    if (url.pathname.startsWith("/api/agents/preview/")) return json({ changes: changes(url.pathname.split("/").pop()) });
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

const inside = (a, b, what) => assert.ok(
  a.left >= b.left - 1 && a.right <= b.right + 1 && a.top >= b.top - 1 && a.bottom <= b.bottom + 1,
  `${what}: ${JSON.stringify(a)} within ${JSON.stringify(b)}`,
);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: disconnect previews use the full body width`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const [width, height, textSize] of [[890, 800, 100], [560, 420, 100], [890, 800, 150]]) {
        // 原生字号由 WebView 页面缩放实现：同一窗口的 CSS 视口随之缩小。
        const zoom = textSize / 100, viewport = { width: Math.round(width / zoom), height: Math.round(height / zoom) };
        const context = await browser.newContext({ viewport, deviceScaleFactor: zoom, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
        await page.route("**/*", serve(lang, textSize, posts));
        await page.goto("http://magpie.test/?view=agents");
        assert.equal(await page.title(), "magpie");
        assert.equal(new URL(page.url()).searchParams.get("view"), "agents");
        for (const id of ["omp", "codex"]) {
          await page.locator(`.row.agent[data-id="${id}"] .ag-conn`).click();
          const ask = page.locator(".editor.disconnect-ask");
          await ask.locator(".ag-diff-file").last().waitFor();
          await page.locator("#modal").evaluate((m) => Promise.all(m.getAnimations({ subtree: true }).map((a) => a.finished)));
          assert.equal(await page.evaluate(() => parseFloat(document.documentElement.style.getPropertyValue("--zoom"))), zoom, "the native text-size setting is retained");
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${id}-${width}x${height}-${textSize}-disconnect.png`) });
          }
          const layout = await ask.evaluate((ed) => {
            const rect = (e) => { const r = e.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width }; };
            const body = ed.querySelector(".ebody"), css = getComputedStyle(body), diff = ed.querySelector(".ag-diff-wrap");
            const r = rect(body), scale = r.width / body.offsetWidth, note = ed.querySelector(".ag-note"), code = diff.querySelector("code");
            // the content box ends at the scrollbar, not at the body's edge
            return {
              content: { left: r.left + parseFloat(css.paddingLeft) * scale, right: r.left + (body.clientLeft + body.clientWidth - parseFloat(css.paddingRight)) * scale },
              diff: rect(diff), note: note && rect(note), code: rect(code), lineHeight: parseFloat(getComputedStyle(code.parentElement).lineHeight) * scale,
              dialog: rect(ed.closest(".dialog")), head: rect(ed.querySelector(".ehead")), bar: rect(ed.querySelector(".bar")),
              overflow: body.scrollWidth - body.clientWidth,
            };
          });
          assert.ok(Math.abs(layout.diff.left - layout.content.left) <= 1 && Math.abs(layout.diff.right - layout.content.right) <= 1,
            `the preview spans the body at ${width}x${height}, ${textSize}%: ${JSON.stringify(layout)}`);
          assert.ok(layout.code.bottom - layout.code.top <= layout.lineHeight * 2 + 1, "a short config line does not break into a column of letters");
          assert.ok(layout.overflow <= 1, "the body has no horizontal overflow");
          const view = { left: 0, right: viewport.width, top: 0, bottom: viewport.height };
          inside(layout.dialog, view, "the dialog fits");
          inside(layout.head, view, "the heading stays visible");
          inside(layout.bar, view, "the buttons stay visible");
          if (id === "codex") {
            assert.ok(Math.abs(layout.note.left - layout.content.left) <= 1 && Math.abs(layout.note.right - layout.content.right) <= 1, "Codex's note spans the body");
            assert.ok(layout.note.top >= layout.diff.bottom - 1, `Codex's note follows the preview on its own row: ${JSON.stringify(layout)}`);
          }
          await ask.locator(".ag-diff-more").click();
          assert.equal(await ask.locator(".ag-diff-file").first().locator(".ag-diff-line").count(), 14, "all remaining lines expand");
          const wrap = ask.locator(".ag-diff-wrap");
          await wrap.hover();
          await page.mouse.wheel(0, 1200);
          await page.waitForFunction(() => {
            const e = document.querySelector(".disconnect-ask .ag-diff-wrap");
            return e.scrollTop >= e.scrollHeight - e.clientHeight - 1;
          });
          const restored = ask.locator(".ag-diff-file").last().locator("code");
          assert.equal(await restored.textContent(), "providers: {}");
          const visible = await restored.evaluate((e) => {
            const rect = (x) => { const r = x.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom }; };
            const body = rect(e.closest(".ebody")), wrap = rect(e.closest(".ag-diff-wrap"));
            return { code: rect(e), clip: { left: Math.max(body.left, wrap.left), right: Math.min(body.right, wrap.right), top: Math.max(body.top, wrap.top), bottom: Math.min(body.bottom, wrap.bottom) } };
          });
          inside(visible.code, visible.clip, "the restored value is visible inside both scrolling regions");
          await ask.locator(".bar").getByRole("button", { name: lang === "zh" ? "取消" : "Cancel", exact: true }).click();
          await ask.waitFor({ state: "detached" });
          assert.deepEqual(posts, [], "reading, expanding and cancelling the preview change no config");
        }
        assert.deepEqual(errors, [], "the real page has no runtime or console errors");
        await context.close();
      }
    });
  }
}
