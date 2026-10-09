// Run with Node's test runner and Playwright on the module path; see README.md.
// A server on this machine (Ollama, LM Studio, oMLX) is reached at another
// port, or on another computer, by its Address, under the key in a Server
// address section that is folded unless the provider is somewhere other
// than the preset's default, or an address typed or refused needs it open.
// The Address is empty with the preset's as its placeholder when it is
// added, the saved one's when it is edited. The
// Endpoints follow what is typed, each API keeping its path; Add or Save
// sends the URLs moved there, and one that left it alone sends them as
// before: none for a new one (the preset's), a saved one's own. One that
// is no address is said, focused, and nothing is sent. A vendor's preset has
// no Address. Nothing moves the page, and the field keeps its width at 440px.
// English, Chinese (Simplified and Traditional), Japanese and German; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const presets = [
  { id: "openai", name: "OpenAI", icon: "openai", kind: "vendor", chat: "https://api.openai.com/v1", added: false },
  { id: "ollama", name: "Ollama", icon: "ollama", kind: "local", chat: "http://localhost:11434/v1", anthropic: "http://localhost:11434", noKey: true, note: "your local models", added: false },
  { id: "lmstudio", name: "LM Studio", icon: "lmstudio", kind: "local", chat: "http://localhost:1234/v1", noKey: true, note: "local server, port 1234 by default", added: true },
  { id: "omlx", name: "oMLX", icon: "omlx", kind: "local", chat: "http://localhost:8000/v1", responses: "http://localhost:8000/v1", anthropic: "http://localhost:8000", noKey: true, note: "local server, port 8000 by default", added: true },
];
const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "qwen3", name: "Qwen3", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false, masked: "", optional: true }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false, unredacted: false,
};
// an oMLX on another computer, and one whose URLs were set apart (another
// app's import): its Anthropic URL on another host than its chat one
const studio = { ...base, id: "omlx", name: "oMLX", icon: "omlx", preset: "omlx", host: "192.168.1.5:8000", chat: "http://192.168.1.5:8000/v1", responses: "http://192.168.1.5:8000/v1", anthropic: "http://192.168.1.5:8000" };
// an LM Studio where the preset puts it
const home = { ...base, id: "lmstudio", name: "LM Studio", icon: "lmstudio", preset: "lmstudio", host: "localhost:1234", chat: "http://localhost:1234/v1" };
const apart = { ...base, id: "omlx-2", name: "oMLX Apart", icon: "omlx", preset: "omlx", host: "10.0.0.2:8000", chat: "http://10.0.0.2:8000/v1", anthropic: "http://10.0.0.3:9000" };

