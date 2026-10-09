// Run with Node's test runner and Playwright on the module path; see README.md.
// A session's conversation and carrying it on in another agent (sxwedo,
// #845). A session opened to its details has Show conversation, which reads
// sessions/transcript for it (agent and id; the server finds the file) and
// shows each part as the Usage page's calls do: the user's words, the
// model's, a tool's call by its name and its result, thinking folded. An
// agent whose sessions can't be read has no such button. A Pi session that
// oh-my-pi can carry on has Continue in, an app menu (never a <select>)
// that copies the command or opens it in the session terminal, which posts
// its agent, id and in: "omp" (the command is made by the server); the
// details say the command too. No click moves the page, nothing has a
// left-border accent. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const fixture = require("node:fs").readFileSync(path.resolve(__dirname, "../../sessions/testdata/reasonix-2.29.0/session.jsonl"), "utf8").trim().split("\n").map(JSON.parse);
const nativeUsage = JSON.parse(require("node:fs").readFileSync(path.resolve(__dirname, "../../sessions/testdata/reasonix-2.29.0/session.jsonl.telemetry.json"), "utf8")).usage;
const nativeTokens = {input:nativeUsage.promptTokens-nativeUsage.cacheHitTokens, output:nativeUsage.completionTokens, cache_read:nativeUsage.cacheHitTokens, cache_write:0};
const reasonixModels = [...new Set(fixture.filter(m => m.role === "assistant").map(m => m.modelRef.replace(/^magpie\//, "")))].map(model => ({model, ...nativeTokens, cost:0, priced:false}));
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const FORK = "cd '/work/pi' && omp --fork '/Users/me/.pi/agent/sessions/--work-pi--/2026-10-01T08-00-00-000Z_p-1.jsonl'";
const sess = (agent, id, title, min, extra) => ({
  agent, id, cwd: "/work/pi", title, start: at(min + 5), last: at(min), models: [],
  resume: `cd '/work/pi' && ${agent} --session ${id}`, path: `~/.pi/agent/sessions/--work-pi--/${id}.jsonl`, size: 4096, messages: 6, files: 1, deletable: true, ...extra,
});

const PARTS = [
  { role: "user", kind: "text", text: "Rename foo to bar" },
  { role: "assistant", kind: "thinking", text: "grep for it first" },
  { role: "assistant", kind: "tool_use", name: "bash", text: '{\n  "command": "grep -rn foo"\n}' },
  { role: "tool", kind: "tool_result", name: "bash", text: "a.go:3: foo()" },
  { role: "assistant", kind: "text", text: "Renamed foo to bar in a.go." },
];

function serve(lang, calls) {
  const store = {
    pi: [
      sess("pi", "p-1", "Rename foo", 5, { transcript: true, carry: [{ agent: "omp", command: FORK }] }),
      sess("pi", "p-2", "an older one", 50, { transcript: true, carry: [{ agent: "omp", command: FORK.replace("p-1", "p-2") }] }),
    ],
    opencode: [{ ...sess("opencode", "o-1", "an opencode chat", 8), deletable: false, resume: "opencode -s o-1" }],
    reasonix: [{ ...sess("reasonix", "r-1", "hello there", 8), deletable: false, transcript: true, usage_incomplete: false, models: reasonixModels, ...nativeTokens, resume: "reasonix --resume '/work/session.jsonl'" }],
  };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      const requested = url.searchParams.get("agent");
      const agent = requested === "opencode" || requested === "reasonix" ? requested : "pi";
      return json({
        agents: [
          { agent: "pi", count: store.pi.length, deletable: true, name: "Pi", icon: "pi" },
          { agent: "opencode", count: store.opencode.length, deletable: false, name: "OpenCode", icon: "opencode" },
          { agent: "reasonix", count: store.reasonix.length, deletable: false, name: "Reasonix Studio", icon: "reasonix-color" },
        ],
        agent, sessions: store[agent], terminal: true, trash: [], trashDir: "~/trash",
      });
    }
    if (url.pathname === "/api/sessions/transcript") {
      calls.push({ path: "transcript", agent: url.searchParams.get("agent"), id: url.searchParams.get("id") });
      return json({ parts: url.searchParams.get("agent") === "reasonix" ? fixture.filter(m => m.role !== "system").map(m => ({role:m.role,kind:"text",text:m.raw_content ?? m.content})) : PARTS });
    }
    if (url.pathname === "/api/sessions/terminal") {
      calls.push({ path: "terminal", body: route.request().postDataJSON() });
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === "/api/copy") {
      calls.push({ path: "copy", body: route.request().postDataJSON() });
      return json({});
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { nav: "Sessions", carry: "Continue in", head: "Continue in another agent", copyNote: "Copy the command", termNote: "Open in session terminal",
    line: "Continue in omp", show: "Show conversation", hide: "Hide conversation", talk: "Conversation", you: "You", asst: "Assistant", call: "Tool call", result: "Tool result", thinking: "Thinking" },
  zh: { nav: "会话", carry: "换 Agent 继续", head: "换个 Agent 继续", copyNote: "复制命令", termNote: "在所选终端中打开",
    line: "用 omp 继续", show: "查看会话", hide: "收起会话", talk: "会话内容" },
  ja: { nav: "セッション", carry: "別のエージェントで続ける", head: "別のエージェントで続ける", copyNote: "コマンドをコピー", termNote: "セッション用ターミナルで開く",
    line: "omp で続ける", show: "会話を表示", hide: "会話を隠す", talk: "会話", partial: "使用履歴が不完全です" },
  de: { nav: "Sitzungen", carry: "Fortsetzen in", head: "In einem anderen Agenten fortsetzen", copyNote: "Befehl kopieren", termNote: "Im Sitzungsterminal öffnen",
    line: "In omp fortsetzen", show: "Unterhaltung anzeigen", hide: "Unterhaltung ausblenden", talk: "Unterhaltung", partial: "Unvollständiger Nutzungsverlauf" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a session's conversation is shown, and a Pi session is carried on in omp`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 600 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sessions-talk.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
      const calls = [];
      await page.route("**/*", serve(lang, calls));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#nav").getByRole("button", { name: w.nav, exact: true }).click();
      const view = page.locator("#view-sessions");
      const row = (id) => view.locator(`.row.sm-sess[data-id="${id}"]`);
      await row("p-1").waitFor();
      const top = (l) => l.evaluate((e) => e.getBoundingClientRect().top);
      const still = async (l, what, click) => {
        const before = await top(l);
        await click();
        await page.waitForTimeout(250);
        assert.equal(await top(l), before, `${what} moved the page`);
      };

      // Continue in: the app's menu, with omp's copy and terminal
      const carry = row("p-1").locator(".sess-carry");
      assert.equal(await carry.count(), 1, "a Pi session can be carried on in omp");
      assert.equal((await carry.textContent()).trim(), w.carry);
      await still(row("p-1"), "opening Continue in", () => carry.click());
      const menu = page.locator(".proto-menu.sess-carry-menu");
      await menu.waitFor();
      assert.equal((await menu.locator(".pm-head").textContent()).trim(), w.head);
      assert.deepEqual(await menu.locator(".pm-item .pm-name").allTextContents(), ["omp", "omp"]);
      assert.deepEqual(await menu.locator(".pm-item .pm-note").allTextContents(), [w.copyNote, w.termNote]);
      assert.equal(await page.locator("select").count(), 0, "no native select");
      assert.equal(await carry.getAttribute("aria-expanded"), "true");
      await still(row("p-1"), "opening it in the terminal", () => menu.locator(".pm-item").nth(1).click());
      await menu.waitFor({ state: "detached" });
      for (let i = 0; i < 40 && !calls.some((c) => c.path === "terminal"); i++) await page.waitForTimeout(50);
      assert.deepEqual(calls.find((c) => c.path === "terminal"), { path: "terminal", body: { agent: "pi", id: "p-1", in: "omp" } });
      assert.equal(calls.filter((c) => c.path === "transcript").length, 0, "the row didn't open");
      await carry.click();
      await menu.waitFor();
      await menu.locator(".pm-item").nth(0).click();
      for (let i = 0; i < 40 && !calls.some((c) => c.path === "copy"); i++) await page.waitForTimeout(50);
      assert.deepEqual(calls.find((c) => c.path === "copy"), { path: "copy", body: { text: FORK } });

      // the details say the command; Show conversation reads the session
      await still(row("p-1"), "opening the row", () => row("p-1").locator(".who").click());
      const detail = view.locator(".sess-detail");
      await detail.waitFor();
      const keys = await detail.locator(".sess-line .k").allInnerTexts();
      assert(keys.includes(w.line), `the details have ${w.line}: ${keys}`);
      assert((await detail.textContent()).includes(FORK));
      assert(keys.includes(w.talk));
      const show = detail.locator(".sess-talk-btn");
      assert.equal((await show.textContent()).trim(), w.show);
      assert.equal(await detail.locator(".sess-talk").isVisible(), false, "the conversation waits to be asked for");
      await still(row("p-1"), "showing the conversation", () => show.click());
      const talk = detail.locator(".sess-talk");
      await talk.locator(".cx-part").first().waitFor();
      assert.deepEqual(calls.filter((c) => c.path === "transcript"), [{ path: "transcript", agent: "pi", id: "p-1" }]);
      assert.equal(await talk.locator(".cx-part").count(), PARTS.length);
      if (lang === "en") {
        assert.deepEqual(await talk.locator(".cx-part .cx-role").allTextContents(), [w.you, w.thinking, w.call, w.result, w.asst]);
      }
      assert.deepEqual(await talk.locator(".cx-name").allTextContents(), ["bash", "bash"]);
      assert.equal(await talk.locator("details.cx-part").getAttribute("open"), null, "thinking is folded");
      assert((await talk.locator(".cx-t").first().textContent()).includes("Rename foo to bar"));
      assert.equal((await show.textContent()).trim(), w.hide);
      // hidden, and shown again from what was read
      await show.click();
      assert.equal(await talk.isVisible(), false);
      await show.click();
      await talk.locator(".cx-part").first().waitFor();
      assert.equal(calls.filter((c) => c.path === "transcript").length, 1, "read once");

      const border = await page.evaluate(() => [...document.querySelectorAll("#view-sessions, #view-sessions *")]
        .filter((e) => parseFloat(getComputedStyle(e).borderLeftWidth) > 1 && getComputedStyle(e).borderLeftColor !== getComputedStyle(e).borderRightColor).map((e) => e.className));
      assert.deepEqual(border, [], "no left-border accent");
      if (process.env.SCREENSHOT_DIR) {
        await fs.mkdir(process.env.SCREENSHOT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.SCREENSHOT_DIR, `${engine}-${lang}-sessions-talk.png`), fullPage: true });
      }

      // an agent whose sessions can't be read or carried has neither
      await view.locator(".sm-agents .opt", { hasText: "OpenCode" }).click();
      const o = view.locator('.row.sm-sess[data-id="o-1"]');
      await o.waitFor();
      assert.equal(await o.locator(".sess-carry").count(), 0);
      await o.locator(".who").click();
      await view.locator(".sess-detail").waitFor();
      assert.equal(await view.locator(".sess-talk-btn").count(), 0);

      await view.locator(".sm-agents .opt", { hasText: "Reasonix Studio" }).click();
      const rx = view.locator('.row.sm-sess[data-id="r-1"]');
      await rx.waitFor();
      const partial = w.partial || (lang === "zh" ? "用量历史不完整" : "Partial usage history");
      assert(!(await rx.textContent()).includes(partial), "complete native wire usage is not labelled incomplete");
      assert((await rx.textContent()).includes("fake-model"), "native 2.29.0 model appears");
      assert.notEqual(await rx.locator(".num b").textContent(), "—", "wire usage appears without double-counting telemetry");
      await rx.locator(".who").click();
      assert((await view.locator(".sess-detail").textContent()).includes("reasonix --resume"));
      await view.locator(".sess-talk-btn").click();
      await view.locator(".sess-talk .cx-part").first().waitFor();
      assert(calls.some((c) => c.path === "transcript" && c.agent === "reasonix" && c.id === "r-1"));
      const nativeTalk = await view.locator(".sess-talk").textContent();
      assert(nativeTalk.includes("hello there") && nativeTalk.includes("follow up") && !nativeTalk.includes("<workspace>"), "native raw user input is shown");

      if (lang === "zh") {
        const missing = await page.evaluate(() => ["Continue in {agent}", "Continue in", "Continue this session in another agent", "Continue in another agent",
          "Copy the command", "Conversation", "Show conversation", "Hide conversation", "Reading…", "Partial usage history", "Only retained native usage is counted; older records are unavailable."].filter((k) => !I18N.zh[k] || !I18N.ja[k] || !I18N.de[k]));
        assert.deepEqual(missing, [], "every string has its Chinese, Japanese and German");
      }
      assert.deepEqual(errors, []);
    });
  }
}
