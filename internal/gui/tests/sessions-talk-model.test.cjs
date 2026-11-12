// Run with Node's test runner and Playwright on the module path; see README.md.
// Each reply in a session's conversation names its model (#2253,
// cnlove7777777-art): the transcript's parts carry the model their own line
// in the agent's file names, and the reader says it muted after the
// reply's first part, once a reply, and again only where the model changes
// inside it. A part that names none (Claude Code's own line, an agent whose
// file has no model) says none, and the user's words and a tool's result
// never do. The label stays inside its line at 420px. In five languages,
// Chromium and WebKit, at 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const LONG = "claude-opus-5-5-20261001-with-a-very-long-relay-suffix[1m]";
const PARTS = [
  { role: "user", kind: "text", text: "Rename foo to bar" },
  { role: "assistant", kind: "thinking", text: "grep for it first", model: "gpt-6-astra" },
  { role: "assistant", kind: "tool_use", name: "bash", text: "{}", model: "gpt-6-astra" },
  { role: "tool", kind: "tool_result", name: "bash", text: "a.go:3: foo()" },
  { role: "assistant", kind: "text", text: "Renamed.", model: "gpt-6-astra" },
  { role: "user", kind: "text", text: "Now with the other one" },
  { role: "assistant", kind: "text", text: "Switched.", model: "deepseek-v4" },
  { role: "assistant", kind: "text", text: "Fell back.", model: LONG },
  { role: "user", kind: "text", text: "Again" },
  { role: "assistant", kind: "text", text: "API Error: 529" },
];
// the parts that say a model, by index, and which
const WANT = [[1, "gpt-6-astra"], [6, "deepseek-v4"], [7, LONG]];

function serve(lang, calls) {
  const s = { agent: "claude", id: "c-1", cwd: "/work/c", title: "Rename foo", start: at(10), last: at(5), models: [],
    resume: "claude --resume c-1", path: "~/.claude/projects/-work-c/c-1.jsonl", size: 4096, messages: 6, files: 1, deletable: true, transcript: true };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      return json({ agents: [{ agent: "claude", count: 1, deletable: true, name: "Claude Code", icon: "claude" }],
        agent: "claude", sessions: [s], terminal: true, trash: [], trashDir: "~/trash" });
    }
    if (url.pathname === "/api/sessions/transcript") {
      calls.push(url.searchParams.get("id"));
      return json({ parts: PARTS });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404 }));
  };
}

const NAV = { en: "Sessions", zh: "会话", "zh-TW": "工作階段", ja: "セッション", de: "Sitzungen" };
const MODEL = { en: "Model", zh: "模型", "zh-TW": "模型", ja: "モデル", de: "Modell" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: each reply in a session's conversation names its model`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-sessions-talk-model.png`), fullPage: true });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const calls = [];
        await page.route("**/*", serve(lang, calls));
        await page.goto("http://magpie.test/?view=sessions");
        const view = page.locator("#view-sessions");
        if (!(await view.isVisible())) {
          const nav = page.locator("#nav").getByRole("button", { name: NAV[lang], exact: true });
          if (await nav.count()) await nav.click();
        }
        const row = view.locator('.row.sm-sess[data-id="c-1"]');
        await row.waitFor();
        await row.locator(".who").click();
        const detail = view.locator(".sess-detail");
        await detail.waitFor();
        await detail.locator(".sess-talk-btn").click();
        const talk = page.locator(".sess-sheet .sess-talk");
        await talk.locator(".cx-part").first().waitFor();
        assert.deepEqual(calls, ["c-1"]);
        assert.equal(await talk.locator(".cx-part").count(), PARTS.length);

        const got = await talk.locator(".cx-part").evaluateAll((ps) => ps.map((p, i) => {
          const m = p.querySelector(".cx-h .cx-model");
          return m ? [i, m.textContent, m.title] : null;
        }).filter(Boolean));
        assert.deepEqual(got.map(([i, m]) => [i, m]), WANT, "a model once a reply, again where it changes");
        for (const [, m, title] of got) assert.equal(title, `${MODEL[lang]}: ${m}`);

        // muted, no larger than the role it follows, and inside its line
        const look = await talk.locator(".cx-model").evaluateAll((ms) => ms.map((m) => {
          const h = m.closest(".cx-h"), role = h.querySelector(".cx-role");
          const r = m.getBoundingClientRect(), hr = h.getBoundingClientRect();
          return { size: parseFloat(getComputedStyle(m).fontSize), role: parseFloat(getComputedStyle(role).fontSize),
            color: getComputedStyle(m).color, text: getComputedStyle(h.closest(".cx-part").querySelector(".cx-t")).color,
            inside: r.right <= hr.right + 0.5 && r.left >= hr.left - 0.5, roleShown: role.getBoundingClientRect().width >= role.scrollWidth - 0.5 };
        }));
        for (const l of look) {
          assert(l.size <= l.role, `the model is no larger than its role: ${JSON.stringify(l)}`);
          assert.notEqual(l.color, l.text, "the model is muted");
          assert(l.inside, `the model stays inside its line at ${width}: ${JSON.stringify(l)}`);
          assert(l.roleShown, "the role isn't squeezed by a long model");
        }
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
        assert.deepEqual(errors, []);
      });
    }
  }
}
