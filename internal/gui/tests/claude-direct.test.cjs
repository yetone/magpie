// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code on its own sign-in (StringKe on Discord): with "model":
// "sonnet" in its settings.json the row showed a bare "sonnet", and with a
// Claude model picked magpie writes no ANTHROPIC_BASE_URL, so the file read
// as if magpie had failed to set it up. The alias now shows as the model it
// stands for, with Claude's logo (the option is magpie's, agent.Option), and
// a model Claude Code asks Anthropic for itself says so: the row's tooltip
// says it isn't through magpie and why the file names no magpie endpoint,
// and picking one says "straight to Anthropic". A magpie model says neither.
// The model is picked beside the connected row's switch. No click
// moves the page. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [
  { value: "sonnet", label: "sonnet · claude-sonnet-5-5", note: "Claude Sonnet 5.5", icon: "claude-color", group: "Claude Code", direct: "Anthropic" },
  { value: "claude-sonnet-5-5", note: "Claude Sonnet 5.5", icon: "claude-color", group: "Claude Code", direct: "Anthropic" },
  { value: "claude-opus-5-5", note: "Claude Opus 5.5", icon: "claude-color", group: "Claude Code", direct: "Anthropic" },
  { value: "magpie/deepseek/pro", label: "DeepSeek Pro", icon: "deepseek-color", group: "DeepSeek", ref: "deepseek/pro" },
];
// the other agents, so Claude Code sits down a list that scrolls; on a
// magpie model, so connected and in view (#726)
const filler = [{ key: "model", label: "model", value: "magpie/deepseek/pro", options: [options[3]] }];
const fresh = () => ({
  agents: [
    ...Array.from({ length: 5 }, (_, i) => ({ id: "agent-" + i, name: "Agent " + i, path: "/test/" + i, wired: true, fields: filler })),
    { id: "claude", name: "Claude Code", icon: "claudecode-color", path: "~/.claude/settings.json", wired: true,
      fields: [{ key: "model", label: "model", value: "sonnet", options }] },
    ...Array.from({ length: 8 }, (_, i) => ({ id: "more-" + i, name: "More " + i, path: "/test/m" + i, wired: true, fields: filler })),
  ],
  profiles: [],
});

function server(lang, sets) {
  let cur = fresh();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...cur, settings: { lang, theme: "light" } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      cur.agents.find((a) => a.id === body.agent).fields.find((f) => f.key === body.field).value = body.value;
      return json({ ...cur, settings: { lang, theme: "light" } });
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    tip: "Not through magpie: Claude Code asks Anthropic for it directly, with its own sign-in or key, so ~/.claude/settings.json has no magpie endpoint — that is expected.",
    said: "straight to Anthropic, not through magpie",
  },
  zh: {
    tip: "不经过 magpie：Claude Code 用它自己的登录或密钥直接向 Anthropic 请求这个模型，所以 ~/.claude/settings.json 里没有 magpie 的地址，这是正常的。",
    said: "直连 Anthropic，不经过 magpie",
  },
};
const row = '.row.agent[data-id="claude"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Claude Code's own model says it goes straight to Anthropic`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 980, height: 420 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const sets = [];
      await page.route("**/*", server(lang, sets));
      await page.goto("http://magpie.test/");
      // beside the switch, one click away
      const field = page.locator(`${row} > .field.ag-start[data-key="model"]`);
      await field.waitFor();

      // the alias, as the model it stands for, with Claude's logo
      assert.equal(await field.locator(".v").textContent(), "sonnet · claude-sonnet-5-5");
      assert.equal(await field.locator('.ic[data-icon="claude-color"]').count(), 1);
      const title = await field.getAttribute("title");
      assert(title.includes(w.tip), `the tooltip says it isn't through magpie: ${title}`);

      // the page scrolled, a pick moves nothing and says where it goes
      const view = page.locator("#view-agents");
      await page.mouse.move(400, 300);
      // down to the row's picker, so the click has no need to scroll
      for (let i = 0; i < 8; i++) { await page.mouse.wheel(0, 30); await page.waitForTimeout(20); }
      await page.waitForTimeout(300);
      const top = await view.evaluate((v) => v.scrollTop);
      assert(top > 0, "the list must be scrolled");
      await field.click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      await page.locator("#list li").filter({ hasText: "claude-opus-5-5" }).first().click();
      await page.waitForFunction((s) => document.querySelector("#status")?.textContent.includes(s), w.said);
      assert.deepEqual(sets, [{ agent: "claude", field: "model", value: "claude-opus-5-5" }]);
      assert.equal(await view.evaluate((v) => v.scrollTop), top, "the pick moved the page");
      assert((await field.getAttribute("title")).includes(w.tip));

      // a magpie model says neither
      await field.click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      await page.locator("#list li").filter({ hasText: "DeepSeek Pro" }).first().click();
      await page.waitForFunction(() => /DeepSeek Pro/.test(document.querySelector("#status")?.textContent || ""));
      assert(!(await page.locator("#status").textContent()).includes(w.said));
      assert(!(await field.getAttribute("title")).includes(w.tip));
      assert.equal(await view.evaluate((v) => v.scrollTop), top, "the pick moved the page");
      assert.deepEqual(errors, []);
    });
  }
}
