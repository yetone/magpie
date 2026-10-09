// Run with Node's test runner and Playwright on the module path; see README.md.
// A saved profile's chip shows what the profile holds before it is applied
// (#467 emo172: a click on the chip applied it at once, so what it held
// could be seen only by applying it over the setup in use). A click on the
// chip opens its details: the agents' names, each one's fields under their
// labels (an empty one the agent's default, one that reads as a key shown as
// dots, each of Claude Code's four tiers, one left to follow the main model
// saying so and what that is: #480, they were missing), what the Library
// gives it; nothing is applied, and the chip clicked
// stays where it is on the screen. Apply in the details applies it, once; a
// second click on the chip closes them; ↻ and × are still on the chip. In the
// window and in the tray panel (its list scrolled to its end), in English
// and Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = ["model-a", "model-b"].map((m) => ({ value: m, label: m }));
const agent = (id, name) => ({ id, name, path: "/test/" + id, fields: [{ key: "model", label: "model", value: "model-a", options: models }] });
const filler = (i) => ({ name: "profile-" + String(i).padStart(2, "0"), summary: "", agents: [] });
const work = {
  name: "work", summary: "codex gpt-5",
  library: { servers: 1, skills: 1, instructions: true },
  agents: [
    { id: "claude", name: "Claude Code", fields: [
      { key: "model", label: "model", value: "magpie/a/main" },
      { key: "opus", label: "opus", value: "", follows: "model" },
      { key: "sonnet", label: "sonnet", value: "magpie/a/s" },
      { key: "haiku", label: "haiku", value: "", follows: "model" },
      { key: "fable", label: "fable", value: "", follows: "model" },
      { key: "subagent", label: "subagents", value: "" },
    ], servers: ["github"], skills: ["review"], models: { hidden: 0 } },
    { id: "codex", name: "Codex", fields: [
      { key: "model", label: "model", value: "magpie/openai/gpt-5" },
      { key: "effort", label: "effort", value: "high" },
      { key: "token", label: "token", value: "", hidden: true },
    ], instructions: true, models: { hidden: 8 } },
    // an agent with no fields saved, only a model list: shown only its picks
    { id: "opencode", name: "OpenCode", fields: [], models: { hidden: 0, only: true, picked: 3 } },
  ],
};

