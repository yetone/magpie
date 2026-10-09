// Run with Node's test runner and Playwright on the module path; see README.md.
// #727 (Sun1090): the Agents page updated one agent's CLI at a time, and a
// new machine got no word on how to install an agent. Now, with two or more
// CLIs behind, a line above the list updates them all, one after another
// (never two at once), each row's pill busy in its turn, the view left where
// it was, and says how it went — a failed one by name, the line staying put
// so the list doesn't jump. Under the list, the agents magpie knows that
// aren't here, folded (open when none is here), each with its vendor's
// install commands to copy. No backend: the API is faked here, and nothing
// is ever installed or updated.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = ["model-a", "model-b"].map((m) => ({ value: m, label: m }));
const NAMES = { codex: "Codex", gemini: "Gemini CLI", crush: "Crush" };
const agent = (id) => ({
  id, name: NAMES[id] || id, path: "/test/" + id, fields: [
    { key: "model", label: "model", value: "model-a", options: models },
  ],
});
const AGENTS = [...Array.from({ length: 10 }, (_, i) => agent("agent-" + i)), agent("codex"), agent("gemini"), agent("crush")];
const CLIS = {
  codex: { version: "0.155.1", latest: "0.159.0", via: "self", command: "codex update", update: true },
  gemini: { version: "0.60.0", latest: "0.61.0", via: "npm", command: "npm install -g @google/gemini-cli@latest", update: true },
  crush: { version: "0.3.0", latest: "0.4.0", via: "brew", command: "brew upgrade crush", update: true },
  "agent-1": { version: "2.0.0", latest: "2.0.0", via: "brew", command: "brew upgrade agent-1" },
};
const INSTALLS = [
  { id: "claude", name: "Claude Code", icon: "claudecode-color", commands: [{ via: "script", command: "curl -fsSL https://claude.ai/install.sh | bash" }, { via: "npm", command: "npm install -g @anthropic-ai/claude-code" }] },
  { id: "opencode", name: "OpenCode", icon: "opencode", commands: [{ via: "npm", command: "npm install -g opencode-ai" }] },
  // one the state already lists (installed since): not offered
  { id: "codex", name: "Codex", icon: "codex-color", commands: [{ via: "npm", command: "npm install -g @openai/codex" }] },
];