function server(lang, saves, asks) {
  const list = { providers: [studio, apart, home], presets, excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(list);
    if (url.pathname === "/api/provider/save") {
      saves.push(route.request().postDataJSON());
      return json(list);
    }
    // a Test asks with the URLs as typed, before a Save
    if (url.pathname === "/api/provider/test") {
      asks.push(route.request().postDataJSON());
      return json({ results: [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { more: "Server address", field: "Address", hint: "Where the server listens; change the port, or give another computer's address", bad: "Address: ftp://box isn't an address like http://localhost:11434", test: "Test", add: "Add", save: "Save" },
  zh: { more: "服务器地址", field: "地址", hint: "服务监听的地址；可改端口，或填另一台电脑的地址", bad: "地址：ftp://box 不是形如 http://localhost:11434 的地址", test: "测试", add: "添加", save: "保存" },
  "zh-TW": { more: "伺服器位址", field: "位址", hint: "伺服器監聽的位址；可改連接埠，或填另一臺電腦的位址", bad: "位址：ftp://box 不是 http://localhost:11434 這樣的位址", test: "測試", add: "新增", save: "儲存" },
  ja: { more: "サーバーアドレス", field: "アドレス", hint: "サーバーが待ち受けるアドレスです。ポートを変えるか、別のコンピューターのアドレスを入力します", bad: "アドレス：ftp://box は http://localhost:11434 のようなアドレスではありません", test: "テスト", add: "追加", save: "保存" },
  de: { more: "Serveradresse", field: "Adresse", hint: "Wo der Server lauscht; ändern Sie den Port oder geben Sie die Adresse eines anderen Computers an", bad: "Adresse: ftp://box ist keine Adresse wie http://localhost:11434", test: "Testen", add: "Hinzufügen", save: "Speichern" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a local server's address", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      for (const width of [900, 440]) {
        await t.test(`${lang} ${width}`, async () => {
          const w = L[lang];
          const saves = [], asks = [];
          const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, saves, asks));
          await page.goto("http://magpie.test/?view=providers");
          const shot = async (name) => {
            if (!process.env.ARTIFACT_DIR) return;
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            // the editor faded in after a Save's list, not caught halfway
            await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => {}))));
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `local-address-${engine}-${lang}-${width}-${name}.png`), fullPage: true });
          };
          const ed = page.locator(".editor");
          const address = ed.locator("input.address");
          const more = ed.locator("details.more", { has: page.locator("input.address") });
          const isOpen = () => more.evaluate((d) => d.open);
          const eps = async () => (await ed.locator(".eps code").allTextContents()).join(" ");
          const press = async (name) => {
            const n = saves.length;
            await ed.locator(".bar").getByRole("button", { name, exact: true }).click();
            for (let i = 0; i < 50 && saves.length === n; i++) await page.waitForTimeout(50);
            return saves.length > n ? saves.at(-1) : null;
          };
          const tested = async () => {
            const n = asks.length;
            await ed.locator(".eps").getByRole("button", { name: w.test, exact: true }).click();
            for (let i = 0; i < 50 && asks.length === n; i++) await page.waitForTimeout(50);
            assert.equal(asks.length, n + 1, "a Test asked");
            return asks.at(-1);
          };

          // added: folded under the key, Server address its summary
          await page.locator("#addProvider").click();
          await page.locator("#addSheet .tile .n", { hasText: /^Ollama$/ }).click();
          await ed.locator(".ehead b", { hasText: "Ollama" }).waitFor();
          assert.equal(await isOpen(), false, "folded when added");
          assert.equal(await address.isVisible(), false);
          const summary = more.locator("summary");
          assert.equal(await summary.textContent(), w.more);
          const key = ed.locator("input[type=password]").first();
          assert((await key.boundingBox()).y < (await summary.boundingBox()).y, "the section under the key");
          await shot("folded");
          // opened: empty, the preset's address its placeholder
          await summary.click();
          assert.equal(await address.isVisible(), true);
          assert.equal(await ed.locator("label", { hasText: new RegExp("^" + w.field + "$") }).count(), 1);
          assert.equal(await address.inputValue(), "");
          assert.equal(await address.getAttribute("placeholder"), "http://localhost:11434");
          assert.equal(await address.locator("xpath=following-sibling::div[contains(@class,'hint')]").textContent(), w.hint);
          const box = await address.boundingBox();
          assert(box.width >= 200 && box.x + box.width <= width, `the field keeps its width: ${JSON.stringify(box)}`);
          assert.equal(await eps(), "http://localhost:11434/v1 http://localhost:11434");

          // no address: said, opened, focused, nothing sent, the page where it was
          await address.fill("ftp://box");
          await summary.click();
          assert.equal(await isOpen(), false);
          const y = await page.evaluate(() => document.scrollingElement.scrollTop);
          assert.equal(await press(w.add), null);
          assert.equal(await isOpen(), true, "opened for the refused address");
          assert.equal(await ed.locator(".editor-error").textContent(), w.bad);
          assert.equal(await page.evaluate(() => document.activeElement?.classList.contains("address")), true);
          assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y);

          // another port: the Endpoints follow, each with its path, and the
          // Add sends the preset's URLs moved there
          await address.fill("127.0.0.1:11435/v1");
          assert.equal(await eps(), "http://127.0.0.1:11435/v1 http://127.0.0.1:11435");
          await shot("add");
          const added = await press(w.add);
          assert.equal(added.preset, "ollama");
          assert.equal(added.new, true);
          assert.equal("address" in added, false);
          assert.equal(added.chat, "http://127.0.0.1:11435/v1");
          assert.equal(added.anthropic, "http://127.0.0.1:11435");
          assert(!added.responses, JSON.stringify(added));

          // edited: the address it is at; left alone, none is sent
          await page.locator(".row.provider", { hasText: /^oMLX$|oMLX(?! Apart)/ }).first().click();
          await ed.locator(".ehead b", { hasText: /^oMLX$/ }).waitFor();
          assert.equal(await isOpen(), true, "open for an address not the preset's");
          assert.equal(await address.inputValue(), "http://192.168.1.5:8000");
          let s = await press(w.save);
          assert.equal("address" in s, false);
          assert.equal(s.chat, "http://192.168.1.5:8000/v1");
          assert.equal(s.anthropic, "http://192.168.1.5:8000");

          // moved: its URLs there
          await page.locator(".row.provider", { hasText: /^oMLX$|oMLX(?! Apart)/ }).first().click();
          await address.fill("https://studio.local:8001");
          assert.equal(await eps(), "https://studio.local:8001/v1 https://studio.local:8001/v1 https://studio.local:8001");
          let asked = await tested();
          assert.equal(asked.chat, "https://studio.local:8001/v1");
          assert.equal(asked.responses, "https://studio.local:8001/v1");
          assert.equal(asked.anthropic, "https://studio.local:8001");
          await shot("edit");
          s = await press(w.save);
          assert.equal("address" in s, false);
          assert.equal(s.chat, "https://studio.local:8001/v1");
          assert.equal(s.responses, "https://studio.local:8001/v1");
          assert.equal(s.anthropic, "https://studio.local:8001");

          // URLs set apart, saved with the address untouched, stay apart
          await page.locator(".row.provider", { hasText: "oMLX Apart" }).click();
          await ed.locator(".ehead b", { hasText: "oMLX Apart" }).waitFor();
          assert.equal(await isOpen(), true);
          assert.equal(await address.inputValue(), "http://10.0.0.2:8000");
          const apartEps = "http://10.0.0.2:8000/v1 http://10.0.0.3:9000";
          assert.equal(await eps(), apartEps);
          s = await press(w.save);
          assert.equal("address" in s, false);
          assert.equal(s.chat, "http://10.0.0.2:8000/v1");
          assert.equal(s.anthropic, "http://10.0.0.3:9000");

          // edited, then put back as it was: still apart, in the Endpoints,
          // a Test and the Save
          await page.locator(".row.provider", { hasText: "oMLX Apart" }).click();
          await ed.locator(".ehead b", { hasText: "oMLX Apart" }).waitFor();
          // on macOS End doesn't move the caret, and focusing a field puts
          // it at the start: focus first, then the caret, then the keys
          await address.focus();
          await address.evaluate((el) => el.setSelectionRange(el.value.length, el.value.length));
          await page.keyboard.press("Backspace");
          assert.equal(await eps(), "http://10.0.0.2:800/v1 http://10.0.0.2:800");
          await page.keyboard.press("0");
          assert.equal(await address.inputValue(), "http://10.0.0.2:8000");
          assert.equal(await eps(), apartEps);
          asked = await tested();
          assert.equal(asked.chat, "http://10.0.0.2:8000/v1");
          assert.equal(asked.anthropic, "http://10.0.0.3:9000");
          s = await press(w.save);
          assert.equal(s.chat, "http://10.0.0.2:8000/v1");
          assert.equal(s.anthropic, "http://10.0.0.3:9000");

          // no address, then put back: the same
          await page.locator(".row.provider", { hasText: "oMLX Apart" }).click();
          await ed.locator(".ehead b", { hasText: "oMLX Apart" }).waitFor();
          await address.fill("ftp://box");
          assert.equal(await eps(), apartEps);
          assert.equal(await press(w.save), null);
          assert.equal(await ed.locator(".editor-error").textContent(), w.bad);
          await address.fill("http://10.0.0.2:8000");
          assert.equal(await eps(), apartEps);
          s = await press(w.save);
          assert.equal(s.chat, "http://10.0.0.2:8000/v1");
          assert.equal(s.anthropic, "http://10.0.0.3:9000");

          // one where the preset puts it: folded, and saved as it is
          await page.locator(".row.provider", { hasText: "LM Studio" }).click();
          await ed.locator(".ehead b", { hasText: "LM Studio" }).waitFor();
          assert.equal(await isOpen(), false, "folded at the preset's address");
          assert.equal(await address.inputValue(), "http://localhost:1234");
          s = await press(w.save);
          assert.equal("address" in s, false);
          assert.equal(s.chat, "http://localhost:1234/v1");

          // added with no address: no URLs, the gateway takes the preset's
          await page.locator("#addProvider").click();
          await page.locator("#addSheet .tile .n", { hasText: /^Ollama$/ }).click();
          await ed.locator(".ehead b", { hasText: "Ollama" }).waitFor();
          s = await press(w.add);
          assert.equal(s.preset, "ollama");
          assert(!s.chat && !s.anthropic && !s.responses && !("address" in s), JSON.stringify(s));

          // a vendor's preset has no address
          await page.locator("#addProvider").click();
          await page.locator("#addSheet .tile .n", { hasText: /^OpenAI$/ }).click();
          await ed.locator(".ehead b", { hasText: "OpenAI" }).waitFor();
          assert.equal(await address.count(), 0);
          assert.deepEqual(errors, []);
          await page.context().close();
        });
      }
    }
  });
}
