// Run with Node's test runner and Playwright on the module path; see README.md.
// usrname1833: the Skills tab says where the library's skills go, each
// agent's own folder (the default) or ~/.agents/skills too, and a switch
// posts it. With them there, the agents that read that folder (Pi from
// 0.54, Codex) have every skill: their chips are locked on. No select, no
// scroll, no left border, nothing wider than the window, in every
// language and at a phone's width. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/ada";

function server(lang, posts) {
  const st = { shared: false };
  const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, how: "link" });
  const readers = () => (st.shared ? ["codex", "pi"] : []);
  const lib = () => ({
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME, copySkills: false,
    sharedSkills: st.shared, sharedDir: `${HOME}/.agents/skills`,
    agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("pi", "Pi")],
    instructions: { agents: [] }, servers: [], foundServers: [], projects: [], foundSkills: [],
    skills: [
      { name: "pdf", description: "Read PDFs", kind: "", agents: ["claude"], always: readers().length ? readers() : undefined },
      { name: "tdd", description: "Tests first", kind: "", agents: ["claude", "pi"], always: readers().length ? readers() : undefined },
    ],
  });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/skills/where") {
      const b = req.postDataJSON();
      posts.push(b);
      st.shared = b.how === "shared";
      return route.fulfill({ json: { ...lib(), result: { changed: ["codex", "pi"], problems: [] } } });
    }
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// where the bar's label is on screen: a switch leaves it where it was (a
// pixel's give, for a page scrolled to its foot whose note changed length)
const labelAt = (page) => page.locator("#lib-skillwhere .label").evaluate((e) => Math.round(e.getBoundingClientRect().top));
const press = async (page, loc) => {
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};
const leftBorders = (page, sel) => page.evaluate((sel) => [...document.querySelectorAll(sel)].flatMap((e) => [e, ...e.querySelectorAll("*")])
  .filter((e) => parseFloat(getComputedStyle(e).borderLeftWidth) > 0 && getComputedStyle(e).borderLeftColor !== getComputedStyle(e).borderTopColor)
  .map((e) => e.className), sel);
// what of the bar reaches past the window's right edge
const pastEdge = (page) => page.evaluate(() => [...document.querySelectorAll("#lib-skillwhere, #lib-skillwhere *")]
  .filter((e) => e.getBoundingClientRect().right > document.documentElement.clientWidth + 0.5).map((e) => e.className || e.tagName));