function server(lang, calls, count) {
  const profiles = [...Array.from({ length: count }, (_, i) => filler(i)), work, filler(99)];
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const state = () => ({ agents: [agent("codex", "Codex"), agent("claude", "Claude Code")], profiles, settings: { lang, theme: "light" } });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: state() });
    if (url.pathname.startsWith("/api/profile/")) {
      calls.push(url.pathname.slice("/api/profile/".length) + " " + (req.postDataJSON()?.name || ""));
      return route.fulfill({ json: { ...state(), changed: 2 } });
    }
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { model: "model", effort: "effort", high: "high", def: "agent default", follows: "follows the main model (magpie/a/main)", subagents: "subagents", servers: "MCP servers", skills: "Skills", instructions: "Instructions", on: "on", apply: "Apply", list: "Model list", all: "every model shown", hidden8: "8 hidden", picked3: "only the 3 picked" },
  zh: { model: "模型", effort: "推理强度", high: "高", def: "Agent 默认值", follows: "跟随主模型（magpie/a/main）", subagents: "子 agent", servers: "MCP 服务器", skills: "技能", instructions: "指令", on: "开启", apply: "应用", list: "模型列表", all: "全部显示", hidden8: "已隐藏 8 个", picked3: "仅显示所选的 3 个" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a profile's details before it is applied", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-profile-details-${i}.png`) });
      }
      await browser.close();
    });

    for (const lang of ["en", "zh"]) for (const panel of [false, true]) {
      await t.test(`${lang}, ${panel ? "panel" : "window"}`, async () => {
        const w = words[lang], calls = [], errors = [];
        const page = await (await browser.newContext({ viewport: panel ? { width: 440, height: 420 } : { width: 900, height: 560 } })).newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, calls, panel ? 30 : 3));
        await page.goto("http://magpie.test/" + (panel ? "?mode=panel" : ""));
        const chip = page.locator("#profiles .chip", { hasText: /^work/ });
        await chip.waitFor({ state: "attached" });
        if (panel) {
          await page.locator("#profBtn").click();
          await page.locator(".profiles.open").waitFor();
          await page.waitForTimeout(450);
          // the reader scrolls the list to its end, where "work" is
          const box = await page.locator(".profiles").boundingBox();
          await page.mouse.move(box.x + box.width / 2, box.y + 60);
          for (let i = 0; i < 20; i++) { await page.mouse.wheel(0, 40); await page.waitForTimeout(20); }
          await page.waitForTimeout(400);
          assert(await page.locator(".profiles").evaluate((v) => v.scrollTop) > 0, "the list must be long enough to scroll");
        } else {
          await chip.scrollIntoViewIfNeeded();
          await page.waitForTimeout(300);
        }
        const top = () => chip.evaluate((c) => c.getBoundingClientRect().top);

        // a click opens its details, applying nothing, the chip where it was
        const before = await top();
        await chip.click();
        const detail = page.locator(".prof-detail");
        await detail.waitFor();
        await page.waitForTimeout(350);
        assert.deepEqual(calls, [], "opening the details must change nothing");
        assert(Math.abs((await top()) - before) < 1, `the chip clicked must stay where it is: ${before} → ${await top()}`);
        assert.equal(await chip.getAttribute("aria-expanded"), "true");
        assert(await chip.locator(".x").count() === 2, "↻ and × stay on the chip");

        const read = await detail.evaluate((d) => ({
          name: d.querySelector(".pd-name").textContent,
          apply: d.querySelector(".pd-apply").textContent,
          groups: [...d.querySelectorAll(".pd-agent")].map((g) => ({
            name: g.querySelector(".pd-gn").textContent,
            rows: [...g.querySelectorAll("dt")].map((dt) => dt.textContent + "=" + dt.nextElementSibling.textContent),
          })),
          inView: (() => { const r = d.getBoundingClientRect(); return r.height > 40 && r.width > 200; })(),
        }));
        assert.equal(read.name, "work");
        assert.equal(read.apply, w.apply);
        assert(read.inView, "the details must be drawn");
        assert.deepEqual(read.groups, [
          { name: "Claude Code", rows: [`${w.model}=magpie/a/main`, `opus=${w.follows}`, "sonnet=magpie/a/s", `haiku=${w.follows}`, `fable=${w.follows}`, `${w.subagents}=${w.def}`, `${w.list}=${w.all}`, `${w.servers}=github`, `${w.skills}=review`] },
          { name: "Codex", rows: [`${w.model}=magpie/openai/gpt-5`, `${w.effort}=${w.high}`, "token=••••••", `${w.list}=${w.hidden8}`, `${w.instructions}=${w.on}`] },
          { name: "OpenCode", rows: [`${w.list}=${w.picked3}`] },
        ]);

        // a second click closes them; a third opens them again
        await chip.click();
        await page.waitForTimeout(200);
        assert.equal(await detail.count(), 0, "a second click closes the details");
        assert(Math.abs((await top()) - before) < 1, "closing them leaves the chip where it is");
        await chip.click();
        await detail.waitFor();
        assert.deepEqual(calls, []);

        // Apply applies it, once
        await page.locator(".prof-detail .pd-apply").click();
        await page.waitForFunction(() => /work/.test(document.querySelector(".status")?.textContent || ""));
        assert.deepEqual(calls, ["use work"]);
        assert.deepEqual(errors, []);
      });
    }

    // the model list's line in the other languages (#1368)
    const lists = {
      "zh-TW": ["模型列表=全部顯示", "模型列表=已隱藏 8 個", "模型列表=僅顯示所選的 3 個"],
      ja: ["モデル一覧=すべて表示", "モデル一覧=8 件非表示", "モデル一覧=選んだ 3 件のみ"],
      de: ["Modellliste=alle angezeigt", "Modellliste=8 ausgeblendet", "Modellliste=nur die 3 ausgewählten"],
    };
    for (const [lang, want] of Object.entries(lists)) {
      await t.test(`${lang}, the model lists`, async () => {
        const errors = [];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 560 } })).newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, [], 3));
        await page.goto("http://magpie.test/");
        const chip = page.locator("#profiles .chip", { hasText: /^work/ });
        await chip.waitFor({ state: "attached" });
        await chip.scrollIntoViewIfNeeded();
        await chip.click();
        await page.locator(".prof-detail").waitFor();
        const rows = await page.locator(".prof-detail").evaluate((d) =>
          [...d.querySelectorAll(".pd-agent dt")].map((dt) => dt.textContent + "=" + dt.nextElementSibling.textContent));
        assert.deepEqual(rows.filter((r) => r.startsWith(want[0].split("=")[0] + "=")), want);
        assert.deepEqual(errors, []);
      });
    }
  });
}