function server(lang, opts) {
  const { agents = AGENTS, log = {} } = opts;
  const clis = structuredClone(opts.clis || CLIS); // what an update leaves is read back after
  log.posts = []; log.copies = []; log.running = 0; log.most = 0;
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents, profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/agents/cli" && req.method() === "GET") return route.fulfill({ json: { agents: clis, pending: false } });
    if (url.pathname === "/api/agents/install") return route.fulfill({ json: opts.installs || INSTALLS });
    if (url.pathname === "/api/copy") { log.copies.push(JSON.parse(req.postData()).text); return route.fulfill({ json: {} }); }
    if (url.pathname.startsWith("/api/agents/cli/") && req.method() === "POST") {
      const id = url.pathname.split("/").pop();
      log.posts.push(id);
      log.running++;
      log.most = Math.max(log.most, log.running);
      await new Promise((r) => setTimeout(r, 500)); // an update takes a while
      log.running--;
      if (id === "gemini") return route.fulfill({ status: 500, json: { error: "npm install -g @google/gemini-cli@latest: EACCES: permission denied" } });
      clis[id] = { ...clis[id], version: clis[id].latest, update: false };
      return route.fulfill({ json: clis[id] });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = (id) => `.row.agent[data-id="${id}"]`;

// the reader scrolls (the wheel) until what they'll click is in view: the
// app puts back a scroll that isn't the reader's, a test's own included
async function wheelTo(page, sel) {
  const l = page.locator(sel).first();
  await page.mouse.move(400, 300);
  for (let i = 0; i < 40 && await l.evaluate((b) => b.getBoundingClientRect().bottom > innerHeight - 70); i++) {
    await page.mouse.wheel(0, 60);
    await page.waitForTimeout(50);
  }
  await page.waitForTimeout(250);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": update every agent, install the ones not here", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, opts = {}, ready = `${row("codex")} .ag-ver`) => {
      const page = await (await browser.newContext({ viewport: { width: 980, height: 520 } })).newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, opts));
      await page.goto("http://magpie.test/");
      await page.locator(ready).waitFor();
      return page;
    };

    await t.test("Update all runs each, one at a time, and says how it went", async () => {
      const log = {};
      const page = await open("en", { log });
      const bar = page.locator("#agentsUpdates");
      await bar.waitFor();
      assert.equal(await bar.locator(".ag-updates-say").textContent(), "3 agents have updates: Codex, Gemini CLI, Crush");
      assert.equal(await bar.locator("button").textContent(), "Update all");
      // above the list
      assert(await page.evaluate(() => document.querySelector("#agentsUpdates").getBoundingClientRect().bottom <= document.querySelector("#agents").getBoundingClientRect().top + 1));
      // the reader has scrolled down a little: the run must not move the page
      const view = page.locator("#view-agents");
      await page.mouse.move(400, 300);
      await page.mouse.wheel(0, 30);
      await page.waitForTimeout(300);
      const top = await view.evaluate((v) => v.scrollTop);
      await bar.locator("button").click();
      await page.waitForTimeout(200);
      assert.match(await bar.locator(".ag-updates-say").textContent(), /^Updating Codex… \(1 of 3\)$/);
      assert.equal(await page.locator(`${row("codex")} .ag-up.busy`).count(), 1, "the row's pill is busy in its turn");
      assert.equal(await page.locator(`${row("gemini")} .ag-up.busy`).count(), 0);
      // clicked again while it runs: nothing more is asked
      await bar.locator("button").click({ force: true });
      await page.waitForTimeout(500);
      assert.match(await bar.locator(".ag-updates-say").textContent(), /Updating Gemini CLI… \(2 of 3\)/);
      await page.waitForFunction(() => /didn't/.test(document.querySelector("#agentsUpdates .ag-updates-say")?.textContent || ""), null, { timeout: 4000 });
      assert.deepEqual(log.posts, ["codex", "gemini", "crush"]);
      assert.equal(log.most, 1, "two updates ran at once");
      // each one updated with the versions it went from and to (37FlowAI on X)
      assert.equal(await bar.locator(".ag-updates-say").textContent(), "2 of 3 agents updated: Codex 0.155.1 → 0.159.0, Crush 0.3.0 → 0.4.0 · 1 didn't: Gemini CLI");
      assert.match(await bar.locator(".ag-updates-say").getAttribute("title"), /^Codex 0\.155\.1 → 0\.159\.0\nCrush 0\.3\.0 → 0\.4\.0\n.*EACCES/);
      assert.match(await page.locator("#status").textContent(), /EACCES/);
      // updated ones lose their pill; the failed one keeps it to try again
      assert.equal(await page.locator(`${row("codex")} .ag-ver`).textContent(), "0.159.0");
      assert.equal(await page.locator(`${row("codex")} .ag-up`).count(), 0);
      assert.equal(await page.locator(`${row("crush")} .ag-up`).count(), 0);
      assert.equal(await page.locator(`${row("gemini")} .ag-up`).textContent(), "Update to 0.61.0");
      // one left behind is no reason to offer Update all again
      assert.equal(await bar.locator("button").count(), 0);
      assert.equal(await view.evaluate((v) => v.scrollTop), top, "the run moved the page");
    });

    await t.test("all updated: the line lists each one's versions, in one line", async () => {
      for (const [lang, want] of [
        ["en", "2 agents updated: Codex 0.155.1 → 0.159.0, Crush 0.3.0 → 0.4.0"],
        ["zh", "已更新 2 个 Agent：Codex 0.155.1 → 0.159.0, Crush 0.3.0 → 0.4.0"],
        ["ja", "2 個のエージェントをアップデートしました：Codex 0.155.1 → 0.159.0, Crush 0.3.0 → 0.4.0"],
        ["de", "2 Agenten aktualisiert: Codex 0.155.1 → 0.159.0, Crush 0.3.0 → 0.4.0"],
      ]) {
        const page = await open(lang, { clis: { codex: CLIS.codex, crush: CLIS.crush } });
        const bar = page.locator("#agentsUpdates");
        await bar.waitFor();
        const h = await bar.evaluate((b) => b.getBoundingClientRect().height);
        await bar.locator("button").click();
        await page.waitForFunction(() => !document.querySelector("#agentsUpdates button"), null, { timeout: 4000 });
        assert.equal(await bar.locator(".ag-updates-say").textContent(), want);
        assert.equal(await page.locator("#status").textContent(), want);
        assert.equal(await bar.locator(".ag-updates-say").getAttribute("title"), "Codex 0.155.1 → 0.159.0\nCrush 0.3.0 → 0.4.0");
        // narrow: still one line, cut short, the list under it not pushed down
        await page.setViewportSize({ width: 420, height: 520 });
        await page.waitForTimeout(200);
        assert.equal(await bar.evaluate((b) => b.getBoundingClientRect().height), h, "the line grew");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
      }
    });

    await t.test("a row's own pill leaves the line where it is", async () => {
      const page = await open("en", { clis: { codex: CLIS.codex, crush: CLIS.crush } });
      const bar = page.locator("#agentsUpdates");
      await bar.waitFor();
      await wheelTo(page, `${row("codex")} .ag-up`);
      const at = () => page.locator(`${row("codex")}`).evaluate((r) => r.getBoundingClientRect().top);
      const was = await at();
      await page.locator(`${row("codex")} .ag-up`).click();
      await page.waitForTimeout(150);
      // one is being updated: the other alone is left to Update all's button
      assert.equal(await bar.locator("button").count(), 0);
      await page.waitForTimeout(700);
      assert.equal(await page.locator(`${row("codex")} .ag-up`).count(), 0);
      // one left: no button, but the line stays, so the list doesn't jump
      assert.equal(await bar.locator(".ag-updates-say").textContent(), "Crush has an update");
      assert.equal(await bar.locator("button").count(), 0);
      assert.equal(await at(), was, "the list moved");
    });

    await t.test("one behind: its own pill, no Update all", async () => {
      const page = await open("en", { clis: { codex: CLIS.codex, "agent-1": CLIS["agent-1"] } });
      await page.waitForTimeout(300);
      assert.equal(await page.locator("#agentsUpdates").count(), 0);
      assert.equal(await page.locator(`${row("codex")} .ag-up`).count(), 1);
    });

    await t.test("the agents not here, with their install commands", async () => {
      const log = {};
      const page = await open("en", { log });
      const box = page.locator("#agentsInstall");
      await box.waitFor();
      // under the list, folded, the installed Codex left out
      assert(await page.evaluate(() => document.querySelector("#agentsInstall").getBoundingClientRect().top >= document.querySelector("#agents").getBoundingClientRect().bottom - 1));
      const head = box.locator(".ag-install-head");
      assert.equal(await head.textContent(), "Install another agent (2)");
      assert.equal(await head.getAttribute("aria-expanded"), "false");
      assert.equal(await box.locator(".ag-install-row").count(), 0);
      await wheelTo(page, "#agentsInstall .ag-install-head");
      const view = page.locator("#view-agents");
      const top = await view.evaluate((v) => v.scrollTop);
      await head.click();
      assert.equal(await head.getAttribute("aria-expanded"), "true");
      assert.equal(await view.evaluate((v) => v.scrollTop), top, "opening it scrolled the page");
      assert.deepEqual(await box.locator(".ag-install-row").evaluateAll((rs) => rs.map((r) => r.dataset.id)), ["claude", "opencode"]);
      const claude = box.locator('.ag-install-row[data-id="claude"]');
      assert.deepEqual(await claude.locator(".ag-install-via").allTextContents(), ["Installer", "npm"]);
      assert.deepEqual(await claude.locator("code").allTextContents(), ["curl -fsSL https://claude.ai/install.sh | bash", "npm install -g @anthropic-ai/claude-code"]);
      await wheelTo(page, '#agentsInstall .ag-install-row[data-id="claude"] button.copy');
      await claude.locator(".ag-install-cmd").first().locator("button.copy").click();
      await page.waitForTimeout(200);
      assert.deepEqual(log.copies, ["curl -fsSL https://claude.ai/install.sh | bash"]);
      assert.match(await page.locator("#status").textContent(), /Claude Code's install command copied/);
      // folded again
      await wheelTo(page, "#agentsInstall .ag-install-head");
      await head.click();
      assert.equal(await box.locator(".ag-install-row").count(), 0);
    });

    await t.test("no agent here: the install commands open at once", async () => {
      const page = await open("en", { agents: [], clis: {} }, '#agentsInstall .ag-install-row[data-id="claude"]');
      assert.equal(await page.locator("#agentsInstall .ag-install-head").getAttribute("aria-expanded"), "true");
      assert.equal(await page.locator("#agentsInstall .ag-install-row").count(), 3);
    });

    // no Node.js on the machine (#727, Sun1090: "curl xxx && npm xxxx"): the
    // server gives each npm command Node's install first; the row says
    // Node.js + npm, copies the whole line, and the note says what it does
    const NVM = 'export NVM_DIR="$HOME/.nvm" && mkdir -p "$NVM_DIR" && curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.8/install.sh | bash && . "$NVM_DIR/nvm.sh" && nvm install --lts && npm install -g ';
    const WINGET = "winget install -e --id OpenJS.NodeJS.LTS; $env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User'); npm.cmd install -g ";
    const noNode = (node, pre) => [
      { id: "claude", name: "Claude Code", icon: "claudecode-color", commands: [{ via: "script", command: "curl -fsSL https://claude.ai/install.sh | bash" }, { via: "npm", node, command: pre + "@anthropic-ai/claude-code" }] },
      { id: "pi", name: "Pi", icon: "pi", commands: [{ via: "npm", node, command: pre + "@earendil-works/pi-coding-agent" }] },
    ];

    await t.test("no Node.js here: the npm commands install it first", async () => {
      const log = {};
      const page = await open("en", { agents: [], clis: {}, log, installs: noNode("nvm-mac", NVM) }, '#agentsInstall .ag-install-row[data-id="pi"]');
      const box = page.locator("#agentsInstall");
      const note = await box.locator(".ag-install-note").textContent();
      assert.match(note, /^No Node\.js was found here, so the npm commands install it with nvm first\. Run a command in a terminal/);
      assert.match(note, /xcode-select --install/);
      assert.doesNotMatch(note, /nodejs\.org/);
      const claude = box.locator('.ag-install-row[data-id="claude"]');
      assert.deepEqual(await claude.locator(".ag-install-via").allTextContents(), ["Installer", "Node.js + npm"]);
      const pi = box.locator('.ag-install-row[data-id="pi"]');
      assert.equal(await pi.locator("code").textContent(), NVM + "@earendil-works/pi-coding-agent");
      // the label stays on one line, and the long command scrolls inside its code
      const via = await pi.locator(".ag-install-via").evaluate((v) => v.getBoundingClientRect().height);
      assert(via < 20, "the label wrapped: " + via);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
      await wheelTo(page, '#agentsInstall .ag-install-row[data-id="pi"] button.copy');
      await pi.locator("button.copy").click();
      await page.waitForTimeout(200);
      assert.deepEqual(log.copies, [NVM + "@earendil-works/pi-coding-agent"]);
      // on Linux: no word of Xcode
      const linux = await open("en", { agents: [], clis: {}, installs: noNode("nvm", NVM) }, '#agentsInstall .ag-install-row[data-id="pi"]');
      assert.doesNotMatch(await linux.locator("#agentsInstall .ag-install-note").textContent(), /xcode/i);
      // on Windows, in Chinese: winget, PowerShell, a new terminal
      const win = await open("zh", { agents: [], clis: {}, installs: noNode("winget", WINGET) }, '#agentsInstall .ag-install-row[data-id="pi"]');
      const wnote = await win.locator("#agentsInstall .ag-install-note").textContent();
      assert.match(wnote, /^本机未找到 Node\.js，npm 命令会先用 winget 安装它/);
      assert.match(wnote, /PowerShell/);
      assert.match(wnote, /新终端/);
      assert.equal(await win.locator('#agentsInstall .ag-install-row[data-id="pi"] .ag-install-via').textContent(), "Node.js + npm");
      assert.equal(await win.locator('#agentsInstall .ag-install-row[data-id="pi"] code').textContent(), WINGET + "@earendil-works/pi-coding-agent");
    });

    await t.test("in Chinese", async () => {
      const page = await open("zh", {});
      const bar = page.locator("#agentsUpdates");
      await bar.waitFor();
      assert.equal(await bar.locator(".ag-updates-say").textContent(), "3 个 Agent 有新版本：Codex, Gemini CLI, Crush");
      assert.equal(await bar.locator("button").textContent(), "全部更新");
      const head = page.locator("#agentsInstall .ag-install-head");
      assert.equal(await head.textContent(), "安装其他 Agent（2）");
      await wheelTo(page, "#agentsInstall .ag-install-head");
      await head.click();
      assert.match(await page.locator("#agentsInstall .ag-install-note").textContent(), /在终端运行/);
      assert.equal(await page.locator('#agentsInstall .ag-install-row[data-id="claude"] .ag-install-via').first().textContent(), "安装脚本");
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.setViewportSize({ width: 980, height: 1100 });
        await page.waitForTimeout(300);
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-agent-update-all.png`), fullPage: true });
      }
    });

    assert.deepEqual(errors, []);
  });
}