const SHARED = "~/.agents/skills";
const words = {
  en: {
    label: "Put skills in", agents: "Each agent's own folder",
    agentsNote: "Each agent gets the skills ticked for it, in its own folder.",
    sharedNote: "Every skill on here is linked into ~/.agents/skills too: each agent that reads that folder has every skill, whatever is ticked. The files stay in the library.",
    toShared: "Every skill is in ~/.agents/skills now", toAgents: "Skills go to the agents they're ticked for now",
  },
  zh: {
    label: "Skills 放在", agents: "各 Agent 自己的目录",
    agentsNote: "每个 Agent 只拿到勾选给它的 Skill，放在它自己的目录里。",
    sharedNote: "这台电脑上启用的每个 Skill 也会链接到 ~/.agents/skills：读这个目录的 Agent 都会拿到全部 Skill，不管勾没勾选。文件仍保存在资源库里。",
    toShared: "所有 Skill 现在都在 ~/.agents/skills 里了", toAgents: "Skill 现在只给勾选的 Agent",
  },
  "zh-TW": {
    label: "Skills 放在", agents: "各 Agent 自己的目錄",
    agentsNote: "每個 Agent 只拿到勾選給它的 Skill，放在它自己的目錄裡。",
    sharedNote: "這台電腦上啟用的每個 Skill 也會連結到 ~/.agents/skills：讀這個目錄的 Agent 都會拿到全部 Skill，不管勾沒勾選。檔案仍保存在資源庫裡。",
    toShared: "所有 Skill 現在都在 ~/.agents/skills 裡了", toAgents: "Skill 現在只給勾選的 Agent",
  },
  ja: {
    label: "スキルの置き場所", agents: "各エージェントのフォルダ",
    agentsNote: "各エージェントには、チェックしたスキルだけがそのエージェントのフォルダに渡ります。",
    sharedNote: "このコンピュータで有効なスキルはすべて ~/.agents/skills にもリンクされます。このフォルダを読むエージェントは、チェックに関係なくすべてのスキルを使えます。ファイルはライブラリに残ります。",
    toShared: "すべてのスキルが ~/.agents/skills に入りました", toAgents: "スキルはチェックしたエージェントにだけ渡るようになりました",
  },
  de: {
    label: "Skills ablegen in", agents: "Ordner jedes Agenten",
    agentsNote: "Jeder Agent bekommt die für ihn angehakten Skills in seinem eigenen Ordner.",
    sharedNote: "Jeder hier aktive Skill wird auch nach ~/.agents/skills verlinkt: Jeder Agent, der diesen Ordner liest, hat jeden Skill, egal was angehakt ist. Die Dateien bleiben in der Bibliothek.",
    toShared: "Jeder Skill liegt jetzt in ~/.agents/skills", toAgents: "Skills gehen jetzt an die Agenten, für die sie angehakt sind",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": where the library's skills go (usrname1833)", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, width, posts) => {
      const ctx = await browser.newContext({ viewport: { width, height: 760 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();
      return page;
    };
    const locked = (page, skill) => page.locator("#view-library .lib-skill", { hasText: skill }).locator(".lib-ag.always")
      .evaluateAll((cs) => cs.map((c) => c.dataset.agent).sort());

    for (const lang of Object.keys(words)) {
      for (const width of [900, 400]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const w = words[lang], posts = [];
          const page = await open(lang, width, posts);
          const bar = page.locator("#lib-skillwhere");
          assert.equal((await bar.locator(".label").textContent()).trim(), w.label);
          const segs = bar.locator(".segs .opt");
          assert.deepEqual((await segs.allTextContents()).map((s) => s.trim()), [w.agents, SHARED]);
          assert.equal((await bar.locator(".segs .opt.on").textContent()).trim(), w.agents, "each agent's folder is the default");
          assert.equal((await bar.locator(".note").textContent()).trim(), w.agentsNote);
          assert.deepEqual(await locked(page, "pdf"), []);
          assert.deepEqual(await pastEdge(page), [], "nothing past the window's edge");
          // at a phone's width the bar is below the fold: scrolled to as a
          // reader does, with the wheel, till its note is in view
          await page.mouse.move(width / 2, 500);
          for (let i = 0; i < 10; i++) {
            const b = await bar.locator(".note").boundingBox();
            if (b.y + b.height <= 740) break;
            await page.mouse.wheel(0, 200);
            await page.waitForTimeout(150);
          }
          const before = await labelAt(page);

          await press(page, segs.nth(1));
          await bar.locator(".segs .opt.on", { hasText: SHARED }).waitFor();
          assert.deepEqual(posts, [{ how: "shared" }]);
          assert.equal((await page.locator("#status").textContent()).trim(), w.toShared);
          const now = page.locator("#lib-skillwhere");
          assert.equal((await now.locator(".note").textContent()).trim(), w.sharedNote);
          // Codex and Pi read ~/.agents/skills: every skill, locked on
          assert.deepEqual(await locked(page, "pdf"), ["codex", "pi"]);
          assert.deepEqual(await locked(page, "tdd"), ["codex", "pi"]);
          assert(Math.abs(await labelAt(page) - before) <= 2, "the switch scrolls nothing");
          assert.deepEqual(await pastEdge(page), [], "nothing past the window's edge");

          await press(page, now.locator(".segs .opt").nth(0));
          await page.locator("#lib-skillwhere .segs .opt.on", { hasText: w.agents }).waitFor();
          assert.deepEqual(posts[1], { how: "agents" });
          assert.equal((await page.locator("#status").textContent()).trim(), w.toAgents);
          assert.deepEqual(await locked(page, "pdf"), []);

          assert.equal(await page.locator("select").count(), 0, "no native select");
          assert.deepEqual(await leftBorders(page, "#lib-skillwhere"), []);
          await page.context().close();
        });
      }
    }

    assert.deepEqual(errors, []);
  });
}
